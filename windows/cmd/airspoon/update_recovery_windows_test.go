package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestShouldRollbackUpdate(t *testing.T) {
	for _, tc := range []struct {
		updated bool
		crashes int
		ranFor  time.Duration
		want    bool
	}{
		{false, maxCrashes, time.Second, false},
		{true, maxCrashes - 1, time.Second, false},
		{true, maxCrashes, time.Second, true},
		{true, maxCrashes, updateHealthyAfter, false},
		{true, maxCrashes + 1, time.Minute * 2, false},
	} {
		if got := shouldRollbackUpdate(tc.updated, tc.crashes, tc.ranFor); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}

func TestRecoverUpdate(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"missing", "broken", "healthy"} {
		t.Run(state, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "sender.exe")
			if err := os.WriteFile(exe+".old", image, 0600); err != nil {
				t.Fatal(err)
			}
			if state != "missing" {
				data := []byte("interrupted download")
				if state == "healthy" {
					data = image
				}
				if err := os.WriteFile(exe, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverUpdate(exe); err != nil {
				t.Fatal(err)
			}
			if !executableOK(exe) {
				t.Fatal("restored executable is invalid")
			}
			_, err := os.Stat(exe + ".old")
			if state == "healthy" && err != nil {
				t.Fatal("backup removed before health confirmation")
			}
			if state != "healthy" && !os.IsNotExist(err) {
				t.Fatal("backup not restored")
			}
		})
	}
}

func TestRestoreUpdatePreservesNewWithoutBackup(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "sender.exe")
	if err := os.WriteFile(exe, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreUpdate(exe); err == nil {
		t.Fatal("missing backup must fail")
	}
	data, err := os.ReadFile(exe)
	if err != nil || string(data) != "new" {
		t.Fatalf("new executable lost: %q %v", data, err)
	}
}

func TestRestoreUpdateAfterCrashes(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "sender.exe")
	if err := os.WriteFile(exe, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".old", []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreUpdate(exe); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil || string(data) != "previous" {
		t.Fatalf("rollback: %q %v", data, err)
	}
}

func TestRestoreUpdateWhileSupervisorImageIsRunning(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "sender.exe")
	for _, path := range []string{exe, exe + ".old"} {
		if err := os.WriteFile(path, image, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AIRSPOON_TEST_PREFLIGHT_HELPER", "hang")
	cmd := exec.Command(exe, "-test.run=TestPreflightHelperProcess")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := restoreUpdate(exe); err != nil {
		t.Fatalf("rollback of running image: %v", err)
	}
	if !executableOK(exe) {
		t.Fatal("restored executable is invalid")
	}
}
