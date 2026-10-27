//go:build windows

package airplay

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// utf16Strings готовит массив PWSTR (как keys/values в DNS_SERVICE_INSTANCE).
func utf16Strings(t *testing.T, items []string) []*uint16 {
	t.Helper()
	out := make([]*uint16, len(items))
	for i, s := range items {
		p, err := windows.UTF16PtrFromString(s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = p
	}
	return out
}

// Разбор DNS_SERVICE_INSTANCE на поддельной памяти: кириллица в имени (UTF-16),
// ключи TXT приводятся к нижнему регистру, адреса копируются.
func TestCopyServiceInstanceFromForgedMemory(t *testing.T) {
	name, _ := windows.UTF16PtrFromString("Телевизор._airplay._tcp.local")
	host, _ := windows.UTF16PtrFromString("android-tv.local")
	keys := utf16Strings(t, []string{"DeviceID", "features", "model", "flags"})
	values := utf16Strings(t, []string{"6a:be:df:18:3e:82", "0x5A7FFEF7,0x40", "AppleTV3,2", "0x44"})
	ip4 := [4]byte{192, 168, 50, 59}
	inst := &dnsServiceInstance{
		InstanceName:  name,
		HostName:      host,
		IP4:           &ip4,
		Port:          7000,
		PropertyCount: uint32(len(keys)),
		Keys:          &keys[0],
		Values:        &values[0],
	}

	res := copyServiceInstance(inst)
	if res.label != "Телевизор._airplay._tcp.local" || res.hostName != "android-tv.local" || res.port != 7000 {
		t.Fatalf("разбор: %+v", res)
	}
	dev := finishNativeResolved(context.Background(), res, "_airplay._tcp")
	if dev == nil {
		t.Fatal("устройство не собралось")
	}
	if dev.Name != "Телевизор" || dev.IP != "192.168.50.59" || dev.Port != 7000 {
		t.Fatalf("устройство: %+v", dev)
	}
	if dev.DeviceID != "6a:be:df:18:3e:82" || dev.Model != "AppleTV3,2" || !dev.SupportsScreen() {
		t.Fatalf("TXT разобран неверно: %+v", dev)
	}
}

// Структуры должны совпадать с windns.h (x64): смещения полей.
func TestNativeStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("раскладка проверяется для 64-разрядной сборки")
	}
	var inst dnsServiceInstance
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"wPort", unsafe.Offsetof(inst.Port), 32},
		{"dwPropertyCount", unsafe.Offsetof(inst.PropertyCount), 40},
		{"keys", unsafe.Offsetof(inst.Keys), 48},
		{"values", unsafe.Offsetof(inst.Values), 56},
		{"dwInterfaceIndex", unsafe.Offsetof(inst.InterfaceIndex), 64},
		{"DNS_RECORD.Data", unsafe.Offsetof(dnsRecord{}.Data), 32},
		{"BROWSE.pBrowseCallback", unsafe.Offsetof(dnsServiceBrowseRequest{}.Callback), 16},
		{"RESOLVE.pQueryContext", unsafe.Offsetof(dnsServiceResolveRequest{}.QueryContext), 24},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("смещение %s = %d, нужно %d", c.name, c.got, c.want)
		}
	}
}

var (
	procDnsServiceConstruct  = dnsapiDLL.NewProc("DnsServiceConstructInstance")
	procDnsServiceRegister   = dnsapiDLL.NewProc("DnsServiceRegister")
	procDnsServiceDeRegister = dnsapiDLL.NewProc("DnsServiceDeRegister")
	registerDone             = make(chan uintptr, 4)
	registerCB               = syscall.NewCallback(func(status uintptr, ctx uintptr, inst *dnsServiceInstance) uintptr {
		select {
		case registerDone <- status:
		default:
		}
		return 0
	})
)

type dnsServiceRegisterRequest struct {
	Version        uint32
	InterfaceIndex uint32
	Instance       *dnsServiceInstance
	Callback       uintptr
	QueryContext   uintptr
	Credentials    uintptr
	UnicastEnabled int32
}

