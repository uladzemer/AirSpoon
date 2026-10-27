package ffmpeg

import (
	"strings"
	"testing"
)

func TestBuildArgsDefaults(t *testing.T) {
	args, err := BuildArgs(Config{ShowCursor: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		"ddagrab=output_idx=0:draw_mouse=1:framerate=30:output_fmt=8bit",
		"scale=w=1280:h=720:force_original_aspect_ratio=decrease",
		" -c:v libx264 ",
		" -profile:v main ",
		" -tune zerolatency ",
		" -bf 0 ",
		" -g 60 ",
		" -keyint_min 60 ",
		"repeat-headers=1:aud=1",
		" -f h264 pipe:1 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("в аргументах нет %q:\n%s", want, joined)
		}
	}
}

func TestBuildArgsFPSAndSize(t *testing.T) {
	args, err := BuildArgs(Config{Width: 1921, Height: 1081, FPS: 60, DisplayIndex: 1, BitrateKbps: 5000})
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{"output_idx=1", "draw_mouse=0", "framerate=60", "w=1920:h=1080", " -g 120 ", " -b:v 5000k "} {
		if !strings.Contains(joined, want) {
			t.Errorf("в аргументах нет %q:\n%s", want, joined)
		}
	}
}

func TestBuildArgsRejectsBadConfig(t *testing.T) {
	for _, cfg := range []Config{{FPS: -1}, {FPS: 500}, {DisplayIndex: -1}, {Width: 1, Height: 1}} {
		if _, err := BuildArgs(cfg); err == nil {
			t.Errorf("конфигурация %+v принята", cfg)
		}
	}
}

func TestAutoBitrate(t *testing.T) {
	if got := autoBitrateKbps(1280, 720, 30); got != minBitrateKbps {
		t.Errorf("720p30 = %d, ожидалось нижнее ограничение %d", got, minBitrateKbps)
	}
	if got := autoBitrateKbps(3840, 2160, 60); got != maxBitrateKbps {
		t.Errorf("4K60 = %d, ожидалось верхнее ограничение %d", got, maxBitrateKbps)
	}
}

// Буфер VBV не должен душить ключевой кадр: не меньше четверти секунды при
// максимальной скорости (раньше было два кадра, 200 кбит при 2500 кбит/с).
func TestBuildArgsVBVBufferHoldsKeyFrame(t *testing.T) {
	args, err := BuildArgs(Config{})
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	// 2500 кбит/с -> maxrate 3125k -> буфер 781k.
	for _, want := range []string{" -maxrate 3125k ", " -bufsize 781k "} {
		if !strings.Contains(joined, want) {
			t.Errorf("в аргументах нет %q:\n%s", want, joined)
		}
	}
	if got := vbvBufsizeKbit(400); got != minVBVBufsizeKbit {
		t.Errorf("нижняя граница буфера = %d, ожидалось %d", got, minVBVBufsizeKbit)
	}
}
