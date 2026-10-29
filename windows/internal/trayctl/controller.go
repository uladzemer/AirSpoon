// Пакет trayctl — логика трея AirSpoon Sender без Win32: список телевизоров,
// выбор и остановка показа, повторные попытки при обрыве, сообщения
// пользователю. Всё окружение (поиск в сети, сам показ, окна и уведомления)
// подключается через интерфейсы Discoverer, Streamer и UI, поэтому пакет
// целиком проверяется юнит-тестами, а Win32-обёртка остаётся тонкой.
package trayctl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"airspoon/internal/update"
)

// AppName — название программы в заголовках уведомлений и подсказке значка.
const AppName = "AirSpoon Sender"

// Phase — состояние показа.
type Phase int

const (
	// Idle — показ не идёт.
	Idle Phase = iota
	// Connecting — подключаемся к телевизору (в том числе ждём ввод кода).
	Connecting
	// Streaming — экран показывается.
	Streaming
	// Reconnecting — связь оборвалась, ждём и пробуем подключиться снова.
	Reconnecting
)

// Device — найденный приёмник.
type Device struct {
	ID   string // стабильный ключ: идентификатор устройства или адрес
	Name string // название, как его знает пользователь («Телевизор»)
	Addr string // адрес host:port
	Data any    // непрозрачные данные для Streamer (запись mDNS)
}

// Target — что и как показывать.
type Target struct {
	Device Device
	Audio  bool
}

// Hooks — обратные вызовы, которые Streamer вызывает по ходу показа.
type Hooks struct {
	// Streaming вызывается, когда кадры пошли на телевизор.
	Streaming func()
	// AskPIN просит у пользователя код с экрана телевизора; wrong — прошлый код
	// не подошёл. Возвращает ErrPINCanceled, если пользователь отказался.
	AskPIN func(ctx context.Context, wrong bool) (string, error)
	// AskPassword запрашивает произвольный пароль приёмника вместо PIN.
	AskPassword func(ctx context.Context, wrong bool) (string, error)
	// Notice показывает короткое сообщение пользователю по ходу подготовки показа
	// (например, «качество снижено»). Может вызываться до Streaming.
	Notice func(text string)
}

// Streamer ведёт показ на один телевизор до остановки (ctx) или ошибки.
// Ошибки сопоставляются с ErrPINCanceled, ErrPINRejected, ErrUnreachable и
// ErrCapture через errors.Is, чтобы пользователь получил понятный текст.
type Streamer interface {
	Stream(ctx context.Context, t Target, h Hooks) error
}

// Discoverer ищет телевизоры в сети (одно сканирование).
type Discoverer interface {
	Discover(ctx context.Context) ([]Device, error)
}

// NoticeKind — важность уведомления.
type NoticeKind int

const (
	NoticeInfo NoticeKind = iota
	NoticeError
)

// UI — всё, что делает интерфейс (Win32 в программе, заглушка в тестах).
// Методы вызываются из разных горутин и должны быть безопасны для этого.
type UI interface {
	// Notify показывает уведомление у значка.
	Notify(kind NoticeKind, title, text string)
	// Changed сообщает, что состояние изменилось (значок, меню).
	Changed()
	// AskPIN показывает окно ввода кода и ждёт ответа. ErrPINCanceled — «Отмена».
	AskPIN(ctx context.Context, wrong bool) (string, error)
	// AskAddress показывает окно ввода адреса; ok=false — «Отмена».
	AskAddress(ctx context.Context, initial string) (addr string, ok bool)
	// Quit завершает программу (убирает значок и выходит из цикла сообщений).
	Quit()
}

