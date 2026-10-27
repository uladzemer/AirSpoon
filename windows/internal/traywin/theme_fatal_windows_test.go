//go:build windows

package traywin

import (
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"airspoon/internal/trayctl"
)

func TestTrayIconNameFollowsThemeAndState(t *testing.T) {
	cases := []struct {
		light, live bool
		want        string
	}{
		{false, false, "TRAYW"}, // тёмная панель: белый знак
		{true, false, "TRAYK"},  // светлая панель: чёрный знак
		{false, true, "LIVEW"},
		{true, true, "LIVEK"},
	}
	for _, c := range cases {
		if got := trayIconName(c.light, c.live); got != c.want {
			t.Errorf("светлая=%v показ=%v: %s, ожидали %s", c.light, c.live, got, c.want)
		}
	}
}

func TestPanelUsesLightTheme(t *testing.T) {
	read := func(v uint32, ok bool) themeReader {
		return func(name string) (uint32, bool) {
			if name != "SystemUsesLightTheme" {
				t.Fatalf("читаем %q: для панели задач нужен SystemUsesLightTheme", name)
			}
			return v, ok
		}
	}
	if !panelUsesLightTheme(read(1, true)) {
		t.Error("значение 1 — светлая панель")
	}
	if panelUsesLightTheme(read(0, true)) {
		t.Error("значение 0 — тёмная панель")
	}
	if panelUsesLightTheme(read(0, false)) {
		t.Error("нет значения — считаем панель тёмной")
	}
}

// Реестр этого компьютера читается без ошибок (значение может отсутствовать).
func TestRegistryThemeDoesNotPanic(t *testing.T) {
	_ = panelUsesLightTheme(registryTheme)
}

func TestOnSettingChangeSwitchesIcon(t *testing.T) {
	a := &App{lightMode: false}
	a.trayIcons[0][0], a.trayIcons[1][0] = 11, 22
	if a.currentIcon() != 11 {
		t.Fatalf("тёмная тема: значок %d", a.currentIcon())
	}
	a.lightMode = true
	if a.currentIcon() != 22 {
		t.Fatalf("светлая тема: значок %d", a.currentIcon())
	}
	a.live = true
	a.trayIcons[1][1] = 33
	if a.currentIcon() != 33 {
		t.Fatalf("светлая тема и показ: значок %d", a.currentIcon())
	}
}

// Повторный вход в showMenu (щелчок по значку или повторный запуск, пока меню
// уже открыто) не открывает второе меню и не трогает состояние первого.
func TestShowMenuIgnoresReentry(t *testing.T) {
	a := &App{ctl: trayctl.New(trayctl.Config{}), inMenu: true}
	a.showMenu(true)
	a.showMenu(false)
	if !a.inMenu {
		t.Fatal("вложенный вызов сбросил флаг открытого меню первого вызова")
	}
	if a.menu != nil || a.menuOpen {
		t.Fatal("вложенный вызов начал строить меню")
	}
}

func TestFatalLayoutFits(t *testing.T) {
	m := fatalLayout()
	inner := m.width - 2*m.margin
	if m.openW+m.gap+m.closeW > inner {
		t.Fatalf("кнопки (%d) не помещаются в ширину %d", m.openW+m.gap+m.closeW, inner)
	}
	if fatalLabelPt < 20 || fatalLabelPt > 22 || fatalBtnPt < 20 || fatalBtnPt > 22 {
		t.Fatalf("шрифт окна аварии должен быть 20-22 pt: %d и %d", fatalLabelPt, fatalBtnPt)
	}
	if h := m.clientHeight(); h < 200 || h > 500 {
		t.Fatalf("странная высота окна аварии: %d", h)
	}
}

func TestFatalTextIsPlainRussian(t *testing.T) {
	if FatalStartText != "AirSpoon Sender не смог запуститься. Перезапустите компьютер и попробуйте снова." {
		t.Fatalf("текст окна аварии: %q", FatalStartText)
	}
}

func TestExplorerCmdLine(t *testing.T) {
	const log = `C:\Users\Иван Петров\AppData\Local\AirSpoon\sender.log`
	got := explorerCmdLine(log, func(string) bool { return true })
	if got != `explorer.exe /select,"`+log+`"` {
		t.Fatalf("с файлом журнала: %s", got)
	}
	got = explorerCmdLine(log, func(string) bool { return false })
	if got != `explorer.exe "C:\Users\Иван Петров\AppData\Local\AirSpoon"` {
		t.Fatalf("без файла журнала открывается папка: %s", got)
	}
}

// Окно аварии открывается, находится по классу и закрывается по WM_CLOSE.
func TestShowFatalOpensAndCloses(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ShowFatal(FatalStartText, `C:\нет\такого\sender.log`)
	}()
	var found bool
	var h uintptr
	for i := 0; i < 50 && !found; i++ {
		h, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr(fatalClassName))), 0)
		found = h != 0
		if !found {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if found {
		if !FocusMessageBox("AirSpoon Sender") {
			t.Error("FocusMessageBox не нашла окно аварии")
		}
		if got := getWindowText(h); got != "AirSpoon Sender" {
			t.Errorf("заголовок окна: %q", got)
		}
		postMessage(h, wmClose, 0, 0)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("окно аварии не закрылось по WM_CLOSE")
	}
	if !found {
		t.Skip("окно не появилось в этом сеансе (нет рабочего стола)")
	}
	if strings.TrimSpace(FatalStartText) == "" {
		t.Fatal("пустой текст")
	}
}

// Ручная проверка вида окна аварии: с AIRSPOON_SHOW_FATAL=1 окно остаётся
// открытым, пока его не закроют (или не пройдёт 40 с), чтобы снять скриншот.
func TestShowFatalManual(t *testing.T) {
	if os.Getenv("AIRSPOON_SHOW_FATAL") != "1" {
		t.Skip("ручная проверка: задайте AIRSPOON_SHOW_FATAL=1")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ShowFatal(FatalStartText, `C:\Users\Иван\AppData\Local\AirSpoon\sender.log`)
	}()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr(fatalClassName))), 0); h != 0 {
			postMessage(h, wmClose, 0, 0)
		}
		<-done
	}
}
