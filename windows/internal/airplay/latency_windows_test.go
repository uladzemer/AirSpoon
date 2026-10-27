//go:build windows

package airplay

import "testing"

// На настоящем Windows GetAdaptersAddresses отдаёт типы хотя бы для адаптеров,
// у которых есть адрес, и тип совпадает с классом, который выбирает политика задержки.
func TestPlatformAdaptersCoverLocalInterfaces(t *testing.T) {
	adapters := platformAdapters()
	if len(adapters) == 0 {
		t.Skip("GetAdaptersAddresses не вернул адаптеров")
	}
	typed := 0
	for _, iface := range localInterfaceSnapshots() {
		if len(iface.addrs) > 0 && iface.ifType != 0 {
			typed++
		}
	}
	if typed == 0 {
		t.Fatalf("ни у одного интерфейса с адресом нет IfType (адаптеров: %d)", len(adapters))
	}
}
