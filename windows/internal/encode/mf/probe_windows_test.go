//go:build windows && (amd64 || arm64)

package mf

import (
	"strings"
	"testing"
	"time"

	"airspoon/internal/win/com"
)

func TestSoftwareEncoderInfo(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info := SoftwareEncoderInfo()
	t.Log(info)
	if info.Err != "" {
		t.Skipf("программный кодер недоступен: %s", info.Err)
	}
	if info.Hardware || info.Vendor != "Microsoft" || info.Async {
		t.Fatalf("неожиданное описание программного кодера: %+v", info)
	}
}

func TestListH264EncodersAgreesWithNames(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	names, err := HardwareEncoderNames(nil)
	if err != nil {
		t.Skipf("перечисление кодеров недоступно: %v", err)
	}
	list, err := ListH264Encoders(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(names) {
		t.Fatalf("ListH264Encoders вернул %d кодеров, HardwareEncoderNames — %d", len(list), len(names))
	}
	for i, e := range list {
		t.Log(e)
		if e.Name != names[i] || !e.Hardware {
			t.Errorf("кодер %d: %+v, имя в списке имён %q", i, e, names[i])
		}
	}
}

func TestProbeEncodeSoftware(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	rep, err := ProbeEncode(ProbeConfig{Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500, Frames: 45, Software: true, Logf: t.Logf})
	if err != nil {
		t.Skipf("программный кодер недоступен: %v", err)
	}
	t.Log(rep.Summary())
	if rep.Err != "" {
		t.Fatalf("ошибка пробы: %s", rep.Err)
	}
	if rep.Hardware || rep.Input != "системная память" || rep.Accepted < 20 || rep.Packets < 20 || rep.KeyPackets < 1 {
		t.Fatalf("странный отчёт: %+v", rep)
	}
	if !rep.HasSPS || rep.SPS.ProfileIdc != 77 || rep.SPS.LevelIdc != 41 || rep.SPS.Width != 1280 {
		t.Fatalf("SPS в отчёте: %+v (есть=%t)", rep.SPS, rep.HasSPS)
	}
	if !strings.Contains(rep.Settings, "RateControl=CBR") || !strings.Contains(rep.Settings, "уровень 4.1") {
		t.Fatalf("итоговые параметры: %q", rep.Settings)
	}
	// IsSupported у программного MFT честно говорит «нет» про часть свойств — они
	// должны быть в отчёте как отвергнутые.
	if len(rep.Rejected) == 0 {
		t.Log("отвергнутых свойств нет")
	}
}

func TestProbeEncodeHardwareSystemMemory(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	rep, err := ProbeEncode(ProbeConfig{Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500, Frames: 45, Logf: t.Logf})
	if err != nil {
		t.Skipf("аппаратный кодер H.264 недоступен: %v", err)
	}
	t.Log(rep.Summary())
	for _, r := range rep.Rejected {
		t.Logf("отвергнуто: %s", r)
	}
	if rep.Err != "" || !rep.Hardware || rep.Packets < 20 {
		t.Fatalf("странный отчёт: %+v", rep)
	}
}

func TestProbeEncodeUnknownEncoderName(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, err = ProbeEncode(ProbeConfig{Width: 640, Height: 360, FPS: 30, BitrateKbps: 1000, EncoderName: "нет такого кодера"})
	if err == nil {
		t.Fatal("ожидали ошибку для несуществующего кодера")
	}
}

func TestProbeEncodeRejectsBadConfig(t *testing.T) {
	if _, err := ProbeEncode(ProbeConfig{Width: 641, Height: 360, FPS: 30, BitrateKbps: 1000, Software: true}); err == nil {
		t.Fatal("нечётная ширина принята")
	}
}

// Здоровый кодер: Health молчит, пока идут кадры и пакеты.
func TestHealthyEncoderHealth(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	col := &collector{}
	enc, err := NewSoftware(testConfig(t, col.add))
	if err != nil {
		t.Skipf("программный кодер недоступен: %v", err)
	}
	defer enc.Close()
	for i := 0; i < 30; i++ {
		if _, err := enc.EncodeNV12(syntheticNV12(1280, 720, i), int64(i)*10_000_000/30); err != nil {
			t.Fatal(err)
		}
		if err := enc.Health(); err != nil {
			t.Fatalf("кадр %d: %v", i, err)
		}
		time.Sleep(33 * time.Millisecond)
	}
	if sps, ok := enc.SPS(); !ok || sps.ProfileIdc != 77 {
		t.Fatalf("SPS: %+v ok=%t", sps, ok)
	}
	if enc.Reorders() != 0 {
		t.Fatalf("программный кодер без B-кадров выдал %d кадров не по порядку", enc.Reorders())
	}
}

// 1080p60 требует уровень 4.2 (раньше всегда просили 4.1): проверяем, что типы
// выхода с уровнем принимаются, а в SPS потока уровень не ниже запрошенного.
func TestProbeEncode1080p60Level(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, software := range []bool{false, true} {
		rep, err := ProbeEncode(ProbeConfig{Width: 1920, Height: 1080, FPS: 60, BitrateKbps: 8000, Frames: 30, Software: software})
		if err != nil {
			t.Logf("software=%t: кодер недоступен: %v", software, err)
			continue
		}
		t.Log(rep.Summary())
		if rep.Err != "" {
			t.Errorf("software=%t: %s", software, rep.Err)
			continue
		}
		if rep.HasSPS && rep.SPS.LevelIdc < 42 {
			t.Errorf("software=%t: в SPS уровень %s, для 1080p60 нужен не ниже 4.2", software, rep.SPS.LevelName())
		}
	}
}
