package main

import (
	"strings"
	"testing"

	"airspoon/internal/encode/native"
)

func TestSoftwareProfile(t *testing.T) {
	tests := []struct {
		name              string
		w, h, fps, cores  int
		keepSize, keepFPS bool
		want              native.Profile
		wantOK            bool
	}{
		{"1080p60 на 4 ядрах", 1920, 1080, 60, 4, false, false, native.Profile{Width: 1280, Height: 720, FPS: 30}, true},
		{"1080p60 на 6 ядрах", 1920, 1080, 60, 6, false, false, native.Profile{Width: 1920, Height: 1080, FPS: 30}, true},
		{"1080p30 на 8 ядрах: менять нечего", 1920, 1080, 30, 8, false, false, native.Profile{}, false},
		{"720p30 на 2 ядрах: менять нечего", 1280, 720, 30, 2, false, false, native.Profile{}, false},
		{"720p60: только частота", 1280, 720, 60, 2, false, false, native.Profile{Width: 1280, Height: 720, FPS: 30}, true},
		{"ультраширокий холст сохраняет пропорции", 1920, 804, 30, 4, false, false, native.Profile{Width: 1280, Height: 536, FPS: 30}, true},
		{"размер задан флагами", 1920, 1080, 60, 4, true, false, native.Profile{Width: 1920, Height: 1080, FPS: 30}, true},
		{"частота задана флагом", 1920, 1080, 60, 4, false, true, native.Profile{Width: 1280, Height: 720, FPS: 60}, true},
		{"всё задано флагами", 1920, 1080, 60, 4, true, true, native.Profile{}, false},
		{"меньше предела не увеличиваем", 800, 600, 30, 2, false, false, native.Profile{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := softwareProfile(tc.w, tc.h, tc.fps, tc.cores, tc.keepSize, tc.keepFPS)
			if ok != tc.wantOK || (ok && got != tc.want) {
				t.Fatalf("получили %+v ok=%v, ждали %+v ok=%v", got, ok, tc.want, tc.wantOK)
			}
			if got.Width%2 != 0 || got.Height%2 != 0 {
				t.Errorf("размер %dx%d должен быть чётным", got.Width, got.Height)
			}
		})
	}
}

func TestProfileChooser(t *testing.T) {
	var logs []string
	var notices []string
	logf := func(f string, a ...any) { logs = append(logs, f) }
	o := mirrorOptions{onNotice: func(s string) { notices = append(notices, s) }}

	// Аппаратный кодер: ничего не меняется.
	c := newProfileChooser(1920, 1080, 60, o, logf)
	c.cores = 4
	if _, ok := c.choose(native.Info{Hardware: true}); ok {
		t.Fatal("аппаратному кодеру профиль менять не нужно")
	}
	if len(notices) != 0 {
		t.Fatal("для аппаратного кодера уведомления нет")
	}

	// Программный: 720p30, одно уведомление, повторный вызов (перезапуск источника) не шумит.
	prof, ok := c.choose(native.Info{Encoder: "Microsoft H.264 Encoder MFT"})
	if !ok || prof.Width != 1280 || prof.Height != 720 || prof.FPS != 30 {
		t.Fatalf("профиль: %+v ok=%v", prof, ok)
	}
	c.choose(native.Info{Encoder: "Microsoft H.264 Encoder MFT"})
	if len(notices) != 1 || !strings.Contains(notices[0], "качество снижено") || !strings.Contains(notices[0], "1280x720") {
		t.Fatalf("уведомления: %q", notices)
	}
	if len(logs) == 0 {
		t.Error("причина должна попасть в журнал")
	}

	// Явные флаги важнее: размер и частота остаются, уведомления нет.
	notices = nil
	o2 := mirrorOptions{width: 1920, height: 1080, fps: 60, onNotice: o.onNotice}
	c2 := newProfileChooser(1920, 1080, 60, o2, logf)
	c2.cores = 4
	if _, ok := c2.choose(native.Info{}); ok {
		t.Fatal("явные флаги пользователя не должны меняться")
	}
	if len(notices) != 0 {
		t.Fatalf("уведомление не нужно: %q", notices)
	}
}