// Ошибки показа, по которым выбирается понятный текст.
var (
	ErrPINCanceled = errors.New("ввод кода отменён")
	ErrPINRejected = errors.New("приёмник не принял код")
	ErrUnreachable = errors.New("приёмник недоступен")
	ErrCapture     = errors.New("не удалось захватить экран")
	// ErrCaptureGPU — экран не захватывается, потому что монитор подключён к
	// другой видеокарте, чем та, на которой работает программа (ноутбук с двумя
	// видеокартами). Включает ErrCapture.
	ErrCaptureGPU = fmt.Errorf("%w: монитор на другой видеокарте", ErrCapture)
	// Причины, по которым экран не захватывается или не кодируется; каждая
	// включает ErrCapture. Повтор подключения их не лечит, поэтому показ после
	// них не пытаются восстанавливать (см. isFatalCapture).
	//
	// ErrNoMediaFoundation — в Windows нет Media Foundation или программного
	// кодера H.264 (редакция N/KN без «Пакета мультимедийных функций»).
	ErrNoMediaFoundation = fmt.Errorf("%w: нет Media Foundation", ErrCapture)
	// ErrCaptureHDR — на мониторе включён HDR, формат кадра не поддерживается.
	ErrCaptureHDR = fmt.Errorf("%w: включён HDR", ErrCapture)
	// ErrCaptureSession — захват недоступен в этом сеансе (удалённый рабочий
	// стол, виртуальная машина), а видеокарту менять бессмысленно.
	ErrCaptureSession = fmt.Errorf("%w: захват недоступен в этом сеансе", ErrCapture)
	// ErrCaptureStopped — захват остановился: монитор пропал или сменил режим,
	// и новые кадры не приходят.
	ErrCaptureStopped = fmt.Errorf("%w: захват остановился", ErrCapture)
	// ErrRestarting — программа сама перезапускается (например, чтобы сменить
	// видеокарту): показ после перезапуска продолжится, сообщение об ошибке не нужно.
	ErrRestarting = errors.New("программа перезапускается")
)

// Config — настройки контроллера. Обязательны Discoverer, Streamer и UI.
type Config struct {
	Discoverer Discoverer
	Streamer   Streamer
	UI         UI

	// Audio — передавать ли звук компьютера (по умолчанию включено у вызывающего).
	Audio bool
	// Version — текущая версия программы ("dev", "0.2.0").
	Version string
	// CheckUpdates — включена ли проверка обновлений по умолчанию.
	CheckUpdates bool
	// SaveCheckUpdates сохраняет настройку «Проверять обновления».
	SaveCheckUpdates func(bool)
	// CheckUpdate — функция запроса последнего релиза (nil — стандартная через update.Check).
	CheckUpdate UpdateCheckFunc
	// InstallUpdate — действие при выборе пункта меню «Обновить до версии X.Y.Z».
	InstallUpdate func(rel update.Release)
	// InitialUpdateDelay — пауза перед первой проверкой обновлений.
	InitialUpdateDelay time.Duration
	// UpdateInterval — пауза между проверками обновлений.
	UpdateInterval time.Duration
	// SupportReport — действие пункта меню «Сохранить отчёт для поддержки»; nil —
	// пункта нет. Вызывается из потока меню и не должно блокировать его надолго.
	SupportReport func()
	// Logf — подробности для журнала (может быть nil).
	Logf func(format string, args ...any)

	// DiscoverInterval — пауза между сканированиями (по умолчанию 2,5 с: вместе с
	// сканированием выходит цикл около 5 с).
	DiscoverInterval time.Duration
	// DiscoverTimeout — длительность одного сканирования (по умолчанию 2,5 с).
	DiscoverTimeout time.Duration
	// MaxRetries — сколько раз подряд пробовать восстановить оборванный показ (по умолчанию 3).
	MaxRetries int
	// RetryDelay — пауза перед попыткой номер n (с 1); по умолчанию 3 с.
	RetryDelay func(n int) time.Duration
	// StableAfter — сколько показ должен идти, чтобы счётчик повторов сбросился (по умолчанию 30 с).
	StableAfter time.Duration
	// Now — часы (для тестов).
	Now func() time.Time
}

// Controller управляет списком телевизоров и показом.
type Controller struct {
	cfg Config

	mu              sync.Mutex
	phase           Phase
	current         *Device
	devices         []Device
	misses          map[string]int
	audio           bool
	checkUpdates    bool
	availableUpdate string
	availableRel    update.Release
	updater         *Updater
	cancel          context.CancelFunc // остановка текущего показа
	done            chan struct{}      // закрывается, когда последняя горутина показа завершилась
	runID           int
	lastAddr        string
	base            context.Context
	stopAll         context.CancelFunc
	refresh         chan struct{}
}

