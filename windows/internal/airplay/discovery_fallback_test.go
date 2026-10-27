package airplay

import (
	"context"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFallbackLayerRunsOnlyWhenOthersFindNothing(t *testing.T) {
	zcCalls := 0
	zc := func(devs ...AirPlayDevice) discoveryLayer {
		return discoveryLayer{name: LayerZeroconf, run: func(context.Context) ([]AirPlayDevice, error) { zcCalls++; return devs, nil }}
	}
	found := discoveryLayer{name: LayerWindows, run: func(context.Context) ([]AirPlayDevice, error) {
		return []AirPlayDevice{testTV("6a:be:df:18:3e:82", "192.168.50.59", 4)}, nil
	}}
	empty := discoveryLayer{name: LayerUnicast, run: func(context.Context) ([]AirPlayDevice, error) { return nil, nil }}

	// Основной слой нашёл: сокет 5353 (zeroconf) не открывается.
	rep := runDiscovery(context.Background(), []discoveryLayer{found, empty, zc()}, "", nil)
	if zcCalls != 0 || len(rep.Devices) != 1 {
		t.Fatalf("zeroconf не должен запускаться: calls=%d %+v", zcCalls, rep.Devices)
	}
	if last := rep.Layers[2]; last.Name != LayerZeroconf || last.Note == "" {
		t.Errorf("в отчёте должна быть пометка о пропуске: %+v", last)
	}

	// Основные слои пусты: zeroconf работает как запасной.
	rep = runDiscovery(context.Background(), []discoveryLayer{empty, zc(testTV("11:22:33:44:55:66", "192.168.50.60", 4))}, "", nil)
	if zcCalls != 1 || len(rep.Devices) != 1 || !reflect.DeepEqual(rep.Devices[0].FoundBy, []string{LayerZeroconf}) {
		t.Fatalf("запасной слой: calls=%d %+v", zcCalls, rep.Devices)
	}
}

func TestSavedProbeDoesNotDelayDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-receiver.json")
	if err := SaveLastReceiver(path, LastReceiver{Name: "Телевизор", Address: "192.168.50.59", Port: 7000}); err != nil {
		t.Fatal(err)
	}
	// Слой работает до отмены контекста поиска; проверка /info идёт параллельно,
	// поэтому после отмены нового ожидания нет.
	layer := discoveryLayer{name: LayerWindows, run: func(ctx context.Context) ([]AirPlayDevice, error) {
		<-ctx.Done()
		return nil, nil
	}}
	probe := func(ctx context.Context, r LastReceiver) (*AirPlayDevice, error) {
		time.Sleep(300 * time.Millisecond)
		d := testTV("", r.Address, 0)
		return &d, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	start := time.Now()
	rep := runDiscovery(ctx, []discoveryLayer{layer}, path, probe)
	if el := time.Since(start); el > 1200*time.Millisecond {
		t.Errorf("поиск занял %v: проверка сохранённого идёт после слоёв и ограничена одним таймаутом", el)
	}
	if len(rep.Devices) != 1 {
		t.Errorf("сохранённый телевизор потерян: %+v", rep.Devices)
	}
}

func TestCheckSameReceiverRejectsOtherDevice(t *testing.T) {
	r := LastReceiver{Address: "192.168.50.59", Port: 7000, DeviceID: "6A:BE:DF:18:3E:82"}
	if err := checkSameReceiver(&ReceiverInfo{DeviceID: "6a:be:df:18:3e:82"}, r); err != nil {
		t.Errorf("тот же deviceid в другом регистре: %v", err)
	}
	if err := checkSameReceiver(&ReceiverInfo{DeviceID: "11:22:33:44:55:66"}, r); err == nil {
		t.Error("другой deviceid должен отвергаться")
	}
	if err := checkSameReceiver(&ReceiverInfo{}, r); err != nil {
		t.Errorf("ответ без deviceid не отвергается: %v", err)
	}
	if err := checkSameReceiver(&ReceiverInfo{DeviceID: "11:22:33:44:55:66"}, LastReceiver{}); err != nil {
		t.Errorf("без сохранённого deviceid сверять нечего: %v", err)
	}
}

func TestSelectInterfacesKeepsExternalHyperVSwitchOnlyAsFallback(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	ext := ifaceCandidate{net.Interface{Index: 5, Name: "vEthernet (External)", Flags: up}, "Hyper-V Virtual Ethernet Adapter", true, false}
	def := ifaceCandidate{net.Interface{Index: 6, Name: "vEthernet (Default Switch)", Flags: up}, "Hyper-V Virtual Ethernet Adapter #2", true, false}
	wifi := ifaceCandidate{net.Interface{Index: 7, Name: "Wi-Fi", Flags: up}, "Intel(R) Wi-Fi 6E AX211", true, false}
	vpn := ifaceCandidate{net.Interface{Index: 8, Name: "Tailscale", Flags: up}, "Tailscale Tunnel", true, false}

	// Есть обычный адаптер: Hyper-V отсеивается.
	got, _ := selectMDNSInterfaces([]ifaceCandidate{ext, def, wifi, vpn})
	if len(got) != 1 || got[0].Name != "Wi-Fi" {
		t.Errorf("с физическим адаптером: %v", got)
	}
	// Физическая карта вынесена во внешний коммутатор: vEthernet остаётся, VPN нет.
	got, traffic := selectMDNSInterfaces([]ifaceCandidate{ext, vpn})
	if len(got) != 1 || got[0].Name != "vEthernet (External)" || traffic == 0 {
		t.Errorf("внешний коммутатор Hyper-V: %v traffic=%v", got, traffic)
	}
	// Одни VPN: ничего.
	if got, _ := selectMDNSInterfaces([]ifaceCandidate{vpn}); len(got) != 0 {
		t.Errorf("VPN не должен выбираться: %v", got)
	}
}
