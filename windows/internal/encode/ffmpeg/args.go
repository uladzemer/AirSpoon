package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"
)

// Config — параметры временного источника на основе ffmpeg.exe.
type Config struct {
	// FFmpegPath — путь к ffmpeg.exe. Пусто: сначала рядом с нашим .exe,
	// затем в PATH.
	FFmpegPath string
	// Width, Height — размер кадра на выходе (чётные). По умолчанию 1280x720.
	Width, Height int
	// FPS — частота кадров. По умолчанию 30.
	FPS int
	// BitrateKbps — битрейт; 0 выбирает автоматически по размеру и частоте.
	BitrateKbps int
	// DisplayIndex — номер монитора для ddagrab (output_idx).
	DisplayIndex int
	// ShowCursor — рисовать курсор мыши.
	ShowCursor bool
	// Logf получает строки stderr ffmpeg и служебные сообщения (может быть nil).
	Logf func(format string, args ...any)
	// Debugf получает подробные служебные сообщения, например командную строку (может быть nil).
	Debugf func(format string, args ...any)
}

const (
	defaultWidth  = 1280
	defaultHeight = 720
	defaultFPS    = 30

	minBitrateKbps = 2500
	maxBitrateKbps = 12000
)

func (c *Config) applyDefaults() error {
	if c.Width == 0 {
		c.Width = defaultWidth
	}
	if c.Height == 0 {
		c.Height = defaultHeight
	}
	if c.FPS == 0 {
		c.FPS = defaultFPS
	}
	c.Width &^= 1
	c.Height &^= 1
	if c.Width <= 0 || c.Height <= 0 {
		return fmt.Errorf("недопустимый размер кадра %dx%d", c.Width, c.Height)
	}
	if c.FPS < 1 || c.FPS > 120 {
		return fmt.Errorf("недопустимая частота кадров %d", c.FPS)
	}
	if c.DisplayIndex < 0 {
		return fmt.Errorf("номер монитора не может быть отрицательным")
	}
	return nil
}

// autoBitrateKbps выбирает битрейт по формуле open-airplay
// (width*height*fps/15000) с границами для рабочего стола.
func autoBitrateKbps(width, height, fps int) int {
	b := (width*height*fps + 7500) / 15000
	if b < minBitrateKbps {
		return minBitrateKbps
	}
	if b > maxBitrateKbps {
		return maxBitrateKbps
	}
	return b
}

// vbvBufsizeKbit — размер VBV-буфера x264: четверть секунды при максимальной
// скорости. Формула open-airplay (два кадра, но не меньше 200 кбит) душила IDR:
// при 2500 кбит/с первый IDR сложной картинки (измерение на тестовом источнике
// 1280x720) получался 10,9 КБ против 135 КБ при буфере 1250 кбит, то есть
// ключевой кадр сильно терял качество, а x264 мог печатать «VBV underflow».
// Четверть секунды ограничивает пик ключевого кадра (~0,25 с отправки при
// максимальной скорости) и не раздувает задержку, как буфер в секунды.
func vbvBufsizeKbit(maxrateKbps int) int {
	bufsize := maxrateKbps / 4
	if bufsize < minVBVBufsizeKbit {
		bufsize = minVBVBufsizeKbit
	}
	return bufsize
}

const minVBVBufsizeKbit = 200

// BuildArgs строит аргументы ffmpeg: ddagrab -> libx264 (Main, zerolatency,
// без B-кадров, GOP = 2*fps) -> Annex-B в stdout. Фильтры и параметры x264
// взяты из capture_windows.go проекта open-airplay.
func BuildArgs(cfg Config) ([]string, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	draw := 0
	if cfg.ShowCursor {
		draw = 1
	}
	input := strings.Join([]string{
		"ddagrab=output_idx=" + strconv.Itoa(cfg.DisplayIndex),
		"draw_mouse=" + strconv.Itoa(draw),
		"framerate=" + strconv.Itoa(cfg.FPS),
		"output_fmt=8bit",
	}, ":")

	filters := []string{
		"hwdownload",
		"format=bgra",
		fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:flags=fast_bilinear", cfg.Width, cfg.Height),
		fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", cfg.Width, cfg.Height),
		"setsar=1",
		// Явная матрица BT.709, чтобы цвета на приёмнике совпадали с рабочим столом.
		"scale=out_color_matrix=bt709:out_range=tv:flags=fast_bilinear",
		"format=yuv420p",
	}

	bitrate := cfg.BitrateKbps
	if bitrate <= 0 {
		bitrate = autoBitrateKbps(cfg.Width, cfg.Height, cfg.FPS)
	}
	maxrate := bitrate + bitrate/4
	bufsize := vbvBufsizeKbit(maxrate)
	gop := cfg.FPS * 2

	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-nostdin",
		"-f", "lavfi",
		"-i", input,
		"-vf", strings.Join(filters, ","),
		"-an",
		"-c:v", "libx264",
		"-profile:v", "main",
		"-preset", "superfast",
		"-tune", "zerolatency",
		// repeat-headers: SPS/PPS перед каждым IDR; aud: разделитель кадров.
		"-x264-params", "repeat-headers=1:aud=1:sliced-threads=1",
		"-b:v", fmt.Sprintf("%dk", bitrate),
		"-maxrate", fmt.Sprintf("%dk", maxrate),
		"-bufsize", fmt.Sprintf("%dk", bufsize),
		"-g", strconv.Itoa(gop),
		"-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0",
		"-bf", "0",
		"-pix_fmt", "yuv420p",
		"-colorspace", "bt709",
		"-color_primaries", "bt709",
		"-color_trc", "bt709",
		"-flush_packets", "1",
		"-f", "h264",
		"pipe:1",
	}, nil
}

// AutoBitrateKbps возвращает битрейт по умолчанию для заданных размера и
// частоты кадров. Тем же значением пользуются и другие источники видео
// (internal/encode/native), чтобы качество не зависело от источника.
func AutoBitrateKbps(width, height, fps int) int { return autoBitrateKbps(width, height, fps) }
