//go:build windows

package traywin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/trayctl"
)

const (
	trayClassName   = "AirSpoonSenderTray"
	dialogClassName = "AirSpoonSenderDialog"
	mutexName       = `Local\AirSpoonSender.SingleInstance`

	wmTrayCallback = wmApp + 1 // сообщение значка трея
	wmRun          = wmApp + 2 // выполнить очередь функций в потоке окон
	wmShowMenu     = wmApp + 3 // показать меню (повторный запуск программы)

	trayIconID = 1
	menuIDBase = 1000

	// Таймер, который пока открыто меню проверяет просьбу выйти: меню модальное
	// (TrackPopupMenu не возвращается), и без него выход из программы ждал бы,
	// пока человек закроет меню.
	menuQuitTimerID   = 1
	menuQuitTimerStep = 100 // мс

)

// Options — настройки обёртки.
type Options struct {
	// Tooltip — подсказка значка («AirSpoon Sender»).
	Tooltip string
	// Logf — журнал ошибок Win32 (может быть nil).
	Logf func(format string, args ...any)
}

// App — трей: значок, скрытое окно, меню и окна ввода. Все вызовы Win32 идут
// из одного потока (поток окон); остальные горутины обращаются к App только
// через методы trayctl.UI, которые передают работу в этот поток.
type App struct {
	opts Options
	ctl  *trayctl.Controller

	hwnd      uintptr
	hInst     uintptr
	iconSmall uintptr // цветной значок программы 16 px (заголовки окон)
	iconBig   uintptr // цветной значок программы для окон ввода (панель задач, Alt+Tab)
	// Значки трея: [светлая тема][идёт показ]. Одноцветные, под цвет панели задач.
	trayIcons [2][2]uintptr
	lightMode bool // панель задач светлая (SystemUsesLightTheme)
	live      bool
	iconAdded bool
	quitting  bool
	menu      *menuStyle  // открытое меню (только поток окон)
	menuOpen  bool        // идёт TrackPopupMenu (только поток окон)
	inMenu    bool        // showMenu уже выполняется: повторный вход (вложенный цикл меню) игнорируется
	quitReq   atomic.Bool // просьба выйти из любого потока (Quit)

	taskbarCreated uint32

	qmu sync.Mutex
	q   []func()

	dialogs map[uintptr]*dialog // только поток окон
}

var theApp *App // одна на процесс: нужна оконной процедуре

// AcquireSingleInstance захватывает именованный мьютекс. first=false — программа
// уже запущена. Вызывающий держит release до выхода.
func AcquireSingleInstance() (release func(), first bool, err error) {
	h, err := windows.CreateMutex(nil, false, utf16Ptr(mutexName))
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return func() {}, false, err
	}
	first = !errors.Is(err, windows.ERROR_ALREADY_EXISTS)
	var once sync.Once
	return func() { once.Do(func() { _ = windows.CloseHandle(h) }) }, first, nil
}

// SignalRunning просит уже запущенный экземпляр показать меню. Возвращает
// false, если его окно не найдено (например, он ещё запускается).
func SignalRunning() bool {
	for i := 0; i < 20; i++ {
		h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr(trayClassName))), 0)
		if h != 0 {
			// Этот процесс только что запущен пользователем и имеет право на
			// передний план: передаём его первому экземпляру, чтобы меню
			// открылось в фокусе и закрывалось щелчком вне его.
			var pid uint32
			procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
			if pid != 0 {
				procAllowSetForegroundWindow.Call(uintptr(pid))
			}
			return postMessage(h, wmShowMenu, 0, 0)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// FocusMessageBox поднимает на передний план окно сообщения (стандартное
// диалоговое окно Windows) с заданным заголовком. Нужно повторному запуску:
// пока открыто окно об аварии, значка в трее нет, и без этого щелчок по ярлыку
// выглядел бы так, будто ничего не происходит. Возвращает false, если окна нет.
func FocusMessageBox(title string) bool {
	// Своё окно аварии (ShowFatal) или стандартное окно сообщения.
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr(fatalClassName))), uintptr(unsafe.Pointer(utf16Ptr(title))))
	if h == 0 {
		h, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr("#32770"))), uintptr(unsafe.Pointer(utf16Ptr(title))))
	}
	if h == 0 {
		return false
	}
	// Процесс только что запущен человеком, поэтому ему разрешён передний план.
	forceForeground(h)
	return true
}

