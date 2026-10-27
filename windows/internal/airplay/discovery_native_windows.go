//go:build windows

package airplay

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Нативный поиск Windows: DnsServiceBrowse + DnsServiceResolve из dnsapi.dll
// (Windows 10+). Трафик mDNS ведёт системная служба Dnscache, у которой есть
// встроенное правило брандмауэра, поэтому окна «Разрешить доступ» для нашего
// exe нет. Раскладка структур — по windns.h (Windows SDK; сверено с
// include/windns.h из Wine/mingw-w64). Колбэки приходят из потоков Windows:
// колбэки создаются один раз на процесс (число NewCallback ограничено), данные из
// них копируются в Go-память сразу, а память Windows освобождается там же.
//
// Windows присылает в колбэке Browse не только PTR, но и SRV, TXT и адреса из
// того же ответа; из них собирается устройство. DnsServiceResolve запускается
// дополнительно для каждого имени и закрывает случай, когда в ответе нет
// адреса или TXT.

const (
	dnsRequestPending = 9506 // DNS_REQUEST_PENDING: запрос принят, ответ придёт в колбэк
	dnsQueryVersion1  = 1    // DNS_QUERY_REQUEST_VERSION1: колбэк со статусом и списком записей
	dnsFreeRecordList = 1    // DnsFreeRecordList для DnsRecordListFree

	dnsTypeA    = 1
	dnsTypePTR  = 12
	dnsTypeTXT  = 16
	dnsTypeAAAA = 28
	dnsTypeSRV  = 33

	nativeQueueCap = 64
)

var (
	dnsapiDLL                = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsServiceBrowse     = dnsapiDLL.NewProc("DnsServiceBrowse")
	procDnsServiceBrowseCanc = dnsapiDLL.NewProc("DnsServiceBrowseCancel")
	procDnsServiceResolve    = dnsapiDLL.NewProc("DnsServiceResolve")
	procDnsServiceResolveCan = dnsapiDLL.NewProc("DnsServiceResolveCancel")
	procDnsServiceFreeInst   = dnsapiDLL.NewProc("DnsServiceFreeInstance")
	procDnsRecordListFree    = dnsapiDLL.NewProc("DnsRecordListFree")
)

// dnsServiceBrowseRequest — DNS_SERVICE_BROWSE_REQUEST (версия 1).
type dnsServiceBrowseRequest struct {
	Version        uint32
	InterfaceIndex uint32
	QueryName      *uint16
	Callback       uintptr
	QueryContext   uintptr
}

// dnsServiceResolveRequest — DNS_SERVICE_RESOLVE_REQUEST.
type dnsServiceResolveRequest struct {
	Version        uint32
	InterfaceIndex uint32
	QueryName      *uint16
	Callback       uintptr
	QueryContext   uintptr
}

// dnsServiceCancel — DNS_SERVICE_CANCEL. Должен жить, пока запрос не отменён.
type dnsServiceCancel struct {
	reserved uintptr
}

// dnsServiceInstance — DNS_SERVICE_INSTANCE.
type dnsServiceInstance struct {
	InstanceName   *uint16
	HostName       *uint16
	IP4            *[4]byte  // IP4_ADDRESS: октеты в сетевом порядке
	IP6            *[16]byte // IP6_ADDRESS
	Port           uint16
	Priority       uint16
	Weight         uint16
	PropertyCount  uint32
	Keys           **uint16
	Values         **uint16
	InterfaceIndex uint32
}

// dnsRecord — DNS_RECORDW: заголовок и начало объединения Data (на 64-разрядной
// системе Data начинается со смещения 32). Для SRV, TXT, A и AAAA данные читаются
// по указателю на Data.
type dnsRecord struct {
	Next       *dnsRecord
	Name       *uint16
	Type       uint16
	DataLength uint16
	Flags      uint32
	TTL        uint32
	Reserved   uint32
	Data       [2]uintptr
}

// dnsSRVData — DNS_SRV_DATAW.
type dnsSRVData struct {
	Target   *uint16
	Priority uint16
	Weight   uint16
	Port     uint16
}

// dnsTXTData — DNS_TXT_DATAW: счётчик и массив указателей на строки.
type dnsTXTData struct {
	Count   uint32
	Strings [1]*uint16
}

// browseBatch — записи одного ответа Browse, скопированные в Go-память.
type browseBatch struct {
	ptrs  []string            // имена экземпляров (TTL > 0)
	srv   map[string]dnsSRVGo // по имени экземпляра в нижнем регистре
	txt   map[string][]string
	addrs map[string][]net.IP // по имени хоста в нижнем регистре
}

