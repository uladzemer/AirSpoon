package airplay

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type connectionLatencyHint int8

const (
	connectionLatencyLow    connectionLatencyHint = -1
	connectionLatencyNormal connectionLatencyHint = 0
	connectionLatencyHigh   connectionLatencyHint = 1

	// The ordinary screen path selects separate video and audio leads from the
	// same semantic connection hint.
	defaultVideoLatencyLow    = 40 * time.Millisecond
	defaultVideoLatencyNormal = 75 * time.Millisecond
	defaultVideoLatencyHigh   = 100 * time.Millisecond
	defaultAudioLatencyLow    = 50 * time.Millisecond
	defaultAudioLatencyNormal = 85 * time.Millisecond
	defaultAudioLatencyHigh   = 170 * time.Millisecond
)

type screenLatencyTargets struct {
	video time.Duration
	audio time.Duration
}

// localNetworkInterface — снимок сетевого интерфейса. На Windows тип
// (ifType, IANA IfType из GetAdaptersAddresses) приходит из latency_windows.go;
// без него (ifType == 0) беспроводность определяется по /sys/class/net.
type localNetworkInterface struct {
	name        string
	index       int
	addrs       []net.Addr
	ifType      uint32
	description string
	linkMbps    uint64
}

const networkInterfaceClassPath = "/sys/class/net"

// NetworkKind — класс сетевого интерфейса, через который идёт трафик к приёмнику.
type NetworkKind string

const (
	NetworkUnknown NetworkKind = "unknown" // интерфейс не найден или тип не определить
	NetworkWiFi    NetworkKind = "wifi"    // беспроводной (IEEE 802.11)
	NetworkWired   NetworkKind = "wired"   // Ethernet
	NetworkOther   NetworkKind = "other"   // туннель, VPN, виртуальный, мобильный и т. п.
)

// IANA IfType, нужные для классификации.
const (
	ifTypeEthernetCSMACD = 6
	ifTypeFastEthernetT  = 62
	ifTypeFastEthernetFX = 69
	ifTypeGigabitEther   = 117
	ifTypeIEEE80211      = 71
)

// networkKindForIfType переводит IANA IfType в класс интерфейса. 0 — неизвестен.
func networkKindForIfType(ifType uint32) NetworkKind {
	switch ifType {
	case 0:
		return NetworkUnknown
	case ifTypeIEEE80211:
		return NetworkWiFi
	case ifTypeEthernetCSMACD, ifTypeFastEthernetT, ifTypeFastEthernetFX, ifTypeGigabitEther:
		return NetworkWired
	default:
		return NetworkOther
	}
}

func (iface localNetworkInterface) kind(classPath string) NetworkKind {
	if iface.ifType != 0 {
		return networkKindForIfType(iface.ifType)
	}
	if networkInterfaceIsWireless(iface.name, classPath) {
		return NetworkWiFi
	}
	return NetworkUnknown
}

// RouteInterface описывает локальный интерфейс маршрута к приёмнику (для diag).
type RouteInterface struct {
	Name        string      // имя интерфейса («Wi-Fi», «Ethernet»)
	Description string      // описание драйвера адаптера (только Windows)
	Kind        NetworkKind // wifi / wired / other / unknown
	IfType      uint32      // IANA IfType (только Windows), 0 — неизвестен
	LinkMbps    uint64      // скорость канала по данным адаптера, Мбит/с (только Windows)
	LocalIP     net.IP      // локальный адрес маршрута
}

// InterfaceForAddr возвращает интерфейс, которому принадлежит локальный адрес ip.
// Второй результат false — адрес не найден ни на одном интерфейсе.
func InterfaceForAddr(ip net.IP) (RouteInterface, bool) {
	return routeInterfaceForLocalIP(ip, localInterfaceSnapshots(), networkInterfaceClassPath)
}

// RouteInterfaceTo определяет интерфейс, через который система отправит трафик
// на host (адрес приёмника): UDP-«соединение» ничего не передаёт, а только
// выбирает маршрут и локальный адрес.
func RouteInterfaceTo(host string) (RouteInterface, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(host, "9"))
	if err != nil {
		return RouteInterface{}, err
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return RouteInterface{}, fmt.Errorf("маршрут к %s: локальный адрес не определён", host)
	}
	info, found := InterfaceForAddr(local.IP)
	if !found {
		info.LocalIP = local.IP
		info.Kind = NetworkUnknown
	}
	return info, nil
}

