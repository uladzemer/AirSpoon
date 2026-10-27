package trayctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- заглушки окружения ---

type notice struct {
	kind NoticeKind
	text string
}

type fakeUI struct {
	mu      sync.Mutex
	notices []notice
	changed atomic.Int64
	quit    atomic.Bool

	askPIN     func(ctx context.Context, wrong bool) (string, error)
	askAddress func(ctx context.Context, initial string) (string, bool)
}

func (u *fakeUI) Notify(kind NoticeKind, title, text string) {
	if title != AppName {
		panic("заголовок уведомления: " + title)
	}
	u.mu.Lock()
	u.notices = append(u.notices, notice{kind, text})
	u.mu.Unlock()
}
func (u *fakeUI) Changed() { u.changed.Add(1) }
func (u *fakeUI) AskPIN(ctx context.Context, wrong bool) (string, error) {
	if u.askPIN == nil {
		return "", ErrPINCanceled
	}
	return u.askPIN(ctx, wrong)
}
func (u *fakeUI) AskAddress(ctx context.Context, initial string) (string, bool) {
	if u.askAddress == nil {
		return "", false
	}
	return u.askAddress(ctx, initial)
}
func (u *fakeUI) Quit() { u.quit.Store(true) }

func (u *fakeUI) texts() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for _, n := range u.notices {
		out = append(out, n.text)
	}
	return out
}

func (u *fakeUI) has(sub string) bool {
	for _, t := range u.texts() {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}

type fakeDiscoverer struct {
	mu   sync.Mutex
	list []Device
	err  error
}

func (d *fakeDiscoverer) set(list ...Device) {
	d.mu.Lock()
	d.list = list
	d.mu.Unlock()
}
func (d *fakeDiscoverer) Discover(context.Context) ([]Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Device(nil), d.list...), d.err
}

// scriptedStreamer выполняет сценарии по очереди: каждый вызов Stream берёт
// следующую функцию.
type scriptedStreamer struct {
	mu      sync.Mutex
	script  []func(ctx context.Context, t Target, h Hooks) error
	targets []Target
	calls   atomic.Int64
}

func (s *scriptedStreamer) Stream(ctx context.Context, t Target, h Hooks) error {
	s.mu.Lock()
	i := int(s.calls.Add(1)) - 1
	s.targets = append(s.targets, t)
	var fn func(ctx context.Context, t Target, h Hooks) error
	if i < len(s.script) {
		fn = s.script[i]
	}
	s.mu.Unlock()
	if fn == nil {
		<-ctx.Done()
		return nil
	}
	return fn(ctx, t, h)
}

// streamUntilStop сообщает «пошли кадры» и держит показ до остановки.
func streamUntilStop(started chan<- struct{}) func(context.Context, Target, Hooks) error {
	return func(ctx context.Context, _ Target, h Hooks) error {
		h.Streaming()
		if started != nil {
			started <- struct{}{}
		}
		<-ctx.Done()
		return nil
	}
}

// dropAfterStreaming сообщает «пошли кадры» и тут же обрывается с ошибкой.
func dropAfterStreaming(ctx context.Context, _ Target, h Hooks) error {
	h.Streaming()
	return errors.New("read: connection reset by peer")
}

func failWith(err error) func(context.Context, Target, Hooks) error {
	return func(context.Context, Target, Hooks) error { return err }
}

type env struct {
	ctl *Controller
	ui  *fakeUI
	dis *fakeDiscoverer
	str *scriptedStreamer
	now atomic.Int64
}

