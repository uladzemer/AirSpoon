package airplay

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// Поиск приёмников складывается из независимых слоёв; результаты объединяются
// по deviceid или адресу. Если один слой не работает (например, брандмауэр
// режет входящий UDP 5353 для zeroconf), остальные всё равно находят телевизор.
//
//	windows  — системный mDNS Windows (dnsapi, служба Dnscache; окна брандмауэра нет);
//	unicast  — запрос mDNS с эфемерного порта, ответ приходит как на исходящий запрос;
//	zeroconf — собственный сокет 5353 (работает там, где брандмауэр открыт);
//	saved    — последний успешный телевизор: проверяется GET /info по сохранённому адресу.
const (
	LayerWindows  = "windows"
	LayerUnicast  = "unicast"
	LayerZeroconf = "zeroconf"
	LayerSaved    = "saved"
)

const airPlayServiceType = "_airplay._tcp"

// savedProbeTimeout — сколько ждём ответа /info от сохранённого телевизора.
const savedProbeTimeout = time.Second

// LayerReport — итог одного слоя за один поиск.
type LayerReport struct {
	Name  string
	Found int
	Err   error
	// Note — пояснение для слоя без ошибки (например, «нет сохранённого телевизора»).
	Note string
}

// DiscoveryReport — результат объединённого поиска.
type DiscoveryReport struct {
	Devices    []AirPlayDevice
	Layers     []LayerReport
	Interfaces []string // имена интерфейсов, по которым искали
}

// Summary — короткая строка для журнала: «windows=1 unicast=1 zeroconf=0 saved=пропущен; интерфейсы: Wi-Fi».
func (r *DiscoveryReport) Summary() string {
	var parts []string
	for _, l := range r.Layers {
		switch {
		case l.Err != nil:
			parts = append(parts, fmt.Sprintf("%s=ошибка (%v)", l.Name, l.Err))
		case l.Note != "":
			parts = append(parts, fmt.Sprintf("%s=%d (%s)", l.Name, l.Found, l.Note))
		default:
			parts = append(parts, fmt.Sprintf("%s=%d", l.Name, l.Found))
		}
	}
	ifaces := "нет"
	if len(r.Interfaces) > 0 {
		ifaces = strings.Join(r.Interfaces, ", ")
	}
	return fmt.Sprintf("найдено %d: %s; интерфейсы: %s", len(r.Devices), strings.Join(parts, " "), ifaces)
}

// DiscoverOptions настраивает поиск.
type DiscoverOptions struct {
	// LastReceiverPath — файл последнего успешного телевизора; пусто — слой saved выключен.
	LastReceiverPath string
}

// DiscoverAirPlay ищет приёмники AirPlay всеми слоями до отмены ctx.
func DiscoverAirPlay(ctx context.Context, opts DiscoverOptions) *DiscoveryReport {
	ifaces, traffic, err := airPlayMDNSInterfaces()
	var names []string
	for _, iface := range ifaces {
		names = append(names, iface.Name)
	}
	layers := []discoveryLayer{
		{LayerWindows, func(ctx context.Context) ([]AirPlayDevice, error) {
			return nativeBrowse(ctx, airPlayServiceType)
		}},
		{LayerUnicast, func(ctx context.Context) ([]AirPlayDevice, error) {
			if err != nil {
				return nil, err
			}
			return unicastBrowse(ctx, airPlayServiceType, unicastTargets(ifaces))
		}},
		{LayerZeroconf, func(ctx context.Context) ([]AirPlayDevice, error) {
			if err != nil {
				return nil, fmt.Errorf("интерфейсы: %w", err)
			}
			return zeroconfBrowse(ctx, ifaces, traffic)
		}},
	}
	rep := runDiscovery(ctx, layers, opts.LastReceiverPath, savedProbe)
	rep.Interfaces = names
	return rep
}

// DiscoverAirPlayDevices — прежний вход: только список приёмников.
func DiscoverAirPlayDevices(ctx context.Context) ([]AirPlayDevice, error) {
	rep := DiscoverAirPlay(ctx, DiscoverOptions{})
	return rep.Devices, rep.Err()
}