func localInterfaceSnapshots() []localNetworkInterface {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	adapters := platformAdapters()
	snapshots := make([]localNetworkInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		snap := localNetworkInterface{name: iface.Name, index: iface.Index, addrs: addrs}
		if adapter, ok := adapters[iface.Index]; ok {
			snap.ifType = adapter.ifType
			snap.description = adapter.description
			snap.linkMbps = adapter.linkMbps
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots
}

// platformAdapterInfo — сведения об адаптере от ОС (Windows).
type platformAdapterInfo struct {
	ifType      uint32
	description string
	linkMbps    uint64
}

// connectionLatencyHintForConnection mirrors the sender's use of the control
// connection interface latency hint. A wireless route uses the high-latency
// screen profile, which leaves enough receiver headroom for normal Wi-Fi jitter.
// Unknown and non-wireless interfaces retain the ordinary profile. На Windows
// Wi-Fi определяется по IfType 71 из GetAdaptersAddresses (latency_windows.go).
func connectionLatencyHintForConnection(conn net.Conn) (connectionLatencyHint, string) {
	if conn == nil {
		return connectionLatencyNormal, ""
	}
	localAddr, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok || localAddr.IP == nil || localAddr.IP.IsUnspecified() {
		return connectionLatencyNormal, ""
	}
	return connectionLatencyHintForLocalIP(localAddr.IP, localInterfaceSnapshots(), networkInterfaceClassPath)
}

func routeInterfaceForLocalIP(localIP net.IP, interfaces []localNetworkInterface, classPath string) (RouteInterface, bool) {
	if localIP == nil {
		return RouteInterface{}, false
	}
	for _, iface := range interfaces {
		for _, addr := range iface.addrs {
			if !networkAddressIP(addr).Equal(localIP) {
				continue
			}
			return RouteInterface{
				Name:        iface.name,
				Description: iface.description,
				Kind:        iface.kind(classPath),
				IfType:      iface.ifType,
				LinkMbps:    iface.linkMbps,
				LocalIP:     localIP,
			}, true
		}
	}
	return RouteInterface{}, false
}

func connectionLatencyHintForLocalIP(localIP net.IP, interfaces []localNetworkInterface, classPath string) (connectionLatencyHint, string) {
	info, ok := routeInterfaceForLocalIP(localIP, interfaces, classPath)
	if !ok {
		return connectionLatencyNormal, ""
	}
	if info.Kind == NetworkWiFi {
		return connectionLatencyHigh, info.Name
	}
	return connectionLatencyNormal, info.Name
}

func networkAddressIP(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

func networkInterfaceIsWireless(name, classPath string) bool {
	for _, marker := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(filepath.Join(classPath, name, marker)); err == nil {
			return true
		}
	}
	return false
}

func connectionLatencyHintName(hint connectionLatencyHint) string {
	switch hint {
	case connectionLatencyLow:
		return "low"
	case connectionLatencyHigh:
		return "high"
	default:
		return "normal"
	}
}

// withMinimumVideoLead gives a locally measured capture pipeline enough room
// while preserving the relative audio/video policy for the connection hint.
// This is sender scheduling compensation, not a receiver or codec-specific
// clock offset.
func (targets screenLatencyTargets) withMinimumVideoLead(minimum time.Duration) screenLatencyTargets {
	if minimum <= targets.video {
		return targets
	}
	delta := minimum - targets.video
	targets.video += delta
	targets.audio += delta
	return targets
}

var targetLatencyNS atomic.Int64

// SetTargetLatency sets the application's explicit joint playout lead. Screen
// and audio overrides are independent, but doubletake historically exposed one
// flag for both; applying the same explicit value preserves that contract. A
// non-positive value restores the automatic policy.
func SetTargetLatency(d time.Duration) {
	targetLatencyNS.Store(int64(clampTargetLatency(d)))
}

// clampTargetLatency keeps an explicit joint lead within 5 ms to 2 s. A
// non-positive value stays zero, which means automatic.
func clampTargetLatency(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d < 5*time.Millisecond {
		return 5 * time.Millisecond
	}
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

// sessionScreenLatencies is the playout policy for one mirror session. A
// positive override (StreamConfig.TargetLatency) is that session's explicit
// joint lead and wins over the process-wide SetTargetLatency; explicit reports
// whether either one replaced the automatic policy.
func sessionScreenLatencies(hint connectionLatencyHint, override time.Duration) (targets screenLatencyTargets, explicit bool) {
	targets = screenLatenciesForHint(hint)
	if override = clampTargetLatency(override); override > 0 {
		return screenLatencyTargets{video: override, audio: override}, true
	}
	return targets, targetLatencyIsExplicit()
}

// TargetLatency returns the video lead for an ordinary connection. It remains
// the compatibility accessor for callers that only need the video timestamp
// bias; new session setup should use screenLatenciesForHint.
func TargetLatency() time.Duration {
	return screenLatenciesForHint(connectionLatencyNormal).video
}

// HasExplicitTargetLatency reports whether SetTargetLatency currently replaces
// automatic capture calibration with one joint audio/video lead.
func HasExplicitTargetLatency() bool {
	return targetLatencyIsExplicit()
}

func targetLatencyIsExplicit() bool {
	return targetLatencyNS.Load() > 0
}

func screenLatenciesForHint(hint connectionLatencyHint) screenLatencyTargets {
	var targets screenLatencyTargets
	switch hint {
	case connectionLatencyLow:
		targets = screenLatencyTargets{video: defaultVideoLatencyLow, audio: defaultAudioLatencyLow}
	case connectionLatencyHigh:
		targets = screenLatencyTargets{video: defaultVideoLatencyHigh, audio: defaultAudioLatencyHigh}
	default:
		targets = screenLatencyTargets{video: defaultVideoLatencyNormal, audio: defaultAudioLatencyNormal}
	}

	if override := time.Duration(targetLatencyNS.Load()); override > 0 {
		targets.video = override
		targets.audio = override
	}
	return targets
}

func targetLatencySamples44k1() uint32 {
	return samplesFor44k1(screenLatenciesForHint(connectionLatencyNormal).audio)
}

func samplesFor44k1(d time.Duration) uint32 {
	// Convert millisecond latency to the integral SETUP and RTP-timebase value by
	// truncation: 85 ms is 3748.5 samples and is advertised as 3748, not rounded
	// to 3749.
	samples := int64(d/time.Second)*44100 + int64(d%time.Second)*44100/int64(time.Second)
	if samples < 1 {
		samples = 1
	}
	if samples > math.MaxUint32 {
		samples = math.MaxUint32
	}
	return uint32(samples)
}
