package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/native"
)

func TestParseEncoder(t *testing.T) {
	cases := []struct {
		value  string
		ffmpeg bool
		mode   native.Mode
		bad    bool
	}{
		{"", false, native.ModeAuto, false},
		{"auto", false, native.ModeAuto, false},
		{"hw", false, native.ModeHardware, false},
		{"sw", false, native.ModeSoftware, false},
		{"ffmpeg", true, native.ModeAuto, false},
		{"nvenc", false, native.ModeAuto, true},
	}
	for _, c := range cases {
		useFFmpeg, mode, err := parseEncoder(c.value)
		if (err != nil) != c.bad {
			t.Errorf("%q: ошибка %v, ожидалась ошибка=%t", c.value, err, c.bad)
			continue
		}
		if !c.bad && (useFFmpeg != c.ffmpeg || mode != c.mode) {
			t.Errorf("%q: ffmpeg=%t mode=%v, ожидалось ffmpeg=%t mode=%v", c.value, useFFmpeg, mode, c.ffmpeg, c.mode)
		}
	}
}

func TestRunMirrorRejectsUnknownEncoder(t *testing.T) {
	if code := runMirror([]string{"--host", "127.0.0.1:1", "--encoder", "nvenc"}); code != 2 {
		t.Fatalf("код возврата %d, ожидалось 2", code)
	}
}

// Выбор источника по --encoder: ffmpeg запускает ffmpeg-источник, остальные
// значения — нативный источник с нужным режимом и параметрами.
func TestMirrorChoosesVideoSourceByEncoder(t *testing.T) {
	cases := []struct {
		encoder    string
		wantFFmpeg bool
		wantMode   native.Mode
	}{
		{"", false, native.ModeAuto},
		{"auto", false, native.ModeAuto},
		{"hw", false, native.ModeHardware},
		{"sw", false, native.ModeSoftware},
		{"ffmpeg", true, native.ModeAuto},
	}
	for _, c := range cases {
		t.Run("encoder="+c.encoder, func(t *testing.T) {
			var gotNative []native.Config
			ffmpegStarts := 0
			oldFF, oldNative := startVideoSource, startNativeSource
			startVideoSource = func(context.Context, ffmpeg.Config) (airplay.VideoSource, error) {
				ffmpegStarts++
				return &tickingSource{}, nil
			}
			startNativeSource = func(_ context.Context, cfg native.Config) (airplay.VideoSource, error) {
				gotNative = append(gotNative, cfg)
				return &tickingSource{}, nil
			}
			t.Cleanup(func() { startVideoSource, startNativeSource = oldFF, oldNative })

			server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay})
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			_ = mirror(ctx, mirrorOptions{
				host: server.Addr().String(), compat: "uxplay", noAudio: true,
				credFile: filepath.Join(t.TempDir(), "creds.json"),
				width:    1280, height: 720, fps: 30, display: 1, noCursor: true, bitrate: 3000,
				encoder: c.encoder,
			})
			if c.wantFFmpeg {
				if ffmpegStarts == 0 || len(gotNative) != 0 {
					t.Fatalf("ожидался только источник ffmpeg: ffmpeg=%d native=%d", ffmpegStarts, len(gotNative))
				}
				return
			}
			if ffmpegStarts != 0 || len(gotNative) == 0 {
				t.Fatalf("ожидался только нативный источник: ffmpeg=%d native=%d", ffmpegStarts, len(gotNative))
			}
			cfg := gotNative[0]
			if cfg.Mode != c.wantMode || cfg.Display != 1 || cfg.ShowCursor || cfg.BitrateKbps != 3000 ||
				cfg.Width != 1280 || cfg.Height != 720 || cfg.FPS == 0 {
				t.Fatalf("параметры нативного источника: %+v", cfg)
			}
		})
	}
}