// New создаёт скрытое окно и значок. Вызывать из горутины, закреплённой за
// потоком ОС (runtime.LockOSThread), и в ней же вызвать Run.
func New(opts Options) (*App, error) {
	if opts.Tooltip == "" {
		opts.Tooltip = trayctl.AppName
	}
	enableDPIAwareness()
	a := &App{opts: opts, hInst: hInstance(), dialogs: map[uintptr]*dialog{}}
	theApp = a

	a.loadIcons()
	trayProc := syscall.NewCallback(trayWndProc)
	if err := registerClass(trayClassName, trayProc, 0, 0, 0, 0); err != nil {
		return nil, err
	}
	dlgProc := syscall.NewCallback(dialogWndProc)
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	if err := registerClass(dialogClassName, dlgProc, cursor, colorWindow+1, a.iconBig, a.iconSmall); err != nil {
		return nil, err
	}

	// Обычное (не message-only) окно без WS_VISIBLE: только такие получают
	// широковещательные WM_QUERYENDSESSION и «TaskbarCreated».
	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(trayClassName))), uintptr(unsafe.Pointer(utf16Ptr(opts.Tooltip))),
		wsOverlapped, 0, 0, 0, 0, 0, 0, a.hInst, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("создание окна: %w", e)
	}
	a.hwnd = hwnd
	a.taskbarCreated = registerWindowMessage("TaskbarCreated")
	if err := a.addIcon(); err != nil {
		return nil, err
	}
	return a, nil
}

// Attach связывает обёртку с контроллером (меню строится из его состояния).
func (a *App) Attach(c *trayctl.Controller) { a.ctl = c }

func registerClass(name string, proc, cursor, brush, icon, iconSm uintptr) error {
	wc := wndClassEx{
		lpfnWndProc:   proc,
		hInstance:     hInstance(),
		hIcon:         icon,
		hCursor:       cursor,
		hbrBackground: brush,
		lpszClassName: utf16Ptr(name),
		hIconSm:       iconSm,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("регистрация класса окна %s: %w", name, e)
	}
	return nil
}

func registerWindowMessage(name string) uint32 {
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16Ptr(name))))
	return uint32(r)
}

func (a *App) loadIcons() {
	cx, _, _ := procGetSystemMetrics.Call(smCXSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCYSmIcon)
	bx, _, _ := procGetSystemMetrics.Call(smCXIcon)
	by, _, _ := procGetSystemMetrics.Call(smCYIcon)
	load := func(name string, w, h uintptr) uintptr {
		ic, _, _ := procLoadImageW.Call(a.hInst, uintptr(unsafe.Pointer(utf16Ptr(name))), imageIcon, w, h, lrShared)
		if ic == 0 { // запуск без встроенных ресурсов (go run, тесты)
			ic, _, _ = procLoadIconW.Call(0, idiApplication)
		}
		return ic
	}
	a.iconSmall = load(iconAppName, cx, cy)
	a.iconBig = load(iconAppName, bx, by)
	for light := 0; light < 2; light++ {
		for live := 0; live < 2; live++ {
			a.trayIcons[light][live] = load(trayIconName(light == 1, live == 1), cx, cy)
		}
	}
	a.lightMode = panelUsesLightTheme(registryTheme)
}

func (a *App) notifyData(flags uint32) notifyIconData {
	var n notifyIconData
	n.cbSize = uint32(unsafe.Sizeof(n))
	n.hWnd = a.hwnd
	n.uID = trayIconID
	n.uFlags = flags
	return n
}

func (a *App) currentIcon() uintptr {
	return a.trayIcons[b2i(a.lightMode)][b2i(a.live)]
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// onSettingChange перечитывает тему панели задач (WM_SETTINGCHANGE приходит и
// при смене темы, и по многим другим поводам, поэтому сравниваем с прежней) и
// при необходимости меняет значок.
func (a *App) onSettingChange() {
	light := panelUsesLightTheme(registryTheme)
	if light == a.lightMode {
		return
	}
	a.lightMode = light
	if a.opts.Logf != nil {
		a.opts.Logf("тема панели задач сменилась: светлая=%v; значок трея перекрашен", light)
	}
	if !a.iconAdded {
		return
	}
	n := a.notifyData(nifIcon)
	n.hIcon = a.currentIcon()
	a.modifyIcon(&n, "смена темы значка")
}

func (a *App) addIcon() error {
	n := a.notifyData(nifMsg | nifIcon | nifTip)
	n.uCallbackMessage = wmTrayCallback
	n.hIcon = a.currentIcon()
	copyUTF16(n.szTip[:], a.opts.Tooltip)
	// Сразу после загрузки Windows оболочка иногда ещё не готова: пробуем несколько раз.
	var last error
	for i := 0; i < 20; i++ {
		r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&n)))
		if r != 0 {
			a.iconAdded = true
			return nil
		}
		last = e
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("добавление значка в трей: %w", last)
}

func (a *App) removeIcon() {
	if !a.iconAdded {
		return
	}
	n := a.notifyData(0)
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
	a.iconAdded = false
}

