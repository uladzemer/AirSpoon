//go:build windows

package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const exitHelperLastLine = "Error opening input: причина-в-последней-строке"

// Вспомогательный процесс: много строк в stderr, последняя содержит причину,
// без вывода в stdout, код возврата 3 — как ffmpeg, не сумевший открыть вход.
func TestExitHelperProcess(t *testing.T) {
	if os.Getenv("AIRSPOON_EXIT_HELPER") != "1" {
		t.Skip("вспомогательный процесс")
	}
	filler := strings.Repeat("x", 100)
	for i := 0; i < 120; i++ {
		fmt.Fprintf(os.Stderr, "строка %d %s\n", i, filler)
	}
	fmt.Fprintln(os.Stderr, exitHelperLastLine)
	os.Exit(3)
}

// Сводная ошибка должна содержать самую последнюю строку stderr: Wait нельзя
// вызывать, пока читающая горутина не дочитала stderr (документация os/exec).
func TestSourceErrorKeepsLastStderrLine(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRSPOON_EXIT_HELPER", "1")
	for i := 0; i < 10; i++ {
		s, err := launch(context.Background(), exe, []string{"-test.run=^TestExitHelperProcess$"}, nil, slowLog)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = s.ReadAccessUnit(ctx)
		cancel()
		_ = s.Close()
		if err == nil {
			t.Fatal("ожидалась ошибка завершения процесса")
		}
		if !strings.Contains(err.Error(), exitHelperLastLine) {
			t.Fatalf("попытка %d: в ошибке нет последней строки stderr: %.300s", i, err)
		}
	}
}

// slowLog имитирует медленный вывод в консоль: читающая горутина отстаёт от
// процесса, и к моменту выхода процесса в канале ещё остаются непрочитанные строки.
func slowLog(string, ...any) { time.Sleep(time.Millisecond) }
