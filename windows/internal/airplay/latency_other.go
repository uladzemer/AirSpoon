//go:build !windows

package airplay

// platformAdapters: вне Windows IfType недоступен, Wi-Fi определяется по
// /sys/class/net (см. networkInterfaceIsWireless).
func platformAdapters() map[int]platformAdapterInfo { return nil }
