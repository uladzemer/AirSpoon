package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestUseUTF8ConsoleSetsAndRestores(t *testing.T) {
	before := consoleOutputCP()
	if before == 0 {
		t.Skip("к процессу не привязана консоль")
	}
	// Проверяем на заведомо отличной от UTF-8 странице, чтобы тест работал
	// и в консоли, уже переключённой на 65001.
	restoreStart := switchConsoleOutputCP(866)
	defer restoreStart()
	if got := consoleOutputCP(); got != 866 {
		t.Fatalf("не удалось выставить 866: %d", got)
	}
	restore := useUTF8Console()
	if got := consoleOutputCP(); got != codePageUTF8 {
		t.Fatalf("после useUTF8Console страница %d, ожидалась %d", got, codePageUTF8)
	}
	restore()
	if got := consoleOutputCP(); got != 866 {
		t.Fatalf("после restore страница %d, ожидалась прежняя 866", got)
	}
	restoreStart()
	if got := consoleOutputCP(); got != before {
		t.Fatalf("исходная страница %d не восстановлена: %d", before, got)
	}
}

func TestUTF8ToCodePage(t *testing.T) {
	cases := []struct {
		cp   uint32
		want []byte
	}{
		{866, []byte{0xAF, 0xA5, 0xE0, 0xA5, 0xA4, 0xA0, 0xE7, 0xA0}},  // «передача» в CP866
		{1251, []byte{0xEF, 0xE5, 0xF0, 0xE5, 0xE4, 0xE0, 0xF7, 0xE0}}, // то же в Windows-1251
	}
	for _, c := range cases {
		if got := utf8ToCodePage(c.cp, []byte("передача")); !bytes.Equal(got, c.want) {
			t.Errorf("cp %d: % x, ожидалось % x", c.cp, got, c.want)
		}
	}
	if got := utf8ToCodePage(866, []byte("ASCII 123\n")); string(got) != "ASCII 123\n" {
		t.Errorf("ASCII изменился: %q", got)
	}
	if got := utf8ToCodePage(866, []byte("а✓б")); !bytes.Equal(got, []byte{0xA0, '?', 0xA1}) {
		t.Errorf("символ вне страницы должен стать «?»: % x", got)
	}
	if got := utf8ToCodePage(codePageUTF8, []byte("передача")); string(got) != "передача" {
		t.Errorf("для UTF-8 данные не меняются: %q", got)
	}
}

// oneByteReader отдаёт данные по одному байту: многобайтовые символы приходят
// разрезанными.
type oneByteReader struct{ b []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}

func TestTranscodeCopyKeepsSplitCharacters(t *testing.T) {
	var out bytes.Buffer
	transcodeCopy(&out, &oneByteReader{b: []byte("имя=«Телевизор»\n")}, 866)
	want := utf8ToCodePage(866, []byte("имя=«Телевизор»\n"))
	if !bytes.Equal(out.Bytes(), want) || bytes.Contains(out.Bytes(), []byte{'?'}) {
		t.Fatalf("получено % x, ожидалось % x", out.Bytes(), want)
	}
}

// Перенаправленный вывод переводится в страницу консоли, консоль и отсутствие
// страницы (0) или UTF-8 (65001) оставляют UTF-8.
func TestTranscodeRedirectedWritesConsoleCodePage(t *testing.T) {
	dir := t.TempDir()
	run := func(cp uint32) []byte {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("out%d", cp)))
		if err != nil {
			t.Fatal(err)
		}
		oldOut, oldErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = f, f
		flush := transcodeRedirected(cp)
		fmt.Fprint(os.Stdout, "передача")
		flush()
		os.Stdout, os.Stderr = oldOut, oldErr
		log.SetOutput(oldErr)
		f.Close()
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if got := run(866); !bytes.Equal(got, []byte{0xAF, 0xA5, 0xE0, 0xA5, 0xA4, 0xA0, 0xE7, 0xA0}) {
		t.Errorf("866: % x", got)
	}
	if got := run(0); string(got) != "передача" {
		t.Errorf("без консоли ожидался UTF-8: % x", got)
	}
	if got := run(codePageUTF8); string(got) != "передача" {
		t.Errorf("65001 ожидался UTF-8: % x", got)
	}
}
