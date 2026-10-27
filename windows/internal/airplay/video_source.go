package airplay

import (
	"context"
	"time"
)

const (
	ntpFractionalSecondScale = uint64(1) << 32
	// annexBLongStartCodeLength — длина четырёхбайтного стартового кода Annex-B.
	annexBLongStartCodeLength = 4
)

var annexBLongStartCode = [...]byte{0, 0, 0, 1}

// VideoAccessUnit — одна целая закодированная картинка H.264 и время её
// захвата. AnnexB содержит полные NAL-единицы, разделённые стартовыми кодами
// Annex-B (допустимы 00 00 01 и 00 00 00 01).
type VideoAccessUnit struct {
	AnnexB []byte
	// PTS — момент захвата кадра (часы time.Now с монотонным отсчётом).
	// Нулевое значение означает «время неизвестно»: кадр получит время отправки.
	PTS time.Time
}

// VideoSource — любой источник H.264 access unit'ов для MirrorSession.
//
// Контракт:
//   - ReadAccessUnit блокируется до готовности следующего кадра и возвращает ровно
//     одну картинку (все её слайсы) в формате Annex-B. Первый кадр потока и каждый
//     ключевой кадр обязаны нести SPS и PPS перед IDR-слайсом.
//   - Кадры B-типа запрещены: порядок выдачи равен порядку показа.
//   - При конце потока возвращается io.EOF (возможно вместе с последним кадром).
//   - Close прерывает блокирующее чтение из другой горутины и освобождает
//     ресурсы; повторный вызов безопасен.
type VideoSource interface {
	ReadAccessUnit(ctx context.Context) (VideoAccessUnit, error)
	Close() error
}

// timeFromNTP переводит 64-битную NTP-метку (секунды с 1900 + доля) во время UTC.
func timeFromNTP(timestamp uint64) time.Time {
	seconds := int64(timestamp>>32) - secondsFrom1900To1970
	fraction := timestamp & (ntpFractionalSecondScale - 1)
	nanoseconds := int64((fraction * uint64(time.Second)) >> 32)
	return time.Unix(seconds, nanoseconds).UTC()
}
