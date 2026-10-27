//go:build windows

package airplay

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformAdapters возвращает сведения об адаптерах из GetAdaptersAddresses по
// номеру интерфейса (IPv4-индекс и, если он есть, IPv6-индекс: Go отдаёт в
// net.Interface.Index один из них). Адреса не запрашиваем: с локальным IP
// сопоставляет net.Interfaces. При ошибке — пустая карта, и тогда интерфейс
// считается неопределённым (обычный профиль задержки).
func platformAdapters() map[int]platformAdapterInfo {
	out := map[int]platformAdapterInfo{}
	const flags = windows.GAA_FLAG_SKIP_UNICAST | windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER
	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if err == nil {
			for a := first; a != nil; a = a.Next {
				info := platformAdapterInfo{
					ifType:      a.IfType,
					description: windows.UTF16PtrToString(a.Description),
					linkMbps:    a.TransmitLinkSpeed / 1_000_000,
				}
				if a.IfIndex != 0 {
					out[int(a.IfIndex)] = info
				}
				if a.Ipv6IfIndex != 0 {
					out[int(a.Ipv6IfIndex)] = info
				}
			}
			return out
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			break
		}
	}
	return out
}