func newEnv(t *testing.T, mutate func(*Config), script ...func(context.Context, Target, Hooks) error) *env {
	t.Helper()
	e := &env{ui: &fakeUI{}, dis: &fakeDiscoverer{}, str: &scriptedStreamer{script: script}}
	e.now.Store(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	cfg := Config{
		Discoverer: e.dis, Streamer: e.str, UI: e.ui, Audio: true,
		DiscoverInterval: 10 * time.Millisecond, DiscoverTimeout: time.Second,
		RetryDelay: func(int) time.Duration { return time.Millisecond },
		Now:        func() time.Time { return time.Unix(0, e.now.Load()) },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.ctl = New(cfg)
	t.Cleanup(func() { e.ctl.Shutdown(2 * time.Second) })
	return e
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func waitPhase(t *testing.T, c *Controller, p Phase) {
	t.Helper()
	waitFor(t, fmt.Sprintf("фаза %d", p), func() bool { return c.Snapshot().Phase == p })
}

var (
	tvLiving = Device{ID: "AA:01", Name: "Телевизор", Addr: "192.168.1.20:7000"}
	tvBed    = Device{ID: "AA:02", Name: "Спальня", Addr: "192.168.1.21:7000"}
)

func texts(items []MenuItem) []string {
	var out []string
	for _, it := range items {
		if it.Kind == ItemSeparator {
			out = append(out, "---")
			continue
		}
		s := it.Text
		if it.Checked {
			s = "[x] " + s
		}
		if it.Disabled {
			s = "(" + s + ")"
		}
		out = append(out, s)
	}
	return out
}

// --- меню ---

func TestMenuWithoutDevices(t *testing.T) {
	e := newEnv(t, nil)
	got := texts(e.ctl.Menu())
	want := []string{
		"(" + TextHeader + ")",
		TextNoDevices,
		"---",
		TextAddress,
		"[x] " + TextAudio,
		"---",
		TextExit,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("меню:\n%q\nожидалось:\n%q", got, want)
	}
}

func TestMenuListsDevicesSortedAndMarksCurrent(t *testing.T) {
	e := newEnv(t, nil, streamUntilStop(nil))
	e.dis.set(tvLiving, tvBed)
	e.ctl.Start(context.Background())
	waitFor(t, "список телевизоров", func() bool { return len(e.ctl.Snapshot().Devices) == 2 })

	got := texts(e.ctl.Menu())
	if got[1] != "Спальня" || got[2] != "Телевизор" {
		t.Fatalf("устройства не по алфавиту: %q", got)
	}
	for _, s := range got {
		if strings.Contains(s, "Остановить") {
			t.Fatalf("пункт остановки без показа: %q", got)
		}
	}

	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	got = texts(e.ctl.Menu())
	if got[1] != "Спальня" || got[2] != "[x] Телевизор" || got[3] != TextStop {
		t.Fatalf("во время показа: %q", got)
	}
}

func TestMenuShowsAddressDeviceNotFoundByDiscovery(t *testing.T) {
	e := newEnv(t, nil, streamUntilStop(nil))
	if err := e.ctl.ConnectAddress("10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, e.ctl, Streaming)
	got := texts(e.ctl.Menu())
	if got[1] != "[x] 10.0.0.5:7000" || got[2] != TextStop {
		t.Fatalf("меню при показе по адресу: %q", got)
	}
}

// Телевизор найден по mDNS и подключён по его же адресу: в меню одна строка с
// именем телевизора и галочкой, без второй строки с голым IP.
func TestMenuMergesAddressDeviceWithDiscoveredOne(t *testing.T) {
	e := newEnv(t, nil, streamUntilStop(nil))
	e.dis.set(tvLiving, tvBed)
	e.ctl.Start(context.Background())
	waitFor(t, "список телевизоров", func() bool { return len(e.ctl.Snapshot().Devices) == 2 })
	if err := e.ctl.ConnectAddress("192.168.1.20"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, e.ctl, Streaming)
	got := texts(e.ctl.Menu())
	if got[1] != "Спальня" || got[2] != "[x] Телевизор" || got[3] != TextStop {
		t.Fatalf("меню при показе по адресу найденного телевизора: %q", got)
	}
	for _, s := range got {
		if strings.Contains(s, "192.168.1.20") {
			t.Fatalf("в меню остался голый адрес: %q", got)
		}
	}
}

func TestMenuActionSelectsDevice(t *testing.T) {
	e := newEnv(t, nil, streamUntilStop(nil))
	e.dis.set(tvLiving)
	e.ctl.Start(context.Background())
	waitFor(t, "список", func() bool { return len(e.ctl.Snapshot().Devices) == 1 })
	for _, it := range e.ctl.Menu() {
		if it.Kind == ItemDevice {
			it.Action()
		}
	}
	waitPhase(t, e.ctl, Streaming)
	if cur := e.ctl.Snapshot().Current; cur == nil || cur.ID != tvLiving.ID {
		t.Fatalf("выбран не тот телевизор: %+v", cur)
	}
}

// --- поиск ---

func TestDiscoveryToleratesOneMissedScan(t *testing.T) {
	d := &fakeDiscoverer{}
	e := newEnv(t, func(c *Config) { c.Discoverer = d })
	c := e.ctl
	d.set(tvLiving)
	c.scan(context.Background())
	d.set()
	c.scan(context.Background())
	if n := len(c.Snapshot().Devices); n != 1 {
		t.Fatalf("после одного пропуска список = %d, ожидался 1 (без мигания)", n)
	}
	c.scan(context.Background())
	if n := len(c.Snapshot().Devices); n != 0 {
		t.Fatalf("после двух пропусков список = %d, ожидался 0", n)
	}
}

func TestDiscoveryErrorKeepsList(t *testing.T) {
	d := &fakeDiscoverer{}
	e := newEnv(t, func(c *Config) { c.Discoverer = d })
	d.set(tvLiving)
	e.ctl.scan(context.Background())
	d.mu.Lock()
	d.err = errors.New("нет сети")
	d.list = nil
	d.mu.Unlock()
	for i := 0; i < 5; i++ {
		e.ctl.scan(context.Background())
	}
	if n := len(e.ctl.Snapshot().Devices); n != 1 {
		t.Fatalf("ошибка поиска стёрла список: %d", n)
	}
}

func TestRefreshDevicesTriggersScan(t *testing.T) {
	var scans atomic.Int64
	d := &countingDiscoverer{n: &scans}
	e := newEnv(t, func(c *Config) { c.Discoverer = d; c.DiscoverInterval = time.Hour })
	e.ctl.Start(context.Background())
	waitFor(t, "первое сканирование", func() bool { return scans.Load() == 1 })
	e.ctl.RefreshDevices()
	waitFor(t, "сканирование по запросу", func() bool { return scans.Load() == 2 })
}

type countingDiscoverer struct{ n *atomic.Int64 }

func (d *countingDiscoverer) Discover(context.Context) ([]Device, error) {
	d.n.Add(1)
	return nil, nil
}

// --- выбор, остановка ---

func TestSelectStreamsAndNotifies(t *testing.T) {
	started := make(chan struct{}, 1)
	e := newEnv(t, nil, streamUntilStop(started))
	e.ctl.Select(tvLiving)
	<-started
	waitPhase(t, e.ctl, Streaming)
	if !e.ui.has("Подключаюсь к «Телевизор»…") || !e.ui.has("Экран показывается на «Телевизор»") {
		t.Fatalf("уведомления: %q", e.ui.texts())
	}
	if tg := e.str.targets[0]; tg.Device.ID != tvLiving.ID || !tg.Audio {
		t.Fatalf("цель показа: %+v", tg)
	}
}

func TestStopEndsStreamAndGoesIdle(t *testing.T) {
	stopped := make(chan struct{})
	e := newEnv(t, nil, func(ctx context.Context, _ Target, h Hooks) error {
		h.Streaming()
		<-ctx.Done()
		close(stopped)
		return nil
	})
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	e.ctl.Stop()
	if snap := e.ctl.Snapshot(); snap.Phase != Idle || snap.Current != nil {
		t.Fatalf("после остановки: %+v", snap)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("показ не получил сигнал остановки")
	}
	if e.ui.has("остановлен") || e.ui.has("прервалась") {
		t.Fatalf("остановка пользователем не должна давать уведомлений об ошибке: %q", e.ui.texts())
	}
}

func TestSelectOtherDeviceWaitsForTeardownOfPrevious(t *testing.T) {
	var order []string
	var mu sync.Mutex
	add := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	e := newEnv(t, nil,
		func(ctx context.Context, tg Target, h Hooks) error {
			h.Streaming()
			<-ctx.Done()
			time.Sleep(30 * time.Millisecond) // TEARDOWN
			add("teardown " + tg.Device.Name)
			return nil
		},
		func(ctx context.Context, tg Target, h Hooks) error {
			add("start " + tg.Device.Name)
			h.Streaming()
			<-ctx.Done()
			return nil
		},
	)
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	e.ctl.Select(tvBed)
	waitFor(t, "второй показ", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) >= 2 })
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(order) != "[teardown Телевизор start Спальня]" {
		t.Fatalf("порядок: %v", order)
	}
}

func TestShutdownWaitsForTeardown(t *testing.T) {
	var finished atomic.Bool
	e := newEnv(t, nil, func(ctx context.Context, _ Target, h Hooks) error {
		h.Streaming()
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	if !e.ctl.Shutdown(2 * time.Second) {
		t.Fatal("Shutdown не дождался")
	}
	if !finished.Load() {
		t.Fatal("Shutdown вернулся до завершения TEARDOWN")
	}
}

func TestExitStopsStreamAndQuits(t *testing.T) {
	var finished atomic.Bool
	e := newEnv(t, nil, func(ctx context.Context, _ Target, h Hooks) error {
		h.Streaming()
		<-ctx.Done()
		finished.Store(true)
		return nil
	})
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	e.ctl.Exit()
	if !finished.Load() || !e.ui.quit.Load() {
		t.Fatalf("выход: teardown=%v quit=%v", finished.Load(), e.ui.quit.Load())
	}
}

// --- повтор при обрыве ---

func TestDropAfterStreamingRetriesThenRecovers(t *testing.T) {
	started := make(chan struct{}, 1)
	e := newEnv(t, nil, dropAfterStreaming, streamUntilStop(started))
	e.ctl.Select(tvLiving)
	<-started
	waitPhase(t, e.ctl, Streaming)
	if !e.ui.has("прервалась") || !e.ui.has("попытка 1 из 3") || !e.ui.has("Связь восстановлена") {
		t.Fatalf("уведомления: %q", e.ui.texts())
	}
	if e.str.calls.Load() != 2 {
		t.Fatalf("вызовов Stream = %d, ожидалось 2", e.str.calls.Load())
	}
}

func TestRetriesAreLimitedToThree(t *testing.T) {
	// Первый показ шёл, затем 3 неудачных повтора подряд: 4 вызова всего.
	e := newEnv(t, nil,
		dropAfterStreaming,
		failWith(errors.New("connect: refused")),
		failWith(errors.New("connect: refused")),
		failWith(errors.New("connect: refused")),
		failWith(errors.New("не должно вызываться")),
	)
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Idle)
	waitFor(t, "итоговая ошибка", func() bool { return e.ui.has("Не получилось восстановить показ") })
	if n := e.str.calls.Load(); n != 4 {
		t.Fatalf("вызовов Stream = %d, ожидалось 4 (показ + 3 повтора)", n)
	}
	for _, want := range []string{"попытка 1 из 3", "попытка 2 из 3", "попытка 3 из 3"} {
		if !e.ui.has(want) {
			t.Errorf("нет уведомления %q: %q", want, e.ui.texts())
		}
	}
	if e.ctl.Snapshot().Current != nil {
		t.Fatal("после исчерпания повторов телевизор остался выбранным")
	}
}

func TestStableStreamResetsRetryCounter(t *testing.T) {
	// Показ идёт дольше StableAfter, обрывается, и так три раза: счётчик
	// сбрасывается, поэтому повторов больше трёх.
	var e *env
	long := func(ctx context.Context, _ Target, h Hooks) error {
		h.Streaming()
		e.now.Add(int64(time.Minute)) // «прошла минута»
		return errors.New("reset")
	}
	e = newEnv(t, nil, long, long, long, long, long)
	e.ctl.Select(tvLiving)
	waitFor(t, "5 вызовов", func() bool { return e.str.calls.Load() >= 5 })
	if e.ui.has("Не получилось восстановить") {
		t.Fatalf("устойчивый показ исчерпал повторы: %q", e.ui.texts())
	}
}

func TestFailureBeforeFirstFrameIsReportedWithoutRetry(t *testing.T) {
	e := newEnv(t, nil, failWith(fmt.Errorf("%w: dial tcp: refused", ErrUnreachable)), streamUntilStop(nil))
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Idle)
	waitFor(t, "ошибка", func() bool {
		return e.ui.has("Не получилось подключиться к «Телевизор»")
	})
	if e.str.calls.Load() != 1 {
		t.Fatalf("повтор при первой неудаче: вызовов %d", e.str.calls.Load())
	}
	for _, tx := range e.ui.texts() {
		for _, bad := range []string{"dial", "tcp", "refused", "RTSP", "ошибка:"} {
			if strings.Contains(tx, bad) {
				t.Fatalf("технические слова в уведомлении %q", tx)
			}
		}
	}
	e.ui.mu.Lock()
	last := e.ui.notices[len(e.ui.notices)-1]
	e.ui.mu.Unlock()
	if last.kind != NoticeError {
		t.Fatalf("вид уведомления = %v, ожидалась ошибка", last.kind)
	}
}

func TestStopDuringRetryDelayCancelsRetry(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RetryDelay = func(int) time.Duration { return time.Hour } }, dropAfterStreaming, streamUntilStop(nil))
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Reconnecting)
	if !e.ctl.Shutdown(time.Second) {
		t.Fatal("остановка во время паузы повтора не сработала")
	}
	if e.str.calls.Load() != 1 {
		t.Fatalf("повтор после остановки: %d", e.str.calls.Load())
	}
}