type discoveryLayer struct {
	name string
	run  func(ctx context.Context) ([]AirPlayDevice, error)
}

// fallbackLayers — слои, которые запускаются только если остальные ничего не
// нашли. zeroconf открывает слушающий сокет UDP 5353, из-за чего на новом пути
// exe Windows показывает окно брандмауэра: пока телевизор находят windows и
// unicast, сокет не создаётся и окна нет.
var fallbackLayers = map[string]bool{LayerZeroconf: true}

// fallbackTimeout — сколько работают запасные слои после основных.
const fallbackTimeout = 2 * time.Second

type layerOutcome struct {
	devs []AirPlayDevice
	err  error
}

// runLayers запускает слои параллельно и ждёт их все.
func runLayers(ctx context.Context, layers []discoveryLayer) []layerOutcome {
	outs := make([]layerOutcome, len(layers))
	var wg sync.WaitGroup
	for i, l := range layers {
		wg.Add(1)
		go func(i int, l discoveryLayer) {
			defer wg.Done()
			// Колбэки системного слоя и чужие библиотеки не должны ронять программу.
			defer func() {
				if r := recover(); r != nil {
					outs[i] = layerOutcome{nil, fmt.Errorf("авария слоя: %v", r)}
				}
			}()
			devs, err := l.run(ctx)
			outs[i] = layerOutcome{devs, err}
		}(i, l)
	}
	wg.Wait()
	return outs
}

// runDiscovery запускает основные слои, затем, если они ничего не нашли,
// запасные, объединяет найденное и только если сохранённого телевизора среди
// найденных нет — проверяет его через probe.
func runDiscovery(ctx context.Context, layers []discoveryLayer, lastPath string,
	probe func(ctx context.Context, r LastReceiver) (*AirPlayDevice, error)) *DiscoveryReport {
	var primary, fallback []int
	for i, l := range layers {
		if fallbackLayers[l.name] {
			fallback = append(fallback, i)
		} else {
			primary = append(primary, i)
		}
	}
	pick := func(idx []int) []discoveryLayer {
		out := make([]discoveryLayer, len(idx))
		for k, i := range idx {
			out[k] = layers[i]
		}
		return out
	}

	var (
		last     LastReceiver
		haveLast bool
	)
	if lastPath != "" {
		last, haveLast = LoadLastReceiver(lastPath)
	}

	outs := make([]layerOutcome, len(layers))
	for k, o := range runLayers(ctx, pick(primary)) {
		outs[primary[k]] = o
	}
	skipped := map[int]bool{}
	if len(fallback) > 0 {
		found := 0
		for _, i := range primary {
			found += len(outs[i].devs)
		}
		if found == 0 {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fallbackTimeout)
			for k, o := range runLayers(fctx, pick(fallback)) {
				outs[fallback[k]] = o
			}
			cancel()
		} else {
			for _, i := range fallback {
				skipped[i] = true
			}
		}
	}

	rep := &DiscoveryReport{}
	var all []AirPlayDevice
	for i, l := range layers {
		devs := outs[i].devs
		for j := range devs {
			devs[j].FoundBy = []string{l.name}
		}
		lr := LayerReport{Name: l.name, Found: len(devs), Err: outs[i].err}
		if skipped[i] {
			lr.Note = "не нужен: другие слои нашли"
		}
		rep.Layers = append(rep.Layers, lr)
		all = append(all, devs...)
	}
	merged := mergeDevices(all)

	if lastPath != "" {
		sr := LayerReport{Name: LayerSaved}
		if !haveLast {
			sr.Note = "нет сохранённого"
		} else {
			if containsReceiver(merged, last) {
				sr.Note = "уже найден"
			} else {
				// Сохранённый телевизор проверяем только если основные слои его не
				// нашли: каждая проверка — TCP-соединение с приёмником, а на
				// приставке оно может выводить приложение на передний план.
				// Контекст поиска к этому моменту обычно уже истёк, поэтому свой.
				pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), savedProbeTimeout)
				dev, err := safeProbe(pctx, probe, last)
				cancel()
				switch {
				case err != nil:
					sr.Err = err
				case dev != nil:
					dev.FoundBy = []string{LayerSaved}
					sr.Found = 1
					merged = mergeDevices(append(merged, *dev))
				}
			}
		}
		rep.Layers = append(rep.Layers, sr)
	}
	rep.Devices = merged
	return rep
}