type dnsSRVGo struct {
	target string
	port   int
}

// parseBrowseRecords копирует список записей в Go-память; саму память не освобождает.
func parseBrowseRecords(rec *dnsRecord) browseBatch {
	b := browseBatch{srv: map[string]dnsSRVGo{}, txt: map[string][]string{}, addrs: map[string][]net.IP{}}
	for r := rec; r != nil; r = r.Next {
		if r.Name == nil {
			continue
		}
		name := strings.ToLower(windows.UTF16PtrToString(r.Name))
		data := unsafe.Pointer(&r.Data)
		switch r.Type {
		case dnsTypePTR:
			// TTL 0 — прощание (устройство пропало).
			if host := *(**uint16)(data); host != nil && r.TTL != 0 {
				b.ptrs = append(b.ptrs, windows.UTF16PtrToString(host))
			}
		case dnsTypeSRV:
			d := (*dnsSRVData)(data)
			if d.Target != nil {
				b.srv[name] = dnsSRVGo{target: windows.UTF16PtrToString(d.Target), port: int(d.Port)}
			}
		case dnsTypeTXT:
			d := (*dnsTXTData)(data)
			n := int(d.Count)
			b.txt[name] = []string{}
			if n > 0 && n < 1024 {
				for _, s := range unsafe.Slice(&d.Strings[0], n) {
					if s != nil {
						b.txt[name] = append(b.txt[name], windows.UTF16PtrToString(s))
					}
				}
			}
		case dnsTypeA:
			ip := *(*[4]byte)(data)
			b.addrs[name] = append(b.addrs[name], net.IP(append([]byte(nil), ip[:]...)))
		case dnsTypeAAAA:
			ip := *(*[16]byte)(data)
			b.addrs[name] = append(b.addrs[name], net.IP(append([]byte(nil), ip[:]...)))
		}
	}
	return b
}

// nativeSession — один поиск; колбэки находят его по числу из pQueryContext.
type nativeSession struct {
	batches chan browseBatch
	results chan nativeResolved
	// keep держит структуры запросов (req, cancel, имена) для Windows: после
	// Cancel система ещё может обращаться к ним, поэтому они живут вместе с
	// сессией и освобождаются вместе с ней по отложенному таймеру.
	keep []any
}

// nativeResolved — ответ DnsServiceResolve в Go-памяти.
type nativeResolved struct {
	label    string // полное имя экземпляра: «Телевизор._airplay._tcp.local»
	hostName string
	ips      []net.IP
	port     int
	txt      map[string]string
}

var (
	nativeSessions  sync.Map // uintptr -> *nativeSession
	nativeNextID    atomic.Uintptr
	nativeCBOnce    sync.Once
	nativeBrowseCB  uintptr
	nativeResolveCB uintptr
)

func initNativeCallbacks() {
	nativeCBOnce.Do(func() {
		nativeBrowseCB = syscall.NewCallback(nativeBrowseCallback)
		nativeResolveCB = syscall.NewCallback(nativeResolveCallback)
	})
}

// nativeBrowseCallback: status, pQueryContext, PDNS_RECORD.
func nativeBrowseCallback(status uintptr, queryCtx uintptr, rec *dnsRecord) (ret uintptr) {
	// Колбэк приходит на чужом потоке Windows: паника здесь уронила бы процесс.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("поиск (windows): авария в колбэке Browse: %v", r)
			ret = 0
		}
	}()
	if rec == nil {
		return 0
	}
	batch := parseBrowseRecords(rec)
	_, _, _ = procDnsRecordListFree.Call(uintptr(unsafe.Pointer(rec)), dnsFreeRecordList)
	if v, ok := nativeSessions.Load(queryCtx); ok {
		select {
		case v.(*nativeSession).batches <- batch:
		default: // переполнение: те же записи придут с повторным ответом
		}
	}
	return 0
}

// nativeResolveCallback: status, pQueryContext, PDNS_SERVICE_INSTANCE.
func nativeResolveCallback(status uintptr, queryCtx uintptr, inst *dnsServiceInstance) (ret uintptr) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("поиск (windows): авария в колбэке Resolve: %v", r)
			ret = 0
		}
	}()
	if inst == nil {
		return 0
	}
	res := copyServiceInstance(inst)
	_, _, _ = procDnsServiceFreeInst.Call(uintptr(unsafe.Pointer(inst)))
	if status != 0 {
		return 0
	}
	if v, ok := nativeSessions.Load(queryCtx); ok {
		select {
		case v.(*nativeSession).results <- res:
		default:
		}
	}
	return 0
}

