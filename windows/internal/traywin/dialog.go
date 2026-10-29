//go:build windows

package traywin

import (
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Окна ввода (код с экрана телевизора, адрес) — свои окна Win32 с крупным
// шрифтом Segoe UI; размеры считаются в точках для 96 DPI и масштабируются по
// DPI монитора, на котором окно появилось.

const (
	ctlLabel = 101
	ctlEdit  = 102
	ctlError = 103
)

// dialogSpec описывает окно ввода.
type dialogSpec struct {
	title      string
	label      string
	errText    string // красный текст под полем (пусто — не показывать)
	initial    string
	okText     string
	cancelText string
	password   bool // скрывать ввод, сохранять пробелы
	numeric    bool // только цифры
	center     bool // текст в поле по центру
	limit      int  // максимум символов
	minLen     int  // сколько символов нужно для кнопки «Подключить»
	width      int  // ширина рабочей области, px при 96 DPI
	labelPt    int
	editPt     int
	errPt      int
	btnPt      int
	editW      int // ширина поля, px при 96 DPI (0 — во всю ширину)
}

func pinSpec(wrong bool) dialogSpec {
	s := dialogSpec{
		title:      "AirSpoon Sender",
		label:      "Введите код с экрана телевизора",
		okText:     "Подключить",
		cancelText: "Отмена",
		numeric:    true,
		center:     true,
		limit:      4,
		minLen:     4,
		width:      560,
		labelPt:    22,
		editPt:     32,
		errPt:      18,
		btnPt:      22,
		editW:      220,
	}
	if wrong {
		s.errText = "Код не подошёл, попробуйте ещё раз"
	}
	return s
}

func passwordSpec(wrong bool) dialogSpec {
	s := addressSpec("")
	s.label = "Введите пароль телевизора"
	s.password = true
	if wrong {
		s.errText = "Пароль не подошёл, попробуйте ещё раз"
	}
	return s
}

// valid проверяет и вставленный текст, и ввод с клавиатуры.
func (s dialogSpec) valid(text string) bool {
	n := len([]rune(text))
	if n < s.minLen || n > s.limit {
		return false
	}
	if s.numeric {
		for _, r := range text {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func addressSpec(initial string) dialogSpec {
	return dialogSpec{
		title:      "AirSpoon Sender",
		label:      "Адрес телевизора (например, 192.168.1.50):",
		initial:    initial,
		okText:     "Подключить",
		cancelText: "Отмена",
		limit:      64,
		minLen:     1,
		width:      640,
		labelPt:    20,
		editPt:     22,
		errPt:      16,
		btnPt:      22,
	}
}

type dialogResult struct {
	text string
	ok   bool
}

// ticket связывает горутину, ждущую ответа, с окном в потоке окон.
type ticket struct {
	mu       sync.Mutex
	hwnd     uintptr
	canceled bool
	res      chan dialogResult
}

type dialog struct {
	spec   dialogSpec
	tk     *ticket
	hwnd   uintptr
	edit   uintptr
	ok     uintptr
	errLbl uintptr
	fonts  []uintptr
	done   bool

	dpi      int
	children []dialogChild
}

// Шрифты окна: индексы в dialog.fonts.
const (
	fontLabel = iota
	fontEdit
	fontErr
	fontBtn
)

// dialogChild — дочернее окно с положением и размером в пикселях при 96 DPI;
// layout пересчитывает их под текущий DPI монитора (и при его смене).
type dialogChild struct {
	hwnd       uintptr
	x, y, w, h int
	font       int
}

// metrics — размеры окна в пикселях при 96 DPI (потом масштабируются).
type metrics struct {
	margin, labelH, editH, errH, btnH, okW, cancelW, gap int
}

func px(pt int) int { return pt * 96 / 72 }

func layoutMetrics(s dialogSpec) metrics {
	return metrics{
		margin:  24,
		labelH:  px(s.labelPt) * 8 / 5,
		editH:   px(s.editPt) + 28,
		errH:    px(s.errPt) * 8 / 5,
		btnH:    px(s.btnPt)*8/5 + 14,
		okW:     250,
		cancelW: 190,
		gap:     16,
	}
}

func (m metrics) clientHeight() int {
	return m.margin + m.labelH + 12 + m.editH + 8 + m.errH + 12 + m.btnH + m.margin
}

func (a *App) openDialog(spec dialogSpec, t *ticket) {
	t.mu.Lock()
	canceled := t.canceled
	t.mu.Unlock()
	if canceled {
		return
	}
	const style = wsCaption | wsSysMenu | wsClipChildren
	const exStyle = wsExTopmost | wsExAppWindow

	// Монитор с курсором выбираем до создания окна и создаём окно уже на нём:
	// DPI окна определяется монитором, на котором оно появилось. Иначе при
	// разном масштабе мониторов (ноутбук 150% и внешний 100%) окно получило бы
	// размеры основного монитора, а потом «поплыло» бы после переноса.
	work, haveWork := cursorMonitorWork()
	x, y := 100, 100
	if haveWork {
		x, y = int(work.left)+50, int(work.top)+50
	}
	hwnd, _, _ := procCreateWindowExW.Call(exStyle,
		uintptr(unsafe.Pointer(utf16Ptr(dialogClassName))), uintptr(unsafe.Pointer(utf16Ptr(spec.title))),
		style, uintptr(x), uintptr(y), 300, 200, 0, 0, a.hInst, 0)
	if hwnd == 0 {
		t.res <- dialogResult{}
		return
	}
	d := &dialog{spec: spec, tk: t, hwnd: hwnd, dpi: dpiForWindow(hwnd)}
	a.dialogs[hwnd] = d
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()

	m := layoutMetrics(spec)
	child := func(class, text string, st, ex uintptr, cx, cy, cw, ch int, id uintptr, font int) uintptr {
		c, _, _ := procCreateWindowExW.Call(ex,
			uintptr(unsafe.Pointer(utf16Ptr(class))), uintptr(unsafe.Pointer(utf16Ptr(text))),
			st, 0, 0, 0, 0, hwnd, id, a.hInst, 0)
		d.children = append(d.children, dialogChild{hwnd: c, x: cx, y: cy, w: cw, h: ch, font: font})
		return c
	}

	inner := spec.width - 2*m.margin
	cy := m.margin
	child("STATIC", spec.label, wsChild|wsVisible|ssLeft|ssNoPrefix, 0, m.margin, cy, inner, m.labelH, ctlLabel, fontLabel)
	cy += m.labelH + 12

	editW := spec.editW
	if editW == 0 || editW > inner {
		editW = inner
	}
	editStyle := uintptr(wsChild | wsVisible | wsTabStop | esAutoHScroll)
	if spec.center {
		editStyle |= esCenter
	}
	if spec.password {
		editStyle |= 0x0020 // ES_PASSWORD
	}
	if spec.numeric {
		editStyle |= esNumber
	}
	d.edit = child("EDIT", spec.initial, editStyle, wsExClientEdge, m.margin+(inner-editW)/2, cy, editW, m.editH, ctlEdit, fontEdit)
	sendMessage(d.edit, emSetLimitText, uintptr(spec.limit), 0)
	cy += m.editH + 8

	d.errLbl = child("STATIC", spec.errText, wsChild|wsVisible|ssCenter|ssNoPrefix, 0, m.margin, cy, inner, m.errH, ctlError, fontErr)
	cy += m.errH + 12

	cancelX := spec.width - m.margin - m.cancelW
	okX := cancelX - m.gap - m.okW
	d.ok = child("BUTTON", spec.okText, wsChild|wsVisible|wsTabStop|bsDefPushBtn, 0, okX, cy, m.okW, m.btnH, idOK, fontBtn)
	child("BUTTON", spec.cancelText, wsChild|wsVisible|wsTabStop|bsPushButton, 0, cancelX, cy, m.cancelW, m.btnH, idCancel, fontBtn)
	d.updateOK()

	// Размер окна по рабочей области, положение — по центру монитора с курсором.
	w, h := d.applyDPI()
	if haveWork {
		x = int(work.left) + (int(work.right-work.left)-w)/2
		y = int(work.top) + (int(work.bottom-work.top)-h)/2
	}
	procMoveWindow.Call(hwnd, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)

	procShowWindow.Call(hwnd, swShow)
	forceForeground(hwnd)
	procSetFocus.Call(d.edit)
	sendMessage(d.edit, emSetSel, 0, ^uintptr(0))
}

// cursorMonitorWork возвращает рабочую область монитора, на котором сейчас курсор.
func cursorMonitorWork() (rect, bool) {
	var cur point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(cur.x))|uintptr(uint32(cur.y))<<32, monitorDefaultToNearest)
	if mon == 0 {
		return rect{}, false
	}
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return rect{}, false
	}
	return mi.rcWork, true
}

// applyDPI создаёт шрифты и расставляет дочерние окна под d.dpi (вызывается при
// создании окна и при смене DPI, например при переносе на другой монитор).
// Возвращает размер окна целиком (с рамкой и заголовком) в пикселях.
func (d *dialog) applyDPI() (w, h int) {
	s := d.spec
	old := d.fonts
	d.fonts = []uintptr{
		fontLabel: makeFont("Segoe UI", s.labelPt, fwNormal, d.dpi),
		fontEdit:  makeFont("Segoe UI", s.editPt, fwSemibold, d.dpi),
		fontErr:   makeFont("Segoe UI", s.errPt, fwNormal, d.dpi),
		fontBtn:   makeFont("Segoe UI", s.btnPt, fwNormal, d.dpi),
	}
	for _, c := range d.children {
		procMoveWindow.Call(c.hwnd, uintptr(scale(c.x, d.dpi)), uintptr(scale(c.y, d.dpi)),
			uintptr(scale(c.w, d.dpi)), uintptr(scale(c.h, d.dpi)), 1)
		sendMessage(c.hwnd, wmSetFont, d.fonts[c.font], 1)
	}
	for _, f := range old {
		procDeleteObject.Call(f)
	}
	m := layoutMetrics(s)
	r := rect{0, 0, int32(scale(s.width, d.dpi)), int32(scale(m.clientHeight(), d.dpi))}
	if procAdjustWindowRectExForDpi.Find() == nil {
		const style = wsCaption | wsSysMenu | wsClipChildren
		const exStyle = wsExTopmost | wsExAppWindow
		procAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), style, 0, exStyle, uintptr(d.dpi))
	}
	return int(r.right - r.left), int(r.bottom - r.top)
}

