package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/trayctl"
)

func TestDevicesFromAdvertsFiltersAndDedupes(t *testing.T) {
	// Бит 7 features — «показ экрана».
	screen := uint64(1) << 7
	found := []airplay.AirPlayDevice{
		{Name: "Телевизор", IP: "10.0.0.2", Port: 7000, DeviceID: "AA:01", Features: screen},
		{Name: "Телевизор", IP: "10.0.0.2", Port: 7000, DeviceID: "aa:01", Features: screen}, // тот же по другому интерфейсу (регистр как в /info)
		{Name: "Колонка", IP: "10.0.0.3", Port: 7000, DeviceID: "AA:02", Features: 0},        // без показа экрана
		{Name: "", IP: "10.0.0.4", Port: 7100, Features: screen},                             // без имени и ID
		{Name: "Без адреса", Features: screen},
	}
	got := devicesFromAdverts(found)
	if len(got) != 2 {
		t.Fatalf("устройств %d, ожидалось 2: %+v", len(got), got)
	}
	if got[0].Name != "Телевизор" || got[0].Addr != "10.0.0.2:7000" || got[0].ID != "aa:01" {
		t.Errorf("первое: %+v", got[0])
	}
	if got[1].Name != "10.0.0.4" || got[1].Addr != "10.0.0.4:7100" || got[1].ID != "10.0.0.4:7100" {
		t.Errorf("второе: %+v", got[1])
	}
	if _, ok := got[0].Data.(*airplay.AirPlayDevice); !ok {
		t.Error("в Data нет записи mDNS")
	}
}

func TestClassifyStreamError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"отмена кода", fmt.Errorf("ввод PIN: %w", trayctl.ErrPINCanceled), trayctl.ErrPINCanceled},
		{"неверный код", fmt.Errorf("сопряжение с PIN: %w", airplay.ErrLegacyPINRejected), trayctl.ErrPINRejected},
		{"неверный код Apple TV", fmt.Errorf("сопряжение с PIN: %w", airplay.ErrHAPPINRejected), trayctl.ErrPINRejected},
		{"захват", fmt.Errorf("SETUP: %w", captureError{errors.New("DXGI")}), trayctl.ErrCapture},
		{"сеть", fmt.Errorf("подключение: %w", &net.OpError{Op: "dial", Err: errors.New("refused")}), trayctl.ErrUnreachable},
		{"таймаут", fmt.Errorf("подключение: %w", context.DeadlineExceeded), trayctl.ErrUnreachable},
	}
	for _, c := range cases {
		got := classifyStreamError(c.in)
		if c.want == nil {
			if got != nil {
				t.Errorf("%s: %v", c.name, got)
			}
			continue
		}
		if !errors.Is(got, c.want) {
			t.Errorf("%s: %v не содержит %v", c.name, got, c.want)
		}
	}
	other := errors.New("что-то иное")
	if got := classifyStreamError(other); got != other {
		t.Errorf("прочие ошибки должны проходить без изменений: %v", got)
	}
}

func TestStreamerBuildsOptionsFromTarget(t *testing.T) {
	var got mirrorOptions
	s := &mirrorStreamer{credFile: "c.json", encoder: "auto", run: func(_ context.Context, o mirrorOptions) error {
		got = o
		return nil
	}}
	advert := &airplay.AirPlayDevice{Name: "ТВ", IP: "10.0.0.2", Port: 7000}
	hooks := trayctl.Hooks{Streaming: func() {}, AskPIN: func(context.Context, bool) (string, error) { return "", nil }}

	_ = s.Stream(context.Background(), trayctl.Target{Device: trayctl.Device{Name: "ТВ", Addr: "10.0.0.2:7000", Data: advert}, Audio: true}, hooks)
	if got.advert != advert || got.host != "" || got.noAudio || got.compat != "auto" || got.pinPrompt == nil || got.onStreaming == nil {
		t.Fatalf("параметры для устройства из списка: %+v", got)
	}
	// Размер и частота кадров не заданы: по умолчанию их выбирает холст приёмника (не выше 1080p60).
	if got.width != 0 || got.height != 0 || got.fps != 0 || got.bitrate != 0 {
		t.Fatalf("по умолчанию размер, частота и битрейт должны выбираться автоматически: %+v", got)
	}
	_ = s.Stream(context.Background(), trayctl.Target{Device: trayctl.Device{Name: "x", Addr: "127.0.0.1:7100"}, Audio: false}, hooks)
	if got.advert != nil || got.host != "127.0.0.1:7100" || !got.noAudio {
		t.Fatalf("параметры для адреса: %+v", got)
	}
}

// --- контроллер против тестового приёмника (настоящее сопряжение и видео) ---

type guiUI struct {
	mu      sync.Mutex
	notices []string
	pins    []string // ответы на запросы кода
	asked   []bool   // флаги wrong
	cancel  bool
}

