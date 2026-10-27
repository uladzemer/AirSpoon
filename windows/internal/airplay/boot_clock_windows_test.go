//go:build windows

package airplay

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsBootClockUsesSystemUptime(t *testing.T) {
	first := bootRelativeNow()
	coarse := getTickCount64Uptime()
	second := bootRelativeNow()
	if delta := first - coarse; delta < -time.Second || delta > time.Second {
		t.Fatalf("boot clock %v differs from system uptime %v by %v", first, coarse, delta)
	}
	if second < first {
		t.Fatalf("boot clock moved backward: %v then %v", first, second)
	}
}

// Точные часы должны находиться (Windows 10+), иначе bootRelativeNow молча
// деградирует до GetTickCount64 с шагом 15,6 мс.
func TestWindowsBootClockHasPreciseSource(t *testing.T) {
	if err := procQueryInterruptTimePrecise.Find(); err != nil {
		if windows.RtlGetVersion().MajorVersion < 10 {
			t.Skipf("QueryInterruptTimePrecise недоступна (старая Windows): %v", err)
		}
		t.Fatalf("QueryInterruptTimePrecise не найдена на Windows 10+: %v", err)
	}
	// Шаг точных часов должен быть заметно мельче тика таймера.
	var smallest time.Duration = time.Second
	prev := bootRelativeNow()
	for i := 0; i < 100000; i++ {
		now := bootRelativeNow()
		if d := now - prev; d > 0 && d < smallest {
			smallest = d
		}
		prev = now
	}
	if smallest >= time.Millisecond {
		t.Fatalf("наименьший шаг boot-часов %v: похоже на грубый тик", smallest)
	}
}