func firstLANIPv4(t *testing.T) net.IP {
	t.Helper()
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ip := ipFromAddr(a).To4(); ip != nil && !ip.IsLinkLocalUnicast() {
				return ip
			}
		}
	}
	return nil
}

// Сквозной тест нативного слоя: регистрируем сервис системным mDNS Windows
// (DnsServiceRegister) и находим его же через nativeBrowse.
func TestNativeBrowseFindsRegisteredService(t *testing.T) {
	if testing.Short() {
		t.Skip("тест занимает несколько секунд")
	}
	for _, p := range []*windows.LazyProc{procDnsServiceConstruct, procDnsServiceRegister, procDnsServiceDeRegister} {
		if err := p.Find(); err != nil {
			t.Skipf("DnsServiceRegister недоступен: %v", err)
		}
	}
	ip := firstLANIPv4(t)
	if ip == nil {
		t.Skip("нет сетевого интерфейса с IPv4")
	}
	const svc = "_airspoontest._tcp"
	suffix := fmt.Sprintf("%d", os.Getpid())
	label := "Тест AirSpoon " + suffix
	instName, _ := windows.UTF16PtrFromString(label + "." + svc + ".local")
	hn, _ := os.Hostname()
	hostName, _ := windows.UTF16PtrFromString(hn + ".local")
	keys := utf16Strings(t, []string{"deviceid", "features", "model"})
	values := utf16Strings(t, []string{"aa:bb:cc:dd:ee:ff", "0x5A7FFEF7,0x40", "AppleTV3,2"})
	ip4 := [4]byte{ip[0], ip[1], ip[2], ip[3]}

	r, _, _ := procDnsServiceConstruct.Call(
		uintptr(unsafe.Pointer(instName)), uintptr(unsafe.Pointer(hostName)),
		uintptr(unsafe.Pointer(&ip4)), 0,
		7123, 0, 0,
		uintptr(len(keys)), uintptr(unsafe.Pointer(&keys[0])), uintptr(unsafe.Pointer(&values[0])))
	if r == 0 {
		t.Skip("DnsServiceConstructInstance не создал экземпляр")
	}
	inst := *(**dnsServiceInstance)(unsafe.Pointer(&r))
	defer procDnsServiceFreeInst.Call(r)

	req := &dnsServiceRegisterRequest{Version: dnsQueryVersion1, Instance: inst, Callback: registerCB}
	var cancel dnsServiceCancel
	code, _, _ := procDnsServiceRegister.Call(uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&cancel)))
	if code != dnsRequestPending && code != 0 {
		t.Skipf("DnsServiceRegister: код %d (регистрация недоступна в этом окружении)", uint32(code))
	}
	defer procDnsServiceDeRegister.Call(uintptr(unsafe.Pointer(req)), 0)
	select {
	case st := <-registerDone:
		if st != 0 {
			t.Skipf("регистрация завершилась кодом %d", uint32(st))
		}
	case <-time.After(5 * time.Second):
		t.Skip("регистрация не завершилась за 5 с")
	}

	ctx, stop := context.WithTimeout(context.Background(), 6*time.Second)
	defer stop()
	devs, err := nativeBrowse(ctx, svc)
	if err != nil {
		t.Fatalf("nativeBrowse: %v", err)
	}
	var found *AirPlayDevice
	for i := range devs {
		if strings.EqualFold(devs[i].Name, "Тест AirSpoon "+suffix) {
			found = &devs[i]
		}
	}
	if found == nil {
		t.Fatalf("зарегистрированный сервис не найден; найдено: %+v", devs)
	}
	if found.Port != 7123 || net.ParseIP(found.IP) == nil || found.DeviceID != "aa:bb:cc:dd:ee:ff" || found.Model != "AppleTV3,2" {
		t.Fatalf("сервис разобран неверно: %+v", *found)
	}
	if !strings.EqualFold(found.Name, "Тест AirSpoon "+suffix) {
		t.Fatalf("имя с кириллицей: %q", found.Name)
	}
}

