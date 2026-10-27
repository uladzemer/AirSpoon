package airplay

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testTV(id, ip string, txtKeys int) AirPlayDevice {
	txt := map[string]string{}
	for _, k := range []string{"deviceid", "features", "model", "srcvers"}[:txtKeys] {
		txt[k] = "x"
	}
	return AirPlayDevice{Name: "Телевизор", IP: ip, Port: 7000, DeviceID: id, Features: FeatureScreen, RawTXT: txt}
}

func TestMergeDevicesDedupesByDeviceIDAndAddress(t *testing.T) {
	a := testTV("6A:BE:DF:18:3E:82", "192.168.50.59", 2)
	a.FoundBy = []string{LayerWindows}
	b := testTV("6a:be:df:18:3e:82", "10.0.0.5", 4) // тот же deviceid, другой адрес (второй интерфейс)
	b.FoundBy = []string{LayerUnicast}
	c := testTV("", "192.168.50.59", 0) // без deviceid, тот же адрес и порт
	c.FoundBy = []string{LayerZeroconf}
	other := testTV("11:22:33:44:55:66", "192.168.50.60", 4)
	other.FoundBy = []string{LayerZeroconf}

	got := mergeDevices([]AirPlayDevice{a, b, c, other})
	if len(got) != 2 {
		t.Fatalf("после объединения %d записей, нужно 2: %+v", len(got), got)
	}
	// Остаётся запись с большим числом TXT-ключей; слои объединяются.
	if got[0].IP != "10.0.0.5" || len(got[0].RawTXT) != 4 {
		t.Errorf("должна остаться запись с полным TXT: %+v", got[0])
	}
	if !reflect.DeepEqual(got[0].FoundBy, []string{LayerWindows, LayerUnicast, LayerZeroconf}) {
		t.Errorf("слои: %v", got[0].FoundBy)
	}
	if got[1].DeviceID != "11:22:33:44:55:66" {
		t.Errorf("второй телевизор потерян: %+v", got[1])
	}
}

func TestRunDiscoveryCombinesLayersAndReportsErrors(t *testing.T) {
	win := testTV("6a:be:df:18:3e:82", "192.168.50.59", 4)
	uni := testTV("6a:be:df:18:3e:82", "192.168.50.59", 4)
	layers := []discoveryLayer{
		{LayerWindows, func(context.Context) ([]AirPlayDevice, error) { return []AirPlayDevice{win}, nil }},
		{LayerUnicast, func(context.Context) ([]AirPlayDevice, error) { return []AirPlayDevice{uni}, nil }},
		{"extra", func(context.Context) ([]AirPlayDevice, error) { return nil, errors.New("порт 5353 занят") }},
	}
	rep := runDiscovery(context.Background(), layers, "", nil)
	if len(rep.Devices) != 1 || !reflect.DeepEqual(rep.Devices[0].FoundBy, []string{LayerWindows, LayerUnicast}) {
		t.Fatalf("устройства: %+v", rep.Devices)
	}
	if rep.Layers[0].Found != 1 || rep.Layers[1].Found != 1 || rep.Layers[2].Err == nil {
		t.Fatalf("отчёт по слоям: %+v", rep.Layers)
	}
	sum := rep.Summary()
	for _, want := range []string{"найдено 1", "windows=1", "unicast=1", "extra=ошибка (порт 5353 занят)"} {
		if !strings.Contains(sum, want) {
			t.Errorf("в сводке нет %q: %s", want, sum)
		}
	}
}

func TestRunDiscoveryLayerPanicIsContained(t *testing.T) {
	layers := []discoveryLayer{
		{LayerWindows, func(context.Context) ([]AirPlayDevice, error) { panic("колбэк упал") }},
		{LayerUnicast, func(context.Context) ([]AirPlayDevice, error) {
			return []AirPlayDevice{testTV("a", "192.168.1.2", 4)}, nil
		}},
	}
	rep := runDiscovery(context.Background(), layers, "", nil)
	if len(rep.Devices) != 1 || rep.Layers[0].Err == nil {
		t.Fatalf("паника слоя должна стать ошибкой слоя: %+v", rep)
	}
}

