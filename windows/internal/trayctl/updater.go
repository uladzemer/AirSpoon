// Пакет trayctl: периодическая проверка обновлений программы.
package trayctl

import (
	"context"
	"fmt"
	"sync"
	"time"

	"airspoon/internal/update"
)

// UpdateCheckFunc выполняет сетевой запрос проверки релиза.
type UpdateCheckFunc func(ctx context.Context, current string) (update.Release, bool, error)

// UpdaterConfig — параметры проверки обновлений.
type UpdaterConfig struct {
	Version      string                                    // текущая версия программы ("dev", "0.2.0")
	Check        UpdateCheckFunc                           // сетевая проверка
	Enabled      func() bool                               // включена ли проверка в настройках
	Notify       func(kind NoticeKind, title, text string) // уведомление человеку
	OnUpdate     func(rel update.Release)                  // вызов при обнаружении более новой версии
	Logf         func(format string, args ...any)          // журнал
	InitialDelay time.Duration                             // пауза до первой проверки (по умолчанию 1 минута)
	Interval     time.Duration                             // пауза между проверками (по умолчанию 24 часа)
	Sleep        func(ctx context.Context, d time.Duration) bool
	Busy         func() bool // идёт показ: пункт меню появится, уведомление — при следующей проверке
}

// Updater управляет расписанием проверок обновлений и уведомлением пользователя.
type Updater struct {
	cfg UpdaterConfig

	mu       sync.Mutex
	notified bool // уведомление о новой версии показывается один раз за запуск
}

// NewUpdater создаёт объект проверки обновлений.
func NewUpdater(cfg UpdaterConfig) *Updater {
	if cfg.InitialDelay <= 0 {
		cfg.InitialDelay = time.Minute
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Updater{cfg: cfg}
}

// Run выполняет периодическую проверку в цикле, пока жив ctx.
// Для версии "dev" или пустой версии проверки не проводятся.
func (u *Updater) Run(ctx context.Context) {
	if u.cfg.Version == "dev" || u.cfg.Version == "" {
		return
	}
	if !u.cfg.Sleep(ctx, u.cfg.InitialDelay) {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		u.CheckOnce(ctx)
		if !u.cfg.Sleep(ctx, u.cfg.Interval) {
			return
		}
	}
}

// CheckOnce выполняет единичную проверку обновлений, если она включена в настройках.
func (u *Updater) CheckOnce(ctx context.Context) {
	if u.cfg.Version == "dev" || u.cfg.Version == "" {
		return
	}
	if u.cfg.Enabled != nil && !u.cfg.Enabled() {
		// Настройка выключена пользователем: запросов в сеть не делать вовсе.
		return
	}
	if u.cfg.Check == nil {
		return
	}
	rel, newer, err := u.cfg.Check(ctx, u.cfg.Version)
	if err != nil {
		u.cfg.Logf("проверка обновлений: %v", err)
		return
	}
	if !newer {
		return
	}
	if u.cfg.Enabled != nil && !u.cfg.Enabled() {
		// Проверку выключили, пока шёл запрос: ответ не показываем.
		return
	}

	quiet := u.cfg.Busy != nil && u.cfg.Busy()
	u.mu.Lock()
	first := !u.notified && !quiet
	if first {
		u.notified = true
	}
	u.mu.Unlock()

	if u.cfg.OnUpdate != nil {
		u.cfg.OnUpdate(rel)
	}
	if first && u.cfg.Notify != nil {
		u.cfg.Notify(NoticeInfo, AppName,
			fmt.Sprintf("Вышла новая версия AirSpoon Sender %s. Откройте меню у значка, чтобы обновить.", rel.Version))
	}
}