// copyServiceInstance копирует всё нужное из памяти Windows в Go-значения.
func copyServiceInstance(inst *dnsServiceInstance) nativeResolved {
	res := nativeResolved{
		label:    windows.UTF16PtrToString(inst.InstanceName),
		hostName: windows.UTF16PtrToString(inst.HostName),
		port:     int(inst.Port),
		txt:      map[string]string{},
	}
	n := int(inst.PropertyCount)
	if n > 0 && n < 1024 && inst.Keys != nil {
		keys := unsafe.Slice(inst.Keys, n)
		var values []*uint16
		if inst.Values != nil {
			values = unsafe.Slice(inst.Values, n)
		}
		for i := 0; i < n; i++ {
			if keys[i] == nil {
				continue
			}
			k := strings.ToLower(windows.UTF16PtrToString(keys[i]))
			v := ""
			if i < len(values) && values[i] != nil {
				v = windows.UTF16PtrToString(values[i])
			}
			res.txt[k] = v
		}
	}
	if inst.IP4 != nil {
		res.ips = append(res.ips, net.IP(append([]byte(nil), inst.IP4[:]...)))
	}
	if inst.IP6 != nil {
		res.ips = append(res.ips, net.IP(append([]byte(nil), inst.IP6[:]...)))
	}
	return res
}

// nativeState копит всё, что узнали о каждом экземпляре из обоих источников.
type nativeState struct {
	names    map[string]string // нижний регистр -> имя как пришло в PTR
	srv      map[string]dnsSRVGo
	txt      map[string][]string
	addrs    map[string][]net.IP
	resolved map[string]nativeResolved
}

func newNativeState() *nativeState {
	return &nativeState{
		names:    map[string]string{},
		srv:      map[string]dnsSRVGo{},
		txt:      map[string][]string{},
		addrs:    map[string][]net.IP{},
		resolved: map[string]nativeResolved{},
	}
}

// addBatch запоминает записи Browse и возвращает имена, которых раньше не было.
func (s *nativeState) addBatch(b browseBatch) (fresh []string) {
	for _, name := range b.ptrs {
		key := strings.ToLower(name)
		if _, ok := s.names[key]; !ok {
			s.names[key] = name
			fresh = append(fresh, name)
		}
	}
	for k, v := range b.srv {
		s.srv[k] = v
	}
	for k, v := range b.txt {
		s.txt[k] = v
	}
	for k, v := range b.addrs {
		for _, ip := range v {
			dup := false
			for _, have := range s.addrs[k] {
				dup = dup || have.Equal(ip)
			}
			if !dup {
				s.addrs[k] = append(s.addrs[k], ip)
			}
		}
	}
	return fresh
}

// devices собирает устройства; адреса, не пришедшие в ответе, ищутся по имени хоста.
func (s *nativeState) devices(ctx context.Context, serviceType string) []AirPlayDevice {
	var out []AirPlayDevice
	for key, name := range s.names {
		res, haveRes := s.resolved[key]
		if !haveRes {
			res = nativeResolved{txt: map[string]string{}}
		}
		res.label = name // регистр имени — из PTR (в SRV/TXT Windows даёт нижний)
		if srv, ok := s.srv[key]; ok {
			if res.hostName == "" {
				res.hostName = srv.target
			}
			if res.port == 0 {
				res.port = srv.port
			}
		}
		// Resolve часто отдаёт только IPv6, а IPv4 приходит в записях Browse: берём оба.
		res.ips = append(append([]net.IP(nil), res.ips...), s.addrs[strings.ToLower(res.hostName)]...)
		if len(res.txt) == 0 {
			for k, v := range parseTXT(s.txt[key]) {
				res.txt[strings.ToLower(k)] = v
			}
		}
		if res.port == 0 {
			continue
		}
		if dev := finishNativeResolved(ctx, res, serviceType); dev != nil {
			out = append(out, *dev)
		}
	}
	// Экземпляры, о которых известен только Resolve (PTR не пришёл).
	for key, res := range s.resolved {
		if _, ok := s.names[key]; ok {
			continue
		}
		if dev := finishNativeResolved(ctx, res, serviceType); dev != nil {
			out = append(out, *dev)
		}
	}
	return out
}

