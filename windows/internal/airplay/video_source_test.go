package airplay

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// sliceVideoSource отдаёт заранее заданные access unit'ы, затем io.EOF.
type sliceVideoSource struct {
	frames []VideoAccessUnit
	index  int
}

func (r *sliceVideoSource) ReadAccessUnit(context.Context) (VideoAccessUnit, error) {
	if r.index == len(r.frames) {
		return VideoAccessUnit{}, io.EOF
	}
	frame := r.frames[r.index]
	r.index++
	return frame, nil
}

func (r *sliceVideoSource) Close() error { return nil }

// blockingVideoSource блокирует чтение до Close.
type blockingVideoSource struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingVideoSource() *blockingVideoSource {
	return &blockingVideoSource{closed: make(chan struct{})}
}

func (r *blockingVideoSource) ReadAccessUnit(context.Context) (VideoAccessUnit, error) {
	<-r.closed
	return VideoAccessUnit{}, os.ErrClosed
}

func (r *blockingVideoSource) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestVideoSourceInterfaceIsImplementedByHelpers(t *testing.T) {
	var _ VideoSource = (*sliceVideoSource)(nil)
	var _ VideoSource = (*blockingVideoSource)(nil)
}

func TestTimeFromNTPRoundTrip(t *testing.T) {
	want := time.Unix(1700000000, 500_000_000).UTC()
	ntp := uint64(want.Unix()+secondsFrom1900To1970)<<32 | uint64(want.Nanosecond())<<32/uint64(time.Second)
	got := timeFromNTP(ntp)
	if d := got.Sub(want); d < -time.Microsecond || d > time.Microsecond {
		t.Fatalf("timeFromNTP = %v, want %v", got, want)
	}
}
