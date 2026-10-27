package main

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/native"
)

// quietDiagnostics отключает настоящую диагностику видеокарт при ошибке захвата
// (она опрашивает видеокарты и кодеры, что в тестах не нужно).
func quietDiagnostics(t *testing.T) {
	t.Helper()
	old := logCaptureDiagnostics
	logCaptureDiagnostics = func(func(string, ...any), error, int) {}
	t.Cleanup(func() { logCaptureDiagnostics = old })
}

// capturedLog собирает вывод log на время теста.
type capturedLog struct {
	mu sync.Mutex
	sb strings.Builder
}

func (c *capturedLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sb.Write(p)
}

func (c *capturedLog) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sb.String()
}

func captureLog(t *testing.T) *capturedLog {
	t.Helper()
	l := &capturedLog{}
	log.SetOutput(l)
	t.Cleanup(func() {
		log.SetOutput(io.Discard)
		if t.Failed() {
			t.Logf("журнал:\n%s", l.String())
		}
	})
	return l
}

// withCapturingVideo подменяет нативный источник видео и запоминает его параметры.
func withCapturingVideo(t *testing.T) *native.Config {
	t.Helper()
	quietDiagnostics(t)
	var got native.Config
	var mu sync.Mutex
	old, oldFF := startNativeSource, startVideoSource
	startNativeSource = func(_ context.Context, cfg native.Config) (airplay.VideoSource, error) {
		mu.Lock()
		got = cfg
		mu.Unlock()
		return &tickingSource{}, nil
	}
	startVideoSource = func(context.Context, ffmpeg.Config) (airplay.VideoSource, error) { return &tickingSource{}, nil }
	t.Cleanup(func() { startNativeSource, startVideoSource = old, oldFF })
	return &got
}

// runAuto запускает mirror в режиме auto (размер, частота и битрейт — по умолчанию).
func runAuto(t *testing.T, server *airplay.ReceiverServer, creds string, mutate func(*mirrorOptions)) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o := mirrorOptions{host: server.Addr().String(), compat: "auto", noAudio: true, credFile: creds, stdin: strings.NewReader("")}
	if mutate != nil {
		mutate(&o)
	}
	// Показ останавливаем, как только приёмник получил первый кадр (PTP-часам
	// профиля modern нужно время на согласование).
	base := server.Stats().VideoPackets
	done := make(chan error, 1)
	go func() { done <- mirror(ctx, o) }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-tick.C:
			if server.Stats().VideoPackets > base {
				cancel()
				return <-done
			}
		}
	}
}

// Режим auto выбирает профиль по ответу приёмника: UxPlay и Apple TV 3 (стек
// 220.68) идут по проверенному пути UxPlay, Apple TV 4K (HAP), Roku, LG и AirServer
// — по универсальному пути ядра. Во всех случаях видео доходит и расшифровывается.
func TestMirrorAutoPicksProfileByReceiver(t *testing.T) {
	cases := []struct {
		profile     airplay.ReceiverProfile
		wantProfile string // профиль в журнале
		wantHAP     bool
	}{
		{airplay.ReceiverProfileUxPlay, "профиль: uxplay", false},
		{airplay.ReceiverProfileUxPlayAndroid, "профиль: uxplay", false},
		{airplay.ReceiverProfileAppleTV3, "профиль: uxplay", false},
		{airplay.ReceiverProfileModern, "профиль: generic", true},
		{airplay.ReceiverProfileRoku, "профиль: generic", false},
		{airplay.ReceiverProfileLG, "профиль: generic", true},
		{airplay.ReceiverProfileAirServer, "профиль: generic", false},
	}
	for _, tc := range cases {
		t.Run(string(tc.profile), func(t *testing.T) {
			withCapturingVideo(t)
			logs := captureLog(t)
			server := startTestReceiver(t, airplay.ReceiverConfig{Profile: tc.profile})
			if err := runAuto(t, server, filepath.Join(t.TempDir(), "credentials.json"), nil); err != nil {
				t.Fatalf("auto с профилем %s: %v\n%s", tc.profile, err, logs.String())
			}
			requireVideoDelivered(t, server, tc.profile)
			if !strings.Contains(logs.String(), tc.wantProfile) {
				t.Fatalf("в журнале нет %q:\n%s", tc.wantProfile, logs.String())
			}
			if tc.wantHAP != strings.Contains(logs.String(), "протокол: HAP") {
				t.Fatalf("ожидалось HAP=%v:\n%s", tc.wantHAP, logs.String())
			}
		})
	}
}