// New создаёт контроллер; поиск телевизоров начинается после Start.
func New(cfg Config) *Controller {
	if cfg.DiscoverInterval <= 0 {
		cfg.DiscoverInterval = 2500 * time.Millisecond
	}
	if cfg.DiscoverTimeout <= 0 {
		cfg.DiscoverTimeout = 2500 * time.Millisecond
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryDelay == nil {
		cfg.RetryDelay = func(int) time.Duration { return 3 * time.Second }
	}
	if cfg.StableAfter <= 0 {
		cfg.StableAfter = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	c := &Controller{
		cfg:          cfg,
		audio:        cfg.Audio,
		checkUpdates: cfg.CheckUpdates,
		misses:       map[string]int{},
		refresh:      make(chan struct{}, 1),
		base:         context.Background(),
		stopAll:      func() {},
	}
	if cfg.CheckUpdate != nil || (cfg.Version != "" && cfg.Version != "dev") {
		checkFunc := cfg.CheckUpdate
		if checkFunc == nil {
			checkFunc = func(ctx context.Context, cur string) (update.Release, bool, error) {
				return update.Check(ctx, nil, update.DefaultAPIURL, cur)
			}
		}
		c.updater = NewUpdater(UpdaterConfig{
			Version: cfg.Version,
			Check:   checkFunc,
			Enabled: func() bool { return c.Snapshot().CheckUpdates },
			Notify:  cfg.UI.Notify,
			// Во время показа уведомление было бы видно на телевизоре.
			Busy:         func() bool { return c.Snapshot().Phase != Idle },
			OnUpdate:     c.SetAvailableUpdate,
			Logf:         cfg.Logf,
			InitialDelay: cfg.InitialUpdateDelay,
			Interval:     cfg.UpdateInterval,
		})
	}
	return c
}

// Start запускает фоновый поиск телевизоров; он идёт, пока жив ctx или не вызван Shutdown.
func (c *Controller) Start(ctx context.Context) {
	c.mu.Lock()
	c.base, c.stopAll = context.WithCancel(ctx)
	base := c.base
	updater := c.updater
	c.mu.Unlock()
	go c.discoverLoop(base)
	if updater != nil {
		go updater.Run(base)
	}
}

// Snapshot — копия состояния для интерфейса.
type Snapshot struct {
	Phase           Phase
	Current         *Device
	Devices         []Device
	Audio           bool
	CheckUpdates    bool
	AvailableUpdate string
}

// Snapshot возвращает текущее состояние.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Snapshot{
		Phase:           c.phase,
		Audio:           c.audio,
		CheckUpdates:    c.checkUpdates,
		AvailableUpdate: c.availableUpdate,
		Devices:         append([]Device(nil), c.devices...),
	}
	if c.current != nil {
		d := *c.current
		s.Current = &d
	}
	return s
}

// RefreshDevices просит обновить список телевизоров сейчас (не блокирует).
func (c *Controller) RefreshDevices() {
	select {
	case c.refresh <- struct{}{}:
	default:
	}
}

// Select начинает показ на телевизоре; идущий показ останавливается.
func (c *Controller) Select(d Device) {
	c.mu.Lock()
	prevCancel, prevDone := c.cancel, c.done
	ctx, cancel := context.WithCancel(c.base)
	done := make(chan struct{})
	c.cancel, c.done = cancel, done
	c.runID++
	id := c.runID
	dev := d
	c.current = &dev
	c.phase = Connecting
	c.mu.Unlock()
	c.cfg.UI.Changed()

	go func() {
		defer close(done)
		if prevCancel != nil {
			prevCancel()
		}
		if prevDone != nil {
			<-prevDone // новый показ начинается после TEARDOWN прежнего
		}
		if ctx.Err() != nil {
			return
		}
		c.run(ctx, id, dev)
	}()
}

