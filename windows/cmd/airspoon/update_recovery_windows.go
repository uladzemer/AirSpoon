package main

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"time"
)

const updateHealthyAfter = 60 * time.Second

// Проверяем структуру PE; ошибки, проявляющиеся только при запуске, отслеживает
// наблюдатель. Отсутствующий exe можно восстановить, запустив сохранённый .old.
func executableOK(path string) bool {
	f, err := pe.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return f.OptionalHeader != nil && len(f.Sections) > 0
}

func recoverUpdate(exe string) error {
	if executableOK(exe) {
		return nil
	}
	if !executableOK(exe + ".old") {
		return fmt.Errorf("исполняемый файл повреждён, исправная резервная копия не найдена: %s", exe)
	}
	return restoreUpdate(exe)
}

// restoreUpdate сохраняет оба файла при ошибке восстановления. Переименование
// позволяет откатить и запущенный exe (наблюдатель всё ещё исполняет его образ).
func restoreUpdate(exe string) error {
	if _, err := os.Stat(exe + ".old"); err != nil {
		return err
	}
	failed := exe + ".failed"
	if err := os.Remove(failed); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	moved := false
	if err := os.Rename(exe, failed); err == nil {
		moved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(exe+".old", exe); err != nil {
		if moved {
			if undo := os.Rename(failed, exe); undo != nil {
				return fmt.Errorf("восстановление: %v; возврат нового файла: %w", err, undo)
			}
		}
		return err
	}
	_ = os.Remove(failed) // работающий образ Windows может оставаться занят
	return nil
}

func shouldRollbackUpdate(updated bool, quickCrashes int, ranFor time.Duration) bool {
	return updated && quickCrashes >= maxCrashes && ranFor < updateHealthyAfter
}

// Вызывается после создания значка и подключения контроллера. Закрытие приложения
// раньше срока сохраняет резервную копию; обычный повторный запуск тоже её проверит.
func confirmUpdateHealth(ctx context.Context, exe string) {
	if _, err := os.Stat(exe + ".old"); err != nil {
		return
	}
	go func() {
		timer := time.NewTimer(updateHealthyAfter)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := os.Remove(exe + ".old"); err != nil && !errors.Is(err, os.ErrNotExist) {
				appendLog("обновление: резервная копия пока не удалена: %v", err)
			}
		}
	}()
}