func (u *guiUI) Notify(_ trayctl.NoticeKind, _, text string) {
	u.mu.Lock()
	u.notices = append(u.notices, text)
	u.mu.Unlock()
}
func (u *guiUI) Changed() {}
func (u *guiUI) AskPIN(_ context.Context, wrong bool) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked = append(u.asked, wrong)
	if u.cancel || len(u.pins) == 0 {
		return "", trayctl.ErrPINCanceled
	}
	p := u.pins[0]
	u.pins = u.pins[1:]
	return p, nil
}
func (u *guiUI) AskAddress(context.Context, string) (string, bool) { return "", false }
func (u *guiUI) Quit()                                             {}
func (u *guiUI) has(sub string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, n := range u.notices {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

type noDevices struct{}

func (noDevices) Discover(context.Context) ([]trayctl.Device, error) { return nil, nil }

func newGUIController(t *testing.T, ui *guiUI, creds string) *trayctl.Controller {
	t.Helper()
	streamer := &mirrorStreamer{credFile: creds, encoder: "auto", run: func(ctx context.Context, o mirrorOptions) error {
		return mirror(ctx, o)
	}}
	c := trayctl.New(trayctl.Config{
		Discoverer: noDevices{}, Streamer: streamer, UI: ui, Audio: false,
		RetryDelay: func(int) time.Duration { return 10 * time.Millisecond },
	})
	t.Cleanup(func() { c.Shutdown(5 * time.Second) })
	return c
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func TestControllerStreamsToTestReceiverByAddress(t *testing.T) {
	withFakeVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay})
	ui := &guiUI{}
	c := newGUIController(t, ui, t.TempDir()+"/credentials.json")

	if err := c.ConnectAddress(server.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "показ идёт", func() bool { return c.Snapshot().Phase == trayctl.Streaming })
	waitUntil(t, "кадры дошли", func() bool { return server.Stats().VideoDecrypted > 0 })
	if server.Stats().VideoCryptoErrors != 0 {
		t.Fatalf("ошибки расшифровки: %+v", server.Stats())
	}
	if !ui.has("Подключаюсь к") || !ui.has("Экран показывается на") {
		t.Fatalf("уведомления: %q", ui.notices)
	}
	c.Stop()
	waitUntil(t, "остановка", func() bool { return c.Snapshot().Phase == trayctl.Idle })
}

func TestControllerPINFlowAgainstTestReceiver(t *testing.T) {
	withFakeVideo(t)
	for _, hide := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidePINStatus=%v", hide), func(t *testing.T) {
			server := startTestReceiver(t, airplay.ReceiverConfig{
				Profile: airplay.ReceiverProfileUxPlayAndroid, Auth: airplay.ReceiverAuthPIN, Code: "4827", HidePINStatus: hide,
			})
			// Сначала неверный код, затем верный: окно кода должно открыться дважды.
			ui := &guiUI{pins: []string{"0000", "4827"}}
			creds := t.TempDir() + "/credentials.json"
			c := newGUIController(t, ui, creds)
			if err := c.ConnectAddress(server.Addr().String()); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "показ идёт", func() bool { return c.Snapshot().Phase == trayctl.Streaming })
			ui.mu.Lock()
			asked := fmt.Sprint(ui.asked)
			ui.mu.Unlock()
			if asked != "[false true]" {
				t.Fatalf("запросы кода (wrong): %s, ожидалось [false true]", asked)
			}
			if stats := server.Stats(); stats.PINStarts < 2 {
				t.Fatalf("после неверного кода приёмник должен показать новый: %+v", stats)
			}
			waitUntil(t, "кадры дошли", func() bool { return server.Stats().VideoDecrypted > 0 })
			c.Stop()
			waitUntil(t, "остановка", func() bool { return c.Snapshot().Phase == trayctl.Idle })

			// Повторное подключение: ключи сохранены, код больше не нужен.
			ui.mu.Lock()
			ui.asked = nil
			ui.mu.Unlock()
			if err := c.ConnectAddress(server.Addr().String()); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "второй показ", func() bool { return c.Snapshot().Phase == trayctl.Streaming })
			ui.mu.Lock()
			n := len(ui.asked)
			ui.mu.Unlock()
			if n != 0 {
				t.Fatalf("при сохранённых ключах код запрошен %d раз", n)
			}
		})
	}
}

