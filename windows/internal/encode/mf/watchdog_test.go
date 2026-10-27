package mf

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func offerN(w *watchdog, n int) {
	for i := 0; i < n; i++ {
		w.offer(t0)
	}
}

func stallKind(err error) StallKind {
	var s *StallError
	if errors.As(err, &s) {
		return s.Kind
	}
	return 0
}

// Кодеру подают кадры, а пакетов нет: до 3 с после старта терпим, потом зависание.
func TestWatchdogNoOutputAtStart(t *testing.T) {
	w := newWatchdog(t0)
	offerN(w, 10)
	if err := w.check(t0.Add(2900 * time.Millisecond)); err != nil {
		t.Fatalf("рано: %v", err)
	}
	err := w.check(t0.Add(3100 * time.Millisecond))
	if stallKind(err) != StallNoOutput || !IsStall(err) {
		t.Fatalf("ожидали зависание, получили %v", err)
	}
}

// После первого пакета порог 2 с.
func TestWatchdogNoOutputAfterStart(t *testing.T) {
	w := newWatchdog(t0)
	offerN(w, 5)
	w.output(t0.Add(100*time.Millisecond), true)
	offerN(w, 5)
	if err := w.check(t0.Add(2000 * time.Millisecond)); err != nil {
		t.Fatalf("рано: %v", err)
	}
	if err := w.check(t0.Add(2200 * time.Millisecond)); stallKind(err) != StallNoOutput {
		t.Fatalf("ожидали зависание после 2 с молчания: %v", err)
	}
	// Пакет пришёл: счётчик молчания обнулён.
	w.output(t0.Add(2300*time.Millisecond), false)
	if err := w.check(t0.Add(2400 * time.Millisecond)); err != nil {
		t.Fatalf("после пакета: %v", err)
	}
}

// Пауза захвата (UAC, блокировка): кадров кодеру не подавали, зависанием это не считается.
func TestWatchdogCapturePauseIsNotStall(t *testing.T) {
	w := newWatchdog(t0)
	offerN(w, 10)
	w.output(t0.Add(100*time.Millisecond), true)
	// 30 секунд тишины, затем один-два кадра.
	offerN(w, 2)
	if err := w.check(t0.Add(30 * time.Second)); err != nil {
		t.Fatalf("пауза захвата принята за зависание: %v", err)
	}
	// Но если кадры идут и дальше, а выхода нет — это зависание.
	offerN(w, 1)
	if err := w.check(t0.Add(30*time.Second + 50*time.Millisecond)); stallKind(err) != StallNoOutput {
		t.Fatalf("ожидали зависание: %v", err)
	}
}

func TestWatchdogKeyFrameWait(t *testing.T) {
	w := newWatchdog(t0)
	offerN(w, 3)
	w.output(t0.Add(10*time.Millisecond), true) // первый IDR
	// Дальше кодер выдаёт только P-кадры, а мы просим IDR.
	w.requestKey(t0.Add(time.Second))
	for i := 0; i < 20; i++ {
		offerN(w, 1)
		w.output(t0.Add(time.Second+time.Duration(i)*100*time.Millisecond), false)
	}
	if err := w.check(t0.Add(3900 * time.Millisecond)); err != nil {
		t.Fatalf("рано (прошло <3 с): %v", err)
	}
	err := w.check(t0.Add(4100 * time.Millisecond))
	if stallKind(err) != StallNoKeyFrame {
		t.Fatalf("ожидали «ключевой кадр не пришёл», получили %v", err)
	}
	// Повторный запрос отсчёт не сбрасывает.
	w.requestKey(t0.Add(4 * time.Second))
	if err := w.check(t0.Add(4200 * time.Millisecond)); stallKind(err) != StallNoKeyFrame {
		t.Fatalf("повторный запрос сбросил ожидание: %v", err)
	}
	// IDR пришёл — ожидание закончено.
	w.output(t0.Add(4300*time.Millisecond), true)
	if err := w.check(t0.Add(4400 * time.Millisecond)); err != nil {
		t.Fatalf("после IDR: %v", err)
	}
}

func TestWatchdogKeyWaitNeedsOfferedFrames(t *testing.T) {
	w := newWatchdog(t0)
	w.output(t0, true)
	w.requestKey(t0.Add(time.Second))
	// Кадров не подавали вовсе: ждать нечего.
	if err := w.check(t0.Add(20 * time.Second)); err != nil {
		t.Fatalf("без кадров: %v", err)
	}
}