func TestRunDiscoverySavedReceiver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-receiver.json")
	if err := SaveLastReceiver(path, LastReceiver{Name: "Телевизор", Address: "192.168.50.59", Port: 7000, DeviceID: "6a:be:df:18:3e:82"}); err != nil {
		t.Fatal(err)
	}
	none := []discoveryLayer{{LayerWindows, func(context.Context) ([]AirPlayDevice, error) { return nil, nil }}}

	// Телевизор не найден слоями, но отвечает на /info: появляется в списке.
	calls := 0
	probe := func(_ context.Context, r LastReceiver) (*AirPlayDevice, error) {
		calls++
		d := testTV(r.DeviceID, r.Address, 0)
		return &d, nil
	}
	rep := runDiscovery(context.Background(), none, path, probe)
	if calls != 1 || len(rep.Devices) != 1 || !reflect.DeepEqual(rep.Devices[0].FoundBy, []string{LayerSaved}) {
		t.Fatalf("calls=%d devices=%+v", calls, rep.Devices)
	}

	// Телевизор уже найден слоем: проверка вообще не запускается (TCP-соединение
	// будило бы приставку), второй записи нет и слой saved в FoundBy не попадает.
	calls = 0
	found := []discoveryLayer{{LayerWindows, func(context.Context) ([]AirPlayDevice, error) {
		return []AirPlayDevice{testTV("6A:BE:DF:18:3E:82", "192.168.50.59", 4)}, nil
	}}}
	rep = runDiscovery(context.Background(), found, path, probe)
	if calls != 0 {
		t.Fatalf("найденный слоями телевизор не должен проверяться: calls=%d", calls)
	}
	if len(rep.Devices) != 1 || !reflect.DeepEqual(rep.Devices[0].FoundBy, []string{LayerWindows}) {
		t.Fatalf("дубль от saved: calls=%d devices=%+v", calls, rep.Devices)
	}

	// Не отвечает: ошибка в отчёте, списка нет.
	bad := func(context.Context, LastReceiver) (*AirPlayDevice, error) { return nil, errors.New("таймаут") }
	rep = runDiscovery(context.Background(), none, path, bad)
	if len(rep.Devices) != 0 || rep.Layers[len(rep.Layers)-1].Err == nil {
		t.Fatalf("недоступный сохранённый телевизор: %+v", rep)
	}

	// Файла нет: слой пропускается без ошибки.
	rep = runDiscovery(context.Background(), none, filepath.Join(t.TempDir(), "нет.json"), probe)
	if last := rep.Layers[len(rep.Layers)-1]; last.Err != nil || last.Found != 0 {
		t.Fatalf("без файла: %+v", last)
	}
}

func TestLastReceiverRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "last-receiver.json")
	if _, ok := LoadLastReceiver(path); ok {
		t.Fatal("несуществующий файл не должен читаться")
	}
	want := LastReceiver{Name: "Телевизор", Address: "192.168.50.59", Port: 7000, DeviceID: "6a:be:df:18:3e:82"}
	if err := SaveLastReceiver(path, want); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadLastReceiver(path); !ok || got != want {
		t.Fatalf("прочитано %+v ok=%v", got, ok)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("временный файл не убран")
	}
	// Повреждённый и неполный файлы игнорируются.
	for _, content := range []string{"{не json", `{"address":"","port":7000}`, `{"address":"1.2.3.4","port":0}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := LoadLastReceiver(path); ok {
			t.Errorf("%q не должен приниматься", content)
		}
	}
}

func TestDeviceFromInfo(t *testing.T) {
	info := &ReceiverInfo{Name: "Телевизор", Model: "AppleTV3,2", DeviceID: "6a:be:df:18:3e:82", Features: 0x4005a7ffef7,
		SourceVersion: "220.68", PK: plistData{0xab, 0xcd}}
	dev := deviceFromInfo(info, LastReceiver{Address: "192.168.50.59", Port: 7000})
	if dev.Name != "Телевизор" || dev.IP != "192.168.50.59" || dev.Port != 7000 || dev.PK != "abcd" || !dev.SupportsScreen() {
		t.Fatalf("%+v", dev)
	}
}

func TestDeviceFromServiceAndInstanceLabel(t *testing.T) {
	ips := []net.IP{net.ParseIP("fe80::1"), net.ParseIP("fd00::5"), net.IPv4(192, 168, 1, 7)}
	dev := deviceFromService("Телевизор", ips, 7000, map[string]string{"DeviceID": "aa", "features": "0x5A7FFEF7,0x40"})
	if dev == nil || dev.IP != "192.168.1.7" || dev.DeviceID != "aa" || !dev.SupportsScreen() {
		t.Fatalf("%+v", dev)
	}
	if deviceFromService("x", []net.IP{net.ParseIP("fe80::1"), net.IPv4zero}, 7000, nil) != nil {
		t.Error("без пригодного адреса устройства быть не должно")
	}
	if deviceFromService("x", ips, 0, nil) != nil {
		t.Error("без порта устройства быть не должно")
	}
	for in, want := range map[string]string{
		"Телевизор._airplay._tcp.local":        "Телевизор",
		"Living Room 2.0._AirPlay._tcp.local.": "Living Room 2.0",
		"Просто имя":                           "Просто имя",
	} {
		if got := instanceLabel(in, "_airplay._tcp"); got != want {
			t.Errorf("instanceLabel(%q) = %q, нужно %q", in, got, want)
		}
	}
	if got := parseTXT([]string{"", "a=b", "флаг"}); len(got) != 2 || got["a"] != "b" {
		t.Errorf("parseTXT пропускает пустые строки: %v", got)
	}
}

func TestVirtualAdaptersAreNotLAN(t *testing.T) {
	virtual := []string{
		"vEthernet (Default Switch)", "vEthernet (WSL (Hyper-V firewall))", "VMware Network Adapter VMnet8",
		"VirtualBox Host-Only Network", "Parsec Virtual Ethernet", "Tailscale", "ZeroTier One [8056c2e21c000001]",
		"Loopback Pseudo-Interface 1", "Local Area Connection* 10",
	}
	for _, name := range virtual {
		if !isNonLANInterfaceName(name) {
			t.Errorf("%q должен считаться виртуальным", name)
		}
	}
	for _, name := range []string{"Ethernet", "Ethernet 2", "Wi-Fi", "Wi-Fi 2", "Беспроводная сеть", "Local Area Connection"} {
		if isNonLANInterfaceName(name) {
			t.Errorf("%q должен считаться обычным адаптером", name)
		}
	}
	for _, desc := range []string{"Hyper-V Virtual Ethernet Adapter", "VMware Virtual Ethernet Adapter for VMnet1",
		"TAP-Windows Adapter V9", "Wintun Userspace Tunnel", "Microsoft Wi-Fi Direct Virtual Adapter #2"} {
		if !isNonLANAdapterDescription(desc) {
			t.Errorf("описание %q должно отсеиваться", desc)
		}
	}
	for _, desc := range []string{"Intel(R) Ethernet Connection (7) I219-V", "Intel(R) Wi-Fi 6E AX211 160MHz", "Realtek PCIe GbE Family Controller"} {
		if isNonLANAdapterDescription(desc) {
			t.Errorf("описание %q отсеиваться не должно", desc)
		}
	}
}

func TestCachedProbeAsksReceiverOncePerTTL(t *testing.T) {
	calls := 0
	fail := true
	inner := func(_ context.Context, r LastReceiver) (*AirPlayDevice, error) {
		calls++
		if fail {
			return nil, errors.New("таймаут")
		}
		d := testTV(r.DeviceID, r.Address, 0)
		return &d, nil
	}
	p := cachedProbe(inner, 50*time.Millisecond)
	r := LastReceiver{Address: "192.168.50.59", Port: 7000, DeviceID: "aa"}
	for i := 0; i < 5; i++ {
		if _, err := p(context.Background(), r); err == nil {
			t.Fatal("ожидалась ошибка из кэша")
		}
	}
	if calls != 1 {
		t.Fatalf("неудачная проверка должна кэшироваться: calls=%d", calls)
	}
	time.Sleep(80 * time.Millisecond)
	fail = false
	for i := 0; i < 5; i++ {
		d, err := p(context.Background(), r)
		if err != nil || d == nil {
			t.Fatalf("после TTL ожидался успех: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("успех тоже кэшируется: calls=%d", calls)
	}
}

func TestReportErrOnlyWhenAllLayersFailed(t *testing.T) {
	boom := func(context.Context) ([]AirPlayDevice, error) { return nil, errors.New("сбой") }
	ok := func(context.Context) ([]AirPlayDevice, error) { return nil, nil }
	rep := runDiscovery(context.Background(), []discoveryLayer{{LayerWindows, boom}, {LayerUnicast, boom}}, "", nil)
	if rep.Err() == nil {
		t.Fatal("все слои упали: ожидалась ошибка")
	}
	rep = runDiscovery(context.Background(), []discoveryLayer{{LayerWindows, boom}, {LayerUnicast, ok}}, "", nil)
	if err := rep.Err(); err != nil {
		t.Fatalf("один слой отработал: ошибки быть не должно, %v", err)
	}
}
