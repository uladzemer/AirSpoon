package ffmpegfind

import (
	"os"
	"strings"
	"testing"
)

func TestFindInWinGet(t *testing.T) {
	root := t.TempDir()
	if got := findInWinGet(root); got != "" {
		t.Fatalf("пустой каталог: ожидалась пустая строка, получено %q", got)
	}
	mk := func(rel string) string {
		p := root + "/Microsoft/WinGet/" + rel
		if err := os.MkdirAll(p[:strings.LastIndex(p, "/")], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(p, "/", string(os.PathSeparator))
	}
	pkg := mk("Packages/Gyan.FFmpeg_x/ffmpeg-8.0-full_build/bin/ffmpeg.exe")
	newer := mk("Packages/Gyan.FFmpeg_x/ffmpeg-9.0.2-full_build/bin/ffmpeg.exe")
	if got := findInWinGet(root); got != newer {
		t.Fatalf("ожидалась новейшая версия %q, получено %q (старая %q)", newer, got, pkg)
	}
	link := mk("Links/ffmpeg.exe")
	if got := findInWinGet(root); got != link {
		t.Fatalf("ссылка Links должна иметь приоритет: %q, получено %q", link, got)
	}
}