// Apple TV с кодом на экране: сопряжение HAP, код из четырёх цифр. Неверный код
// не прерывает показ: окно кода открывается снова (wrong=true), приёмник
// выдаёт новый код, и правильный код подключает. Потом хватает сохранённых ключей.
func TestMirrorAutoAppleTVWithOnScreenPIN(t *testing.T) {
	withCapturingVideo(t)
	logs := captureLog(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileModern, Auth: airplay.ReceiverAuthPIN, Code: "4827"})
	creds := filepath.Join(t.TempDir(), "credentials.json")

	var asked []bool
	answers := []string{"0000", "4827"}
	err := runAuto(t, server, creds, func(o *mirrorOptions) {
		o.pinPrompt = func(_ context.Context, wrong bool) (string, error) {
			asked = append(asked, wrong)
			if len(answers) == 0 {
				return "", errors.New("лишний запрос кода")
			}
			a := answers[0]
			answers = answers[1:]
			return a, nil
		}
	})
	if err != nil {
		t.Fatalf("Apple TV с кодом: %v\n%s", err, logs.String())
	}
	if len(asked) != 2 || asked[0] || !asked[1] {
		t.Fatalf("запросы кода (wrong): %v, ожидали [false true]", asked)
	}
	requireVideoDelivered(t, server, airplay.ReceiverProfileModern)
	if stats := server.Stats(); stats.PINStarts < 2 {
		t.Fatalf("приёмнику нужно было показать код дважды: %+v", stats)
	}
	if !strings.Contains(logs.String(), "протокол: HAP") || !strings.Contains(logs.String(), "профиль: generic") {
		t.Fatalf("ожидалось HAP и профиль generic:\n%s", logs.String())
	}

	// Второй запуск: ключи сохранены, код не нужен.
	before := server.Stats()
	if err := runAuto(t, server, creds, func(o *mirrorOptions) {
		o.pinPrompt = func(context.Context, bool) (string, error) {
			return "", errors.New("код не должен запрашиваться")
		}
	}); err != nil {
		t.Fatalf("повторный запуск с сохранёнными ключами: %v", err)
	}
	if after := server.Stats(); after.PINStarts != before.PINStarts || after.VideoPackets <= before.VideoPackets {
		t.Fatalf("повторный запуск: %+v -> %+v", before, after)
	}
}

// Отмена окна кода у Apple TV прерывает показ без повторов.
func TestMirrorAutoAppleTVPINCanceled(t *testing.T) {
	withCapturingVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileModern, Auth: airplay.ReceiverAuthPIN, Code: "4827"})
	cancelErr := errors.New("отмена")
	err := runAuto(t, server, filepath.Join(t.TempDir(), "credentials.json"), func(o *mirrorOptions) {
		o.pinPrompt = func(context.Context, bool) (string, error) { return "", cancelErr }
	})
	if !errors.Is(err, cancelErr) {
		t.Fatalf("ожидалась отмена кода, получено: %v", err)
	}
}

// Apple TV без кода (транзиентное сопряжение) подключается сразу, окно кода не открывается.
func TestMirrorAutoAppleTVTransientPairingNeedsNoPIN(t *testing.T) {
	withCapturingVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileModern})
	err := runAuto(t, server, filepath.Join(t.TempDir(), "credentials.json"), func(o *mirrorOptions) {
		o.pinPrompt = func(context.Context, bool) (string, error) { return "", errors.New("код не нужен") }
	})
	if err != nil {
		t.Fatalf("транзиентное сопряжение: %v", err)
	}
	requireVideoDelivered(t, server, airplay.ReceiverProfileModern)
}

// По умолчанию кодируется холст приёмника, но не больше 1920x1080 и 60 к/с;
// битрейт — по формуле ширина*высота*fps/15000 (1080p60 — около 8,3 Мбит/с).
func TestMirrorDefaultsToReceiverCanvasUpTo1080p60(t *testing.T) {
	cases := []struct {
		name         string
		display      [2]int // холст приёмника; 0 — как у профиля (1920x1080)
		mutate       func(*mirrorOptions)
		wantW, wantH int
		wantFPS      int
	}{
		{"холст 1080p, maxFPS 60", [2]int{}, nil, 1920, 1080, 60},
		{"холст 4K ограничен 1080p", [2]int{3840, 2160}, nil, 1920, 1080, 60},
		{"холст 720p не увеличивается", [2]int{1280, 720}, nil, 1280, 720, 60},
		{"ручной размер и частота", [2]int{}, func(o *mirrorOptions) { o.width, o.height, o.fps = 1280, 720, 30 }, 1280, 720, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withCapturingVideo(t)
			cfg := airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay}
			if tc.display != [2]int{} {
				cfg.DisplayWidth, cfg.DisplayHeight = tc.display[0], tc.display[1]
			}
			server := startTestReceiver(t, cfg)
			if err := runAuto(t, server, filepath.Join(t.TempDir(), "credentials.json"), tc.mutate); err != nil {
				t.Fatal(err)
			}
			if got.Width != tc.wantW || got.Height != tc.wantH || got.FPS != tc.wantFPS {
				t.Fatalf("кодируем %dx%d@%d, ожидали %dx%d@%d", got.Width, got.Height, got.FPS, tc.wantW, tc.wantH, tc.wantFPS)
			}
		})
	}
}

