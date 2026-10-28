package trayctl

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"airspoon/internal/update"
)

func TestUpdaterDevSkipsCheck(t *testing.T) {
	var called atomic.Bool
	u := NewUpdater(UpdaterConfig{
		Version: "dev",
		Check: func(ctx context.Context, current string) (update.Release, bool, error) {
			called.Store(true)
			return update.Release{}, false, nil
		},
		InitialDelay: time.Millisecond,
		Interval:     time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	u.Run(ctx)

	if called.Load() {
		t.Error("для dev-версии запросы обновлений выполняться не должны")
	}
}

func TestUpdaterDisabledSkipsCheck(t *testing.T) {
	var called atomic.Bool
	u := NewUpdater(UpdaterConfig{
		Version: "0.2.0",
		Enabled: func() bool { return false },
		Check: func(ctx context.Context, current string) (update.Release, bool, error) {
			called.Store(true)
			return update.Release{}, false, nil
		},
		InitialDelay: time.Millisecond,
		Interval:     time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	u.Run(ctx)

	if called.Load() {
		t.Error("при выключенной настройке запросы обновлений выполняться не должны")
	}
}

func TestUpdaterFindsUpdateAndNotifiesOnce(t *testing.T) {
	var checks atomic.Int64
	var updates atomic.Int64
	var notices []string
	var logged []string

	fakeRel := update.Release{
		TagName: "v0.3.0",
		Version: "0.3.0",
	}

	u := NewUpdater(UpdaterConfig{
		Version: "0.2.0",
		Enabled: func() bool { return true },
		Check: func(ctx context.Context, current string) (update.Release, bool, error) {
			checks.Add(1)
			return fakeRel, true, nil
		},
		Notify: func(kind NoticeKind, title, text string) {
			if title != AppName {
				t.Errorf("title = %q, want %q", title, AppName)
			}
			notices = append(notices, text)
		},
		OnUpdate: func(rel update.Release) {
			updates.Add(1)
		},
		Logf: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	})

	ctx := context.Background()
	// Первая проверка
	u.CheckOnce(ctx)
	// Вторая проверка (следующий цикл)
	u.CheckOnce(ctx)

	if checks.Load() != 2 {
		t.Fatalf("ожидалось 2 проверки, выполнено %d", checks.Load())
	}
	if updates.Load() != 2 {
		t.Fatalf("ожидалось 2 вызова OnUpdate, выполнено %d", updates.Load())
	}
	if len(notices) != 1 {
		t.Fatalf("уведомление должно быть показано ровно 1 раз, получено %d", len(notices))
	}
	wantText := "Вышла новая версия AirSpoon Sender 0.3.0. Откройте меню у значка, чтобы обновить."
	if notices[0] != wantText {
		t.Errorf("текст уведомления:\n got %q\nwant %q", notices[0], wantText)
	}
	if len(logged) != 0 {
		t.Errorf("неожиданные записи в журнал: %v", logged)
	}
}

func TestUpdaterErrorLoggedOnly(t *testing.T) {
	var notices []string
	var logged []string

	u := NewUpdater(UpdaterConfig{
		Version: "0.2.0",
		Enabled: func() bool { return true },
		Check: func(ctx context.Context, current string) (update.Release, bool, error) {
			return update.Release{}, false, errors.New("network failure")
		},
		Notify: func(kind NoticeKind, title, text string) {
			notices = append(notices, text)
		},
		Logf: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	})

	u.CheckOnce(context.Background())

	if len(notices) != 0 {
		t.Errorf("человеку не должны показываться ошибки сети: %v", notices)
	}
	if len(logged) != 1 {
		t.Fatalf("ожидалась 1 запись в журнал, получено %d", len(logged))
	}
}

func TestUpdaterDisabledDuringCheckDropsResult(t *testing.T) {
	enabled := true
	var updated, notified atomic.Bool
	u := NewUpdater(UpdaterConfig{
		Version: "0.1.0",
		Enabled: func() bool { return enabled },
		Check: func(ctx context.Context, current string) (update.Release, bool, error) {
			enabled = false // галочку сняли, пока шёл запрос
			return update.Release{Version: "0.2.0"}, true, nil
		},
		OnUpdate: func(update.Release) { updated.Store(true) },
		Notify:   func(NoticeKind, string, string) { notified.Store(true) },
	})
	u.CheckOnce(context.Background())
	if updated.Load() || notified.Load() {
		t.Fatal("ответ после выключения проверки не должен показываться")
	}
}

func TestUpdaterBusyDefersNotice(t *testing.T) {
	busy := true
	var updates, notices int
	u := NewUpdater(UpdaterConfig{
		Version: "0.1.0",
		Busy:    func() bool { return busy },
		Check: func(ctx context.Context, current string) (update.Release, bool, error) {
			return update.Release{Version: "0.2.0"}, true, nil
		},
		OnUpdate: func(update.Release) { updates++ },
		Notify:   func(NoticeKind, string, string) { notices++ },
	})
	u.CheckOnce(context.Background())
	if updates != 1 || notices != 0 {
		t.Fatalf("во время показа: пункт меню есть, уведомления нет; got updates=%d notices=%d", updates, notices)
	}
	busy = false
	u.CheckOnce(context.Background())
	if notices != 1 {
		t.Fatalf("после показа уведомление должно появиться один раз, got %d", notices)
	}
}
