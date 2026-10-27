package ffmpeg

import (
	"context"
	"os"
	"testing"
	"time"
)

// Интеграционный тест: нужен ffmpeg.exe с ddagrab (AIRSPOON_FFMPEG или PATH) и
// интерактивный рабочий стол. Иначе пропускается.
func TestStartProducesDecodableFirstFrame(t *testing.T) {
	requireMonitor(t)
	path, err := FindFFmpeg(os.Getenv("AIRSPOON_FFMPEG"))
	if err != nil {
		t.Skip("ffmpeg.exe не найден:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := probeDDAGrab(ctx, path); err != nil {
		t.Skip("ddagrab недоступен:", err)
	}
	src, err := Start(ctx, Config{FFmpegPath: path, Logf: t.Logf, Debugf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	first, err := src.ReadAccessUnit(ctx)
	if err != nil {
		t.Fatalf("первый кадр: %v", err)
	}
	// Первый кадр обязан содержать AUD, SPS, PPS и IDR.
	types := map[byte]bool{}
	for i := findStart(first.AnnexB, 0); i >= 0; {
		hdr := startCodeLen(first.AnnexB[i:])
		types[first.AnnexB[i+hdr]&0x1f] = true
		next := findStart(first.AnnexB, i+hdr)
		i = next
	}
	for _, want := range []byte{9, 7, 8, 5} {
		if !types[want] {
			t.Errorf("в первом кадре нет NAL типа %d (есть %v)", want, types)
		}
	}
	if first.PTS.IsZero() {
		t.Error("PTS не задан")
	}
	second, err := src.ReadAccessUnit(ctx)
	if err != nil {
		t.Fatalf("второй кадр: %v", err)
	}
	if !second.PTS.After(first.PTS) {
		t.Errorf("PTS не растёт: %v -> %v", first.PTS, second.PTS)
	}
	// Close должен завершать процесс ffmpeg.
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if src.cmd.ProcessState == nil {
		t.Error("процесс ffmpeg не завершён после Close")
	}
}
