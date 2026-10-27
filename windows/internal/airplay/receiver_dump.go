package airplay

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// syncWriter сериализует записи нескольких видеосоединений в один приёмник дампа.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// avccFrameToAnnexB переводит кадр с четырёхбайтными длинами NAL (AVCC) в Annex-B.
func avccFrameToAnnexB(frame []byte) ([]byte, error) {
	out := make([]byte, 0, len(frame)+16)
	for len(frame) > 0 {
		if len(frame) < 4 {
			return nil, fmt.Errorf("обрезанная длина NAL в кадре AVCC")
		}
		n := uint64(binary.BigEndian.Uint32(frame[:4]))
		frame = frame[4:]
		if n == 0 || n > uint64(len(frame)) {
			return nil, fmt.Errorf("недопустимая длина NAL %d при остатке %d байт", n, len(frame))
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, frame[:n]...)
		frame = frame[n:]
	}
	return out, nil
}

// avcDecoderConfigToAnnexB извлекает SPS и PPS из AVCDecoderConfigurationRecord
// (пакет конфигурации кодека зеркалирования) и записывает их в Annex-B.
func avcDecoderConfigToAnnexB(rec []byte) ([]byte, error) {
	if len(rec) < 7 || rec[0] != 1 {
		return nil, fmt.Errorf("запись AVCDecoderConfigurationRecord не распознана")
	}
	var out []byte
	pos := 5
	readSets := func(count int) error {
		for i := 0; i < count; i++ {
			if pos+2 > len(rec) {
				return fmt.Errorf("обрезанная запись параметров")
			}
			n := int(binary.BigEndian.Uint16(rec[pos:]))
			pos += 2
			if n == 0 || pos+n > len(rec) {
				return fmt.Errorf("недопустимая длина набора параметров %d", n)
			}
			out = append(out, 0, 0, 0, 1)
			out = append(out, rec[pos:pos+n]...)
			pos += n
		}
		return nil
	}
	numSPS := int(rec[pos] & 0x1f)
	pos++
	if err := readSets(numSPS); err != nil {
		return nil, err
	}
	if pos >= len(rec) {
		return nil, fmt.Errorf("в записи нет PPS")
	}
	numPPS := int(rec[pos])
	pos++
	if err := readSets(numPPS); err != nil {
		return nil, err
	}
	return out, nil
}
