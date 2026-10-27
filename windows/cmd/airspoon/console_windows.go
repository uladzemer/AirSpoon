package main

import (
	"io"
	"log"
	"os"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const codePageUTF8 = 65001

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procAttachConsole      = kernel32.NewProc("AttachConsole")
)

// useUTF8Console переключает кодовую страницу вывода консоли на UTF-8, чтобы
// русские сообщения не превращались в кракозябры в cmd.exe и Windows PowerShell
// (там по умолчанию 866 или 1251; Go всегда пишет UTF-8). Возвращает функцию,
// которая возвращает прежнюю кодовую страницу: страница относится ко всей
// консоли и без возврата осталась бы изменённой после выхода программы.
// Если консоли нет (запуск без неё), ничего не меняет.
func useUTF8Console() (restore func()) {
	return switchConsoleOutputCP(codePageUTF8)
}

func switchConsoleOutputCP(cp uint32) (restore func()) {
	old, _, _ := procGetConsoleOutputCP.Call()
	if old == 0 || uint32(old) == cp {
		return func() {}
	}
	if r, _, _ := procSetConsoleOutputCP.Call(uintptr(cp)); r == 0 {
		return func() {}
	}
	return func() { procSetConsoleOutputCP.Call(old) }
}

// consoleOutputCP возвращает текущую кодовую страницу вывода консоли (0 — консоли нет).
func consoleOutputCP() uint32 {
	cp, _, _ := procGetConsoleOutputCP.Call()
	return uint32(cp)
}

// attachParentConsole подключает программу (собранную как оконная, -H windowsgui)
// к консоли, из которой её запустили, чтобы консольные команды (list, mirror)
// печатали в cmd.exe и PowerShell. Если стандартные дескрипторы уже заданы
// (вывод перенаправлен в файл или конвейер), они остаются как есть. Если
// родительской консоли нет (запуск двойным щелчком), программа работает молча.
//
// Оконная программа не удерживает консоль: cmd.exe вернёт приглашение сразу, не
// дожидаясь завершения команды, поэтому в cmd запускайте через «start /wait», а в
// PowerShell — через «Start-Process -Wait -NoNewWindow». Перенаправленный вывод
// (конвейер, файл) переводится в кодовую страницу консоли (см. transcodeRedirected):
// Windows PowerShell 5.1 читает вывод программ именно в ней, а чистый UTF-8
// давал кракозябры.
func attachParentConsole() {
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS = (DWORD)-1
	stdHandles := []struct {
		id     uint32
		device string
		access uint32
		set    func(*os.File)
	}{
		{windows.STD_INPUT_HANDLE, "CONIN$", windows.GENERIC_READ | windows.GENERIC_WRITE, func(f *os.File) { os.Stdin = f }},
		{windows.STD_OUTPUT_HANDLE, "CONOUT$", windows.GENERIC_READ | windows.GENERIC_WRITE, func(f *os.File) { os.Stdout = f }},
		{windows.STD_ERROR_HANDLE, "CONOUT$", windows.GENERIC_READ | windows.GENERIC_WRITE, func(f *os.File) { os.Stderr = f }},
	}
	var missing []int
	for i, s := range stdHandles {
		if !validStdHandle(s.id) {
			missing = append(missing, i)
		}
	}
	// К консоли подключаемся и тогда, когда все дескрипторы уже заданы
	// (перенаправлены): иначе не узнать кодовую страницу консоли запустившей
	// оболочки, а в ней нужно писать конвейер (см. transcodeRedirected).
	if r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess)); r == 0 {
		return // консоли нет: работаем молча
	}
	if len(missing) == 0 {
		return
	}
	for _, i := range missing {
		s := stdHandles[i]
		name, err := windows.UTF16PtrFromString(s.device)
		if err != nil {
			continue
		}
		h, err := windows.CreateFile(name, s.access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		_ = windows.SetStdHandle(s.id, h) // чтобы дочерние процессы (ffmpeg) наследовали консоль
		s.set(os.NewFile(uintptr(h), s.device))
	}
	log.SetOutput(os.Stderr) // стандартный лог запомнил прежний os.Stderr
}

