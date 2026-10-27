package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"airspoon/internal/gpudiag"
	"airspoon/internal/trayctl"
)

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sender.log")
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString("строка " + strings.Repeat("x", 10) + " №" + itoa(i) + "\r\n")
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Ограничение по числу строк.
	got, err := tailLines(p, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[2], "№100") || !strings.HasSuffix(lines[0], "№98") {
		t.Fatalf("хвост по строкам: %q", got)
	}
	// Ограничение по байтам: неполная первая строка отбрасывается.
	got, err = tailLines(p, 100, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(got, "\n") {
		if !strings.HasPrefix(l, "строка ") {
			t.Fatalf("оборванная строка в хвосте: %q", l)
		}
	}
	if !strings.HasSuffix(got, "№100") || strings.Contains(got, "\r") {
		t.Fatalf("хвост по байтам: %q", got)
	}
	if _, err := tailLines(filepath.Join(dir, "нет.log"), 10, 10); err == nil {
		t.Fatal("нет файла — должна быть ошибка")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestSupportReporterWritesFileOnDesktop(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sender.log")
	if err := os.WriteFile(logPath, []byte("первая\nпоследняя строка журнала\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 18, 30, 45, 0, time.UTC)
	var gotOpts gpudiag.Options
	rp := supportReporter{
		desktop: func() (string, error) { return dir, nil },
		logPath: logPath,
		now:     func() time.Time { return now },
		collect: func(o gpudiag.Options) gpudiag.Report {
			gotOpts = o
			return gpudiag.Report{At: now, Exe: `C:\x.exe`, Pref: "не задано"}
		},
	}
	path, err := rp.Run(gpudiag.Options{Extended: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != "AirSpoon-report-20261027-183045.txt" {
		t.Fatalf("путь отчёта: %s", path)
	}
	if !gotOpts.Extended {
		t.Error("параметры диагностики не дошли до сборщика")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, utf8BOM) {
		t.Error("нужен BOM UTF-8")
	}
	for _, want := range []string{"Отчёт для поддержки AirSpoon Sender", "Диагностика видеокарт AirSpoon Sender", "Хвост журнала", "последняя строка журнала"} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Error("все переводы строк должны быть CRLF")
	}
}

func TestSupportReporterWithoutLog(t *testing.T) {
	dir := t.TempDir()
	rp := supportReporter{
		desktop: func() (string, error) { return dir, nil },
		logPath: filepath.Join(dir, "нет.log"),
		now:     time.Now,
		collect: func(gpudiag.Options) gpudiag.Report { return gpudiag.Report{At: time.Now()} },
	}
	path, err := rp.Run(gpudiag.Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "журнал не прочитан") {
		t.Errorf("отчёт без журнала: %s", data)
	}
}

func TestSupportReportOptions(t *testing.T) {
	idle := trayctl.Snapshot{Phase: trayctl.Idle}
	o, streaming := supportReportOptions(idle, "1", "10.0.0.5")
	if streaming || !o.Extended || o.Peer != "10.0.0.5" || o.RetryEnv != "1" || o.EncodeProbe < 0 || o.MeasureCapture < 0 || o.Probe <= 0 {
		t.Fatalf("в покое: %+v", o)
	}
	busy := trayctl.Snapshot{Phase: trayctl.Streaming, Current: &trayctl.Device{Addr: "192.168.10.41:7000"}}
	o, streaming = supportReportOptions(busy, "", "10.0.0.5")
	if !streaming || o.Peer != "192.168.10.41:7000" || o.EncodeProbe >= 0 || o.MeasureCapture >= 0 || o.Probe != 0 {
		t.Fatalf("во время показа пробы должны отключаться: %+v", o)
	}
}

func TestSupportReportActionNotifiesAndGuardsReentry(t *testing.T) {
	dir := t.TempDir()
	var texts []string
	var kinds []trayctl.NoticeKind
	notify := func(k trayctl.NoticeKind, s string) { kinds = append(kinds, k); texts = append(texts, s) }
	rp := supportReporter{
		desktop: func() (string, error) { return dir, nil },
		logPath: filepath.Join(dir, "sender.log"),
		now:     time.Now,
		collect: func(gpudiag.Options) gpudiag.Report { return gpudiag.Report{At: time.Now()} },
	}
	var busy atomic.Bool
	snap := func() trayctl.Snapshot { return trayctl.Snapshot{} }
	supportReportAction(rp, snap, notify, &busy, "", "")
	if len(texts) != 2 || !strings.Contains(texts[0], "около минуты") || !strings.Contains(texts[1], "Отчёт сохранён на рабочем столе: AirSpoon-report-") {
		t.Fatalf("уведомления: %q", texts)
	}
	if busy.Load() {
		t.Fatal("флаг занятости должен сниматься")
	}

	// Повторное нажатие, пока отчёт готовится.
	texts = nil
	busy.Store(true)
	supportReportAction(rp, snap, notify, &busy, "", "")
	if len(texts) != 1 || !strings.Contains(texts[0], "уже готовится") {
		t.Fatalf("повторное нажатие: %q", texts)
	}
	busy.Store(false)

	// Ошибка записи: понятное сообщение.
	texts, kinds = nil, nil
	rp.desktop = func() (string, error) { return filepath.Join(dir, "нет", "такой", "папки"), nil }
	supportReportAction(rp, snap, notify, &busy, "", "")
	last := len(texts) - 1
	if last < 0 || kinds[last] != trayctl.NoticeError || !strings.Contains(texts[last], "Не получилось сохранить отчёт") {
		t.Fatalf("ошибка записи: %q", texts)
	}

	// Паника сборщика не убивает программу.
	texts, kinds = nil, nil
	rp.desktop = func() (string, error) { return dir, nil }
	rp.collect = func(gpudiag.Options) gpudiag.Report { panic("сбой драйвера") }
	supportReportAction(rp, snap, notify, &busy, "", "")
	last = len(texts) - 1
	if last < 0 || kinds[last] != trayctl.NoticeError || busy.Load() {
		t.Fatalf("паника: %q busy=%v", texts, busy.Load())
	}
}
