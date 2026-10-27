// Программа AirSpoon Sender (AirSpoonSender.exe) — передатчик экрана Windows на
// приёмник AirPlay (UxPlay на Android TV).
//
// Без аргументов запускается режим трея: значок у часов, меню со списком
// телевизоров, окна на русском языке (tray_windows.go). С аргументами работает
// прежний консольный режим: list и mirror (для проверки и продвинутых
// пользователей). Видео: захват DXGI и кодер Media Foundation (запасной путь
// для отладки — ffmpeg.exe, --encoder ffmpeg).
//
// Лицензия GPL-3.0-or-later (см. LICENSE и NOTICE).
package main

import (
	"fmt"
	"os"
	"runtime"
)

// Ресурсы .exe (значки, манифест, версия) собирает go-winres в
// rsrc_windows_amd64.syso; обычно это делает build.ps1, вручную — go generate.
//
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --in ../../winres/winres.json --out rsrc --arch amd64

func init() {
	// Окна и цикл сообщений трея живут в потоке главной горутины.
	runtime.LockOSThread()
}

const usageText = `AirSpoon Sender — передача экрана Windows на приёмник AirPlay (UxPlay)

Запуск без аргументов открывает программу в области уведомлений (значок у часов).

Консольные команды:
  AirSpoonSender list   [--timeout 3s] [--verbose]
  AirSpoonSender mirror (--name "Телевизор" | --host 100.65.152.57[:7000]) [параметры]
  AirSpoonSender diag   [--quick] [--probe 1s] [--display 0] [--host адрес] [--out файл]
  AirSpoonSender help

Команды:
  list    найти приёмники AirPlay в сети по mDNS (_airplay._tcp)
  mirror  передать экран на выбранный приёмник
  diag    показать систему, видеокарты, мониторы и кодеры H.264, проверить кодирование и захват экрана
          (около минуты; --quick — коротко)
          (при «не получилось захватить экран» на ноутбуке с двумя видеокартами)

Запустите "AirSpoonSender mirror -h" или "AirSpoonSender list -h", чтобы увидеть все параметры.
`

func main() {
	if len(os.Args) <= 1 {
		os.Exit(runTray())
	}
	attachParentConsole()
	shellCP := consoleOutputCP() // страница консоли оболочки до переключения: в ней ждут конвейер
	restoreConsole := useUTF8Console()
	if os.Getenv("AIRSPOON_UTF8") == "1" {
		shellCP = 0 // явная просьба: перенаправленный вывод всегда UTF-8 (для программ-читателей)
	}
	flushOutput := transcodeRedirected(shellCP)
	code := run(os.Args[1:])
	flushOutput()
	restoreConsole()
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "list":
		return runList(args[1:])
	case "mirror":
		return runMirror(args[1:])
	case "diag":
		return runDiag(args[1:])
	case "help", "-h", "--help", "-help":
		fmt.Print(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n%s", args[0], usageText)
		return 2
	}
}
