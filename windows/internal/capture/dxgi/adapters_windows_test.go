//go:build windows && (amd64 || arm64)

package dxgi

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"airspoon/internal/win/com"
)

func TestUnsupportedErrorMatchesSentinelAndKeepsHRESULT(t *testing.T) {
	base := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	var err error = &UnsupportedError{Adapter: "NVIDIA GeForce", Monitor: `\\.\DISPLAY1`, Err: base}
	if !errors.Is(err, ErrDuplicateUnsupported) {
		t.Fatal("errors.Is(err, ErrDuplicateUnsupported) должен быть истинным")
	}
	if hr, ok := com.HR(err); !ok || hr != hrUnsupported {
		t.Fatalf("HRESULT потерян: %#x %v", hr, ok)
	}
	if !strings.Contains(err.Error(), "NVIDIA GeForce") || !strings.Contains(err.Error(), "0x887A0004") {
		t.Fatalf("в тексте нужны видеокарта и HRESULT: %v", err)
	}
	// Обёртка fmt.Errorf %w (так ошибка идёт через native и mirror) не мешает.
	wrapped := errors.Join(errors.New("захват экрана"), err)
	if !errors.Is(wrapped, ErrDuplicateUnsupported) {
		t.Fatal("потеряна при оборачивании")
	}
}

func TestLUIDString(t *testing.T) {
	if got := (LUID{Low: 0xA2B3, High: 0}).String(); got != "00000000:0000A2B3" {
		t.Fatalf("LUID: %s", got)
	}
	if !(LUID{}).IsZero() || (LUID{Low: 1}).IsZero() {
		t.Fatal("IsZero")
	}
}

func TestListAdaptersReportsAdaptersAndMonitorOwner(t *testing.T) {
	list, err := ListAdapters()
	if err != nil {
		t.Skipf("перечисление видеокарт недоступно: %v", err)
	}
	if len(list) == 0 {
		t.Skip("видеокарт нет")
	}
	for _, a := range list {
		t.Logf("видеокарта %d: %s vendor=%04X device=%04X LUID=%s flags=%#x VRAM=%d МБ выходов=%d",
			a.Index, a.Name, a.VendorID, a.DeviceID, a.LUID, a.Flags, a.DedicatedVRAM>>20, len(a.Outputs))
		if a.Name == "" {
			t.Errorf("видеокарта %d без названия", a.Index)
		}
		for _, o := range a.Outputs {
			t.Logf("  монитор %s %+v attached=%v GDI-владелец=%q", o.Name, o.Bounds, o.Attached, o.GDIAdapter)
		}
	}
	outs, lerr := ListOutputs()
	if lerr != nil || len(outs) == 0 {
		t.Skip("мониторов нет")
	}
	if outs[0].AdapterLUID.IsZero() && outs[0].AdapterName != "" {
		t.Logf("LUID монитора 0 нулевой (бывает у виртуальных адаптеров): %s", outs[0].AdapterName)
	}
	t.Logf("владелец монитора %s по GDI: %q", outs[0].Name, OwnerOfMonitor(list, outs[0].Name))
}

func TestOwnerOfMonitorUsesGDIAdapterNotDXGIParent(t *testing.T) {
	// Процесс на NVIDIA видит монитор ноутбука под NVIDIA (выход «переподчинён»),
	// а по GDI он принадлежит Intel: именно это нужно для выбора предпочтения.
	const d1, d2 = `\\.\DISPLAY1`, `\\.\DISPLAY2`
	list := []AdapterInfo{
		{Name: "NVIDIA GeForce RTX 3060 Laptop GPU", Outputs: []OutputDetail{
			{Name: d1, Attached: true, GDIAdapter: "Intel(R) UHD Graphics"},
			{Name: d2, Attached: true, GDIAdapter: "NVIDIA GeForce RTX 3060 Laptop GPU"},
		}},
		{Name: "Intel(R) UHD Graphics"},
	}
	if got := OwnerOfMonitor(list, d1); got != "Intel(R) UHD Graphics" {
		t.Errorf("DISPLAY1: %q", got)
	}
	if got := OwnerOfMonitor(list, d2); got != "NVIDIA GeForce RTX 3060 Laptop GPU" {
		t.Errorf("DISPLAY2: %q", got)
	}
	if got := OwnerOfMonitor(list, `\\.\DISPLAY9`); got != "" {
		t.Errorf("неизвестный монитор: %q", got)
	}
	if got := OwnerOfMonitor(list, ""); got != "Intel(R) UHD Graphics" {
		t.Errorf("без имени — первый подключённый монитор: %q", got)
	}
}

