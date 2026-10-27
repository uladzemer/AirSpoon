package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"

	"airspoon/internal/airplay"
	"airspoon/internal/applog"
	"airspoon/internal/gpudiag"
	"airspoon/internal/trayctl"
)

// Отчёт для поддержки (план windows-devices, A12): расширенная диагностика и
// хвост sender.log одним текстовым файлом на рабочем столе. Человеку остаётся
// отправить один файл тому, кто ему помогает.

// utf8BOM — метка кодировки в начале файла отчёта.
const utf8BOM = "\xef\xbb\xbf"

const (
	// supportLogTailBytes и supportLogTailLines ограничивают хвост журнала: журнал
	// ротируется около 5 МБ, а в отчёте нужны последние события.
	supportLogTailBytes = 256 << 10
	supportLogTailLines = 1500
)

// tailLines возвращает последние maxLines строк из последних maxBytes байт файла.
// Неполная первая строка среза отбрасывается.
func tailLines(path string, maxBytes int64, maxLines int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	cut := false
	if st.Size() > maxBytes {
		if _, err := f.Seek(st.Size()-maxBytes, io.SeekStart); err != nil {
			return "", err
		}
		cut = true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if cut {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n"), nil
}

// supportReporter собирает отчёт; поля подменяются в тестах.
type supportReporter struct {
	// desktop возвращает папку рабочего стола пользователя.
	desktop func() (string, error)
	logPath string
	collect func(gpudiag.Options) gpudiag.Report
	now     func() time.Time
}

func newSupportReporter() supportReporter {
	return supportReporter{desktop: desktopDir, logPath: applog.DefaultPath(), collect: gpudiag.Collect, now: time.Now}
}

// desktopDir — рабочий стол пользователя (с учётом переноса в OneDrive).
func desktopDir() (string, error) {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0); err == nil && p != "" {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Desktop"), nil
	}
	return "", fmt.Errorf("папка рабочего стола не найдена")
}

// reportText склеивает строки диагностики и хвост журнала.
func reportText(now time.Time, diag []string, logPath, tail string, tailErr error) string {
	var b strings.Builder
	b.WriteString("=== Отчёт для поддержки AirSpoon Sender ===\n")
	fmt.Fprintf(&b, "Создан: %s\n\n", now.Format("2006-01-02 15:04:05"))
	for _, l := range diag {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "\n=== Хвост журнала %s ===\n", logPath)
	switch {
	case tailErr != nil:
		fmt.Fprintf(&b, "(журнал не прочитан: %v)\n", tailErr)
	case tail == "":
		b.WriteString("(журнал пуст)\n")
	default:
		b.WriteString(tail + "\n")
	}
	return b.String()
}

// Run собирает диагностику по opts и записывает отчёт на рабочий стол. Возвращает
// путь к файлу. Файл в UTF-8 с BOM и строками CRLF: блокнот Windows 10 без BOM
// путает кодировку.
func (s supportReporter) Run(opts gpudiag.Options) (string, error) {
	dir, err := s.desktop()
	if err != nil {
		return "", err
	}
	rep := s.collect(opts)
	tail, tailErr := tailLines(s.logPath, supportLogTailBytes, supportLogTailLines)
	now := s.now()
	text := reportText(now, rep.Lines(), s.logPath, tail, tailErr)
	text = utf8BOM + strings.ReplaceAll(text, "\n", "\r\n")
	path := filepath.Join(dir, "AirSpoon-report-"+now.Format("20060102-150405")+".txt")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("отчёт не записан в %s: %w", path, err)
	}
	return path, nil
}

// lastReceiverHost — адрес последнего телевизора (для определения типа сети).
func lastReceiverHost() string {
	if r, ok := airplay.LoadLastReceiver(defaultLastReceiverFile()); ok {
		return r.Address
	}
	return ""
}

// supportReportOptions подбирает режим диагностики. Пока идёт показ экрана,
// пробное кодирование, замер захвата и проба дублирования пропускаются: они
// мешали бы идущему показу (второй кодер и второе дублирование того же монитора).
func supportReportOptions(s trayctl.Snapshot, retryEnv, lastHost string) (opts gpudiag.Options, streaming bool) {
	opts = gpudiag.Options{Probe: time.Second, Extended: true, RetryEnv: retryEnv, Peer: lastHost}
	if s.Current != nil && s.Current.Addr != "" {
		opts.Peer = s.Current.Addr
	}
	if s.Phase != trayctl.Idle {
		opts.Probe, opts.EncodeProbe, opts.MeasureCapture = 0, -1, -1
		return opts, true
	}
	return opts, false
}

// supportReportAction — пункт трея «Сохранить отчёт для поддержки». Выполняется
// в своей горутине (диагностика занимает около минуты); busy защищает от
// повторного нажатия. Итог показывается уведомлением.
func supportReportAction(rp supportReporter, snap func() trayctl.Snapshot, notify func(trayctl.NoticeKind, string), busy *atomic.Bool, retryEnv, lastHost string) {
	if !busy.CompareAndSwap(false, true) {
		notify(trayctl.NoticeInfo, "Отчёт уже готовится. Подождите, пожалуйста.")
		return
	}
	defer busy.Store(false)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("авария при сборе отчёта: %v\n%s", r, debug.Stack())
			notify(trayctl.NoticeError, "Не получилось собрать отчёт. Попробуйте ещё раз.")
		}
	}()
	opts, streaming := supportReportOptions(snap(), retryEnv, lastHost)
	if streaming {
		notify(trayctl.NoticeInfo, "Готовлю отчёт. Идёт показ экрана, поэтому пробное кодирование пропущено, чтобы не мешать показу.")
	} else {
		notify(trayctl.NoticeInfo, "Готовлю отчёт. Это займёт около минуты, не закрывайте программу.")
	}
	path, err := rp.Run(opts)
	if err != nil {
		log.Printf("отчёт для поддержки: %v", err)
		notify(trayctl.NoticeError, "Не получилось сохранить отчёт: "+err.Error())
		return
	}
	log.Printf("отчёт для поддержки сохранён: %s", path)
	notify(trayctl.NoticeInfo, "Отчёт сохранён на рабочем столе: "+filepath.Base(path)+". Отправьте этот файл тому, кто вам помогает.")
}