// nativeBrowse ищет сервисы serviceType ("_airplay._tcp") системным mDNS до
// отмены ctx. Список возвращается после отмены и освобождения всех запросов.
func nativeBrowse(ctx context.Context, serviceType string) ([]AirPlayDevice, error) {
	for _, p := range []*windows.LazyProc{procDnsServiceBrowse, procDnsServiceBrowseCanc, procDnsServiceResolve,
		procDnsServiceResolveCan, procDnsServiceFreeInst, procDnsRecordListFree} {
		if err := p.Find(); err != nil {
			return nil, fmt.Errorf("системный mDNS недоступен: %w", err)
		}
	}
	initNativeCallbacks()

	id := nativeNextID.Add(1)
	s := &nativeSession{
		batches: make(chan browseBatch, nativeQueueCap),
		results: make(chan nativeResolved, nativeQueueCap),
	}
	nativeSessions.Store(id, s)
	// Запоздавший колбэк после отмены найдёт сессию и положит данные в канал, а
	// Windows может обратиться к req/cancel уже после Cancel; держать всё это
	// вечно незачем, поэтому сессия и её запросы освобождаются через время.
	defer time.AfterFunc(10*time.Second, func() {
		nativeSessions.Delete(id)
		s.keep = nil
	})

	queryName, err := windows.UTF16PtrFromString(serviceType + ".local")
	if err != nil {
		return nil, err
	}
	req := &dnsServiceBrowseRequest{
		Version:      dnsQueryVersion1,
		QueryName:    queryName,
		Callback:     nativeBrowseCB,
		QueryContext: id,
	}
	cancel := &dnsServiceCancel{}
	s.keep = append(s.keep, req, cancel, queryName)
	r, _, _ := procDnsServiceBrowse.Call(uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(cancel)))
	if r != dnsRequestPending && r != 0 {
		return nil, fmt.Errorf("DnsServiceBrowse: код %d", uint32(r))
	}

	type resolving struct {
		req    *dnsServiceResolveRequest
		cancel *dnsServiceCancel
	}
	var pending []resolving // только для Cancel; сами структуры держит s.keep
	state := newNativeState()
	var firstErr error

	startResolve := func(name string) {
		qn, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return
		}
		p := resolving{
			req: &dnsServiceResolveRequest{
				Version:      dnsQueryVersion1,
				QueryName:    qn,
				Callback:     nativeResolveCB,
				QueryContext: id,
			},
			cancel: &dnsServiceCancel{},
		}
		pending = append(pending, p)
		s.keep = append(s.keep, p.req, p.cancel, qn)
		r, _, _ := procDnsServiceResolve.Call(uintptr(unsafe.Pointer(p.req)), uintptr(unsafe.Pointer(p.cancel)))
		if r != dnsRequestPending && r != 0 && firstErr == nil {
			firstErr = fmt.Errorf("DnsServiceResolve: код %d", uint32(r))
		}
	}
	addResolved := func(res nativeResolved) {
		state.resolved[strings.ToLower(res.label)] = res
	}

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case b := <-s.batches:
			for _, name := range state.addBatch(b) {
				startResolve(name)
			}
		case res := <-s.results:
			addResolved(res)
		}
	}

	_, _, _ = procDnsServiceBrowseCanc.Call(uintptr(unsafe.Pointer(cancel)))
	for _, p := range pending {
		_, _, _ = procDnsServiceResolveCan.Call(uintptr(unsafe.Pointer(p.cancel)))
	}
	// Ответы, успевшие прийти до отмены.
	for drained := false; !drained; {
		select {
		case b := <-s.batches:
			state.addBatch(b)
		case res := <-s.results:
			addResolved(res)
		default:
			drained = true
		}
	}

	devices := state.devices(context.Background(), serviceType)
	if len(devices) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return devices, nil
}

// finishNativeResolved превращает собранные данные в устройство. Если адреса нет,
// пробует узнать его по имени хоста системным резолвером.
func finishNativeResolved(ctx context.Context, res nativeResolved, serviceType string) *AirPlayDevice {
	label := instanceLabel(res.label, serviceType)
	ips := res.ips
	if len(usableServiceIPs(ips)) == 0 && res.hostName != "" {
		lctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		if addrs, err := net.DefaultResolver.LookupIPAddr(lctx, strings.TrimSuffix(res.hostName, ".")); err == nil {
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
		}
	}
	return deviceFromService(label, ips, res.port, res.txt)
}
