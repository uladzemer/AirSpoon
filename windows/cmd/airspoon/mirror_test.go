package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/native"
)

// Тестовые NAL-единицы H.264 (SPS, PPS, IDR, P) для поддельного источника кадров.
var (
	testSPS = []byte{0x67, 0x42, 0xc0, 0x1f, 0xda, 0x01, 0x40, 0x16, 0xe9, 0xb8, 0x08, 0x08, 0x08, 0x10}
	testPPS = []byte{0x68, 0xce, 0x3c, 0x80}
	testIDR = []byte{0x65, 0x88, 0x84, 0x00, 0x10, 0xff, 0xfe}
	testP   = []byte{0x41, 0x9a, 0x21, 0x6c, 0x42}
)

const testReceiverDeviceID = "F2:B1:C4:23:11:AF"

func annexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

// tickingSource отдаёт IDR с SPS/PPS, затем P-кадры с шагом 1/30 с.
type tickingSource struct {
	n      int
	closed bool
}

func (s *tickingSource) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	if s.n > 0 {
		select {
		case <-time.After(33 * time.Millisecond):
		case <-ctx.Done():
			return airplay.VideoAccessUnit{}, ctx.Err()
		}
	}
	s.n++
	if s.n == 1 {
		return airplay.VideoAccessUnit{AnnexB: annexB(testSPS, testPPS, testIDR), PTS: time.Now()}, nil
	}
	return airplay.VideoAccessUnit{AnnexB: annexB(testP), PTS: time.Now()}, nil
}

func (s *tickingSource) Close() error { s.closed = true; return nil }

func withFakeVideo(t *testing.T) {
	t.Helper()
	old := startVideoSource
	startVideoSource = func(context.Context, ffmpeg.Config) (airplay.VideoSource, error) {
		return &tickingSource{}, nil
	}
	oldNative := startNativeSource
	startNativeSource = func(context.Context, native.Config) (airplay.VideoSource, error) {
		return &tickingSource{}, nil
	}
	t.Cleanup(func() { startVideoSource = old; startNativeSource = oldNative })
}

func startTestReceiver(t *testing.T, cfg airplay.ReceiverConfig) *airplay.ReceiverServer {
	t.Helper()
	return startTestReceiverAt(t, cfg, "127.0.0.1:0")
}

// startTestReceiverAt запускает тестовый приёмник на заданном адресе (например,
// на адресе только что закрытого приёмника).
func startTestReceiverAt(t *testing.T, cfg airplay.ReceiverConfig, listen string) *airplay.ReceiverServer {
	t.Helper()
	server, err := tryStartTestReceiverAt(t, cfg, listen)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// tryStartTestReceiverAt — то же, но с ошибкой вместо t.Fatal (адрес может быть
// занят, пока ОС не освободила порт).
func tryStartTestReceiverAt(t *testing.T, cfg airplay.ReceiverConfig, listen string) (*airplay.ReceiverServer, error) {
	t.Helper()
	cfg.ListenAddress = listen
	cfg.DeviceID = testReceiverDeviceID
	cfg.Logger = log.New(io.Discard, "", 0)
	server, err := airplay.NewReceiverServer(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = server.Serve(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		<-done
	})
	return server, nil
}

func runMirrorFor(t *testing.T, server *airplay.ReceiverServer, credFile, stdin, pin string) error {
	t.Helper()
	return runMirrorCompat(t, server, "uxplay", credFile, stdin, pin)
}

func runMirrorCompat(t *testing.T, server *airplay.ReceiverServer, compat, credFile, stdin, pin string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return mirror(ctx, mirrorOptions{
		host:     server.Addr().String(),
		compat:   compat,
		noAudio:  true,
		pin:      pin,
		credFile: credFile,
		stdin:    strings.NewReader(stdin),
		width:    1280, height: 720, fps: 30,
	})
}

func requireDecryptedVideo(t *testing.T, server *airplay.ReceiverServer) {
	t.Helper()
	stats := server.Stats()
	if stats.VideoDecrypted == 0 || stats.VideoCryptoErrors != 0 {
		t.Fatalf("видео: расшифровано=%d ошибок=%d", stats.VideoDecrypted, stats.VideoCryptoErrors)
	}
}

// Режим по умолчанию (--compat=uxplay) с приёмником, которому нужен PIN, больше
// не падает после ввода PIN: сопряжение идёт по /pair-setup-pin и остаётся raw.
func TestMirrorUxPlayModeWorksWithPIN(t *testing.T) {
	withFakeVideo(t)
	for _, profile := range []airplay.ReceiverProfile{airplay.ReceiverProfileUxPlay, airplay.ReceiverProfileUxPlayAndroid} {
		t.Run(string(profile), func(t *testing.T) {
			server := startTestReceiver(t, airplay.ReceiverConfig{Profile: profile, Auth: airplay.ReceiverAuthPIN, Code: "4827"})
			creds := filepath.Join(t.TempDir(), "credentials.json")

			if err := runMirrorFor(t, server, creds, "4827\n", ""); err != nil {
				t.Fatalf("первый запуск: %v", err)
			}
			stats := server.Stats()
			if stats.PINStarts != 1 || stats.PairSetup != 3 {
				t.Fatalf("статистика первого запуска = %+v (ожидался 1 старт PIN и 3 шага setup)", stats)
			}
			requireDecryptedVideo(t, server)

			store, err := airplay.NewCredentialStore(creds)
			if err != nil {
				t.Fatal(err)
			}
			saved := store.Lookup(testReceiverDeviceID)
			if saved == nil || !saved.HasPairingCredentials() {
				t.Fatal("ключи сопряжения не сохранены")
			}

			// Повторный запуск: PIN не нужен, достаточно pair-verify по сохранённым ключам.
			before := server.Stats()
			if err := runMirrorFor(t, server, creds, "", ""); err != nil {
				t.Fatalf("повторный запуск с сохранёнными ключами: %v", err)
			}
			after := server.Stats()
			if after.PairSetup != before.PairSetup || after.PairVerify <= before.PairVerify {
				t.Fatalf("повторный запуск: setup %d->%d, verify %d->%d (ожидался только verify)",
					before.PairSetup, after.PairSetup, before.PairVerify, after.PairVerify)
			}
			if after.VideoDecrypted <= before.VideoDecrypted || after.VideoCryptoErrors != 0 {
				t.Fatalf("повторный запуск не передал видео: %+v", after)
			}
		})
	}
}

func TestMirrorWrongPINReportsErrorInsteadOfCompatMessage(t *testing.T) {
	withFakeVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlayAndroid, Auth: airplay.ReceiverAuthPIN, Code: "4827"})
	err := runMirrorFor(t, server, filepath.Join(t.TempDir(), "credentials.json"), "0000\n", "")
	if err == nil || !strings.Contains(err.Error(), "сопряжение с PIN") || strings.Contains(err.Error(), "legacy-протоколу") {
		t.Fatalf("ожидалась ошибка неверного PIN, получено: %v", err)
	}
}

