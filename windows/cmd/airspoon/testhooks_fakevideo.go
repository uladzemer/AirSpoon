//go:build testhooks

package main

import (
	"context"
	"os"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/encode/native"
)

// AIRSPOON_TEST_FAKE_VIDEO=1 подменяет захват экрана и кодер синтетическим
// потоком кадров (SPS, PPS, IDR и P-кадры с шагом 1/30 с). Нужен для проверки
// всей программы (трей, окно кода, перезапуск, сопряжение) против testreceiver
// на компьютере, где захват экрана недоступен: заблокированный сеанс,
// отключённый удалённый рабочий стол, виртуальная машина без монитора.
const envTestFakeVideo = "AIRSPOON_TEST_FAKE_VIDEO"

func init() {
	if os.Getenv(envTestFakeVideo) != "1" {
		return
	}
	startNativeSource = func(context.Context, native.Config) (airplay.VideoSource, error) {
		return &syntheticSource{}, nil
	}
}

// Тестовые NAL-единицы H.264 (те же, что в автотестах).
var (
	synthSPS = []byte{0x67, 0x42, 0xc0, 0x1f, 0xda, 0x01, 0x40, 0x16, 0xe9, 0xb8, 0x08, 0x08, 0x08, 0x10}
	synthPPS = []byte{0x68, 0xce, 0x3c, 0x80}
	synthIDR = []byte{0x65, 0x88, 0x84, 0x00, 0x10, 0xff, 0xfe}
	synthP   = []byte{0x41, 0x9a, 0x21, 0x6c, 0x42}
)

func synthAnnexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

type syntheticSource struct{ n int }

func (s *syntheticSource) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	if s.n > 0 {
		select {
		case <-time.After(33 * time.Millisecond):
		case <-ctx.Done():
			return airplay.VideoAccessUnit{}, ctx.Err()
		}
	}
	s.n++
	if s.n == 1 || s.n%60 == 0 { // ключевой кадр в начале и раз в ~2 с
		return airplay.VideoAccessUnit{AnnexB: synthAnnexB(synthSPS, synthPPS, synthIDR), PTS: time.Now()}, nil
	}
	return airplay.VideoAccessUnit{AnnexB: synthAnnexB(synthP), PTS: time.Now()}, nil
}

func (s *syntheticSource) Close() error { return nil }