// containsReceiver: есть ли сохранённый телевизор среди найденных.
func containsReceiver(devs []AirPlayDevice, r LastReceiver) bool {
	for _, d := range devs {
		if sameReceiver(d.DeviceID, d.IP, d.Port, r.DeviceID, r.Address, r.Port) {
			return true
		}
	}
	return false
}

func sameReceiver(idA, ipA string, portA int, idB, ipB string, portB int) bool {
	if idA != "" && idB != "" && strings.EqualFold(idA, idB) {
		return true
	}
	return ipA != "" && ipA == ipB && portA == portB
}

// mergeDevices убирает повторы: один телевизор — это один deviceid или один
// адрес с портом. Из дублей остаётся запись с большим числом TXT-ключей, а
// список слоёв, нашедших устройство, объединяется. Порядок — по первому появлению.
func mergeDevices(devs []AirPlayDevice) []AirPlayDevice {
	type entry struct {
		dev   AirPlayDevice
		addrs map[string]bool // все «адрес:порт», под которыми видели устройство
	}
	var entries []*entry
	for _, d := range devs {
		addr := d.IP + "|" + strconv.Itoa(d.Port)
		var hit *entry
		for _, e := range entries {
			if e.addrs[addr] || (d.DeviceID != "" && strings.EqualFold(e.dev.DeviceID, d.DeviceID)) {
				hit = e
				break
			}
		}
		if hit == nil {
			entries = append(entries, &entry{dev: cloneAirPlayDevice(d), addrs: map[string]bool{addr: true}})
			continue
		}
		hit.addrs[addr] = true
		found := unionStrings(hit.dev.FoundBy, d.FoundBy)
		if len(d.RawTXT) > len(hit.dev.RawTXT) {
			hit.dev = cloneAirPlayDevice(d)
		}
		hit.dev.FoundBy = found
	}
	out := make([]AirPlayDevice, len(entries))
	for i, e := range entries {
		out[i] = e.dev
	}
	return out
}

