package ffmpeg

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"airspoon/internal/airplay"
)

// scriptedSource отдаёт заданные кадры, затем заданную ошибку.
type scriptedSource struct {
	frames []airplay.VideoAccessUnit
	end    error
	closed bool
}

func (s *scriptedSource) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	if len(s.frames) == 0 {
		return airplay.VideoAccessUnit{}, s.end
	}
	au := s.frames[0]
	s.frames = s.frames[1:]
	return au, nil
}

func (s *scriptedSource) Close() error { s.closed = true; return nil }

func frame(tag byte, pts time.Time) airplay.VideoAccessUnit {
	return airplay.VideoAccessUnit{AnnexB: []byte{0, 0, 0, 1, tag}, PTS: pts}
}

// fakeClock — время, которое двигает только sleep.
type fakeClock struct {
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

func newTestSupervised(cfg SuperviseConfig, clock *fakeClock, first airplay.VideoSource) *supervised {
	cfg.now, cfg.sleep = clock.now, clock.sleep
	return Supervise(context.Background(), cfg, first).(*supervised)
}

func TestSupervisedRestartsAfterUnexpectedExit(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	first := &scriptedSource{frames: []airplay.VideoAccessUnit{frame(1, clock.t), frame(2, clock.t)}, end: errors.New("ffmpeg завершился: ACCESS_LOST")}
	second := &scriptedSource{frames: []airplay.VideoAccessUnit{frame(3, clock.t)}, end: io.EOF}
	starts := 0
	s := newTestSupervised(SuperviseConfig{Start: func(context.Context) (airplay.VideoSource, error) {
		starts++
		return second, nil
	}}, clock, first)

	var got []byte
	for i := 0; i < 3; i++ {
		au, err := s.ReadAccessUnit(context.Background())
		if err != nil {
			t.Fatalf("кадр %d: %v", i, err)
		}
		got = append(got, au.AnnexB[4])
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("порядок кадров = %v", got)
	}
	if starts != 1 || !first.closed {
		t.Fatalf("перезапусков %d, старый источник закрыт=%v", starts, first.closed)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 500*time.Millisecond {
		t.Fatalf("паузы перед перезапуском = %v", clock.sleeps)
	}
}

func TestSupervisedGivesUpAfterContinuousFailure(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	first := &scriptedSource{end: errors.New("упал")}
	starts := 0
	s := newTestSupervised(SuperviseConfig{GiveUpAfter: 20 * time.Second, Start: func(context.Context) (airplay.VideoSource, error) {
		starts++
		return nil, errors.New("Failed to enumerate DXGI output")
	}}, clock, first)
	_, err := s.ReadAccessUnit(context.Background())
	if err == nil || !strings.Contains(err.Error(), "не восстановился") || !strings.Contains(err.Error(), "DXGI") {
		t.Fatalf("ожидалась ошибка с последней причиной, получено: %v", err)
	}
	if starts < 3 {
		t.Fatalf("попыток запуска всего %d", starts)
	}
	var total time.Duration
	for _, d := range clock.sleeps {
		total += d
		if d > maxRestartBackoff {
			t.Fatalf("пауза %v больше предела %v", d, maxRestartBackoff)
		}
	}
	if total < 20*time.Second || total > 25*time.Second {
		t.Fatalf("общее ожидание %v, ожидалось около порога 20 с (паузы %v)", total, clock.sleeps)
	}
}

// Постоянная ошибка (включили HDR) не повторяется до GiveUpAfter: ошибка
// возвращается сразу, без пауз и перезапусков, причина остаётся в цепочке.
func TestSupervisedFatalErrorIsNotRetried(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	errHDR := errors.New("формат кадра не поддерживается (HDR)")
	first := &scriptedSource{end: errHDR}
	starts := 0
	s := newTestSupervised(SuperviseConfig{
		Fatal: func(err error) bool { return errors.Is(err, errHDR) },
		Start: func(context.Context) (airplay.VideoSource, error) {
			starts++
			return nil, errors.New("не должен вызываться")
		},
	}, clock, first)
	_, err := s.ReadAccessUnit(context.Background())
	if !errors.Is(err, errHDR) {
		t.Fatalf("ожидалась исходная ошибка в цепочке, получено: %v", err)
	}
	if starts != 0 || len(clock.sleeps) != 0 || !first.closed {
		t.Fatalf("перезапусков %d, пауз %v, источник закрыт %v: ожидалось 0, нет, да", starts, clock.sleeps, first.closed)
	}
}

// Фатальной может оказаться и ошибка запуска нового источника при перезапуске.
func TestSupervisedFatalErrorFromStartStopsRetries(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	errHDR := errors.New("HDR")
	first := &scriptedSource{end: errors.New("упал")}
	starts := 0
	s := newTestSupervised(SuperviseConfig{
		Fatal: func(err error) bool { return errors.Is(err, errHDR) },
		Start: func(context.Context) (airplay.VideoSource, error) {
			starts++
			return nil, errHDR
		},
	}, clock, first)
	_, err := s.ReadAccessUnit(context.Background())
	if !errors.Is(err, errHDR) || starts != 1 {
		t.Fatalf("ошибка %v, запусков %d: ожидалась ErrHDR после одного запуска", err, starts)
	}
}

func TestSupervisedStableRunResetsFailureWindow(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	next := func() *scriptedSource {
		return &scriptedSource{frames: []airplay.VideoAccessUnit{frame(7, clock.t)}, end: errors.New("снова упал")}
	}
	starts := 0
	s := newTestSupervised(SuperviseConfig{GiveUpAfter: 30 * time.Second, StableAfter: 10 * time.Second,
		Start: func(context.Context) (airplay.VideoSource, error) {
			starts++
			return next(), nil
		}}, clock, next())
	for i := 0; i < 8; i++ {
		if _, err := s.ReadAccessUnit(context.Background()); err != nil {
			t.Fatalf("кадр %d: %v (перезапусков %d)", i, err, starts)
		}
		// Источник проработал дольше StableAfter, прежде чем упасть.
		clock.t = clock.t.Add(15 * time.Second)
	}
	if starts < 7 {
		t.Fatalf("перезапусков %d: исправный источник должен перезапускаться без исчерпания лимита", starts)
	}
}

func TestSupervisedNoRestartAfterCancelOrClose(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	fail := func(context.Context) (airplay.VideoSource, error) {
		t.Fatal("источник перезапущен после отмены или закрытия")
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := Supervise(ctx, SuperviseConfig{Start: fail}, &scriptedSource{end: context.Canceled})
	cancel()
	if _, err := s.ReadAccessUnit(ctx); err == nil {
		t.Fatal("после отмены ожидалась ошибка")
	}

	s2 := newTestSupervised(SuperviseConfig{Start: fail}, clock, &scriptedSource{end: errors.New("x")})
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ReadAccessUnit(context.Background()); err != io.EOF {
		t.Fatalf("после Close ожидался EOF, получено %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("повторный Close: %v", err)
	}
}

func TestSupervisedCloseInterruptsRestartWait(t *testing.T) {
	first := &scriptedSource{end: errors.New("упал")}
	started := make(chan struct{})
	s := Supervise(context.Background(), SuperviseConfig{Start: func(context.Context) (airplay.VideoSource, error) {
		close(started)
		return &scriptedSource{}, nil
	}}, first)
	done := make(chan error, 1)
	go func() {
		_, err := s.ReadAccessUnit(context.Background())
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // читатель ждёт 0,5 с перед перезапуском
	_ = s.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("ожидался EOF, получено %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Close не прервал ожидание перезапуска")
	}
	select {
	case <-started:
		t.Fatal("источник запущен после Close")
	default:
	}
}

func TestSupervisedRestartsWhenFirstFrameIsStale(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	stale := &scriptedSource{frames: []airplay.VideoAccessUnit{frame(1, clock.t.Add(-3*time.Second)), frame(2, clock.t)}, end: io.EOF}
	fresh := &scriptedSource{frames: []airplay.VideoAccessUnit{frame(9, clock.t)}, end: io.EOF}
	starts := 0
	s := newTestSupervised(SuperviseConfig{MaxStartAge: time.Second, Start: func(context.Context) (airplay.VideoSource, error) {
		starts++
		return fresh, nil
	}}, clock, stale)
	au, err := s.ReadAccessUnit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if au.AnnexB[4] != 9 || starts != 1 || !stale.closed {
		t.Fatalf("кадр %d, перезапусков %d, старый закрыт=%v: просроченный первый кадр должен был заменить свежий источник",
			au.AnnexB[4], starts, stale.closed)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("замена просроченного кадра не должна ждать: %v", clock.sleeps)
	}

	// Свежий первый кадр отдаётся как есть.
	ok := &scriptedSource{frames: []airplay.VideoAccessUnit{frame(5, clock.t.Add(-200*time.Millisecond))}, end: io.EOF}
	s2 := newTestSupervised(SuperviseConfig{MaxStartAge: time.Second, Start: func(context.Context) (airplay.VideoSource, error) {
		t.Fatal("ненужный перезапуск")
		return nil, nil
	}}, clock, ok)
	if au, err := s2.ReadAccessUnit(context.Background()); err != nil || au.AnnexB[4] != 5 {
		t.Fatalf("свежий кадр: %v %v", au, err)
	}
}