// Проба либо открывает дублирование, либо возвращает ошибку с HRESULT (сеанс
// заблокирован или без экрана); в обоих случаях устройство создаётся на той же
// видеокарте, что и монитор.
func TestProbeDuplicationUsesSameAdapter(t *testing.T) {
	res := ProbeDuplication(0, 600*time.Millisecond)
	t.Logf("проба: %+v", res)
	if res.Monitor == "" {
		t.Skipf("монитор недоступен в этом окружении: %v", res.Err)
	}
	if res.Attempts < 1 {
		t.Fatal("ни одной попытки")
	}
	if res.DeviceAdapter != "" && res.DeviceAdapter != res.Adapter {
		t.Errorf("устройство D3D на %q, а монитор на %q: DuplicateOutput требует одну видеокарту", res.DeviceAdapter, res.Adapter)
	}
	if res.DeviceLUID != res.AdapterLUID && !res.DeviceLUID.IsZero() {
		t.Errorf("LUID устройства %s не совпал с LUID монитора %s", res.DeviceLUID, res.AdapterLUID)
	}
	if !res.OK && res.Err == nil {
		t.Error("не открылось, но ошибки нет")
	}
}

// Гибридная система: три подряд DXGI_ERROR_UNSUPPORTED до первого кадра — ошибка
// ErrDuplicateUnsupported; иначе (одна видеокарта, уже были кадры, другая ошибка)
// попытки продолжаются.
func TestNoteOpenErrorFailsFastOnlyOnHybridBeforeFirstFrame(t *testing.T) {
	unsupported := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	lost := &com.Error{Op: "DuplicateOutput", HR: hrAccessLost}

	c := &Capturer{hybrid: true, adapter: "NVIDIA", monitor: `\.\DISPLAY1`}
	for i := 1; i < unsupportedFailAfter; i++ {
		if err := c.noteOpenError(unsupported, time.Now()); err != nil {
			t.Fatalf("попытка %d: рано сдаёмся: %v", i, err)
		}
	}
	err := c.noteOpenError(unsupported, time.Now())
	if !errors.Is(err, ErrDuplicateUnsupported) {
		t.Fatalf("после %d подряд ожидалась ErrDuplicateUnsupported, получено %v", unsupportedFailAfter, err)
	}

	// Другая ошибка между попытками сбрасывает счёт.
	c = &Capturer{hybrid: true}
	c.noteOpenError(unsupported, time.Now())
	c.noteOpenError(unsupported, time.Now())
	c.noteOpenError(lost, time.Now())
	if err := c.noteOpenError(unsupported, time.Now()); err != nil {
		t.Fatalf("счёт должен сбрасываться другой ошибкой: %v", err)
	}

	// Одна видеокарта, сеанс RDP: порог выше (временный UNSUPPORTED при смене
	// режима проходит), но не бесконечный: после unsupportedFailAfterSingle
	// отказов понятная ошибка про сеанс.
	c = &Capturer{hybrid: false, remoteFn: func() bool { return true }}
	for i := 1; i < unsupportedFailAfterSingle; i++ {
		if err := c.noteOpenError(unsupported, time.Now()); err != nil {
			t.Fatalf("одна видеокарта: ошибка на попытке %d: %v", i, err)
		}
	}
	err = c.noteOpenError(unsupported, time.Now())
	var ue *UnsupportedError
	if !errors.Is(err, ErrDuplicateUnsupported) || !errors.As(err, &ue) || ue.Hybrid {
		t.Fatalf("одна видеокарта: после %d подряд ожидалась UnsupportedError без Hybrid, получено %v", unsupportedFailAfterSingle, err)
	}
	if !strings.Contains(err.Error(), "в этом сеансе") {
		t.Fatalf("текст ошибки должен называть сеанс, а не смену видеокарты: %v", err)
	}
	// Кадры уже были (рабочий стол потерян в середине показа): сразу не сдаёмся.
	c = &Capturer{hybrid: true, haveDesktop: true}
	for i := 0; i < 10; i++ {
		if err := c.noteOpenError(unsupported, time.Now()); err != nil {
			t.Fatalf("после первого кадра: ошибка на попытке %d: %v", i, err)
		}
	}
}