func TestBitrateFormulaGives8Mbit1080p60(t *testing.T) {
	if got := ffmpeg.AutoBitrateKbps(1920, 1080, 60); got < 8200 || got > 8400 {
		t.Fatalf("1080p60: %d кбит/с, ожидалось около 8300 (проверено владельцем)", got)
	}
	if got := ffmpeg.AutoBitrateKbps(1280, 720, 30); got != 2500 {
		t.Fatalf("720p30: %d кбит/с (минимум 2500)", got)
	}
}

func TestChooseVideoSize(t *testing.T) {
	cases := []struct {
		canvasW, canvasH, flagW, flagH int
		wantW, wantH                   int
	}{
		{1920, 1080, 0, 0, 1920, 1080},
		{3840, 2160, 0, 0, 1920, 1080},
		{2560, 1440, 0, 0, 1920, 1080},
		{4096, 2160, 0, 0, 1920, 1012}, // пропорции сохраняются, размер чётный
		{1280, 720, 0, 0, 1280, 720},
		{1366, 768, 0, 0, 1366, 768},
		{0, 0, 0, 0, 1280, 720}, // холст не назван
		{1920, 1080, 1280, 720, 1280, 720},
		{1920, 1080, 3841, 2161, 3840, 2160}, // ручной размер чётный и не ограничивается
	}
	for _, c := range cases {
		w, h := chooseVideoSize(c.canvasW, c.canvasH, c.flagW, c.flagH)
		if w != c.wantW || h != c.wantH {
			t.Errorf("холст %dx%d, флаги %dx%d: %dx%d, ожидали %dx%d", c.canvasW, c.canvasH, c.flagW, c.flagH, w, h, c.wantW, c.wantH)
		}
	}
	for _, bad := range [][2]int{{1280, 0}, {0, 720}, {-1, 720}} {
		if checkVideoSizeFlags(bad[0], bad[1]) == nil {
			t.Errorf("флаги %v должны отвергаться", bad)
		}
	}
	if checkVideoSizeFlags(0, 0) != nil || checkVideoSizeFlags(1280, 720) != nil {
		t.Error("0x0 и полный размер допустимы")
	}
}

// Профиль UxPlay, выбранный по /info, снимается, если приёмник всё же сопрягся по HAP
// (иначе профиль UxPlay запрещает канал HAP). Здесь приёмнику Apple TV 4K профиль
// UxPlay навязан явно: режим uxplay сообщает об ошибке, а auto — нет.
func TestMirrorExplicitUxPlayRejectsHAPReceiver(t *testing.T) {
	withCapturingVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileModern})
	err := runAuto(t, server, filepath.Join(t.TempDir(), "credentials.json"), func(o *mirrorOptions) { o.compat = "uxplay" })
	if err == nil {
		t.Fatal("явный --compat=uxplay с Apple TV 4K должен сообщать об ошибке (канал HAP запрещён)")
	}
}

// requireVideoDelivered: видео дошло до приёмника. Расшифровку приёмник проверяет
// только у профилей с legacy FairPlay (AES-CTR); у HAP и остальных он считает
// принятые пакеты.
func requireVideoDelivered(t *testing.T, server *airplay.ReceiverServer, profile airplay.ReceiverProfile) {
	t.Helper()
	switch profile {
	case airplay.ReceiverProfileUxPlay, airplay.ReceiverProfileUxPlayAndroid, airplay.ReceiverProfileAppleTV3:
		requireDecryptedVideo(t, server)
	default:
		if stats := server.Stats(); stats.VideoPackets == 0 || stats.VideoCryptoErrors != 0 {
			t.Fatalf("видео: пакетов=%d ошибок=%d", stats.VideoPackets, stats.VideoCryptoErrors)
		}
	}
}
