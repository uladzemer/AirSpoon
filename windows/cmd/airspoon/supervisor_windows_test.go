package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestIsCrashExit(t *testing.T) {
	cases := []struct {
		code  uint32
		crash bool
	}{
		{0, false},                  // обычный выход
		{1, false},                  // ошибка запуска показана человеку; «Завершить задачу»
		{statusControlCExit, false}, // закрытие консоли или выход из сеанса
		{exitRestartGPU, false},     // просьба перезапуска для смены видеокарты
		{2, true},                   // необработанная паника Go
		{exitCrash, true},           // перехваченная паника
		{0xC0000005, true},          // нарушение доступа в нативном коде
		{0xC0000409, true},          // сбой стека (fail fast)
	}
	for _, c := range cases {
		if got := isCrashExit(c.code); got != c.crash {
			t.Errorf("isCrashExit(0x%X) = %v, ожидалось %v", c.code, got, c.crash)
		}
	}
}

func TestRecentCrashesDropsOldOnes(t *testing.T) {
	now := time.Now()
	list := []time.Time{now.Add(-2 * crashWindow), now.Add(-crashWindow / 2), now.Add(-time.Second)}
	got := recentCrashes(list, now)
	if len(got) != 2 {
		t.Fatalf("осталось %d аварий, ожидалось 2: %v", len(got), got)
	}
}

// Закрытие задания должно убивать привязанный процесс: так «Снять задачу» с
// наблюдателя закрывает и рабочий процесс (а не оставляет значок без присмотра).
func TestKillOnCloseJobKillsChild(t *testing.T) {
	job, err := newKillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			windows.CloseHandle(job)
		}
	}()

	ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe")
	cmd := exec.Command(ping, "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skip("ping.exe не запустился:", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer cmd.Process.Kill()

	if err := addToJob(job, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
		t.Fatal("процесс завершился до закрытия задания")
	case <-time.After(300 * time.Millisecond):
	}
	closed = true
	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("процесс не завершён после закрытия задания")
	}
}

// Дочерний процесс проверки предпочтения: зависший драйвер убивается по таймауту,
// наблюдатель не зависает вместе с ним.
func TestWaitPreflightKillsHungChild(t *testing.T) {
	t.Setenv("AIRSPOON_TEST_PREFLIGHT_HELPER", "hang")
	cmd := exec.Command(os.Args[0], "-test.run=TestPreflightHelperProcess")
	start := time.Now()
	if waitPreflight(cmd, 500*time.Millisecond) {
		t.Fatal("зависший процесс не может считаться успешным")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("ожидание не ограничено таймаутом: %v", d)
	}
}

func TestWaitPreflightOKAndFail(t *testing.T) {
	t.Setenv("AIRSPOON_TEST_PREFLIGHT_HELPER", "ok")
	if !waitPreflight(exec.Command(os.Args[0], "-test.run=TestPreflightHelperProcess"), 10*time.Second) {
		t.Fatal("штатный выход должен считаться успехом")
	}
	t.Setenv("AIRSPOON_TEST_PREFLIGHT_HELPER", "fail")
	if waitPreflight(exec.Command(os.Args[0], "-test.run=TestPreflightHelperProcess"), 10*time.Second) {
		t.Fatal("ненулевой код — не успех")
	}
}

func TestWaitPreflightStartError(t *testing.T) {
	if waitPreflight(exec.Command(filepath.Join(t.TempDir(), "нет-такого.exe")), time.Second) {
		t.Fatal("незапустившийся процесс — не успех")
	}
}

// TestPreflightHelperProcess — не тест, а тело дочернего процесса для тестов выше.
func TestPreflightHelperProcess(t *testing.T) {
	switch os.Getenv("AIRSPOON_TEST_PREFLIGHT_HELPER") {
	case "hang":
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(3)
	}
}
