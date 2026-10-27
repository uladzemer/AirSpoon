//go:build windows

package traywin

import (
	"unsafe"

	"airspoon/internal/trayctl"
)

// Крупное меню. Системное меню Windows рисуется шрифтом около 9 пунктов и
// серым для недоступных строк; пожилым людям это читать трудно. Поэтому
// пункты рисуем сами (MF_OWNERDRAW): шрифт Segoe UI 14 пунктов, высокие строки,
// чёрный текст, длинная подсказка переносится на несколько строк.

const (
	wmMeasureItem = 0x002C
	wmDrawItem    = 0x002B

	mfOwnerDraw = 0x0100
	odtMenu     = 1
	odsSelected = 0x0001
	odsGrayed   = 0x0002
	odsDisabled = 0x0004

	dtLeft       = 0x0000
	dtVCenter    = 0x0004
	dtWordBreak  = 0x0010
	dtSingleLine = 0x0020
	dtNoPrefix   = 0x0800
	dtCalcRect   = 0x0400

	colorMenu          = 4
	colorMenuText      = 7
	colorHighlight     = 13
	colorHighlightText = 14
	colorGrayText      = 17

	menuFontPt     = 14
	menuMaxTextW   = 460 // ширина текста пункта при 96 DPI; длиннее — перенос строки
	menuPadX       = 14  // отступ слева/справа при 96 DPI
	menuCheckW     = 30  // место под галочку при 96 DPI
	menuPadY       = 9   // отступ сверху/снизу при 96 DPI
	menuMarlettPt  = 14
	marlettCheck   = "a" // в шрифте Marlett это галочка
	fwNormalWeight = 400
)

var (
	procGetDC     = user32.NewProc("GetDC")
	procReleaseDC = user32.NewProc("ReleaseDC")
	procDrawTextW = user32.NewProc("DrawTextW")
	procFillRect  = user32.NewProc("FillRect")
	procSelectObj = gdi32.NewProc("SelectObject")
)

type measureItemStruct struct {
	ctlType, ctlID, itemID, itemWidth, itemHeight uint32
	itemData                                      uintptr
}

type drawItemStruct struct {
	ctlType, ctlID, itemID, itemAction, itemState uint32
	hwndItem, hDC                                 uintptr
	rcItem                                        rect
	itemData                                      uintptr
}

// menuStyle — шрифты и данные открытого меню (только поток окон).
type menuStyle struct {
	items []trayctl.MenuItem
	dpi   int
	font  uintptr // Segoe UI
	check uintptr // Marlett (галочка)
}

func newMenuStyle(items []trayctl.MenuItem, dpi int) *menuStyle {
	return &menuStyle{
		items: items,
		dpi:   dpi,
		font:  makeFont("Segoe UI", menuFontPt, fwNormalWeight, dpi),
		check: makeFont("Marlett", menuMarlettPt, fwNormalWeight, dpi),
	}
}

func (m *menuStyle) release() {
	procDeleteObject.Call(m.font)
	procDeleteObject.Call(m.check)
}

func (m *menuStyle) item(data uintptr) (trayctl.MenuItem, bool) {
	if int(data) < 0 || int(data) >= len(m.items) {
		return trayctl.MenuItem{}, false
	}
	return m.items[data], true
}

// textRect считает прямоугольник текста пункта в пикселях (с переносом строк).
func (m *menuStyle) textSize(hdc uintptr, text string) (w, h int) {
	old, _, _ := procSelectObj.Call(hdc, m.font)
	defer procSelectObj.Call(hdc, old)
	r := rect{0, 0, int32(scale(menuMaxTextW, m.dpi)), 0}
	u := utf16Ptr(text)
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u)), ^uintptr(0), uintptr(unsafe.Pointer(&r)),
		dtCalcRect|dtWordBreak|dtNoPrefix|dtLeft)
	return int(r.right - r.left), int(r.bottom - r.top)
}

// measure отвечает на WM_MEASUREITEM.
func (m *menuStyle) measure(hwnd, lParam uintptr) bool {
	p := *(*unsafe.Pointer)(unsafe.Pointer(&lParam))
	mi := (*measureItemStruct)(p)
	if mi == nil || mi.ctlType != odtMenu {
		return false
	}
	it, ok := m.item(mi.itemData)
	if !ok {
		return false
	}
	hdc, _, _ := procGetDC.Call(hwnd)
	defer procReleaseDC.Call(hwnd, hdc)
	w, h := m.textSize(hdc, it.Text)
	mi.itemWidth = uint32(w + scale(menuPadX*2+menuCheckW, m.dpi))
	mi.itemHeight = uint32(h + scale(menuPadY*2, m.dpi))
	return true
}

// draw отвечает на WM_DRAWITEM.
func (m *menuStyle) draw(lParam uintptr) bool {
	p := *(*unsafe.Pointer)(unsafe.Pointer(&lParam))
	di := (*drawItemStruct)(p)
	if di == nil || di.ctlType != odtMenu {
		return false
	}
	it, ok := m.item(di.itemData)
	if !ok {
		return false
	}
	disabled := di.itemState&(odsGrayed|odsDisabled) != 0
	selected := di.itemState&odsSelected != 0 && !disabled

	bg, fg := colorMenu, colorMenuText
	switch {
	case selected:
		bg, fg = colorHighlight, colorHighlightText
	case disabled:
		fg = colorGrayText
	}
	procFillRect.Call(di.hDC, uintptr(unsafe.Pointer(&di.rcItem)), sysColorBrush(bg))
	procSetBkMode.Call(di.hDC, transparent)
	color, _, _ := procGetSysColor.Call(uintptr(fg))
	procSetTextColor.Call(di.hDC, color)

	r := di.rcItem
	if it.Checked {
		old, _, _ := procSelectObj.Call(di.hDC, m.check)
		cr := rect{r.left + int32(scale(menuPadX/2, m.dpi)), r.top, r.left + int32(scale(menuPadX+menuCheckW, m.dpi)), r.bottom}
		procDrawTextW.Call(di.hDC, uintptr(unsafe.Pointer(utf16Ptr(marlettCheck))), ^uintptr(0), uintptr(unsafe.Pointer(&cr)),
			dtSingleLine|dtVCenter|dtLeft|dtNoPrefix)
		procSelectObj.Call(di.hDC, old)
	}
	old, _, _ := procSelectObj.Call(di.hDC, m.font)
	tr := rect{r.left + int32(scale(menuPadX+menuCheckW, m.dpi)), r.top + int32(scale(menuPadY, m.dpi)), r.right - int32(scale(menuPadX, m.dpi)), r.bottom - int32(scale(menuPadY, m.dpi))}
	procDrawTextW.Call(di.hDC, uintptr(unsafe.Pointer(utf16Ptr(it.Text))), ^uintptr(0), uintptr(unsafe.Pointer(&tr)),
		dtWordBreak|dtNoPrefix|dtLeft)
	procSelectObj.Call(di.hDC, old)
	return true
}