// Stop останавливает показ (TEARDOWN идёт в фоне).
func (c *Controller) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.runID++ // запоздалые сообщения остановленного показа больше не меняют состояние
	c.phase = Idle
	c.current = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.cfg.UI.Changed()
}

// Shutdown останавливает поиск и показ и ждёт TEARDOWN не дольше timeout.
// Возвращает false, если не дождались.
func (c *Controller) Shutdown(timeout time.Duration) bool {
	c.mu.Lock()
	stopAll := c.stopAll
	c.mu.Unlock()
	stopAll()
	c.Stop()
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// SetAudio включает или выключает звук. Если показ идёт, он перезапускается,
// чтобы настройка подействовала сразу.
func (c *Controller) SetAudio(on bool) {
	c.mu.Lock()
	c.audio = on
	cur := c.current
	active := c.phase != Idle && cur != nil
	var d Device
	if active {
		d = *cur
	}
	c.mu.Unlock()
	c.cfg.UI.Changed()
	if active {
		c.Select(d)
	}
}

// SetCheckUpdates включает или выключает проверку обновлений. Выключение
// убирает найденное обновление из меню, включение сразу проверяет релиз.
func (c *Controller) SetCheckUpdates(on bool) {
	c.mu.Lock()
	c.checkUpdates = on
	save := c.cfg.SaveCheckUpdates
	updater, base := c.updater, c.base
	c.mu.Unlock()
	if save != nil {
		save(on)
	}
	if !on {
		c.ClearAvailableUpdate()
		return
	}
	c.cfg.UI.Changed()
	if updater != nil {
		go updater.CheckOnce(base)
	}
}

// SetAvailableUpdate выставляет доступное для установки обновление.
func (c *Controller) SetAvailableUpdate(rel update.Release) {
	c.mu.Lock()
	c.availableUpdate = rel.Version
	c.availableRel = rel
	c.mu.Unlock()
	c.cfg.UI.Changed()
}

// ClearAvailableUpdate сбрасывает найденное обновление.
func (c *Controller) ClearAvailableUpdate() {
	c.mu.Lock()
	c.availableUpdate = ""
	c.availableRel = update.Release{}
	c.mu.Unlock()
	c.cfg.UI.Changed()
}

func (c *Controller) applyUpdate() {
	c.mu.Lock()
	act := c.cfg.InstallUpdate
	rel := c.availableRel
	c.mu.Unlock()
	if act != nil {
		act(rel)
	}
}

// NormalizeAddress приводит введённый адрес к виду host:port (порт по умолчанию 7000).
func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("адрес не указан")
	}
	if strings.ContainsAny(s, " \t/\\") {
		return "", fmt.Errorf("недопустимый адрес %q", s)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// Нет порта (или голый IPv6).
		host, port = strings.Trim(s, "[]"), "7000"
	}
	if host == "" {
		return "", fmt.Errorf("недопустимый адрес %q", s)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("недопустимый порт %q", port)
	}
	return net.JoinHostPort(host, port), nil
}

// ConnectAddress начинает показ по введённому адресу (без поиска в сети).
func (c *Controller) ConnectAddress(addr string) error {
	norm, err := NormalizeAddress(addr)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.lastAddr = addr
	c.mu.Unlock()
	c.Select(Device{ID: "addr:" + norm, Name: norm, Addr: norm})
	return nil
}

// --- поиск телевизоров ---

func (c *Controller) discoverLoop(ctx context.Context) {
	for {
		c.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.DiscoverInterval):
		case <-c.refresh:
		}
	}
}

// missesBeforeDrop — сколько сканирований подряд телевизор может не отвечать,
// прежде чем исчезнет из меню (mDNS теряет ответы, список не должен мигать).
const missesBeforeDrop = 2

func (c *Controller) scan(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, c.cfg.DiscoverTimeout)
	found, err := c.cfg.Discoverer.Discover(sctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.cfg.Logf("поиск телевизоров: %v", err)
		return
	}
	c.mu.Lock()
	changed := c.mergeLocked(found)
	c.mu.Unlock()
	if changed {
		c.cfg.UI.Changed()
	}
}