// UxPlay не сообщает о PIN в /info: если PIN задан флагом или переменной, он
// применяется сразу, без запроса на вводе.
func TestMirrorUsesGivenPINWhenReceiverDoesNotAdvertiseIt(t *testing.T) {
	withFakeVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{
		Profile: airplay.ReceiverProfileUxPlayAndroid, Auth: airplay.ReceiverAuthPIN, Code: "4827", HidePINStatus: true,
	})
	if err := runMirrorFor(t, server, filepath.Join(t.TempDir(), "credentials.json"), "", "4827"); err != nil {
		t.Fatalf("запуск с заданным PIN: %v", err)
	}
	if stats := server.Stats(); stats.PairSetup != 3 {
		t.Fatalf("PIN-сопряжение не выполнено: %+v", stats)
	}
	// Запрос /pair-pin-start нужен и при заданном PIN: UxPlay со случайным PIN
	// до него не знает PIN и не показывает его на экране.
	if stats := server.Stats(); stats.PINStarts != 1 {
		t.Fatalf("/pair-pin-start при заданном PIN не вызван: %+v", stats)
	}
	requireDecryptedVideo(t, server)
}

// Неверный PIN, заданный флагом, останавливает сопряжение сразу: ни повторной
// попытки, ни обычного (без PIN) сопряжения, которое настоящий UxPlay пропустил бы.
func TestMirrorWrongGivenPINAbortsWithoutFallback(t *testing.T) {
	withFakeVideo(t)
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	server := startTestReceiver(t, airplay.ReceiverConfig{
		Profile: airplay.ReceiverProfileUxPlayAndroid, Auth: airplay.ReceiverAuthPIN, Code: "4827", HidePINStatus: true,
	})
	err := runMirrorFor(t, server, filepath.Join(t.TempDir(), "credentials.json"), "", "0000")
	if err == nil || !strings.Contains(err.Error(), "PIN") {
		t.Fatalf("ожидалась ошибка неверного PIN, получено: %v", err)
	}
	// Шаги 1 и 2 /pair-setup-pin; после отказа не должно быть ни raw pair-setup, ни pair-verify.
	if stats := server.Stats(); stats.PairSetup != 2 || stats.PairVerify != 0 || stats.SetupRequests != 0 {
		t.Fatalf("после неверного PIN сопряжение продолжилось: %+v", stats)
	}
	for _, bad := range []string{"продолжаем обычным", "без PIN не удалось", "пробуем с PIN"} {
		if strings.Contains(logs.String(), bad) {
			t.Fatalf("в логе есть откат после неверного PIN (%q):\n%s", bad, logs.String())
		}
	}
}

// --compat=generic (прежний «auto») не должен ломаться на UxPlay с набором
// features по умолчанию для Android (бит 27 включён): по этому биту ключ FairPlay
// не смешивается с секретом pair-verify, а UxPlay смешивает его всегда. От Apple
// TV 3 с тем же бит-27, srcvers и моделью такой приёмник по /info не отличить,
// поэтому при отказе SETUP (HTTP 400) один раз повторяется попытка с обратным режимом.
func TestMirrorGenericModeRecoversFairPlayKeyMixing(t *testing.T) {
	for _, tc := range []struct {
		profile   airplay.ReceiverProfile
		wantRetry bool
	}{
		{airplay.ReceiverProfileUxPlayAndroid, true},
		{airplay.ReceiverProfileUxPlay, false},
		{airplay.ReceiverProfileAppleTV3, false},
	} {
		t.Run(string(tc.profile), func(t *testing.T) {
			withFakeVideo(t)
			server := startTestReceiver(t, airplay.ReceiverConfig{Profile: tc.profile})
			creds := filepath.Join(t.TempDir(), "credentials.json")
			if err := runMirrorCompat(t, server, "generic", creds, "", ""); err != nil {
				t.Fatalf("generic: %v", err)
			}
			stats := server.Stats()
			requireDecryptedVideo(t, server)
			if tc.wantRetry != (stats.Connections > 1) {
				t.Fatalf("повтор на новом соединении: ожидался=%v, соединений=%d", tc.wantRetry, stats.Connections)
			}
		})
	}
}
