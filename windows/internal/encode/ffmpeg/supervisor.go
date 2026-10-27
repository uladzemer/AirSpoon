package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"airspoon/internal/airplay"
)

// SuperviseConfig задаёт, как перезапускать источник кадров.
type SuperviseConfig struct {
	// Start запускает новый источник (новый процесс ffmpeg). Обязателен.
	Start func(ctx context.Context) (airplay.VideoSource, error)
	// Logf получает сообщения о перезапусках (может быть nil).
	Logf func(format string, args ...any)
	// MaxStartAge: если первый кадр, отданный источником, к моменту первого
	// чтения старше этого значения (долгий SETUP), он отбрасывается, а источник
	// запускается заново, чтобы не слать приёмнику пачку просроченных кадров.
	// Следом за ним идут P-кадры, поэтому один IDR выбросить нельзя. 0 — не проверять.
	MaxStartAge time.Duration
	// GiveUpAfter: сколько подряд длится неудача (источник падает или не
	// запускается), прежде чем сдаться и вернуть ошибку. По умолчанию 2 минуты:
	// блокировка экрана или окно UAC (ACCESS_LOST) проходят за это время.
	GiveUpAfter time.Duration
	// StableAfter: источник, проработавший столько, считается исправным, и счёт
	// неудач начинается заново. По умолчанию 10 секунд.
	StableAfter time.Duration
	// Fatal сообщает, что ошибку повтором не вылечить (включили HDR, нет Media
	// Foundation): перезапусков нет, ошибка возвращается сразу, а не после
	// GiveUpAfter с замёрзшим кадром на телевизоре. nil — все ошибки повторяемые.
	Fatal func(err error) bool

	// Подмена времени для тестов.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

const (
	defaultGiveUpAfter = 2 * time.Minute
	defaultStableAfter = 10 * time.Second
	maxRestartBackoff  = 3 * time.Second
)

// supervised — VideoSource, который перезапускает нижележащий источник после
// неожиданного завершения. Типичная причина — потеря рабочего стола (ACCESS_LOST
// при блокировке экрана, UAC, смене режима): ffmpeg с ddagrab в этом случае
// завершается, а передача не должна из-за этого падать.
type supervised struct {
	ctx context.Context
	cfg SuperviseConfig

	mu      sync.Mutex
	cur     airplay.VideoSource
	startAt time.Time // когда запущен текущий источник
	closed  bool
	wake    chan struct{} // закрывается в Close, будит ожидание перед перезапуском

	firstRead bool      // первый кадр уже отдан (проверка MaxStartAge выполнена)
	failSince time.Time // начало текущей серии неудач; нулевое — серии нет
	attempt   int       // номер попытки внутри серии (для задержки)
}

// Supervise оборачивает уже запущенный и проверенный источник first. ctx — время
// жизни всей передачи: после его отмены перезапусков нет.
func Supervise(ctx context.Context, cfg SuperviseConfig, first airplay.VideoSource) airplay.VideoSource {
	if cfg.GiveUpAfter <= 0 {
		cfg.GiveUpAfter = defaultGiveUpAfter
	}
	if cfg.StableAfter <= 0 {
		cfg.StableAfter = defaultStableAfter
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	s := &supervised{ctx: ctx, cfg: cfg, cur: first, wake: make(chan struct{})}
	s.startAt = s.now()
	return s
}

func (s *supervised) now() time.Time {
	if s.cfg.now != nil {
		return s.cfg.now()
	}
	return time.Now()
}

func (s *supervised) sleep(ctx context.Context, d time.Duration) error {
	if s.cfg.sleep != nil {
		return s.cfg.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.wake:
		return io.EOF
	}
}

func (s *supervised) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *supervised) current() airplay.VideoSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// ReadAccessUnit возвращает следующий кадр; если источник завершился сам,
// запускает его заново (первый кадр нового процесса — ключевой с SPS/PPS).
func (s *supervised) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	for {
		if s.isClosed() {
			return airplay.VideoAccessUnit{}, io.EOF
		}
		src := s.current()
		if src == nil {
			if err := s.restart(ctx, errors.New("источник не запущен")); err != nil {
				return airplay.VideoAccessUnit{}, err
			}
			continue
		}
		au, err := src.ReadAccessUnit(ctx)
		if err == nil {
			if !s.firstRead {
				s.firstRead = true
				if max := s.cfg.MaxStartAge; max > 0 && !au.PTS.IsZero() {
					if age := s.now().Sub(au.PTS); age > max {
						s.cfg.Logf("первый кадр устарел на %v (SETUP был долгим): запускаем захват заново, чтобы не слать просроченные кадры",
							age.Round(time.Millisecond))
						if err := s.restartNow(ctx); err != nil {
							return airplay.VideoAccessUnit{}, err
						}
						continue
					}
				}
			}
			return au, nil
		}
		if ctx.Err() != nil {
			return airplay.VideoAccessUnit{}, ctx.Err()
		}
		if s.ctx.Err() != nil || s.isClosed() {
			return airplay.VideoAccessUnit{}, err
		}
		if rerr := s.restart(ctx, err); rerr != nil {
			return airplay.VideoAccessUnit{}, rerr
		}
	}
}