func unionStrings(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, s := range b {
		dup := false
		for _, have := range out {
			if have == s {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

// zeroconfBrowse — прежний поиск через zeroconf (собственный сокет 5353).
func zeroconfBrowse(ctx context.Context, ifaces []net.Interface, traffic zeroconf.IPType) ([]AirPlayDevice, error) {
	if len(ifaces) == 0 {
		return nil, nil
	}
	resolver, err := zeroconf.NewResolver(
		zeroconf.SelectIfaces(ifaces),
		zeroconf.SelectIPTraffic(traffic),
	)
	if err != nil {
		return nil, fmt.Errorf("zeroconf resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry, 16)
	var devices []AirPlayDevice

	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entries {
			if dev := parseServiceEntry(entry); dev != nil {
				devices = append(devices, *dev)
			}
		}
	}()

	if err := resolver.Browse(ctx, airPlayServiceType, "local.", entries); err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}

	<-ctx.Done()
	<-done
	return devices, nil
}

// probeReceiver спрашивает GET /info у сохранённого телевизора и, если он
// отвечает, строит из ответа запись для списка.
func probeReceiver(ctx context.Context, r LastReceiver) (*AirPlayDevice, error) {
	client := NewAirPlayClient(r.Address, r.Port)
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	timeout := savedProbeTimeout
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left < timeout {
			timeout = left
		}
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	info, err := client.getInfoWithTimeout(timeout)
	if err != nil {
		return nil, err
	}
	if err := checkSameReceiver(info, r); err != nil {
		return nil, err
	}
	return deviceFromInfo(info, r), nil
}

// checkSameReceiver: по сохранённому адресу мог оказаться другой приёмник
// (адрес перераздали по DHCP) — под старым именем он в список не попадает.
func checkSameReceiver(info *ReceiverInfo, r LastReceiver) error {
	if r.DeviceID != "" && info.DeviceID != "" && !strings.EqualFold(r.DeviceID, info.DeviceID) {
		return fmt.Errorf("по сохранённому адресу другой приёмник (deviceid %s)", info.DeviceID)
	}
	return nil
}

// deviceFromInfo собирает запись списка из ответа /info.
func deviceFromInfo(info *ReceiverInfo, r LastReceiver) *AirPlayDevice {
	name := info.Name
	if name == "" {
		name = r.Name
	}
	id := info.DeviceID
	if id == "" {
		id = r.DeviceID
	}
	dev := &AirPlayDevice{
		Name:            name,
		Model:           info.Model,
		IP:              r.Address,
		Port:            r.Port,
		DeviceID:        id,
		Features:        info.Features,
		FeaturesEx:      info.FeaturesEx.clone(),
		PK:              hex.EncodeToString(info.PK),
		Flags:           info.StatusFlags,
		SourceVersion:   info.SourceVersion,
		ProtocolVersion: info.ProtocolVersion,
		VV:              info.VV,
		PI:              info.PI,
		PSI:             info.PSI,
	}
	if dev.Features == 0 {
		dev.Features = dev.FeaturesEx.Low64()
	}
	return dev
}

// safeProbe вызывает probe, превращая панику в ошибку.
func safeProbe(ctx context.Context, probe func(context.Context, LastReceiver) (*AirPlayDevice, error), r LastReceiver) (dev *AirPlayDevice, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			dev, err = nil, fmt.Errorf("авария проверки: %v", rec)
		}
	}()
	return probe(ctx, r)
}

// probeCacheTTL — как долго помним итог проверки сохранённого телевизора.
// Трей ищет каждые 2,5 с; без кэша каждый поиск (когда mDNS телевизор не видит)
// открывал бы TCP-соединение с приёмником.
const probeCacheTTL = 45 * time.Second

// cachedProbe оборачивает probe кэшем по «адрес:порт» (и успех, и неудача).
func cachedProbe(probe func(context.Context, LastReceiver) (*AirPlayDevice, error), ttl time.Duration) func(context.Context, LastReceiver) (*AirPlayDevice, error) {
	type entry struct {
		at  time.Time
		dev *AirPlayDevice
		err error
	}
	var mu sync.Mutex
	cache := map[string]entry{}
	return func(ctx context.Context, r LastReceiver) (*AirPlayDevice, error) {
		key := net.JoinHostPort(r.Address, strconv.Itoa(r.Port)) + "|" + strings.ToLower(r.DeviceID)
		mu.Lock()
		e, ok := cache[key]
		mu.Unlock()
		if ok && time.Since(e.at) < ttl {
			if e.dev != nil {
				d := cloneAirPlayDevice(*e.dev)
				return &d, nil
			}
			return nil, e.err
		}
		dev, err := probe(ctx, r)
		mu.Lock()
		cache[key] = entry{at: time.Now(), dev: dev, err: err}
		mu.Unlock()
		return dev, err
	}
}

var savedProbe = cachedProbe(probeReceiver, probeCacheTTL)

// Err возвращает ошибку, если не нашлось ничего и каждый слой поиска (кроме
// проверки сохранённого) завершился ошибкой: тогда «не найдено» было бы ложным
// ответом, а на деле поиск просто не работал.
func (r *DiscoveryReport) Err() error {
	if len(r.Devices) > 0 {
		return nil
	}
	var errs []string
	n := 0
	for _, l := range r.Layers {
		if l.Name == LayerSaved {
			continue
		}
		n++
		if l.Err == nil {
			return nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", l.Name, l.Err))
	}
	if n == 0 {
		return nil
	}
	return fmt.Errorf("все слои поиска завершились ошибкой (%s)", strings.Join(errs, "; "))
}
