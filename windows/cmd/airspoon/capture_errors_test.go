package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/mf"
	"airspoon/internal/trayctl"
	"airspoon/internal/win/com"
)

func singleGPUUnsupported() error {
	return captureError{&dxgi.UnsupportedError{Adapter: "Microsoft Basic Render Driver", Monitor: `\.\DISPLAY1`,
		Hybrid: false, Err: &com.Error{Op: "DuplicateOutput", HR: 0x887A0004}}}
}

func TestClassifyCaptureError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"нет Media Foundation", fmt.Errorf("видеокодер H.264 не запущен: %w", mf.ErrUnavailable), trayctl.ErrNoMediaFoundation},
		{"HDR", fmt.Errorf("захват экрана: %w", &dxgi.FrameFormatError{Format: 10}), trayctl.ErrCaptureHDR},
		{"одна видеокарта", singleGPUUnsupported(), trayctl.ErrCaptureSession},
		{"гибрид не трогаем", unsupportedErr(), nil},
		{"захват остановился", fmt.Errorf("захват экрана: %w", &dxgi.StaleError{Monitor: "m", Adapter: "a"}), trayctl.ErrCaptureStopped},
		{"прочее", errors.New("EOF"), nil},
		{"nil", nil, nil},
	}
	for _, tc := range tests {
		got := classifyCaptureError(tc.err)
		if !errors.Is(got, tc.want) || (tc.want == nil) != (got == nil) {
			t.Errorf("%s: получили %v, ждали %v", tc.name, got, tc.want)
		}
	}
}

// Показ из трея: на одной видеокарте смена карты не предлагается, перезапуска
// нет, пользователь получает текст про сеанс.
func TestStreamerSingleGPUUnsupportedDoesNotRestart(t *testing.T) {
	asked := false
	s := &mirrorStreamer{credFile: "c.json", encoder: "auto",
		run:           func(context.Context, mirrorOptions) error { return singleGPUUnsupported() },
		onUnsupported: func(trayctl.Target, error) bool { asked = true; return true }}
	err := s.Stream(context.Background(), trayctl.Target{Device: trayctl.Device{Name: "ТВ", Addr: "10.0.0.2:7000"}}, trayctl.Hooks{})
	if asked {
		t.Fatal("перезапуск для смены видеокарты на одной карте не нужен")
	}
	if !errors.Is(err, trayctl.ErrCaptureSession) || !errors.Is(err, dxgi.ErrDuplicateUnsupported) {
		t.Fatalf("ошибка: %v", err)
	}
	if msg := trayctl.UserMessage(err, "ТВ"); !strings.Contains(msg, "в этом сеансе") {
		t.Fatalf("сообщение: %q", msg)
	}
}

func TestStreamerMapsMediaFoundationAndHDR(t *testing.T) {
	cases := map[string]struct {
		err  error
		want error
	}{
		"mf":  {captureError{fmt.Errorf("видеокодер H.264 не запущен: %w", mf.ErrUnavailable)}, trayctl.ErrNoMediaFoundation},
		"hdr": {fmt.Errorf("передача видео: %w", &dxgi.FrameFormatError{Format: 10}), trayctl.ErrCaptureHDR},
	}
	for name, tc := range cases {
		err := tc.err
		s := &mirrorStreamer{credFile: "c.json", encoder: "auto", run: func(context.Context, mirrorOptions) error { return err }}
		got := s.Stream(context.Background(), trayctl.Target{Device: trayctl.Device{Name: "ТВ", Addr: "10.0.0.2:7000"}}, trayctl.Hooks{})
		if !errors.Is(got, tc.want) || !errors.Is(got, trayctl.ErrCapture) {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestCLICaptureAdvice(t *testing.T) {
	var out []string
	say := func(s string) { out = append(out, s) }
	cliCaptureAdvice(fmt.Errorf("запуск: %w", mf.ErrUnavailable), say)
	cliCaptureAdvice(&dxgi.FrameFormatError{Format: 10}, say)
	cliCaptureAdvice(singleGPUUnsupported(), say)
	cliCaptureAdvice(errors.New("EOF"), say)
	if len(out) != 3 {
		t.Fatalf("строк совета %d: %q", len(out), out)
	}
	for i, want := range []string{"Пакет мультимедийных функций", "HDR", "в этом сеансе"} {
		if !strings.Contains(out[i], want) {
			t.Errorf("совет %d %q без %q", i, out[i], want)
		}
	}
}

func TestWarnIgnoredEncoderFlags(t *testing.T) {
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }

	warnIgnoredEncoderFlags(mirrorOptions{encoder: "ffmpeg", noGPUConvert: true}, logf)
	if len(logged) != 1 || !strings.Contains(logged[0], "--no-gpu-convert") {
		t.Fatalf("при --encoder=ffmpeg ожидалось предупреждение о --no-gpu-convert, получено %q", logged)
	}
	logged = nil
	warnIgnoredEncoderFlags(mirrorOptions{encoder: "ffmpeg"}, logf)
	warnIgnoredEncoderFlags(mirrorOptions{encoder: "hw", noGPUConvert: true}, logf)
	warnIgnoredEncoderFlags(mirrorOptions{noGPUConvert: true}, logf)
	if len(logged) != 0 {
		t.Fatalf("лишнее предупреждение: %q", logged)
	}
}

func TestIsFatalCaptureError(t *testing.T) {
	for _, err := range []error{dxgi.ErrFrameFormat, fmt.Errorf("кадр: %w", dxgi.ErrFrameFormat), mf.ErrUnavailable} {
		if !isFatalCaptureError(err) {
			t.Errorf("%v должна быть фатальной", err)
		}
	}
	for _, err := range []error{nil, errors.New("ACCESS_LOST"), dxgi.ErrCaptureStale} {
		if isFatalCaptureError(err) {
			t.Errorf("%v не должна быть фатальной", err)
		}
	}
}

func TestNotifyAudioLost(t *testing.T) {
	notifyAudioLost(nil) // консольный режим: не падает
	var got string
	notifyAudioLost(func(s string) { got = s })
	if !strings.Contains(got, "Звук не передаётся") {
		t.Fatalf("уведомление = %q", got)
	}
}