// restartNow заменяет работающий источник новым без учёта неудач и задержек.
func (s *supervised) restartNow(ctx context.Context) error {
	s.dropCurrent()
	next, err := s.cfg.Start(s.ctx)
	if err != nil {
		return s.restart(ctx, err)
	}
	return s.install(next)
}

func (s *supervised) dropCurrent() {
	s.mu.Lock()
	old := s.cur
	s.cur = nil
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (s *supervised) install(next airplay.VideoSource) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = next.Close()
		return io.EOF
	}
	s.cur = next
	s.startAt = s.now()
	s.mu.Unlock()
	return nil
}

// restart обрабатывает завершение источника по причине cause: учитывает серию
// неудач, ждёт и запускает источник заново. Возвращает ошибку, когда пора сдаться.
func (s *supervised) restart(ctx context.Context, cause error) error {
	for {
		if s.cfg.Fatal != nil && s.cfg.Fatal(cause) {
			s.dropCurrent()
			return fmt.Errorf("захват экрана остановился, повтор не поможет: %w", cause)
		}
		now := s.now()
		s.mu.Lock()
		ranFor := now.Sub(s.startAt)
		s.mu.Unlock()
		if s.current() != nil && ranFor >= s.cfg.StableAfter {
			// Источник проработал долго: это новая неисправность, а не продолжение серии.
			s.failSince, s.attempt = time.Time{}, 0
		}
		s.dropCurrent()
		if s.failSince.IsZero() {
			s.failSince = now
		}
		if failed := now.Sub(s.failSince); failed >= s.cfg.GiveUpAfter {
			return fmt.Errorf("захват экрана не восстановился за %v: %w", failed.Round(time.Second), cause)
		}
		delay := restartDelay(s.attempt)
		s.attempt++
		s.cfg.Logf("захват остановился (%v); перезапуск через %v (попытка %d)", cause, delay, s.attempt)
		if err := s.sleep(ctx, delay); err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
		next, err := s.cfg.Start(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			cause = err
			continue
		}
		if err := s.install(next); err != nil {
			return err
		}
		s.cfg.Logf("захват перезапущен")
		return nil
	}
}

// restartDelay: 0,5 с, 1 с, 2 с, затем 3 с.
func restartDelay(attempt int) time.Duration {
	d := 500 * time.Millisecond << uint(min(attempt, 6))
	if d > maxRestartBackoff {
		return maxRestartBackoff
	}
	return d
}

func (s *supervised) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.wake)
	old := s.cur
	s.cur = nil
	s.mu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}