// Отмена по контексту возвращает управление быстро, даже если сервисов нет.
func TestNativeBrowseStopsOnCancel(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := nativeBrowse(ctx, "_airspoon-nothing._tcp"); err != nil {
		t.Skipf("системный mDNS недоступен: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("nativeBrowse завис после отмены: %v", d)
	}
}

// Разбор списка записей Browse на поддельной памяти: PTR, SRV, TXT, A.
func TestParseBrowseRecords(t *testing.T) {
	type header struct {
		Next       *dnsRecord
		Name       *uint16
		Type       uint16
		DataLength uint16
		Flags      uint32
		TTL        uint32
		Reserved   uint32
	}
	type ptrRec struct {
		header
		Host *uint16
	}
	type srvRec struct {
		header
		dnsSRVData
	}
	type txtRec struct {
		header
		Count   uint32
		Strings [3]*uint16
	}
	type aRec struct {
		header
		IP [4]byte
	}
	u := func(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }
	inst := "Телевизор._airplay._tcp.local"

	a := &aRec{header: header{Name: u("android-tv.local"), Type: dnsTypeA, TTL: 120}, IP: [4]byte{192, 168, 50, 59}}
	txt := &txtRec{header: header{Name: u(inst), Type: dnsTypeTXT, TTL: 4500}, Count: 3,
		Strings: [3]*uint16{u("deviceid=6a:be:df:18:3e:82"), u("features=0x5A7FFEF7,0x40"), u("model=AppleTV3,2")}}
	txt.Next = (*dnsRecord)(unsafe.Pointer(a))
	srv := &srvRec{header: header{Name: u(inst), Type: dnsTypeSRV, TTL: 120}, dnsSRVData: dnsSRVData{Target: u("android-tv.local"), Port: 7000}}
	srv.Next = (*dnsRecord)(unsafe.Pointer(txt))
	bye := &ptrRec{header: header{Name: u("_airplay._tcp.local"), Type: dnsTypePTR, TTL: 0}, Host: u("Ушёл._airplay._tcp.local")}
	bye.Next = (*dnsRecord)(unsafe.Pointer(srv))
	ptr := &ptrRec{header: header{Name: u("_airplay._tcp.local"), Type: dnsTypePTR, TTL: 4500}, Host: u(inst)}
	ptr.Next = (*dnsRecord)(unsafe.Pointer(bye))

	b := parseBrowseRecords((*dnsRecord)(unsafe.Pointer(ptr)))
	if len(b.ptrs) != 1 || b.ptrs[0] != inst {
		t.Fatalf("PTR (запись с TTL 0 — прощание, пропускается): %v", b.ptrs)
	}
	key := strings.ToLower(inst)
	if s := b.srv[key]; s.target != "android-tv.local" || s.port != 7000 {
		t.Fatalf("SRV: %+v", b.srv)
	}
	if len(b.txt[key]) != 3 || b.txt[key][2] != "model=AppleTV3,2" {
		t.Fatalf("TXT: %v", b.txt)
	}
	if ips := b.addrs["android-tv.local"]; len(ips) != 1 || ips[0].String() != "192.168.50.59" {
		t.Fatalf("A: %v", b.addrs)
	}

	// Состояние собирает из этого устройство без Resolve.
	st := newNativeState()
	if fresh := st.addBatch(b); len(fresh) != 1 {
		t.Fatalf("новые имена: %v", fresh)
	}
	devs := st.devices(context.Background(), "_airplay._tcp")
	if len(devs) != 1 || devs[0].Name != "Телевизор" || devs[0].IP != "192.168.50.59" || devs[0].Port != 7000 ||
		devs[0].DeviceID != "6a:be:df:18:3e:82" || !devs[0].SupportsScreen() {
		t.Fatalf("устройство из Browse: %+v", devs)
	}
}
