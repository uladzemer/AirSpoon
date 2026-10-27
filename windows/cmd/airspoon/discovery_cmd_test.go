package main

import (
	"path/filepath"
	"strings"
	"testing"

	"airspoon/internal/airplay"
)

// После успешного старта показа телевизор запоминается (имя и deviceid — из /info).
func TestRememberReceiverSavesAfterStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-receiver.json")
	rememberReceiver(path, "192.168.50.59", 7000,
		&airplay.AirPlayDevice{Name: "из mDNS", DeviceID: "aa"},
		&airplay.ReceiverInfo{Name: "Телевизор", DeviceID: "6a:be:df:18:3e:82"})
	got, ok := airplay.LoadLastReceiver(path)
	want := airplay.LastReceiver{Name: "Телевизор", Address: "192.168.50.59", Port: 7000, DeviceID: "6a:be:df:18:3e:82"}
	if !ok || got != want {
		t.Fatalf("сохранено %+v ok=%v, нужно %+v", got, ok, want)
	}
	// Без пути (тесты, консоль без профиля) ничего не пишется и не падает.
	rememberReceiver("", "1.2.3.4", 7000, nil, nil)
}

func TestPrintDevicesShowsDiscoveryLayers(t *testing.T) {
	var sb strings.Builder
	printDevices(&sb, []airplay.AirPlayDevice{{
		Name: "Телевизор", IP: "192.168.50.59", Port: 7000, Features: 1 << 7,
		FoundBy: []string{airplay.LayerWindows, airplay.LayerUnicast},
	}})
	if !strings.Contains(sb.String(), "найден=windows+unicast") {
		t.Fatalf("в выводе list нет слоёв: %s", sb.String())
	}
}

func TestDiscovererLogsOnlyChanges(t *testing.T) {
	d := &mdnsDiscoverer{}
	rep := &airplay.DiscoveryReport{Layers: []airplay.LayerReport{{Name: airplay.LayerWindows, Found: 1}}}
	d.logIfChanged(rep)
	first := d.lastSummary
	d.logIfChanged(rep)
	if d.lastSummary != first || first == "" {
		t.Fatalf("сводка: %q", d.lastSummary)
	}
}
