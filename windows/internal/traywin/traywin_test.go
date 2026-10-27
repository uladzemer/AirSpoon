//go:build windows

package traywin

import (
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCopyUTF16TruncatesAndTerminates(t *testing.T) {
	var buf [8]uint16
	copyUTF16(buf[:], "абвгдежзик") // длиннее буфера
	if buf[7] != 0 {
		t.Fatalf("нет завершающего нуля: %v", buf)
	}
	if got := syscall.UTF16ToString(buf[:]); got != "абвгдеж" {
		t.Fatalf("усечение: %q", got)
	}

	// Короткая строка полностью затирает прежнее содержимое.
	for i := range buf {
		buf[i] = 'x'
	}
	copyUTF16(buf[:], "ab")
	if got := syscall.UTF16ToString(buf[:]); got != "ab" || buf[5] != 0 {
		t.Fatalf("остаток после короткой строки: %q %v", got, buf)
	}
}

func TestCopyUTF16DoesNotSplitSurrogatePair(t *testing.T) {
	var buf [4]uint16
	copyUTF16(buf[:], "ab😀") // 'a','b', пара суррогатов: влезает только половина
	for _, c := range buf {
		if u16IsHighSurrogate(c) && c != 0 {
			// Допустимо, только если за ней стоит низкий суррогат.
			t.Fatalf("оборванная суррогатная пара: %v", buf)
		}
	}
	if got := string(utf16.Decode(buf[:2])); got != "ab" {
		t.Fatalf("начало строки: %q", got)
	}
}

func TestLoHiWord(t *testing.T) {
	v := uintptr(0x0300<<16 | 102)
	if loword(v) != 102 || hiword(v) != 0x0300 {
		t.Fatalf("loword=%d hiword=%#x", loword(v), hiword(v))
	}
}

func TestPINDialogSpecFollowsRequirements(t *testing.T) {
	s := pinSpec(false)
	if s.label != "Введите код с экрана телевизора" || s.okText != "Подключить" || s.cancelText != "Отмена" {
		t.Fatalf("тексты окна кода: %+v", s)
	}
	if s.limit != 4 || s.minLen != 4 || !s.numeric || s.errText != "" {
		t.Fatalf("поле на 4 цифры: %+v", s)
	}
	if s.labelPt < 20 || s.labelPt > 24 || s.btnPt < 20 || s.btnPt > 24 {
		t.Fatalf("шрифт должен быть 20-24 pt: подпись %d, кнопки %d", s.labelPt, s.btnPt)
	}
	if w := pinSpec(true); w.errText != "Код не подошёл, попробуйте ещё раз" {
		t.Fatalf("текст ошибки: %q", w.errText)
	}
}

func TestDialogLayoutFits(t *testing.T) {
	for name, s := range map[string]dialogSpec{"код": pinSpec(true), "адрес": addressSpec("10.0.0.1")} {
		m := layoutMetrics(s)
		inner := s.width - 2*m.margin
		if m.okW+m.gap+m.cancelW > inner {
			t.Errorf("%s: кнопки (%d) не помещаются в ширину %d", name, m.okW+m.gap+m.cancelW, inner)
		}
		if m.clientHeight() < 200 || m.clientHeight() > 500 {
			t.Errorf("%s: странная высота окна %d", name, m.clientHeight())
		}
		if !strings.HasSuffix(s.label, ":") && name == "адрес" {
			t.Errorf("%s: подпись поля без двоеточия: %q", name, s.label)
		}
	}
}

func TestScaleFollowsDPI(t *testing.T) {
	if scale(100, 96) != 100 || scale(100, 192) != 200 || scale(100, 144) != 150 {
		t.Fatalf("масштаб по DPI: %d %d %d", scale(100, 96), scale(100, 192), scale(100, 144))
	}
}

func TestDpiForPointIsSane(t *testing.T) {
	dpi, ok := dpiForPoint(point{0, 0})
	if !ok {
		t.Skip("DPI монитора недоступен в этом окружении (нет монитора или shcore.dll)")
	}
	if dpi < 96 || dpi > 480 {
		t.Fatalf("DPI монитора %d вне разумных пределов", dpi)
	}
}

func TestFocusMessageBoxMissing(t *testing.T) {
	if FocusMessageBox("AirSpoon: такого окна точно нет") {
		t.Fatal("окно найдено, хотя его нет")
	}
}

// FocusMessageBox находит настоящее окно сообщения по заголовку.
func TestFocusMessageBoxFindsMessageBox(t *testing.T) {
	const title = "AirSpoon тест окна сообщения"
	done := make(chan struct{})
	go func() {
		defer close(done)
		windows.MessageBox(0, windows.StringToUTF16Ptr("тест"), windows.StringToUTF16Ptr(title), windows.MB_OK)
	}()
	var found bool
	for i := 0; i < 50 && !found; i++ {
		found = FocusMessageBox(title)
		time.Sleep(100 * time.Millisecond)
	}
	// Закрываем окно в любом случае, иначе горутина зависнет.
	if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr("#32770"))), uintptr(unsafe.Pointer(utf16Ptr(title)))); h != 0 {
		postMessage(h, wmClose, 0, 0)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("окно сообщения не закрылось")
	}
	if !found {
		t.Skip("окно сообщения не появилось в этом сеансе (нет рабочего стола)")
	}
}
