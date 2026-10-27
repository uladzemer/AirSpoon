//go:build windows

package airplay

import (
	"math/bits"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	bootClockKernel32   = windows.NewLazySystemDLL("kernel32.dll")
	bootClockKernelBase = windows.NewLazySystemDLL("kernelbase.dll")
	// QueryInterruptTimePrecise живёт в KernelBase.dll, а не в kernel32.dll: из kernel32
	// процедура не находилась, и часы молча падали на GetTickCount64 с тиком 15,6 мс.
	procQueryInterruptTimePrecise = bootClockKernelBase.NewProc("QueryInterruptTimePrecise")
	procGetTickCount64            = bootClockKernel32.NewProc("GetTickCount64")
)

// bootRelativeNow returns Windows interrupt time since system start, including
// time spent suspended, in the same clock domain as Linux CLOCK_BOOTTIME.
// QueryInterruptTimePrecise is available on supported Windows 10+ systems and
// reports 100-nanosecond units. GetTickCount64 is a lower-resolution fallback.
func bootRelativeNow() time.Duration {
	if err := procQueryInterruptTimePrecise.Find(); err == nil {
		var ticks100ns uint64
		procQueryInterruptTimePrecise.Call(uintptr(unsafe.Pointer(&ticks100ns)))
		return time.Duration(ticks100ns) * 100 * time.Nanosecond
	}
	return getTickCount64Uptime()
}

func getTickCount64Uptime() time.Duration {
	low, high, _ := procGetTickCount64.Call()
	milliseconds := uint64(low)
	if bits.UintSize == 32 {
		milliseconds |= uint64(high) << 32
	}
	return time.Duration(milliseconds) * time.Millisecond
}
