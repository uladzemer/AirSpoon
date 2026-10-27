//go:build windows

package airplay

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// adapterDescriptions возвращает описания драйверов сетевых адаптеров по номеру
// интерфейса («Hyper-V Virtual Ethernet Adapter»). При ошибке — пустая карта.
func adapterDescriptions() map[int]string {
	out := map[int]string{}
	const flags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER
	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if err == nil {
			for a := first; a != nil; a = a.Next {
				out[int(a.IfIndex)] = windows.UTF16PtrToString(a.Description)
			}
			return out
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			break
		}
	}
	return out
}