func TestControllerPINCancelEndsQuietlyAgainstTestReceiver(t *testing.T) {
	withFakeVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{
		Profile: airplay.ReceiverProfileUxPlayAndroid, Auth: airplay.ReceiverAuthPIN, Code: "4827",
	})
	ui := &guiUI{cancel: true}
	c := newGUIController(t, ui, t.TempDir()+"/credentials.json")
	if err := c.ConnectAddress(server.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "возврат в покой", func() bool {
		ui.mu.Lock()
		asked := len(ui.asked)
		ui.mu.Unlock()
		return asked == 1 && c.Snapshot().Phase == trayctl.Idle
	})
	if ui.has("Не получилось") || ui.has("Код не подошёл") {
		t.Fatalf("отмена кода не должна давать ошибку: %q", ui.notices)
	}
	if server.Stats().VideoDecrypted != 0 {
		t.Fatal("видео пошло без кода")
	}
}

func TestControllerReportsUnreachableReceiverInRussian(t *testing.T) {
	withFakeVideo(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // порт закрыт: «connection refused»
	ui := &guiUI{}
	c := newGUIController(t, ui, t.TempDir()+"/credentials.json")
	if err := c.ConnectAddress(addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "сообщение об ошибке", func() bool { return ui.has("Не получилось подключиться к") })
	waitUntil(t, "покой", func() bool { return c.Snapshot().Phase == trayctl.Idle })
	for _, n := range ui.notices {
		for _, bad := range []string{"dial", "tcp", "refused", "connect"} {
			if strings.Contains(n, bad) {
				t.Fatalf("технические слова в уведомлении: %q", n)
			}
		}
	}
}

// Настоящий обрыв: приёмник закрывает сокеты (как выключенный телевизор или
// пропавший Wi-Fi), показ не отменяется контекстом. Потом приёмник снова
// появляется на том же адресе, и программа сама восстанавливает показ.
func TestControllerRecoversAfterRealReceiverDisconnect(t *testing.T) {
	withFakeVideo(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay,
		PairingPrivateKey: privateKey, PairingID: testReceiverDeviceID}
	first := startTestReceiver(t, cfg)
	addr := first.Addr().String()
	ui := &guiUI{}
	streamer := &mirrorStreamer{credFile: t.TempDir() + "/credentials.json", encoder: "auto", run: mirror}
	c := trayctl.New(trayctl.Config{
		Discoverer: noDevices{}, Streamer: streamer, UI: ui,
		MaxRetries: 8,
		RetryDelay: func(int) time.Duration { return 300 * time.Millisecond },
	})
	t.Cleanup(func() { c.Shutdown(5 * time.Second) })

	if err := c.ConnectAddress(addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "показ идёт", func() bool { return c.Snapshot().Phase == trayctl.Streaming })
	waitUntil(t, "кадры дошли", func() bool { return first.Stats().VideoDecrypted > 0 })

	// Обрыв: закрываются все соединения приёмника, контекст показа не трогаем.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "сообщение об обрыве", func() bool { return ui.has("прервалась") })

	// Приёмник снова на том же адресе (порт может освободиться не сразу).
	var second *airplay.ReceiverServer
	for deadline := time.Now().Add(5 * time.Second); second == nil; {
		s, err := tryStartTestReceiverAt(t, cfg, addr)
		if err == nil {
			second = s
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("не удалось поднять приёмник на %s: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	waitUntil(t, "восстановление связи", func() bool { return ui.has("Связь восстановлена") })
	waitUntil(t, "кадры дошли до приёмника после восстановления", func() bool { return second.Stats().VideoDecrypted > 0 })
	if c.Snapshot().Phase != trayctl.Streaming {
		t.Fatalf("после восстановления показ не идёт: %+v", c.Snapshot())
	}
}

func TestControllerRetriesAfterReceiverDropsConnection(t *testing.T) {
	withFakeVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay})
	ui := &guiUI{}
	var runs atomic.Int64
	streamer := &mirrorStreamer{credFile: t.TempDir() + "/credentials.json", encoder: "auto", run: func(ctx context.Context, o mirrorOptions) error {
		n := runs.Add(1)
		if n == 1 {
			// Первый показ: сами обрываем его через долю секунды после старта кадров.
			sub, cancel := context.WithCancel(ctx)
			defer cancel()
			orig := o.onStreaming
			o.onStreaming = func() {
				orig()
				time.AfterFunc(700*time.Millisecond, cancel)
			}
			err := mirror(sub, o)
			if ctx.Err() == nil {
				return errors.New("read: connection reset by peer")
			}
			return err
		}
		return mirror(ctx, o)
	}}
	c := trayctl.New(trayctl.Config{
		Discoverer: noDevices{}, Streamer: streamer, UI: ui,
		RetryDelay: func(int) time.Duration { return 10 * time.Millisecond },
	})
	t.Cleanup(func() { c.Shutdown(5 * time.Second) })
	if err := c.ConnectAddress(server.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "повтор и восстановление", func() bool { return ui.has("Связь восстановлена") })
	if !ui.has("прервалась") || runs.Load() != 2 {
		t.Fatalf("уведомления %q, запусков %d", ui.notices, runs.Load())
	}
}