// После первого кадра UNSUPPORTED или NOT_FOUND (в том числе «монитор не найден»
// при поиске по имени) дольше staleAfter означает «захват нужно создать заново»;
// другая ошибка или успешное открытие обрывают отсчёт.
func TestNoteOpenErrorStaleAfterFirstFrame(t *testing.T) {
	unsupported := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	notFound := &com.Error{Op: "EnumOutputs", HR: hrNotFound}
	gone := fmt.Errorf("%w: %s", errNoDisplay, `\\.\DISPLAY2`)
	lost := &com.Error{Op: "DuplicateOutput", HR: hrAccessLost}
	t0 := time.Now()

	for name, e := range map[string]error{"UNSUPPORTED": unsupported, "NOT_FOUND": notFound, "монитор исчез": gone} {
		c := &Capturer{haveDesktop: true, adapter: "Intel", monitor: `\\.\DISPLAY2`}
		if err := c.noteOpenError(e, t0); err != nil {
			t.Fatalf("%s: первая ошибка не должна быть окончательной: %v", name, err)
		}
		if err := c.noteOpenError(e, t0.Add(staleAfter-time.Second)); err != nil {
			t.Fatalf("%s: рано сдаёмся: %v", name, err)
		}
		err := c.noteOpenError(e, t0.Add(staleAfter))
		if !errors.Is(err, ErrCaptureStale) || !errors.Is(err, ErrDeviceLost) {
			t.Fatalf("%s: после %v ожидалась StaleError (ErrCaptureStale и ErrDeviceLost), получено %v", name, staleAfter, err)
		}
	}

	// ACCESS_LOST между ними обрывает серию.
	c := &Capturer{haveDesktop: true}
	c.noteOpenError(unsupported, t0)
	c.noteOpenError(lost, t0.Add(staleAfter-time.Second))
	if err := c.noteOpenError(unsupported, t0.Add(staleAfter+time.Second)); err != nil {
		t.Fatalf("серия должна начинаться заново после другой ошибки: %v", err)
	}

	// Успешное открытие дублирования (tryOpenDup) тоже обрывает.
	fail := true
	c = &Capturer{haveDesktop: true, deviceRemovedFn: func() bool { return false },
		openDupFn: func() error {
			if fail {
				return unsupported
			}
			return nil
		}}
	now := t0
	for i := 0; i < 3; i++ {
		now = now.Add(dupRetryInterval + time.Millisecond)
		if err := c.tryOpenDup(now); err != nil {
			t.Fatal(err)
		}
	}
	if c.badSince.IsZero() {
		t.Fatal("отсчёт не начат")
	}
	fail = false
	now = now.Add(dupRetryInterval + time.Millisecond)
	if err := c.tryOpenDup(now); err != nil || !c.badSince.IsZero() {
		t.Fatalf("успех должен обнулять отсчёт: err=%v badSince=%v", err, c.badSince)
	}
}

// Корень быстрого отказа: на гибридной системе, пока дублирование не открылось
// из-за DXGI_ERROR_UNSUPPORTED, Grab не выдаёт кадров (иначе чёрный IDR дошёл бы
// до приёмника, а ошибка пришла бы с опозданием), а через unsupportedFailAfter
// попыток возвращает ErrDuplicateUnsupported. Проверяется настоящий путь Grab ->
// tryOpenDup -> noteOpenError; открытие дублирования подменено.
func TestGrabFailsFastWithoutFramesOnHybridUnsupported(t *testing.T) {
	unsupported := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	opens := 0
	c := &Capturer{
		hybrid: true, adapter: "NVIDIA", monitor: `\.\DISPLAY1`,
		interval:        time.Second / 30,
		openDupFn:       func() error { opens++; return unsupported },
		deviceRemovedFn: func() bool { return false },
	}
	type result struct {
		f        Frame
		err      error
		panicked any
	}
	done := make(chan result, 1)
	stop := make(chan struct{})
	defer close(stop)
	start := time.Now()
	go func() {
		var r result
		defer func() {
			r.panicked = recover() // кадр без дублирования дошёл бы до produce на пустых ресурсах
			done <- r
		}()
		r.f, r.err = c.Grab(stop)
	}()
	select {
	case r := <-done:
		if r.panicked != nil {
			t.Fatalf("Grab начал выдавать кадр, хотя дублирование не открывалось (паника в produce: %v)", r.panicked)
		}
		if !errors.Is(r.err, ErrDuplicateUnsupported) {
			t.Fatalf("ожидалась ErrDuplicateUnsupported, получено кадр=%+v err=%v", r.f, r.err)
		}
		if opens != unsupportedFailAfter {
			t.Fatalf("попыток открыть дублирование %d, ожидалось %d", opens, unsupportedFailAfter)
		}
		// Три попытки по 250 мс: t0, +250, +500. С запасом, но намного быстрее 2 минут.
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("быстрый отказ занял %v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Grab не вернул ни ошибки, ни кадра за 10 с")
	}
}