// Run — цикл сообщений; возвращается после Quit или завершения сеанса.
func (a *App) Run() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		// Клавиши Tab, Enter и Esc в окнах ввода (они не диалоги Windows).
		if act, _, _ := procGetActiveWindow.Call(); act != 0 {
			if _, ok := a.dialogs[act]; ok {
				if handled, _, _ := procIsDialogMessageW.Call(act, uintptr(unsafe.Pointer(&m))); handled != 0 {
					continue
				}
			}
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	a.removeIcon()
}

// post выполняет f в потоке окон.
func (a *App) post(f func()) {
	a.qmu.Lock()
	a.q = append(a.q, f)
	a.qmu.Unlock()
	postMessage(a.hwnd, wmRun, 0, 0)
}

func (a *App) drain() {
	a.qmu.Lock()
	q := a.q
	a.q = nil
	a.qmu.Unlock()
	for _, f := range q {
		f()
	}
}

// --- trayctl.UI ---

// Notify показывает уведомление у значка (balloon/toast).
func (a *App) Notify(kind trayctl.NoticeKind, title, text string) {
	a.post(func() {
		n := a.notifyData(nifInfo)
		copyUTF16(n.szInfoTitle[:], title)
		copyUTF16(n.szInfo[:], text)
		n.dwInfoFlags = niifInfo
		if kind == trayctl.NoticeError {
			n.dwInfoFlags = niifError
		}
		a.modifyIcon(&n, "уведомление")
	})
}

// Changed обновляет значок: с точкой, пока идёт показ.
func (a *App) Changed() {
	a.post(func() {
		if a.ctl == nil {
			return
		}
		live := a.ctl.Snapshot().Phase == trayctl.Streaming
		if live == a.live {
			return
		}
		a.live = live
		n := a.notifyData(nifIcon)
		n.hIcon = a.currentIcon()
		a.modifyIcon(&n, "смена значка")
	})
}

// Quit убирает значок и выходит из цикла сообщений.
func (a *App) Quit() {
	a.quitReq.Store(true)
	a.post(a.doQuit)
}

// doQuit — выход в потоке окон. Если открыто меню, его нужно закрыть: иначе
// цикл сообщений не получит WM_QUIT, пока человек не закроет меню сам.
func (a *App) doQuit() {
	a.quitting = true
	a.removeIcon()
	if a.menuOpen {
		procEndMenu.Call()
	}
	procPostQuitMessage.Call(0)
}

// AskPIN показывает окно ввода кода с экрана телевизора.
func (a *App) AskPIN(ctx context.Context, wrong bool) (string, error) {
	text, ok, err := a.ask(ctx, pinSpec(wrong))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", trayctl.ErrPINCanceled
	}
	return text, nil
}

// AskPassword запрашивает пароль приёмника, скрывая ввод.
func (a *App) AskPassword(ctx context.Context, wrong bool) (string, error) {
	text, ok, err := a.ask(ctx, passwordSpec(wrong))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", trayctl.ErrPINCanceled
	}
	return text, nil
}

// AskAddress показывает окно ввода адреса телевизора.
func (a *App) AskAddress(ctx context.Context, initial string) (string, bool) {
	text, ok, err := a.ask(ctx, addressSpec(initial))
	if err != nil || !ok {
		return "", false
	}
	return text, true
}

// ask открывает окно в потоке окон и ждёт ответа; ctx закрывает окно.
func (a *App) ask(ctx context.Context, spec dialogSpec) (string, bool, error) {
	t := &ticket{res: make(chan dialogResult, 1)}
	a.post(func() { a.openDialog(spec, t) })
	select {
	case r := <-t.res:
		return r.text, r.ok, nil
	case <-ctx.Done():
		a.post(func() { a.cancelTicket(t) })
		return "", false, ctx.Err()
	}
}

// --- меню ---

