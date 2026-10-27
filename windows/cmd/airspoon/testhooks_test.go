//go:build testhooks

package main

import "testing"

func TestParseFakeDevices(t *testing.T) {
	got := parseFakeDevices("Гостиная=127.0.0.1:7100; Спальня = 10.0.0.5 ;мусор;=1.2.3.4;Пусто=;Плохой=a b")
	if len(got) != 2 {
		t.Fatalf("устройств %d: %+v", len(got), got)
	}
	if got[0].Name != "Гостиная" || got[0].Addr != "127.0.0.1:7100" || got[1].Name != "Спальня" || got[1].Addr != "10.0.0.5:7000" {
		t.Fatalf("разбор: %+v", got)
	}
	if parseFakeDevices("") != nil {
		t.Fatal("пустая строка должна давать пустой список")
	}
}