// Одна видеокарта, обычный сеанс (экран блокировки, смена режима): UNSUPPORTED
// после порога не приговор. Ошибка «ждём рабочий стол» (не UnsupportedError и не
// ErrDuplicateUnsupported), показ переподключается с паузой. Basic Render без
// монитора и RDP остаются окончательными.
func TestSingleGPUUnsupportedWaitsUnlessSessionIsHopeless(t *testing.T) {
	unsupported := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	run := func(c *Capturer) error {
		var err error
		for i := 0; i < unsupportedFailAfterSingle; i++ {
			err = c.noteOpenError(unsupported, time.Now())
		}
		return err
	}

	c := &Capturer{hybrid: false, adapter: "Intel", monitor: `\.\DISPLAY1`, remoteFn: func() bool { return false }}
	err := run(c)
	var wait *DesktopWaitError
	if !errors.As(err, &wait) || !wait.WaitingForDesktop() {
		t.Fatalf("одна карта, обычный сеанс: ожидалась DesktopWaitError, получено %v", err)
	}
	var ue *UnsupportedError
	if errors.Is(err, ErrDuplicateUnsupported) || errors.As(err, &ue) {
		t.Fatalf("ожидание рабочего стола не должно совпадать с окончательной ошибкой: %v", err)
	}
	if hr, ok := com.HR(err); !ok || hr != hrUnsupported {
		t.Fatalf("HRESULT должен сохраняться: %v", err)
	}

	c = &Capturer{hybrid: false, remoteFn: func() bool { return true }}
	if err := run(c); !errors.As(err, &ue) || ue.Hybrid {
		t.Fatalf("RDP: ожидалась UnsupportedError, получено %v", err)
	}
	c = &Capturer{hybrid: false, basic: true, remoteFn: func() bool { return false }}
	if err := run(c); !errors.As(err, &ue) {
		t.Fatalf("Basic Render: ожидалась UnsupportedError, получено %v", err)
	}
}

// Задержка кадров касается только гибридной системы до первого кадра и только
// цепочки DXGI_ERROR_UNSUPPORTED; остальные случаи работают как прежде
// (чёрный кадр лучше тишины при блокировке экрана).
func TestHoldFramesOnlyDuringUnsupportedStreakOnHybrid(t *testing.T) {
	unsupported := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	lost := &com.Error{Op: "DuplicateOutput", HR: hrAccessLost}

	c := &Capturer{hybrid: true}
	if c.holdFrames() {
		t.Fatal("до первой попытки задерживать нечего")
	}
	c.noteOpenError(unsupported, time.Now())
	if !c.holdFrames() {
		t.Fatal("гибридная система, UNSUPPORTED, кадров ещё не было: кадры нужно держать")
	}
	c.noteOpenError(lost, time.Now()) // другая ошибка (блокировка экрана): серия оборвана
	if c.holdFrames() {
		t.Fatal("после другой ошибки кадры держать нельзя")
	}

	c = &Capturer{hybrid: false}
	c.noteOpenError(unsupported, time.Now())
	if !c.holdFrames() {
		t.Fatal("одна видеокарта: до первого кадра цепочка UNSUPPORTED тоже задерживает кадры (порог ошибки выше)")
	}
	c = &Capturer{hybrid: true, haveDesktop: true}
	c.noteOpenError(unsupported, time.Now())
	if c.holdFrames() {
		t.Fatal("кадры уже были: показ не прерывается")
	}
	c = &Capturer{hybrid: true, dup: &com.Object{}}
	c.unsupported = 2
	if c.holdFrames() {
		t.Fatal("дублирование открыто: кадры нужны")
	}
}

// Успешное открытие дублирования обрывает счёт «подряд идущих» отказов: два отказа,
// успех, два отказа — это не три подряд.
func TestTryOpenDupSuccessResetsUnsupportedCount(t *testing.T) {
	unsupported := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	results := []error{unsupported, unsupported, nil, unsupported, unsupported}
	i := 0
	c := &Capturer{hybrid: true, openDupFn: func() error { e := results[i]; i++; return e }, deviceRemovedFn: func() bool { return false }}
	now := time.Now()
	for range results {
		now = now.Add(dupRetryInterval + time.Millisecond)
		if err := c.tryOpenDup(now); err != nil {
			t.Fatalf("попытка %d: рано сдаёмся: %v", i, err)
		}
	}
	if c.unsupported != 2 {
		t.Fatalf("счётчик %d, ожидалось 2 (после успеха считаем заново)", c.unsupported)
	}
}