func (c *Controller) mergeLocked(found []Device) bool {
	before := fmt.Sprint(c.devices)
	byID := map[string]Device{}
	for _, d := range c.devices {
		byID[d.ID] = d
	}
	seen := map[string]bool{}
	for _, d := range found {
		if d.ID == "" {
			d.ID = d.Addr
		}
		byID[d.ID] = d
		seen[d.ID] = true
		c.misses[d.ID] = 0
	}
	for id := range byID {
		if seen[id] {
			continue
		}
		c.misses[id]++
		if c.misses[id] >= missesBeforeDrop {
			delete(byID, id)
			delete(c.misses, id)
		}
	}
	list := make([]Device, 0, len(byID))
	for _, d := range byID {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].ID < list[j].ID
	})
	c.devices = list
	return before != fmt.Sprint(list)
}

// --- показ ---

func (c *Controller) setPhase(id int, p Phase) {
	c.mu.Lock()
	ok := c.runID == id
	if ok {
		c.phase = p
	}
	c.mu.Unlock()
	if ok {
		c.cfg.UI.Changed()
	}
}

func (c *Controller) isCurrent(id int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runID == id
}

func (c *Controller) notify(ctx context.Context, kind NoticeKind, text string) {
	if ctx.Err() != nil {
		return
	}
	c.cfg.Logf("уведомление: %s", text)
	c.cfg.UI.Notify(kind, AppName, text)
}