// forceForeground выводит окно на передний план и в фокус, даже если
// Windows не даёт фоновому процессу перехватить фокус.
func forceForeground(hwnd uintptr) {
	fg, _, _ := procGetForegroundWindow.Call()
	var myTid, fgTid uintptr
	attached := false
	if fg != 0 && fg != hwnd {
		fgTid, _, _ = procGetWindowThreadProcessId.Call(fg, 0)
		myTid, _, _ = procGetWindowThreadProcessId.Call(hwnd, 0)
		if fgTid != 0 && fgTid != myTid {
			r, _, _ := procAttachThreadInput.Call(myTid, fgTid, 1)
			attached = r != 0
		}
	}
	procSetWindowPos.Call(hwnd, hwndTopmst, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	if cur, _, _ := procGetForegroundWindow.Call(); cur != hwnd {
		// Windows не отдаёт фокус (пользователь печатает в другом окне): пробуем
		// второй способ, а если и он не помог, мигаем кнопкой окна на панели
		// задач. Окно остаётся поверх всех, так что его всё равно видно.
		procSwitchToThisWindow.Call(hwnd, 1)
		if cur, _, _ = procGetForegroundWindow.Call(); cur != hwnd {
			fi := flashInfo{hwnd: hwnd, dwFlags: flashwAll | flashwTimerNoFG, uCount: 5}
			fi.cbSize = uint32(unsafe.Sizeof(fi))
			procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
		}
	}
	if attached {
		procAttachThreadInput.Call(myTid, fgTid, 0)
	}
}

func (a *App) cancelTicket(t *ticket) {
	t.mu.Lock()
	t.canceled = true
	hwnd := t.hwnd
	t.mu.Unlock()
	if d := a.dialogs[hwnd]; d != nil && hwnd != 0 {
		d.finish("", false)
	}
}

// text возвращает введённый текст (для числового поля — только цифры).
func (d *dialog) text() string {
	s := getWindowText(d.edit)
	if !d.spec.password {
		s = strings.TrimSpace(s)
	}
	return s
}

func (d *dialog) updateOK() {
	enable := uintptr(0)
	if d.spec.valid(d.text()) {
		enable = 1
	}
	procEnableWindow.Call(d.ok, enable)
}

func (d *dialog) finish(text string, ok bool) {
	if d.done {
		return
	}
	d.done = true
	select {
	case d.tk.res <- dialogResult{text: text, ok: ok}:
	default:
	}
	procDestroyWindow.Call(d.hwnd)
}

func (d *dialog) submit() {
	t := d.text()
	if !d.spec.valid(t) {
		return
	}
	d.finish(t, true)
}

func dialogWndProc(hwnd, m, wParam, lParam uintptr) uintptr {
	a := theApp
	var d *dialog
	if a != nil {
		d = a.dialogs[hwnd]
	}
	if d == nil {
		return defWindowProc(hwnd, uint32(m), wParam, lParam)
	}
	switch uint32(m) {
	case dmGetDefID:
		return dcHasDefID<<16 | idOK
	case wmCommand:
		id, code := loword(wParam), hiword(wParam)
		switch {
		case id == ctlEdit && code == enChange:
			d.updateOK()
			if d.spec.errText != "" {
				setWindowText(d.errLbl, "") // ошибка относилась к прежнему коду
			}
		case id == idOK && code == bnClicked:
			d.submit()
		case id == idCancel && code == bnClicked:
			d.finish("", false)
		}
		return 0
	case wmActivate:
		// При любой активации окна фокус возвращается в поле ввода: так цифры
		// попадают в него, даже если Windows передала окну передний план позже,
		// чем мы вызвали SetFocus.
		if loword(wParam) != 0 && d.edit != 0 {
			procSetFocus.Call(d.edit)
		}
		return 0
	case wmSetFocus:
		if d.edit != 0 {
			procSetFocus.Call(d.edit)
		}
		return 0
	case wmDPIChanged:
		// Окно перенесли на монитор с другим масштабом: пересчитываем шрифты и
		// размеры, а положение берём то, что предлагает Windows (lParam — RECT).
		d.dpi = int(hiword(wParam))
		d.applyDPI()
		p := *(*unsafe.Pointer)(unsafe.Pointer(&lParam))
		if r := (*rect)(p); r != nil {
			procSetWindowPos.Call(hwnd, 0, uintptr(r.left), uintptr(r.top),
				uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoZOrder|swpNoActivate)
		}
		return 0
	case wmClose:
		d.finish("", false)
		return 0
	case wmCtlColorStat:
		procSetBkMode.Call(wParam, transparent)
		if lParam == d.errLbl {
			procSetTextColor.Call(wParam, 0x0000B0) // тёмно-красный (COLORREF 0x00BBGGRR)
		} else {
			procSetTextColor.Call(wParam, 0x000000)
		}
		return sysColorBrush(colorWindow)
	case wmDestroy:
		for _, f := range d.fonts {
			procDeleteObject.Call(f)
		}
		delete(a.dialogs, hwnd)
		d.tk.mu.Lock()
		d.tk.hwnd = 0
		d.tk.mu.Unlock()
		return 0
	}
	return defWindowProc(hwnd, uint32(m), wParam, lParam)
}

// Confirm спрашивает у пользователя подтверждение Да/Нет окном сообщения в стиле окон программы.
// Возвращает true, если нажата кнопка «Да».
func Confirm(title, text string) bool {
	r, _ := windows.MessageBox(0, windows.StringToUTF16Ptr(text), windows.StringToUTF16Ptr(title),
		windows.MB_YESNO|windows.MB_ICONQUESTION|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
	return r == idYes
}
