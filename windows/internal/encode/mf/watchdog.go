package mf

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// StallKind — вид зависания кодера.
type StallKind int

const (
	// StallNoOutput — кодеру подают кадры, а на выходе ничего.
	StallNoOutput StallKind = iota + 1
	// StallNoKeyFrame — ключевой кадр запрошен, а не приходит.
	StallNoKeyFrame
)

// StallError — кодер завис. После такого сбоя кодер нужно создать заново.
type StallError struct {
	Kind StallKind
	Wait time.Duration
}

func (e *StallError) Error() string {
	if e.Kind == StallNoKeyFrame {
		return fmt.Sprintf("ключевой кадр не получен за %v", e.Wait.Round(100*time.Millisecond))
	}
	return fmt.Sprintf("кодер принимает кадры, но не выдаёт пакеты уже %v", e.Wait.Round(100*time.Millisecond))
}

// IsStall сообщает, что ошибка — зависание кодера (*StallError).
func IsStall(err error) bool {
	var s *StallError
	return errors.As(err, &s)
}

// Пороги сторожа (план windows-devices, A4).
const (
	// stallStartLimit — сколько ждать первый пакет после создания кодера
	// (аппаратному кодеру нужно время на прогрев).
	stallStartLimit = 3 * time.Second
	// stallOutputLimit — сколько допустимо молчание кодера, который уже выдавал пакеты.
	stallOutputLimit = 2 * time.Second
	// stallKeyLimit — сколько ждать запрошенный ключевой кадр.
	stallKeyLimit = 3 * time.Second
	// stallMinOffered — сколько кадров должно быть предложено кодеру за время
	// молчания. Нужно, чтобы длинная пауза захвата (экран UAC, блокировка) не
	// принималась за зависание кодера: после неё предлагается один-два кадра.
	stallMinOffered = 3
	// keyRequestInterval — не чаще раза в это время просим у кодера IDR.
	keyRequestInterval = time.Second
)

// watchdog следит за тем, что кодер отвечает. Время передаётся снаружи, чтобы
// логику можно было проверить без часов. Все методы безопасны для вызова из
// разных потоков.
type watchdog struct {
	mu sync.Mutex

	started    bool      // кодеру уже предлагали кадры: часы пошли
	offered    uint64    // кадров предложено кодеру (принятых и отброшенных)
	outputs    uint64    // пакетов получено
	lastOut    time.Time // последний пакет (или момент создания)
	offeredOut uint64    // значение offered в момент последнего пакета

	keyPending bool
	keySince   time.Time
	offeredKey uint64

	startLimit, outputLimit, keyLimit time.Duration
	minOffered                        uint64
}

func newWatchdog(now time.Time) *watchdog {
	return &watchdog{
		lastOut: now, startLimit: stallStartLimit, outputLimit: stallOutputLimit,
		keyLimit: stallKeyLimit, minOffered: stallMinOffered,
	}
}

// offer учитывает кадр, предложенный кодеру. Часы старта и ожидания первого
// ключевого кадра идут от первого кадра, а не от создания сторожа: создание
// кодера (setup) у аппаратных MFT занимает секунды, а первые кадры могут прийти
// с задержкой захвата, и это не зависание кодера.
func (w *watchdog) offer(now time.Time) {
	w.mu.Lock()
	if !w.started {
		w.started = true
		if w.outputs == 0 {
			w.lastOut = now
		}
		if w.keyPending {
			w.keySince = now
		}
	}
	w.offered++
	w.mu.Unlock()
}

// output учитывает пакет от кодера.
func (w *watchdog) output(now time.Time, key bool) {
	w.mu.Lock()
	w.outputs++
	w.lastOut = now
	w.offeredOut = w.offered
	if key {
		w.keyPending = false
	}
	w.mu.Unlock()
}

// requestKey запоминает момент, с которого ждём ключевой кадр (повторный запрос
// пока ожидание идёт отсчёт не сбрасывает).
func (w *watchdog) requestKey(now time.Time) {
	w.mu.Lock()
	if !w.keyPending {
		w.keyPending = true
		w.keySince = now
		w.offeredKey = w.offered
	}
	w.mu.Unlock()
}

// check возвращает *StallError, если кодер завис, иначе nil.
func (w *watchdog) check(now time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	limit := w.outputLimit
	if w.outputs == 0 {
		limit = w.startLimit
	}
	if wait := now.Sub(w.lastOut); wait > limit && w.offered-w.offeredOut >= w.minOffered {
		return &StallError{Kind: StallNoOutput, Wait: wait}
	}
	if w.keyPending {
		if wait := now.Sub(w.keySince); wait > w.keyLimit && w.offered-w.offeredKey >= w.minOffered {
			return &StallError{Kind: StallNoKeyFrame, Wait: wait}
		}
	}
	return nil
}

// keyGate хранит запрос ключевого кадра. Запрос не сбрасывается, пока кодер не
// принял его (раньше флаг гасился до SetValue, и при отказе повтора не было), и
// уходит к кодеру не чаще раза в interval: иначе при отказе SetValue вызывался
// бы на каждый кадр.
type keyGate struct {
	mu       sync.Mutex
	interval time.Duration
	pending  bool
	seq      uint64 // растёт с каждым запросом: успех гасит только тот запрос, что был отправлен
	last     time.Time
	haveLast bool
}

func newKeyGate(interval time.Duration) *keyGate {
	return &keyGate{interval: interval}
}

// request просит ключевой кадр.
func (g *keyGate) request() {
	g.mu.Lock()
	g.pending = true
	g.seq++
	g.mu.Unlock()
}

// take решает, пора ли отправлять запрос кодеру. ok=true — отправить и затем
// вызвать done(seq, результат).
func (g *keyGate) take(now time.Time) (seq uint64, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.pending || (g.haveLast && now.Sub(g.last) < g.interval) {
		return 0, false
	}
	g.last, g.haveLast = now, true
	return g.seq, true
}

// done сообщает итог отправки. Запрос снимается только при успехе и только если
// за время отправки не пришёл новый.
func (g *keyGate) done(seq uint64, success bool) {
	g.mu.Lock()
	if success && g.seq == seq {
		g.pending = false
	}
	g.mu.Unlock()
}

// isPending — запрос ещё не доставлен кодеру.
func (g *keyGate) isPending() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending
}
