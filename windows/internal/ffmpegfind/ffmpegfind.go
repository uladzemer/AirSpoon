// Package ffmpegfind ищет ffmpeg.exe. Вынесен в отдельный пакет без зависимостей
// от airplay, чтобы им могли пользоваться и тесты самого airplay (например,
// независимая проверка кодера ALAC через ffmpeg).
package ffmpegfind

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Find ищет ffmpeg.exe: явный путь, затем рядом с исполняемым файлом,
// затем PATH, затем каталоги установки winget. Последний шаг нужен потому, что
// winget добавляет ffmpeg в PATH только для новых сессий: уже открытый терминал
// (и тесты, запущенные из него) его не видят.
func Find(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("ffmpeg не найден по пути %q: %w", explicit, err)
		}
		return explicit, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "ffmpeg.exe")
		if isFile(candidate) {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path, nil
	}
	if path := findInWinGet(os.Getenv("LOCALAPPDATA")); path != "" {
		return path, nil
	}
	return "", errors.New("ffmpeg.exe не найден: положите его рядом с airspoon.exe, добавьте в PATH или укажите --ffmpeg")
}

// findInWinGet ищет ffmpeg.exe в каталогах winget под localAppData: ссылка
// Links/ffmpeg.exe (пакеты с алиасом) или Packages/<пакет>/[<версия>/]bin/ffmpeg.exe
// (распакованный архив, например Gyan.FFmpeg). Из найденного берётся
// лексикографически последний путь. Пустая строка — не найдено.
func findInWinGet(localAppData string) string {
	if localAppData == "" {
		return ""
	}
	base := filepath.Join(localAppData, "Microsoft", "WinGet")
	if link := filepath.Join(base, "Links", "ffmpeg.exe"); isFile(link) {
		return link
	}
	var found []string
	for _, pattern := range []string{
		filepath.Join(base, "Packages", "*", "bin", "ffmpeg.exe"),
		filepath.Join(base, "Packages", "*", "*", "bin", "ffmpeg.exe"),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if isFile(m) {
				found = append(found, m)
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[len(found)-1]
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