func (c *Controller) run(ctx context.Context, id int, d Device) {
	defer func() {
		c.mu.Lock()
		if c.runID == id { // показ закончился сам, а не заменён или остановлен
			c.phase, c.current, c.cancel = Idle, nil, nil
		}
		c.mu.Unlock()
		c.cfg.UI.Changed()
	}()

	failures := 0
	waiting := false // уже сказали «ждём рабочий стол» в этой серии
	var everStreamed atomic.Bool
	c.notify(ctx, NoticeInfo, fmt.Sprintf("Подключаюсь к «%s»…", d.Name))
	for {
		var streamedAt atomic.Int64 // UnixNano начала показа в этой попытке
		hooks := Hooks{
			Streaming: func() {
				streamedAt.Store(c.cfg.Now().UnixNano())
				first := !everStreamed.Swap(true)
				c.setPhase(id, Streaming)
				if first {
					c.notify(ctx, NoticeInfo, fmt.Sprintf("Экран показывается на «%s»", d.Name))
				} else {
					c.notify(ctx, NoticeInfo, fmt.Sprintf("Связь восстановлена. Экран показывается на «%s»", d.Name))
				}
			},
			AskPIN: c.cfg.UI.AskPIN,
			Notice: func(text string) { c.notify(ctx, NoticeInfo, text) },
		}
		if ui, ok := c.cfg.UI.(interface {
			AskPassword(context.Context, bool) (string, error)
		}); ok {
			hooks.AskPassword = ui.AskPassword
		}
		c.mu.Lock()
		audio := c.audio
		c.mu.Unlock()
		err := c.cfg.Streamer.Stream(ctx, Target{Device: d, Audio: audio}, hooks)
		if ctx.Err() != nil {
			return
		}
		if t := streamedAt.Load(); t != 0 && c.cfg.Now().Sub(time.Unix(0, t)) >= c.cfg.StableAfter {
			failures = 0
		}
		if errors.Is(err, ErrPINCanceled) {
			c.cfg.Logf("ввод кода отменён пользователем")
			return
		}
		if errors.Is(err, ErrRestarting) {
			c.cfg.Logf("показ на %q прерван: программа перезапускается", d.Name)
			return
		}
		if err == nil {
			c.notify(ctx, NoticeInfo, fmt.Sprintf("Показ на «%s» остановлен.", d.Name))
			return
		}
		c.cfg.Logf("показ на %q (%s) прервался: %v", d.Name, d.Addr, err)
		if !everStreamed.Load() {
			c.notify(ctx, NoticeError, UserMessage(err, d.Name))
			return
		}
		if isFatalCapture(err) {
			// Повтор не поможет (включён HDR, нет Media Foundation и т. п.):
			// говорим причину сразу, а не после трёх попыток.
			c.notify(ctx, NoticeError, UserMessage(err, d.Name))
			return
		}
		if isWaitingCapture(err) {
			// Рабочий стол временно недоступен (экран блокировки, смена режима):
			// ждём без счёта попыток, пока пользователь не остановит показ.
			// Сообщение одно на серию ожиданий.
			c.setPhase(id, Reconnecting)
			if !waiting {
				waiting = true
				c.notify(ctx, NoticeInfo, UserMessage(err, d.Name))
			}
			if !sleep(ctx, c.cfg.RetryDelay(1)) {
				return
			}
			continue
		}
		waiting = false
		failures++
		if failures > c.cfg.MaxRetries {
			c.notify(ctx, NoticeError, fmt.Sprintf(
				"Не получилось восстановить показ на «%s». Выберите телевизор в меню ещё раз.", d.Name))
			return
		}
		c.setPhase(id, Reconnecting)
		c.notify(ctx, NoticeInfo, fmt.Sprintf(
			"Связь с «%s» прервалась. Пробую подключиться снова (попытка %d из %d)…", d.Name, failures, c.cfg.MaxRetries))
		if !sleep(ctx, c.cfg.RetryDelay(failures)) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// UserMessage превращает ошибку показа в понятный текст без технических слов.
// Подробности ошибки пишутся в журнал отдельно.
func UserMessage(err error, name string) string {
	switch {
	case isWaitingCapture(err):
		return "Жду рабочий стол: экран пока нельзя захватить (компьютер заблокирован или меняется режим экрана). Показ продолжится сам, когда рабочий стол вернётся."
	case errors.Is(err, ErrPINRejected):
		return "Код не подошёл. Выберите телевизор в меню и попробуйте ещё раз."
	case errors.Is(err, ErrUnreachable):
		return fmt.Sprintf("Не получилось подключиться к «%s». Проверьте, что телевизор включён и в той же сети Wi‑Fi, и попробуйте ещё раз.", name)
	case errors.Is(err, ErrCaptureGPU):
		return "Не получилось захватить экран: он подключён к другой видеокарте компьютера. Закройте программу, запустите её ещё раз; если не поможет, перезагрузите компьютер."
	case errors.Is(err, ErrNoMediaFoundation):
		return "Не получилось начать показ: в этой Windows нет компонентов для кодирования видео. Установите «Пакет мультимедийных функций» (Media Feature Pack) от Microsoft для вашей версии Windows и запустите программу ещё раз."
	case errors.Is(err, ErrCaptureHDR):
		return "Не получилось захватить экран: на мониторе включён HDR. Выключите его: Параметры Windows > Система > Дисплей > HDR."
	case errors.Is(err, ErrCaptureSession):
		return "Не получилось захватить экран: в этом сеансе Windows он недоступен (удалённый рабочий стол или виртуальная машина). Запустите программу на самом компьютере, под обычным входом в Windows."
	case errors.Is(err, ErrCaptureStopped):
		return "Захват экрана остановился: монитор отключили или сменили его режим. Проверьте монитор и выберите телевизор в меню ещё раз."
	case errors.Is(err, ErrCapture):
		return "Не получилось захватить экран компьютера. Если компьютер заблокирован, разблокируйте его и попробуйте ещё раз."
	}
	return fmt.Sprintf("Не получилось показать экран на «%s». Попробуйте ещё раз.", name)
}

// isWaitingCapture — захват временно недоступен, и показ нужно возобновлять с
// паузой (например, DXGI_ERROR_UNSUPPORTED на экране блокировки при одной
// видеокарте). Ошибка сама объявляет это методом WaitingForDesktop, так что
// trayctl не зависит от пакета захвата.
func isWaitingCapture(err error) bool {
	var w interface{ WaitingForDesktop() bool }
	return errors.As(err, &w) && w.WaitingForDesktop()
}

// isFatalCapture — ошибка, которую повторное подключение не лечит.
func isFatalCapture(err error) bool {
	return errors.Is(err, ErrNoMediaFoundation) || errors.Is(err, ErrCaptureHDR) || errors.Is(err, ErrCaptureSession)
}
