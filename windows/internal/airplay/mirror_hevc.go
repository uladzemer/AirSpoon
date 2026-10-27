package airplay

import (
	"context"
	"errors"
	"time"
)

// ErrHEVCUnsupported возвращается, если сессия договорилась о HEVC. В AirSpoon
// HEVC не поддерживается (приёмник UxPlay на приставке принимает только H.264),
// поэтому исходный HEVC-конвейер open-airplay не перенесён. Клиент должен
// запрашивать VideoCodecH264.
var ErrHEVCUnsupported = errors.New("HEVC не поддерживается в AirSpoon, используйте H.264")

func (s *MirrorSession) streamHEVCFrames(context.Context, VideoSource, time.Duration) error {
	return ErrHEVCUnsupported
}