func TestStreamEndingWithoutErrorReportsStop(t *testing.T) {
	e := newEnv(t, nil, func(_ context.Context, _ Target, h Hooks) error { h.Streaming(); return nil })
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Idle)
	waitFor(t, "уведомление", func() bool { return e.ui.has("остановлен") })
}

// --- PIN ---

func TestPINIsAskedThroughUIAndPassedBack(t *testing.T) {
	var asked []bool
	e := newEnv(t, nil, func(ctx context.Context, _ Target, h Hooks) error {
		pin, err := h.AskPIN(ctx, false)
		if err != nil || pin != "4827" {
			return fmt.Errorf("первый ввод: %q %v", pin, err)
		}
		pin, err = h.AskPIN(ctx, true)
		if err != nil || pin != "1111" {
			return fmt.Errorf("второй ввод: %q %v", pin, err)
		}
		h.Streaming()
		<-ctx.Done()
		return nil
	})
	answers := []string{"4827", "1111"}
	e.ui.askPIN = func(_ context.Context, wrong bool) (string, error) {
		asked = append(asked, wrong)
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	if fmt.Sprint(asked) != "[false true]" {
		t.Fatalf("флаги wrong: %v", asked)
	}
}

func TestPINCancelEndsQuietly(t *testing.T) {
	e := newEnv(t, nil, func(ctx context.Context, _ Target, h Hooks) error {
		_, err := h.AskPIN(ctx, false)
		return fmt.Errorf("ввод PIN: %w", err)
	})
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Idle)
	time.Sleep(20 * time.Millisecond)
	if e.ui.has("Не получилось") || e.ui.has("Код") {
		t.Fatalf("отмена кода не должна давать ошибку: %q", e.ui.texts())
	}
	if e.str.calls.Load() != 1 {
		t.Fatalf("повтор после отмены: %d", e.str.calls.Load())
	}
}

