//go:build windows

// Пакет traywin — тонкая Win32-обёртка трея AirSpoon Sender: значок
// (Shell_NotifyIconW), скрытое окно, меню (TrackPopupMenu), уведомления и
// крупные окна ввода кода и адреса. Вся логика живёт в пакете trayctl.
// Без cgo и внешних DLL: только системные user32, shell32, gdi32, kernel32.
package traywin

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")

	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procIsDialogMessageW           = user32.NewProc("IsDialogMessageW")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procSendMessageW               = user32.NewProc("SendMessageW")
	procFindWindowW                = user32.NewProc("FindWindowW")
	procRegisterWindowMessageW     = user32.NewProc("RegisterWindowMessageW")
	procLoadImageW                 = user32.NewProc("LoadImageW")
	procLoadCursorW                = user32.NewProc("LoadCursorW")
	procCreatePopupMenu            = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                = user32.NewProc("AppendMenuW")
	procDestroyMenu                = user32.NewProc("DestroyMenu")
	procTrackPopupMenu             = user32.NewProc("TrackPopupMenu")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetActiveWindow            = user32.NewProc("GetActiveWindow")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput          = user32.NewProc("AttachThreadInput")
	procBringWindowToTop           = user32.NewProc("BringWindowToTop")
	procSetFocus                   = user32.NewProc("SetFocus")
	procGetCursorPos               = user32.NewProc("GetCursorPos")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procSetWindowPos               = user32.NewProc("SetWindowPos")
	procGetWindowRect              = user32.NewProc("GetWindowRect")
	procGetClientRect              = user32.NewProc("GetClientRect")
	procMoveWindow                 = user32.NewProc("MoveWindow")
	procEnableWindow               = user32.NewProc("EnableWindow")
	procSetWindowTextW             = user32.NewProc("SetWindowTextW")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	procGetDpiForWindow            = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem            = user32.NewProc("GetDpiForSystem")
	procAdjustWindowRectExForDpi   = user32.NewProc("AdjustWindowRectExForDpi")
	procMonitorFromPoint           = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW            = user32.NewProc("GetMonitorInfoW")
	procGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	procGetSysColorBrush           = user32.NewProc("GetSysColorBrush")
	procGetSysColor                = user32.NewProc("GetSysColor")
	procGetDlgItem                 = user32.NewProc("GetDlgItem")
	procSetTimer                   = user32.NewProc("SetTimer")
	procKillTimer                  = user32.NewProc("KillTimer")
	procEndMenu                    = user32.NewProc("EndMenu")
	procDestroyIcon                = user32.NewProc("DestroyIcon")
	procLoadIconW                  = user32.NewProc("LoadIconW")
	procFlashWindowEx              = user32.NewProc("FlashWindowEx")
	procSwitchToThisWindow         = user32.NewProc("SwitchToThisWindow")
	procAllowSetForegroundWindow   = user32.NewProc("AllowSetForegroundWindow")
	procShellNotifyIconW           = shell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRect     = shell32.NewProc("Shell_NotifyIconGetRect")
	procCreateFontIndirectW        = gdi32.NewProc("CreateFontIndirectW")
	procDeleteObject               = gdi32.NewProc("DeleteObject")
	procSetBkMode                  = gdi32.NewProc("SetBkMode")
	procSetTextColor               = gdi32.NewProc("SetTextColor")
	procSetBkColor                 = gdi32.NewProc("SetBkColor")
	procGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	procMulDiv                     = kernel32.NewProc("MulDiv")
	procSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwarenessCtxt = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForMonitor           = shcore.NewProc("GetDpiForMonitor")
)

// Сообщения и стили Win32 (только нужные).
const (
	wmNull         = 0x0000
	wmCreate       = 0x0001
	wmNCCreate     = 0x0081
	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmQueryEnd     = 0x0011
	wmEndSession   = 0x0016
	wmSetFont      = 0x0030
	wmActivate     = 0x0006
	wmSetFocus     = 0x0007
	wmCommand      = 0x0111
	wmTimer        = 0x0113
	wmCtlColorStat = 0x0138
	wmUser         = 0x0400
	wmApp          = 0x8000
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	wmContextMenu  = 0x007B
	wmDPIChanged   = 0x02E0
	dmGetDefID     = wmUser + 0
	dcHasDefID     = 0x534B

	emSetLimitText = 0x00C5
	emSetSel       = 0x00B1
	enChange       = 0x0300
	bnClicked      = 0

	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsTabStop      = 0x00010000
	wsClipChildren = 0x02000000
	wsDisabled     = 0x08000000
	wsExTopmost    = 0x00000008
	wsExAppWindow  = 0x00040000
	wsExClientEdge = 0x00000200

	esCenter      = 0x0001
	esAutoHScroll = 0x0080
	esNumber      = 0x2000
	bsPushButton  = 0x00000000
	bsDefPushBtn  = 0x00000001
	ssLeft        = 0x00000000
	ssCenter      = 0x00000001
	ssNoPrefix    = 0x00000080

	swHide     = 0
	swShow     = 5
	swShowNA   = 8
	swRestore  = 9
	hwndTopmst = ^uintptr(0) // (HWND)-1

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
	swpShowWindow = 0x0040

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	tpmLeftAlign   = 0x0000
	tpmCenterAlign = 0x0004
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNoNotify    = 0x0080

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	nifMsg    = 0x01
	nifIcon   = 0x02
	nifTip    = 0x04
	nifInfo   = 0x10
	niifInfo  = 0x01
	niifError = 0x03

	imageIcon     = 1
	lrDefaultSize = 0x40
	lrShared      = 0x8000

	smCXIcon   = 11
	smCYIcon   = 12
	smCXSmIcon = 49
	smCYSmIcon = 50

	colorWindow  = 5
	colorBtnFace = 15

	transparent = 1

	fwNormal   = 400
	fwSemibold = 600
	fwBold     = 700

	monitorDefaultToNearest = 2

	idiApplication = 32512
	idcArrow       = 32512

	vkReturn = 0x0D
	vkEscape = 0x1B

	idOK     = 1
	idCancel = 2

	mbIconInfo = 0x40

	flashwAll       = 0x00000003
	flashwTimerNoFG = 0x0000000C
)

