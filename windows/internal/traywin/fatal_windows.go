//go:build windows

package traywin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Окно аварии: программа не смогла запуститься. Это не стандартное окно
// сообщения, а своё крупное окно в стиле окна кода (Segoe UI 20-22 pt): текст
// понятен без технических слов, кнопка «Открыть журнал» показывает в Проводнике
// файл журнала, «Закрыть» — закрывает окно. Работает без App и без значка трея
// (его показывают и наблюдатель, и рабочий процесс, у которого значок не создался).

// FatalStartText — текст окна, если программа не смогла запуститься.
const FatalStartText = "AirSpoon Sender не смог запуститься. Перезапустите компьютер и попробуйте снова."

const (
	fatalClassName = "AirSpoonSenderFatal"
	fatalTitle     = "AirSpoon Sender"

	idOpenLog    = 1001
	ctlFatalText = 201

	fatalLabelPt = 22
	fatalBtnPt   = 20
)

// fatalMetrics — размеры окна аварии в пикселях при 96 DPI.
type fatalMetrics struct {
	width, margin, textH, btnH, openW, closeW, gap int
}

func fatalLayout() fatalMetrics {
	return fatalMetrics{
		width:  640,
		margin: 28,
		textH:  px(fatalLabelPt) * 3 / 2 * 4, // до четырёх строк
		btnH:   px(fatalBtnPt)*8/5 + 14,
		openW:  300,
		closeW: 190,
		gap:    16,
	}
}

func (m fatalMetrics) clientHeight() int { return m.margin + m.textH + 20 + m.btnH + m.margin }

// fatalWindow — единственное окно аварии процесса (оконной процедуре нужен доступ).
type fatalWindow struct {
	hwnd    uintptr
	logPath string
	fonts   []uintptr
	dpi     int
	items   []dialogChild
	done    bool
}

var theFatal *fatalWindow

// ShowFatal показывает окно аварии и ждёт, пока его закроют. text — что
// случилось (обычно FatalStartText), logPath — файл журнала для кнопки
// «Открыть журнал». Вызывается из любого потока: у окна собственный цикл сообщений.
func ShowFatal(text, logPath string) {
	// Окно и его цикл сообщений должны жить в одном потоке ОС.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	enableDPIAwareness()
	inst := hInstance()

	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	big := loadAppIcon(inst, smCXIcon, smCYIcon)
	small := loadAppIcon(inst, smCXSmIcon, smCYSmIcon)
	// Повторная регистрация класса при втором показе в этом процессе вернёт ошибку
	// «класс уже есть» — это не страшно.
	_ = registerClass(fatalClassName, syscall.NewCallback(fatalWndProc), cursor, colorWindow+1, big, small)

	const style = wsCaption | wsSysMenu | wsClipChildren
	const exStyle = wsExTopmost | wsExAppWindow
	work, haveWork := cursorMonitorWork()
	x, y := 100, 100
	if haveWork {
		x, y = int(work.left)+50, int(work.top)+50
	}
	hwnd, _, _ := procCreateWindowExW.Call(exStyle,
		uintptr(unsafe.Pointer(utf16Ptr(fatalClassName))), uintptr(unsafe.Pointer(utf16Ptr(fatalTitle))),
		style, uintptr(x), uintptr(y), 300, 200, 0, 0, inst, 0)
	if hwnd == 0 {
		// Окно создать не удалось: хотя бы стандартное окно сообщения.
		_, _ = windows.MessageBox(0, windows.StringToUTF16Ptr(text), windows.StringToUTF16Ptr(fatalTitle),
			windows.MB_OK|windows.MB_ICONERROR|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
		return
	}
	w := &fatalWindow{hwnd: hwnd, logPath: logPath, dpi: dpiForWindow(hwnd)}
	theFatal = w
	defer func() { theFatal = nil }()

	m := fatalLayout()
	inner := m.width - 2*m.margin
	child := func(class, caption string, st uintptr, cx, cy, cw, ch int, id uintptr, font int) {
		c, _, _ := procCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(utf16Ptr(class))), uintptr(unsafe.Pointer(utf16Ptr(caption))),
			st, 0, 0, 0, 0, hwnd, id, inst, 0)
		w.items = append(w.items, dialogChild{hwnd: c, x: cx, y: cy, w: cw, h: ch, font: font})
	}
	// Подпись переносится по словам (стиль без SS_LEFTNOWORDWRAP).
	child("STATIC", text, wsChild|wsVisible|ssLeft|ssNoPrefix, m.margin, m.margin, inner, m.textH, ctlFatalText, fontLabel)
	by := m.margin + m.textH + 20
	closeX := m.width - m.margin - m.closeW
	openX := closeX - m.gap - m.openW
	child("BUTTON", "Открыть журнал", wsChild|wsVisible|wsTabStop|bsPushButton, openX, by, m.openW, m.btnH, idOpenLog, fontBtn)
	child("BUTTON", "Закрыть", wsChild|wsVisible|wsTabStop|bsDefPushBtn, closeX, by, m.closeW, m.btnH, idCancel, fontBtn)

	ww, hh := w.applyDPI()
	if haveWork {
		x = int(work.left) + (int(work.right-work.left)-ww)/2
		y = int(work.top) + (int(work.bottom-work.top)-hh)/2
	}
	procMoveWindow.Call(hwnd, uintptr(x), uintptr(y), uintptr(ww), uintptr(hh), 1)
	procShowWindow.Call(hwnd, swShow)
	forceForeground(hwnd)

	var msgv msg
	for !w.done {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msgv)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if handled, _, _ := procIsDialogMessageW.Call(hwnd, uintptr(unsafe.Pointer(&msgv))); handled != 0 {
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msgv)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msgv)))
	}
}

