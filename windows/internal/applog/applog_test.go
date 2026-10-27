package applog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotateKeepsTwoFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "sender.log")
	r, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	line := strings.Repeat("x", 39) + "\n" // 40 байт
	for i := 0; i < 7; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := os.ReadFile(path)
	old, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("нет ротированного файла: %v", err)
	}
	if len(cur) > 100 || len(old) > 100 {
		t.Fatalf("файлы больше лимита: %d и %d", len(cur), len(old))
	}
	if len(cur)+len(old) < 80 {
		t.Fatalf("потеряны записи: %d + %d", len(cur), len(old))
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("появился лишний файл .2")
	}
}

func TestAppendsToExistingFileAndCountsItsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sender.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 90)), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, _ = r.Write([]byte(strings.Repeat("b", 20)))
	old, err := os.ReadFile(path + ".1")
	if err != nil || len(old) != 90 {
		t.Fatalf("старое содержимое должно уйти в .1 целиком: %d %v", len(old), err)
	}
	cur, _ := os.ReadFile(path)
	if string(cur) != strings.Repeat("b", 20) {
		t.Fatalf("новый файл: %q", cur)
	}
}

func TestConcurrentWritesAreWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sender.log")
	r, err := Open(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_, _ = r.Write([]byte("строка журнала\n"))
			}
		}()
	}
	wg.Wait()
	_ = r.Close()
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "строка журнала\n"); n != 400 {
		t.Fatalf("целых строк %d, ожидалось 400", n)
	}
	if _, err := r.Write([]byte("x")); err == nil {
		t.Fatal("запись в закрытый журнал не вернула ошибку")
	}
}

func TestDefaultPathUnderLocalAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\test\AppData\Local`)
	got := DefaultPath()
	if !strings.HasSuffix(got, filepath.Join("AirSpoon", "sender.log")) || !strings.Contains(got, "Local") {
		t.Fatalf("путь журнала: %s", got)
	}
}
