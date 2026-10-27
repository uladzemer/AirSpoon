package main

import (
	"errors"
	"math"
)

// Размер видео по умолчанию: холст, который предлагает приёмник (/info
// displays), но не больше 1920x1080; частота — maxFPS приёмника, но не больше 60
// (её выбирает ядро: StreamConfig.FPS = 0). Битрейт считает ffmpeg.AutoBitrateKbps
// по формуле ширина*высота*fps/15000: 1080p60 даёт около 8,3 Мбит/с (проверено
// владельцем на настоящем приёмнике: плавно). Флаги --width/--height/--fps
// задают размер и частоту вручную.
const (
	maxDefaultWidth  = 1920
	maxDefaultHeight = 1080
	// fallbackWidth, fallbackHeight — если приёмник холст не назвал.
	fallbackWidth  = 1280
	fallbackHeight = 720
)

// checkVideoSizeFlags проверяет флаги --width/--height: их задают вместе или не
// задают вовсе (0 — автоматически).
func checkVideoSizeFlags(width, height int) error {
	if width < 0 || height < 0 {
		return errors.New("--width и --height не могут быть отрицательными")
	}
	if (width == 0) != (height == 0) {
		return errors.New("укажите --width и --height вместе (или ни одного: размер тогда берётся по холсту приёмника)")
	}
	return nil
}

// chooseVideoSize выбирает размер кодируемого видео: заданный вручную
// (flagW x flagH, оба больше нуля) или холст приёмника canvasW x canvasH,
// уменьшенный с сохранением пропорций до 1920x1080. Размер чётный (требование
// кодера 4:2:0).
func chooseVideoSize(canvasW, canvasH, flagW, flagH int) (int, int) {
	if flagW > 0 && flagH > 0 {
		return flagW &^ 1, flagH &^ 1
	}
	if canvasW <= 0 || canvasH <= 0 {
		return fallbackWidth, fallbackHeight
	}
	w, h := canvasW, canvasH
	if w > maxDefaultWidth || h > maxDefaultHeight {
		scale := math.Min(float64(maxDefaultWidth)/float64(w), float64(maxDefaultHeight)/float64(h))
		w = int(math.Round(float64(w) * scale))
		h = int(math.Round(float64(h) * scale))
	}
	w &^= 1
	h &^= 1
	if w < 2 || h < 2 {
		return fallbackWidth, fallbackHeight
	}
	return w, h
}