func (a *App) showMenu(atIcon bool) {
	// Пока меню открыто, TrackPopupMenu крутит свой цикл сообщений, и в него могут
	// прийти ещё один щелчок по значку или повторный запуск программы. Второе меню
	// не открываем: иначе вложенные вызовы по-разному освободят шрифты и обработчики.
	if a.inMenu || a.ctl == nil || a.quitting {
		return
	}
	a.inMenu = true
	defer func() { a.inMenu = false }()
	a.ctl.RefreshDevices()
	items := a.ctl.Menu()

	hmenu, _, _ := procCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hmenu)
	// Где откроется меню: у значка (его может держать панель задач на любом
	// мониторе) или у курсора.
	var pt point
	align := uintptr(tpmBottomAlign | tpmLeftAlign)
	if atIcon {
		if r, ok := a.iconRect(); ok {
			pt = point{(r.left + r.right) / 2, r.top}
			align = tpmBottomAlign | tpmCenterAlign
		} else {
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		}
	} else {
		procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	}

	// Пункты рисуем сами (крупно, см. menu_draw.go): в данных пункта — его номер.
	// Масштаб берём у монитора, где откроется меню, а не у скрытого окна трея
	// (оно всегда на основном мониторе): Windows наше рисование не масштабирует.
	dpi, ok := dpiForPoint(pt)
	if !ok {
		dpi = dpiForWindow(a.hwnd)
	}
	style := newMenuStyle(items, dpi)
	defer style.release()
	for i, it := range items {
		if it.Kind == trayctl.ItemSeparator {
			procAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfOwnerDraw)
		if it.Disabled {
			flags |= mfGrayed
		}
		procAppendMenuW.Call(hmenu, flags, uintptr(menuIDBase+i), uintptr(i))
	}
	a.menu = style
	defer func() { a.menu = nil }()

	// Без этого меню не закрывается щелчком вне его.
	procSetForegroundWindow.Call(a.hwnd)
	a.menuOpen = true
	procSetTimer.Call(a.hwnd, menuQuitTimerID, menuQuitTimerStep, 0)
	cmd, _, _ := procTrackPopupMenu.Call(hmenu, align|tpmRightButton|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.x), uintptr(pt.y), 0, a.hwnd, 0)
	procKillTimer.Call(a.hwnd, menuQuitTimerID)
	a.menuOpen = false
	postMessage(a.hwnd, wmNull, 0, 0)
	if a.quitReq.Load() || a.quitting {
		// Меню закрыли ради выхода: WM_QUIT мог пропасть во вложенном цикле меню.
		procPostQuitMessage.Call(0)
		return
	}
	if cmd >= menuIDBase && int(cmd-menuIDBase) < len(items) {
		if act := items[cmd-menuIDBase].Action; act != nil {
			go act() // действия могут ждать окна ввода: не блокируем поток окон
		}
	}
}

func (a *App) iconRect() (rect, bool) {
	id := notifyIconIdentifier{hWnd: a.hwnd, uID: trayIconID}
	id.cbSize = uint32(unsafe.Sizeof(id))
	var r rect
	hr, _, _ := procShellNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&r)))
	if hr != 0 || (r.right == 0 && r.left == 0) {
		return rect{}, false
	}
	return r, true
}

// --- оконная процедура скрытого окна ---

func trayWndProc(hwnd, m, wParam, lParam uintptr) uintptr {
	a := theApp
	if a == nil || hwnd != a.hwnd {
		return defWindowProc(hwnd, uint32(m), wParam, lParam)
	}
	switch uint32(m) {
	case wmTrayCallback:
		switch uint32(lParam & 0xFFFF) {
		case wmLButtonUp, wmRButtonUp:
			a.showMenu(false)
		}
		return 0
	case wmRun:
		a.drain()
		return 0
	case wmMeasureItem:
		if a.menu != nil && a.menu.measure(hwnd, lParam) {
			return 1
		}
	case wmDrawItem:
		if a.menu != nil && a.menu.draw(lParam) {
			return 1
		}
	case wmTimer:
		if wParam == menuQuitTimerID {
			if a.quitReq.Load() && a.menuOpen {
				procEndMenu.Call()
			}
			return 0
		}
	case wmShowMenu:
		a.showMenu(true)
		return 0
	case wmSettingChange:
		a.onSettingChange()
		return 0
	case wmQueryEnd:
		return 1 // согласны завершить сеанс
	case wmEndSession:
		if wParam != 0 {
			a.onSessionEnd()
		}
		return 0
	}
	if a.taskbarCreated != 0 && uint32(m) == a.taskbarCreated {
		// Проводник перезапущен: значок пропал, добавляем заново.
		a.iconAdded = false
		_ = a.addIcon()
		return 0
	}
	return defWindowProc(hwnd, uint32(m), wParam, lParam)
}

// onSessionEnd: Windows завершает сеанс — останавливаем показ (TEARDOWN) и убираем значок.
func (a *App) onSessionEnd() {
	a.quitting = true
	if a.ctl != nil {
		a.ctl.Shutdown(3 * time.Second)
	}
	a.removeIcon()
	if a.menuOpen {
		procEndMenu.Call()
	}
	procPostQuitMessage.Call(0) // Windows сама завершит процесс; выходим из цикла, чтобы сделать это чисто
}

// modifyIcon отправляет NIM_MODIFY и пишет в журнал, если оболочка отказала.
func (a *App) modifyIcon(n *notifyIconData, what string) {
	r, _, e := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(n)))
	if r == 0 && a.opts.Logf != nil {
		a.opts.Logf("Shell_NotifyIconW (%s) не удалось: %v", what, e)
	}
}
