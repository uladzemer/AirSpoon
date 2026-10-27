package mf

import (
	"bytes"
	"reflect"
	"testing"
)

var (
	testSPS = []byte{0x67, 0x4d, 0x40, 0x1f, 0xda, 0x01, 0x40, 0x16, 0xe8}
	testPPS = []byte{0x68, 0xee, 0x3c, 0x80}
	testIDR = []byte{0x65, 0x88, 0x84, 0x00, 0x33}
	testP   = []byte{0x41, 0x9a, 0x24, 0x6c}
	testAUD = []byte{0x09, 0xf0}
	testSEI = []byte{0x06, 0x05, 0x02, 0xaa, 0xbb, 0x80}
)

func join(codes int, nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		if codes == 3 {
			b = append(b, 0, 0, 1)
		} else {
			b = append(b, 0, 0, 0, 1)
		}
		b = append(b, n...)
	}
	return b
}

func TestSplitAnnexB(t *testing.T) {
	for _, codes := range []int{3, 4} {
		got := splitAnnexB(join(codes, testSPS, testPPS, testIDR))
		want := [][]byte{testSPS, testPPS, testIDR}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("стартовые коды по %d байта: получено %x, ожидалось %x", codes, got, want)
		}
	}
	// Смешанные коды, мусор перед первым кодом и нули перед следующим кодом.
	mixed := append([]byte{0xAB, 0xCD}, join(3, testSPS)...)
	mixed = append(mixed, join(4, testPPS)...)
	mixed = append(mixed, 0, 0) // хвостовые нули
	got := splitAnnexB(mixed)
	if len(got) != 2 || !bytes.Equal(got[0], testSPS) || !bytes.Equal(got[1], testPPS) {
		t.Errorf("смешанные коды: %x", got)
	}
	if n := splitAnnexB([]byte{1, 2, 3}); len(n) != 0 {
		t.Errorf("без стартового кода NAL нет: %x", n)
	}
}

func TestPrepareInsertsParameterSets(t *testing.T) {
	var p paramSets
	p.learnSequenceHeader(join(4, testSPS, testPPS))
	if !p.ready() {
		t.Fatal("заголовок последовательности не разобран")
	}
	// IDR без SPS/PPS получает их перед собой.
	out, key, complete := p.prepare(join(4, testIDR))
	if !key || !complete {
		t.Fatalf("key=%t complete=%t", key, complete)
	}
	if want := join(4, testSPS, testPPS, testIDR); !bytes.Equal(out, want) {
		t.Errorf("получено %x, ожидалось %x", out, want)
	}
	// AUD остаётся первым, SPS/PPS идут за ним и перед SEI.
	out, _, _ = p.prepare(join(3, testAUD, testSEI, testIDR))
	if want := join(4, testAUD, testSPS, testPPS, testSEI, testIDR); !bytes.Equal(out, want) {
		t.Errorf("с AUD и SEI: %x, ожидалось %x", out, want)
	}
}

func TestPrepareKeepsCompleteAndDeltaFrames(t *testing.T) {
	var p paramSets
	full := join(4, testSPS, testPPS, testIDR)
	out, key, complete := p.prepare(full)
	if !key || !complete || !bytes.Equal(out, full) {
		t.Fatalf("полный IDR изменён: %x key=%t complete=%t", out, key, complete)
	}
	// Наборы запомнились из потока и пригодятся для следующих IDR без них.
	out, key, complete = p.prepare(join(4, testIDR))
	if !key || !complete || !bytes.Equal(out, join(4, testSPS, testPPS, testIDR)) {
		t.Fatalf("запомненные наборы не вставлены: %x", out)
	}
	delta := join(4, testP)
	out, key, _ = p.prepare(delta)
	if key || !bytes.Equal(out, delta) {
		t.Fatalf("P-кадр не должен меняться: %x key=%t", out, key)
	}
}

func TestPrepareMissingOnlyOneSet(t *testing.T) {
	var p paramSets
	p.learnSequenceHeader(join(4, testSPS, testPPS))
	out, _, complete := p.prepare(join(4, testSPS, testIDR))
	if !complete || !bytes.Equal(out, join(4, testSPS, testPPS, testIDR)) {
		t.Fatalf("должен добавиться только PPS: %x", out)
	}
}

func TestPrepareWithoutKnownSets(t *testing.T) {
	var p paramSets
	data := join(4, testIDR)
	out, key, complete := p.prepare(data)
	if !key || complete || !bytes.Equal(out, data) {
		t.Fatalf("без известных наборов кадр возвращается как есть: key=%t complete=%t", key, complete)
	}
}

func TestPrepareConvertsAVCC(t *testing.T) {
	var p paramSets
	avcc := []byte{0, 0, 0, byte(len(testSPS))}
	avcc = append(avcc, testSPS...)
	avcc = append(avcc, 0, 0, 0, byte(len(testPPS)))
	avcc = append(avcc, testPPS...)
	avcc = append(avcc, 0, 0, 0, byte(len(testIDR)))
	avcc = append(avcc, testIDR...)
	out, key, complete := p.prepare(avcc)
	if !key || !complete || !bytes.Equal(out, join(4, testSPS, testPPS, testIDR)) {
		t.Fatalf("AVCC не преобразован: %x", out)
	}
	if _, ok := avccToAnnexB([]byte{0, 0, 0, 9, 1}); ok {
		t.Error("обрезанные данные приняты за AVCC")
	}
}

func TestLearnKeepsLatest(t *testing.T) {
	var p paramSets
	p.learn([][]byte{testSPS, testPPS})
	newSPS := []byte{0x67, 0x64, 0x00, 0x28}
	p.learn([][]byte{newSPS})
	if !bytes.Equal(p.sps, newSPS) || !bytes.Equal(p.pps, testPPS) {
		t.Fatalf("sps=%x pps=%x", p.sps, p.pps)
	}
}