func loadAppIcon(inst uintptr, cxMetric, cyMetric uintptr) uintptr {
	cx, _, _ := procGetSystemMetrics.Call(cxMetric)
	cy, _, _ := procGetSystemMetrics.Call(cyMetric)
	ic, _, _ := procLoadImageW.Call(inst, uintptr(unsafe.Pointer(utf16Ptr(iconAppName))), imageIcon, cx, cy, lrShared)
	if ic == 0 { // запуск без встроенных ресурсов (go run, тесты)
		ic, _, _ = procLoadIconW.Call(0, idiApplication)
	}
	return ic
}

// applyDPI создаёт шрифты и расставляет дочерние окна под w.dpi; возвращает
// размер окна целиком (с рамкой и заголовком) в пикселях.
func (w *fatalWindow) applyDPI() (width, height int) {
	old := w.fonts
	w.fonts = []uintptr{
		fontLabel: makeFont("Segoe UI", fatalLabelPt, fwNormal, w.dpi),
		fontEdit:  0,
		fontErr:   0,
		fontBtn:   makeFont("Segoe UI", fatalBtnPt, fwNormal, w.dpi),
	}
	for _, c := range w.items {
		procMoveWindow.Call(c.hwnd, uintptr(scale(c.x, w.dpi)), uintptr(scale(c.y, w.dpi)),
			uintptr(scale(c.w, w.dpi)), uintptr(scale(c.h, w.dpi)), 1)
		sendMessage(c.hwnd, wmSetFont, w.fonts[c.font], 1)
	}
	for _, f := range old {
		if f != 0 {
			procDeleteObject.Call(f)
		}
	}
	m := fatalLayout()
	r := rect{0, 0, int32(scale(m.width, w.dpi)), int32(scale(m.clientHeight(), w.dpi))}
	if procAdjustWindowRectExForDpi.Find() == nil {
		const style = wsCaption | wsSysMenu | wsClipChildren
		const exStyle = wsExTopmost | wsExAppWindow
		procAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), style, 0, exStyle, uintptr(w.dpi))
	}
	return int(r.right - r.left), int(r.bottom - r.top)
}

func (w *fatalWindow) close() {
	if w.done {
		return
	}
	w.done = true
	procDestroyWindow.Call(w.hwnd)
	// Будим цикл сообщений, который проверяет w.done.
	postMessage(0, wmNull, 0, 0)
}

func fatalWndProc(hwnd, m, wParam, lParam uintptr) uintptr {
	w := theFatal
	if w == nil || w.hwnd != hwnd {
		return defWindowProc(hwnd, uint32(m), wParam, lParam)
	}
	switch uint32(m) {
	case dmGetDefID:
		return dcHasDefID<<16 | idCancel
	case wmCommand:
		id, code := loword(wParam), hiword(wParam)
		switch {
		case id == idOpenLog && code == bnClicked:
			OpenLogFolder(w.logPath)
		case id == idCancel && code == bnClicked:
			w.close()
		}
		return 0
	case wmDPIChanged:
		w.dpi = int(hiword(wParam))
		w.applyDPI()
		p := *(*unsafe.Pointer)(unsafe.Pointer(&lParam))
		if r := (*rect)(p); r != nil {
			procSetWindowPos.Call(hwnd, 0, uintptr(r.left), uintptr(r.top),
				uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoZOrder|swpNoActivate)
		}
		return 0
	case wmClose:
		w.close()
		return 0
	case wmCtlColorStat:
		procSetBkMode.Call(wParam, transparent)
		procSetTextColor.Call(wParam, 0x000000)
		return sysColorBrush(colorWindow)
	case wmDestroy:
		for _, f := range w.fonts {
			if f != 0 {
				procDeleteObject.Call(f)
			}
		}
		w.fonts = nil
		return 0
	}
	return defWindowProc(hwnd, uint32(m), wParam, lParam)
}

// explorerCmdLine возвращает командную строку Проводника, который откроет папку
// с выделенным файлом журнала; если файла ещё нет — просто папку.
func explorerCmdLine(logPath string, exists func(string) bool) string {
	if exists(logPath) {
		return `explorer.exe /select,"` + logPath + `"`
	}
	return `explorer.exe "` + filepath.Dir(logPath) + `"`
}

// OpenLogFolder открывает в Проводнике папку журнала с выделенным файлом.
func OpenLogFolder(logPath string) {
	if logPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	cmd := exec.Command("explorer.exe")
	// Строку задаём сами: Проводнику нужны именно кавычки вокруг пути после запятой.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: explorerCmdLine(logPath, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