// Запрос IDR не гасится, пока SetValue не удался; попытки не чаще раза в секунду.
func TestKeyGateRetriesUntilSuccess(t *testing.T) {
	g := newKeyGate(time.Second)
	if _, ok := g.take(t0); ok {
		t.Fatal("запроса нет, а попытка разрешена")
	}
	g.request()
	seq, ok := g.take(t0)
	if !ok {
		t.Fatal("первая попытка должна быть сразу")
	}
	g.done(seq, false) // кодер отверг ForceKeyFrame
	if !g.isPending() {
		t.Fatal("запрос погашен после отказа кодера")
	}
	if _, ok := g.take(t0.Add(500 * time.Millisecond)); ok {
		t.Fatal("повтор раньше чем через секунду")
	}
	seq, ok = g.take(t0.Add(1100 * time.Millisecond))
	if !ok {
		t.Fatal("повтор через секунду не разрешён")
	}
	g.done(seq, true)
	if g.isPending() {
		t.Fatal("после успеха запрос остался")
	}
	if _, ok := g.take(t0.Add(5 * time.Second)); ok {
		t.Fatal("попытка без запроса")
	}
}

// Запрос, пришедший во время отправки, успехом предыдущей отправки не гасится.
func TestKeyGateNewRequestDuringSend(t *testing.T) {
	g := newKeyGate(time.Second)
	g.request()
	seq, _ := g.take(t0)
	g.request()
	g.done(seq, true)
	if !g.isPending() {
		t.Fatal("новый запрос потерян")
	}
	if _, ok := g.take(t0.Add(500 * time.Millisecond)); ok {
		t.Fatal("ограничение частоты не действует и после успеха")
	}
	if _, ok := g.take(t0.Add(1500 * time.Millisecond)); !ok {
		t.Fatal("отложенный запрос не ушёл")
	}
}

// Имитация кодера, отвергающего ForceKeyFrame: цепочка «запрос -> попытки -> зависание».
func TestRejectingEncoderEndsInStall(t *testing.T) {
	g := newKeyGate(time.Second)
	w := newWatchdog(t0)
	w.output(t0, true)
	g.request()
	w.requestKey(t0.Add(time.Second))
	attempts := 0
	var err error
	for ms := 1000; ms <= 5000 && err == nil; ms += 33 { // кадры по ~30 к/с
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		w.offer(now)
		if seq, ok := g.take(now); ok {
			attempts++
			g.done(seq, false) // SetValue отвергнут
		}
		w.output(now, false) // кодер выдаёт только P-кадры
		err = w.check(now)
	}
	if stallKind(err) != StallNoKeyFrame {
		t.Fatalf("ожидали зависание по ключевому кадру, получили %v", err)
	}
	if attempts < 3 || attempts > 5 {
		t.Fatalf("попыток SetValue %d: ожидали около одной в секунду за 3-4 с", attempts)
	}
}

// Медленный setup и пачка кадров после долгой задержки захвата: часы старта и
// ключевого кадра идут от первого offer(), а не от создания сторожа.
func TestWatchdogClocksStartAtFirstOffer(t *testing.T) {
	first := t0.Add(10 * time.Second)
	w := newWatchdog(t0)
	w.requestKey(t0)
	for i := 0; i < 5; i++ {
		w.offer(first)
	}
	if err := w.check(first.Add(2900 * time.Millisecond)); err != nil {
		t.Fatalf("ложное зависание сразу после первых кадров: %v", err)
	}
	if err := w.check(first.Add(3100 * time.Millisecond)); stallKind(err) != StallNoOutput {
		t.Fatalf("через 3 с от первого кадра ждали зависание: %v", err)
	}

	// Ключевой кадр: отсчёт тоже от первого кадра.
	w2 := newWatchdog(t0)
	w2.requestKey(t0)
	for i := 0; i < 5; i++ {
		w2.offer(first)
	}
	w2.output(first.Add(100*time.Millisecond), false)
	for i := 0; i < 5; i++ {
		w2.offer(first.Add(2 * time.Second))
	}
	w2.output(first.Add(2900*time.Millisecond), false)
	if err := w2.check(first.Add(2950 * time.Millisecond)); err != nil {
		t.Fatalf("ключевой кадр: ложное зависание: %v", err)
	}
	if err := w2.check(first.Add(3200 * time.Millisecond)); stallKind(err) != StallNoKeyFrame {
		t.Fatalf("ключевой кадр не пришёл за 3 с от первого кадра: %v", err)
	}
}