type point struct{ x, y int32 }

type rect struct{ left, top, right, bottom int32 }

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeout         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

type notifyIconIdentifier struct {
	cbSize   uint32
	hWnd     uintptr
	uID      uint32
	guidItem windows.GUID
}

type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

type logFont struct {
	height, width, escapement, orientation, weight int32
	italic, underline, strikeOut, charSet          byte
	outPrecision, clipPrecision, quality, pitchFam byte
	faceName                                       [32]uint16
}

type flashInfo struct {
	cbSize    uint32
	hwnd      uintptr
	dwFlags   uint32
	uCount    uint32
	dwTimeout uint32
}

func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		// Строки с NUL внутри не бывают: заменяем и пробуем снова.
		p, _ = windows.UTF16PtrFromString("?")
	}
	return p
}

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	n := copy(dst[:len(dst)-1], u)
	// Не режем суррогатную пару пополам.
	if n > 0 && n == len(dst)-1 && u16IsHighSurrogate(dst[n-1]) {
		n--
	}
	dst[n] = 0
	for i := n + 1; i < len(dst); i++ {
		dst[i] = 0
	}
}

func u16IsHighSurrogate(c uint16) bool { return c >= 0xD800 && c < 0xDC00 }

func hInstance() uintptr {
	h, _, _ := procGetModuleHandleW.Call(0)
	return h
}

func loword(v uintptr) uint16 { return uint16(v & 0xFFFF) }
func hiword(v uintptr) uint16 { return uint16((v >> 16) & 0xFFFF) }

func sendMessage(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(hwnd, uintptr(m), w, l)
	return r
}

func postMessage(hwnd uintptr, m uint32, w, l uintptr) bool {
	r, _, _ := procPostMessageW.Call(hwnd, uintptr(m), w, l)
	return r != 0
}

func defWindowProc(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(m), w, l)
	return r
}

func getWindowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func setWindowText(hwnd uintptr, s string) {
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(s))))
}

func mulDiv(a, b, c int) int {
	r, _, _ := procMulDiv.Call(uintptr(int32(a)), uintptr(int32(b)), uintptr(int32(c)))
	return int(int32(r))
}

func sysColorBrush(index int) uintptr {
	r, _, _ := procGetSysColorBrush.Call(uintptr(index))
	return r
}

// enableDPIAwareness включает PerMonitorV2 на случай, если манифест не встроен
// (go run, тесты); при встроенном манифесте вызов безвреден (вернёт ошибку).
func enableDPIAwareness() {
	if procSetProcessDpiAwarenessCtxt.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
		minusFour := ^uintptr(3)
		procSetProcessDpiAwarenessCtxt.Call(minusFour)
	}
}

func dpiForWindow(hwnd uintptr) int {
	if procGetDpiForWindow.Find() == nil {
		if d, _, _ := procGetDpiForWindow.Call(hwnd); d != 0 {
			return int(d)
		}
	}
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d != 0 {
			return int(d)
		}
	}
	return 96
}

// dpiForPoint возвращает DPI монитора, на котором лежит точка экрана (или
// ближайшего к ней). Меню рисуется самой программой, и скрытое окно трея всегда
// живёт на основном мониторе, поэтому для меню у значка на втором мониторе с
// другим масштабом нужен DPI именно того монитора. ok=false — монитор не найден.
func dpiForPoint(pt point) (dpi int, ok bool) {
	if procGetDpiForMonitor.Find() != nil {
		return 0, false
	}
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(pt.x))|uintptr(uint32(pt.y))<<32, monitorDefaultToNearest)
	if mon == 0 {
		return 0, false
	}
	var dpiX, dpiY uint32
	// MDT_EFFECTIVE_DPI = 0; S_OK = 0.
	if hr, _, _ := procGetDpiForMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY))); hr != 0 || dpiX == 0 {
		return 0, false
	}
	return int(dpiX), true
}

func scale(v, dpi int) int { return mulDiv(v, dpi, 96) }

func makeFont(face string, pt, weight, dpi int) uintptr {
	lf := logFont{
		height:  int32(-mulDiv(pt, dpi, 72)),
		weight:  int32(weight),
		charSet: 1, // DEFAULT_CHARSET
		quality: 5, // CLEARTYPE_QUALITY
	}
	copyUTF16(lf.faceName[:], face)
	h, _, _ := procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	return h
}
