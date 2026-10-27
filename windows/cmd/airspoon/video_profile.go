package main

import (
	"fmt"
	"math"
	"runtime"

	"airspoon/internal/encode/native"
)

// Ограничение нагрузки по классу кодера (план windows-devices, A1). Программный
// кодер (на процессоре) не тянет 1080p60: на слабой машине получается 10-20
// кадров в секунду, задержка и нагрев. Поэтому без явных флагов пользователя при
// программном кодере (нет аппаратного: Basic Display, виртуальная машина, старая
// видеокарта) ставим 720p30, а на процессоре с шестью и более логическими ядрами
// 1080p30.
const (
	softwareFPS         = 30
	softwareLowWidth    = 1280
	softwareLowHeight   = 720
	softwareHighWidth   = 1920
	softwareHighHeight  = 1080
	softwareHighCores   = 6
	softwareQualityText = "аппаратного кодера нет — качество снижено"
)

// softwareProfile возвращает профиль для программного кодера: размер не больше
// 720p (при cores >= 6 — не больше 1080p) с сохранением пропорций и не больше 30
// кадров в секунду. keepSize и keepFPS — пользователь задал размер или частоту
// флагами: они важнее. ok=false — менять нечего. Битрейт в профиль не кладётся:
// источник пересчитывает его сам, если пользователь не задавал битрейт.
func softwareProfile(width, height, fps, cores int, keepSize, keepFPS bool) (p native.Profile, ok bool) {
	maxW, maxH := softwareLowWidth, softwareLowHeight
	if cores >= softwareHighCores {
		maxW, maxH = softwareHighWidth, softwareHighHeight
	}
	newW, newH, newFPS := width, height, fps
	if !keepSize && width > 0 && height > 0 && (width > maxW || height > maxH) {
		scale := math.Min(float64(maxW)/float64(width), float64(maxH)/float64(height))
		newW = int(math.Round(float64(width)*scale)) &^ 1
		newH = int(math.Round(float64(height)*scale)) &^ 1
		if newW < 2 || newH < 2 {
			newW, newH = width, height
		}
	}
	if !keepFPS && fps > softwareFPS {
		newFPS = softwareFPS
	}
	if newW == width && newH == height && newFPS == fps {
		return native.Profile{}, false
	}
	return native.Profile{Width: newW, Height: newH, FPS: newFPS}, true
}

// profileChooser собирает хук native.Config.ChooseProfile. Хук вызывается, когда
// кодер уже выбран. Для аппаратного кодера профиль не меняется. Для программного
// выбирается softwareProfile; причина пишется в журнал, а notice (может быть nil)
// получает один раз короткий текст для уведомления в трее. Хук может вызываться
// снова: при перезапуске источника и когда аппаратный кодер отказал посреди
// показа и источник перешёл на программный (сначала хук видит аппаратный и ничего
// не меняет, потом программный). Уведомление при этом одно, не повторяется.
type profileChooser struct {
	width, height, fps int
	keepSize, keepFPS  bool
	cores              int
	logf               func(string, ...any)
	notice             func(string)
	noticed            bool
}

func newProfileChooser(width, height, fps int, o mirrorOptions, logf func(string, ...any)) *profileChooser {
	return &profileChooser{
		width: width, height: height, fps: fps,
		keepSize: o.width > 0 && o.height > 0,
		keepFPS:  o.fps > 0,
		cores:    runtime.NumCPU(),
		logf:     logf,
		notice:   o.onNotice,
	}
}

func (c *profileChooser) choose(info native.Info) (native.Profile, bool) {
	if info.Class() == native.ClassHardware {
		return native.Profile{}, false
	}
	prof, ok := softwareProfile(c.width, c.height, c.fps, c.cores, c.keepSize, c.keepFPS)
	if !ok {
		if c.keepSize || c.keepFPS {
			c.logf("кодер %q программный; параметры заданы флагами (%dx%d@%d), оставляем как есть: при нехватке процессора возможны пропуски кадров",
				info.Encoder, c.width, c.height, c.fps)
		}
		return native.Profile{}, false
	}
	c.logf("кодер %q программный: %s; профиль %dx%d@%d меняется на %dx%d@%d (логических ядер: %d)",
		info.Encoder, softwareQualityText, c.width, c.height, c.fps, prof.Width, prof.Height, prof.FPS, c.cores)
	if c.notice != nil && !c.noticed {
		c.noticed = true
		c.notice(fmt.Sprintf("Аппаратного кодера видео нет — качество снижено до %dx%d, %d кадров в секунду.", prof.Width, prof.Height, prof.FPS))
	}
	return prof, true
}