func TestPINRejectedMessage(t *testing.T) {
	e := newEnv(t, nil, failWith(fmt.Errorf("сопряжение: %w", ErrPINRejected)))
	e.ctl.Select(tvLiving)
	waitFor(t, "сообщение", func() bool { return e.ui.has("Код не подошёл") })
}

func TestPINDialogClosedWhenShowStopped(t *testing.T) {
	closed := make(chan struct{})
	asked := make(chan struct{})
	e := newEnv(t, nil, func(ctx context.Context, _ Target, h Hooks) error {
		_, err := h.AskPIN(ctx, false)
		return err
	})
	e.ui.askPIN = func(ctx context.Context, _ bool) (string, error) {
		close(asked)
		<-ctx.Done()
		close(closed)
		return "", ctx.Err()
	}
	e.ctl.Select(tvLiving)
	<-asked // окно кода открыто
	e.ctl.Stop()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("окно кода не закрылось при остановке")
	}
}

// --- звук ---

func TestAudioToggleRestartsStreamWithNewSetting(t *testing.T) {
	e := newEnv(t, nil, streamUntilStop(nil), streamUntilStop(nil))
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Streaming)
	e.ctl.SetAudio(false)
	waitFor(t, "перезапуск", func() bool { return e.str.calls.Load() == 2 })
	e.str.mu.Lock()
	defer e.str.mu.Unlock()
	if !e.str.targets[0].Audio || e.str.targets[1].Audio {
		t.Fatalf("звук в целях: %+v", e.str.targets)
	}
}

