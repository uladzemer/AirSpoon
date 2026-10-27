package ffmpeg

import (
	"bytes"
	"testing"
	"time"
)

func nal(startLen int, typ byte, payload ...byte) []byte {
	var out []byte
	if startLen == 4 {
		out = append(out, 0, 0, 0, 1)
	} else {
		out = append(out, 0, 0, 1)
	}
	out = append(out, typ)
	return append(out, payload...)
}

func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// Поток: [AUD SPS PPS IDR] [AUD P] [AUD P(2 слайса)] — три кадра.
func testStream() ([]byte, [][]byte) {
	f1 := concat(nal(4, 0x09, 0xf0), nal(4, 0x67, 0x42, 0x00, 0x1f), nal(4, 0x68, 0xce, 0x06), nal(4, 0x65, 0x88, 0x01, 0x02))
	f2 := concat(nal(4, 0x09, 0xf0), nal(4, 0x41, 0x9a, 0x03))
	f3 := concat(nal(4, 0x09, 0xf0), nal(4, 0x41, 0x9a, 0x04), nal(3, 0x41, 0x05, 0x06))
	return concat(f1, f2, f3), [][]byte{f1, f2, f3}
}

func TestAssemblerSplitsAccessUnitsAtAnyChunking(t *testing.T) {
	stream, frames := testStream()
	for _, chunk := range []int{1, 2, 3, 5, 7, 64, len(stream)} {
		var a assembler
		var got [][]byte
		for off := 0; off < len(stream); off += chunk {
			end := off + chunk
			if end > len(stream) {
				end = len(stream)
			}
			for _, au := range a.push(stream[off:end], time.Now()) {
				got = append(got, au.AnnexB)
			}
		}
		for _, au := range a.finish() {
			got = append(got, au.AnnexB)
		}
		if len(got) != len(frames) {
			t.Fatalf("chunk=%d: кадров %d, ожидалось %d", chunk, len(got), len(frames))
		}
		for i := range frames {
			if !bytes.Equal(got[i], frames[i]) {
				t.Fatalf("chunk=%d: кадр %d отличается:\n got %x\nwant %x", chunk, i, got[i], frames[i])
			}
		}
	}
}

func TestAssemblerWithoutAUDSplitsByFirstMB(t *testing.T) {
	// Без AUD: слайс с first_mb_in_slice=0 (старший бит 1) открывает новый кадр,
	// слайс с first_mb != 0 (старший бит 0) остаётся в текущем.
	f1 := concat(nal(4, 0x67, 0x42, 0x00, 0x1f), nal(4, 0x68, 0xce), nal(4, 0x65, 0x88), nal(3, 0x65, 0x10))
	f2 := nal(4, 0x41, 0x9a)
	var a assembler
	var got [][]byte
	for _, au := range a.push(concat(f1, f2), time.Now()) {
		got = append(got, au.AnnexB)
	}
	for _, au := range a.finish() {
		got = append(got, au.AnnexB)
	}
	if len(got) != 2 || !bytes.Equal(got[0], f1) || !bytes.Equal(got[1], f2) {
		t.Fatalf("неверное разбиение: %x", got)
	}
}

func TestAssemblerPTSIsTimeOfFirstNAL(t *testing.T) {
	stream, _ := testStream()
	var a assembler
	t0 := time.Unix(1700000000, 0)
	// Первый кусок содержит кадр 1 и начало кадра 2, второй — остальное позже.
	cut := len(stream) / 2
	var aus []time.Time
	for _, au := range a.push(stream[:cut], t0) {
		aus = append(aus, au.PTS)
	}
	for _, au := range a.push(stream[cut:], t0.Add(time.Second)) {
		aus = append(aus, au.PTS)
	}
	if len(aus) == 0 || !aus[0].Equal(t0) {
		t.Fatalf("PTS первого кадра = %v, ожидалось %v", aus, t0)
	}
}

func TestAssemblerSkipsLeadingGarbage(t *testing.T) {
	stream, frames := testStream()
	var a assembler
	var got [][]byte
	for _, au := range a.push(concat([]byte{9, 9, 9, 9, 9}, stream), time.Now()) {
		got = append(got, au.AnnexB)
	}
	for _, au := range a.finish() {
		got = append(got, au.AnnexB)
	}
	if len(got) != len(frames) || !bytes.Equal(got[0], frames[0]) {
		t.Fatalf("мусор перед потоком сломал разбор: %d кадров", len(got))
	}
}