// validStdHandle сообщает, открыт ли стандартный дескриптор процесса.
func validStdHandle(id uint32) bool {
	h, err := windows.GetStdHandle(id)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && t != windows.FILE_TYPE_UNKNOWN
}

// --- вывод в конвейер и файл ---

var procWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")

// utf8ToCodePage переводит UTF-8 в кодовую страницу cp (866, 1251 и т. п.);
// символы, которых в странице нет, превращаются в «?». При сбое WinAPI возвращает
// исходные байты.
func utf8ToCodePage(cp uint32, b []byte) []byte {
	if len(b) == 0 || cp == 0 || cp == codePageUTF8 {
		return b
	}
	wide := utf16.Encode([]rune(string(b)))
	if len(wide) == 0 {
		return b
	}
	n, _, _ := procWideCharToMultiByte.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)), 0, 0, 0, 0)
	if n == 0 {
		return b
	}
	out := make([]byte, n)
	r, _, _ := procWideCharToMultiByte.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)),
		uintptr(unsafe.Pointer(&out[0])), n, 0, 0)
	if r == 0 {
		return b
	}
	return out[:r]
}

// transcodeCopy копирует src в dst, переводя UTF-8 в кодовую страницу cp. Символ,
// разрезанный границей чтения, дожидается продолжения.
func transcodeCopy(dst io.Writer, src io.Reader, cp uint32) {
	buf := make([]byte, 32*1024)
	var pending []byte
	for {
		n, err := src.Read(buf)
		data := append(pending, buf[:n]...)
		pending = nil
		// Неполный символ в конце порции переносим в следующую.
		for k := 1; k <= utf8.UTFMax && k <= len(data); k++ {
			if utf8.RuneStart(data[len(data)-k]) {
				if !utf8.FullRune(data[len(data)-k:]) {
					pending = append([]byte(nil), data[len(data)-k:]...)
					data = data[:len(data)-k]
				}
				break
			}
		}
		if len(data) > 0 {
			_, _ = dst.Write(utf8ToCodePage(cp, data))
		}
		if err != nil {
			if len(pending) > 0 {
				_, _ = dst.Write(utf8ToCodePage(cp, pending))
			}
			return
		}
	}
}

// isConsoleHandle сообщает, что дескриптор — консоль (а не файл или конвейер).
func isConsoleHandle(f *os.File) bool {
	var mode uint32
	return f != nil && windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// transcodeRedirected переводит перенаправленный вывод (конвейер, файл) из UTF-8
// в кодовую страницу консоли cp, в которой её ждёт запустившая программу оболочка:
// Windows PowerShell 5.1 читает вывод программ в кодовой странице консоли на
// момент своего запуска (866 или 1251), cmd.exe с «| findstr» и файлами «>» тоже
// живёт в ней; чистый UTF-8 там превращался в кракозябры. cp == 0 (консоли нет:
// запуск из другой программы) или 65001 (человек уже переключил консоль на UTF-8,
// в PowerShell это [Console]::OutputEncoding) — вывод остаётся UTF-8.
// Консольные дескрипторы не трогаются: для них работает useUTF8Console.
// Возвращённая функция дожидается записи всего накопленного и должна быть
// вызвана перед выходом.
func transcodeRedirected(cp uint32) (flush func()) {
	if cp == 0 || cp == codePageUTF8 {
		return func() {}
	}
	var closers []func()
	for _, target := range []**os.File{&os.Stdout, &os.Stderr} {
		orig := *target
		if orig == nil || isConsoleHandle(orig) {
			continue
		}
		r, w, err := os.Pipe()
		if err != nil {
			continue
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			transcodeCopy(orig, r, cp)
			r.Close()
		}()
		*target = w
		closers = append(closers, func() {
			w.Close()
			<-done
		})
	}
	log.SetOutput(os.Stderr) // стандартный лог запомнил прежний os.Stderr
	return func() {
		for _, c := range closers {
			c()
		}
	}
}