func TestAudioToggleWhenIdleDoesNotStart(t *testing.T) {
	e := newEnv(t, nil)
	e.ctl.SetAudio(false)
	if e.str.calls.Load() != 0 || e.ctl.Snapshot().Audio {
		t.Fatalf("звук в покое: вызовов %d, audio=%v", e.str.calls.Load(), e.ctl.Snapshot().Audio)
	}
	items := e.ctl.Menu()
	for _, it := range items {
		if it.Kind == ItemAudio && it.Checked {
			t.Fatal("пункт звука отмечен после выключения")
		}
	}
}

// --- адрес ---

func TestNormalizeAddress(t *testing.T) {
	cases := map[string]string{
		"100.65.152.57":        "100.65.152.57:7000",
		" 100.65.152.57:7100 ": "100.65.152.57:7100",
		"tv.local":             "tv.local:7000",
		"[fe80::1]":            "[fe80::1]:7000",
	}
	for in, want := range cases {
		got, err := NormalizeAddress(in)
		if err != nil || got != want {
			t.Errorf("NormalizeAddress(%q) = %q, %v; ожидалось %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "a b", "10.0.0.1:99999", "10.0.0.1:abc", ":7000", "http://x/y"} {
		if got, err := NormalizeAddress(bad); err == nil {
			t.Errorf("NormalizeAddress(%q) = %q, ожидалась ошибка", bad, got)
		}
	}
}

func TestAddressMenuActionConnects(t *testing.T) {
	e := newEnv(t, nil, streamUntilStop(nil))
	var gotInitial string
	e.ui.askAddress = func(_ context.Context, initial string) (string, bool) {
		gotInitial = initial
		return "127.0.0.1:7100", true
	}
	for _, it := range e.ctl.Menu() {
		if it.Kind == ItemAddress {
			it.Action()
		}
	}
	waitPhase(t, e.ctl, Streaming)
	if cur := e.ctl.Snapshot().Current; cur == nil || cur.Addr != "127.0.0.1:7100" {
		t.Fatalf("текущий: %+v", cur)
	}
	if gotInitial != "" {
		t.Fatalf("начальное значение = %q", gotInitial)
	}
	// Следующее открытие окна подставляет прошлый адрес.
	for _, it := range e.ctl.Menu() {
		if it.Kind == ItemAddress {
			it.Action()
		}
	}
	if gotInitial != "127.0.0.1:7100" {
		t.Fatalf("прошлый адрес не подставлен: %q", gotInitial)
	}
}

func TestAddressMenuActionBadAddressNotifies(t *testing.T) {
	e := newEnv(t, nil)
	e.ui.askAddress = func(context.Context, string) (string, bool) { return "не адрес", true }
	for _, it := range e.ctl.Menu() {
		if it.Kind == ItemAddress {
			it.Action()
		}
	}
	if !e.ui.has("Адрес указан неверно") || e.str.calls.Load() != 0 {
		t.Fatalf("плохой адрес: %q, вызовов %d", e.ui.texts(), e.str.calls.Load())
	}
}

func TestAddressDialogCancelDoesNothing(t *testing.T) {
	e := newEnv(t, nil)
	for _, it := range e.ctl.Menu() {
		if it.Kind == ItemAddress {
			it.Action()
		}
	}
	if len(e.ui.texts()) != 0 || e.str.calls.Load() != 0 {
		t.Fatal("отмена окна адреса что-то запустила")
	}
}

func TestUserMessageHasNoTechnicalWords(t *testing.T) {
	for _, err := range []error{ErrPINRejected, ErrUnreachable, ErrCapture, ErrCaptureGPU, ErrNoMediaFoundation, ErrCaptureHDR, ErrCaptureSession, ErrCaptureStopped, errors.New("EOF")} {
		msg := UserMessage(fmt.Errorf("RTSP SETUP: %w", err), "Телевизор")
		for _, bad := range []string{"RTSP", "SETUP", "EOF", "TCP", "mDNS"} {
			if strings.Contains(msg, bad) {
				t.Errorf("в сообщении %q есть %q", msg, bad)
			}
		}
	}
}

func TestCaptureGPUErrorGivesSpecificMessage(t *testing.T) {
	e := newEnv(t, nil, failWith(fmt.Errorf("захват: %w", ErrCaptureGPU)))
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Idle)
	waitFor(t, "ошибка про видеокарту", func() bool { return e.ui.has("подключён к другой видеокарте") })
	if !errors.Is(ErrCaptureGPU, ErrCapture) {
		t.Fatal("ErrCaptureGPU должна включать ErrCapture")
	}
}

// Перезапуск программы для смены видеокарты не показывает пользователю ошибку и
// не повторяет попытку: после перезапуска показ продолжится сам.
func TestRestartingErrorIsSilent(t *testing.T) {
	e := newEnv(t, nil, failWith(fmt.Errorf("захват: %w", ErrRestarting)), streamUntilStop(nil))
	e.ctl.Select(tvLiving)
	waitPhase(t, e.ctl, Idle)
	if e.str.calls.Load() != 1 {
		t.Fatalf("повтор при перезапуске: вызовов %d", e.str.calls.Load())
	}
	for _, tx := range e.ui.texts() {
		if strings.Contains(tx, "Не получилось") {
			t.Fatalf("уведомление об ошибке при перезапуске: %q", tx)
		}
	}
}
