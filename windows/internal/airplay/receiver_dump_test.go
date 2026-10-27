package airplay

import (
	"bytes"
	"testing"
)

func TestAVCCFrameToAnnexB(t *testing.T) {
	frame := []byte{0, 0, 0, 3, 0x65, 0x88, 0x84, 0, 0, 0, 2, 0x41, 0x9a}
	got, err := avccFrameToAnnexB(frame)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 0, 1, 0x65, 0x88, 0x84, 0, 0, 0, 1, 0x41, 0x9a}
	if !bytes.Equal(got, want) {
		t.Fatalf("получено %x, ожидалось %x", got, want)
	}
	if _, err := avccFrameToAnnexB([]byte{0, 0, 0, 9, 1}); err == nil {
		t.Fatal("обрезанный кадр принят")
	}
}

func TestAVCDecoderConfigToAnnexB(t *testing.T) {
	sps := []byte{0x67, 0x4d, 0x40, 0x1f}
	pps := []byte{0x68, 0xee, 0x3c, 0x80}
	rec := []byte{1, 0x4d, 0x40, 0x1f, 0xff, 0xe1, 0, byte(len(sps))}
	rec = append(rec, sps...)
	rec = append(rec, 1, 0, byte(len(pps)))
	rec = append(rec, pps...)
	got, err := avcDecoderConfigToAnnexB(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{0, 0, 0, 1}, sps...), 0, 0, 0, 1), pps...)
	if !bytes.Equal(got, want) {
		t.Fatalf("получено %x, ожидалось %x", got, want)
	}
	if _, err := avcDecoderConfigToAnnexB([]byte{1, 2, 3}); err == nil {
		t.Fatal("короткая запись принята")
	}
}
