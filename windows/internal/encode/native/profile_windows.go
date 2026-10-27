//go:build windows

package native

import (
	"fmt"
	"time"

	"airspoon/internal/encode/ffmpeg"
)

// EncoderClass — класс выбранного кодера. От него зависит, какую нагрузку можно
// ставить: программный кодер не тянет 1080p60 (план windows-devices, A1).
type EncoderClass int

const (
	// ClassSoftware — программный Microsoft H.264 Encoder MFT (CPU).
	ClassSoftware EncoderClass = iota
	// ClassHardware — аппаратный кодер видеокарты.
	ClassHardware
)

func (c EncoderClass) String() string {
	if c == ClassHardware {
		return "аппаратный"
	}
	return "программный"
}

// Class возвращает класс кодера, который запущен.
func (i Info) Class() EncoderClass {
	if i.Hardware {
		return ClassHardware
	}
	return ClassSoftware
}

// Profile — размер, частота кадров и битрейт, которые источник должен выдавать.
// Нулевое поле — «не менять».
type Profile struct {
	Width, Height, FPS int
	BitrateKbps        int
}

func (p Profile) String() string {
	return fmt.Sprintf("%dx%d@%d, %d кбит/с", p.Width, p.Height, p.FPS, p.BitrateKbps)
}

// profileOf — профиль, в котором работает конфигурация.
func profileOf(c Config) Profile {
	return Profile{Width: c.Width, Height: c.Height, FPS: c.FPS, BitrateKbps: c.BitrateKbps}
}

// applyProfile накладывает профиль на конфигурацию. changed=false, если
// получилась та же конфигурация. Если битрейт не задан в профиле и вычислялся
// автоматически (autoBitrate), он пересчитывается под новый размер и частоту; если
// его задал пользователь, остаётся как есть.
func applyProfile(cfg Config, autoBitrate bool, p Profile) (out Config, changed bool) {
	out = cfg
	if p.Width >= 2 {
		out.Width = p.Width &^ 1
	}
	if p.Height >= 2 {
		out.Height = p.Height &^ 1
	}
	if p.FPS > 0 {
		out.FPS = p.FPS
	}
	switch {
	case p.BitrateKbps > 0:
		out.BitrateKbps = p.BitrateKbps
	case autoBitrate && (out.Width != cfg.Width || out.Height != cfg.Height || out.FPS != cfg.FPS):
		out.BitrateKbps = ffmpeg.AutoBitrateKbps(out.Width, out.Height, out.FPS)
	}
	changed = out.Width != cfg.Width || out.Height != cfg.Height || out.FPS != cfg.FPS || out.BitrateKbps != cfg.BitrateKbps
	return out, changed
}

// statsWindow — счётчики за период для проверки «есть ли проблемы».
type statsWindow struct {
	Dur                       time.Duration
	Offered, Skipped, Dropped uint64
	AvgLatency                time.Duration
}

const (
	// Пороги для строки о проблемах в обычном журнале (план, A11).
	problemSkipPercent = 10
	problemLatency     = 100 * time.Millisecond
	// problemLogInterval — как часто проверять, нет ли проблем.
	problemLogInterval = 10 * time.Second
	// problemMinFrames — меньше кадров за период процент пропусков не считаем.
	problemMinFrames = 10
)

// statsProblem возвращает строку для журнала, если за период были проблемы:
// пропущено больше 10% кадров, сброшены пакеты или средняя задержка больше 100 мс.
// Пустая строка — всё в порядке, в журнал ничего не пишем.
func statsProblem(w statsWindow) string {
	var parts []string
	if w.Offered >= problemMinFrames && w.Skipped*100 > w.Offered*problemSkipPercent {
		parts = append(parts, fmt.Sprintf("кодер не успевает: пропущено %d кадров из %d (%d%%)",
			w.Skipped, w.Offered, w.Skipped*100/w.Offered))
	}
	if w.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("отстаёт потребитель или сеть: сброшено пакетов %d", w.Dropped))
	}
	if w.AvgLatency > problemLatency {
		parts = append(parts, fmt.Sprintf("задержка захват->кодер в среднем %d мс", w.AvgLatency.Milliseconds()))
	}
	if len(parts) == 0 {
		return ""
	}
	s := fmt.Sprintf("[ВИДЕО] проблемы за %.0f с: ", w.Dur.Seconds())
	for i, p := range parts {
		if i > 0 {
			s += "; "
		}
		s += p
	}
	return s
}
