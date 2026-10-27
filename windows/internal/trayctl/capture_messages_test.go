package trayctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Каждая причина сбоя захвата даёт свой понятный текст и включает ErrCapture.
func TestUserMessageForCaptureCauses(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrNoMediaFoundation, "Пакет мультимедийных функций"},
		{ErrCaptureHDR, "HDR"},
		{ErrCaptureSession, "удалённый рабочий стол"},
		{ErrCaptureStopped, "Захват экрана остановился"},
	}
	for _, tc := range tests {
		if !errors.Is(tc.err, ErrCapture) {
			t.Errorf("%v должна включать ErrCapture", tc.err)
		}
		msg := UserMessage(fmt.Errorf("обёртка: %w", tc.err), "Телевизор")
		if !strings.Contains(msg, tc.want) {
			t.Errorf("для %v сообщение %q без %q", tc.err, msg, tc.want)
		}
		if strings.Contains(msg, "Не получилось захватить экран компьютера. Если компьютер заблокирован") {
			t.Errorf("для %v выдан общий текст: %q", tc.err, msg)
		}
	}
}

// Ошибки, которые повтор не лечит, сразу сообщают причину и не гоняют попытки
// даже после того, как показ уже шёл.
func TestFatalCaptureErrorIsNotRetriedAfterStreaming(t *testing.T) {
	for _, cause := range []error{ErrCaptureHDR, ErrNoMediaFoundation, ErrCaptureSession} {
		e := newEnv(t, nil, streamThenFail(fmt.Errorf("передача видео: %w", cause)), streamUntilStop(nil))
		e.ctl.Select(tvLiving)
		waitPhase(t, e.ctl, Idle)
		if n := e.str.calls.Load(); n != 1 {
			t.Errorf("%v: вызовов Stream %d, ждали 1 (без повторов)", cause, n)
		}
		if !e.ui.has(UserMessage(cause, "")) {
			t.Errorf("%v: нет сообщения причины, уведомления: %q", cause, e.ui.texts())
		}
		e.ctl.Shutdown(0)
	}
}

// Обычный обрыв после показа по-прежнему повторяется (поведение не меняется).
func TestNonFatalErrorStillRetries(t *testing.T) {
	e := newEnv(t, nil, dropAfterStreaming, streamUntilStop(nil))
	e.ctl.Select(tvLiving)
	waitFor(t, "повтор", func() bool { return e.str.calls.Load() >= 2 })
}

// Notice из Streamer доходит до пользователя уведомлением.
func TestStreamerNoticeIsShown(t *testing.T) {
	e := newEnv(t, nil, func(ctx_ context.Context, _ Target, h Hooks) error {
		h.Notice("Аппаратного кодера видео нет — качество снижено до 1280x720, 30 кадров в секунду.")
		h.Streaming()
		<-ctx_.Done()
		return nil
	})
	e.ctl.Select(tvLiving)
	waitFor(t, "уведомление о качестве", func() bool { return e.ui.has("качество снижено") })
}

// Пункт «Сохранить отчёт для поддержки» есть только когда задано действие.
func TestMenuSupportReportItem(t *testing.T) {
	e := newEnv(t, nil)
	for _, it := range e.ctl.Menu() {
		if it.Kind == ItemReport {
			t.Fatal("пункт отчёта без действия не нужен")
		}
	}
	called := 0
	e2 := newEnv(t, func(c *Config) { c.SupportReport = func() { called++ } })
	var found *MenuItem
	items := e2.ctl.Menu()
	for i := range items {
		if items[i].Kind == ItemReport {
			found = &items[i]
		}
	}
	if found == nil || found.Text != TextReport {
		t.Fatalf("пункта отчёта нет: %v", texts(items))
	}
	found.Action()
	if called != 1 {
		t.Fatalf("действие вызвано %d раз", called)
	}
	// Перед «Выход» пункт стоит выше разделителя.
	last := items[len(items)-1]
	if last.Kind != ItemExit {
		t.Fatalf("последний пункт: %v", last.Kind)
	}
}

// streamThenFail сообщает «пошли кадры» и обрывается с заданной ошибкой.
func streamThenFail(err error) func(context.Context, Target, Hooks) error {
	return func(ctx context.Context, _ Target, h Hooks) error {
		h.Streaming()
		return err
	}
}

// waitErr — ошибка захвата «рабочий стол пока недоступен» (как dxgi.DesktopWaitError).
type waitErr struct{}

func (waitErr) Error() string           { return "рабочий стол пока недоступен" }
func (waitErr) WaitingForDesktop() bool { return true }

// Одна видеокарта, экран блокировки: DXGI_ERROR_UNSUPPORTED после показа не
// останавливает повторы и не тратит попытки. Пользователь видит мягкое «жду
// рабочий стол» один раз, а показ возвращается, когда стол вернулся.
func TestWaitingCaptureKeepsRetryingAfterStreaming(t *testing.T) {
	wait := fmt.Errorf("передача видео: %w", waitErr{})
	script := []func(context.Context, Target, Hooks) error{streamThenFail(errors.New("EOF"))}
	for i := 0; i < 8; i++ { // больше MaxRetries: счёт попыток не тратится
		script = append(script, failWith(wait))
	}
	script = append(script, streamUntilStop(nil))
	e := newEnv(t, nil, script...)
	e.ctl.Select(tvLiving)
	waitFor(t, "показ вернулся", func() bool { return e.str.calls.Load() >= 10 && e.ctl.Snapshot().Phase == Streaming })
	if e.ui.has("Не получилось восстановить") || e.ui.has("недоступно в этом сеансе") {
		t.Fatalf("ожидание не должно быть окончательной ошибкой: %q", e.ui.texts())
	}
	cnt := 0
	for _, s := range e.ui.texts() {
		if strings.Contains(s, "Жду рабочий стол") {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("сообщение «жду рабочий стол» должно быть одно, получено %d: %q", cnt, e.ui.texts())
	}
	if isFatalCapture(wait) {
		t.Fatal("ожидание не фатально")
	}
	e.ctl.Shutdown(0)
}
