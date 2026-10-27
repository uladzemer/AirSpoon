package airplay

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScreenLatenciesForConnectionHint(t *testing.T) {
	SetTargetLatency(0)
	t.Cleanup(func() {
		SetTargetLatency(0)
	})

	tests := []struct {
		name  string
		hint  connectionLatencyHint
		video time.Duration
		audio time.Duration
	}{
		{name: "low", hint: connectionLatencyLow, video: 40 * time.Millisecond, audio: 50 * time.Millisecond},
		{name: "normal", hint: connectionLatencyNormal, video: 75 * time.Millisecond, audio: 85 * time.Millisecond},
		{name: "high", hint: connectionLatencyHigh, video: 100 * time.Millisecond, audio: 170 * time.Millisecond},
		{name: "unknown is normal", hint: connectionLatencyHint(99), video: 75 * time.Millisecond, audio: 85 * time.Millisecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := screenLatenciesForHint(test.hint)
			if got.video != test.video || got.audio != test.audio {
				t.Fatalf("latencies = video %v audio %v, want video %v audio %v", got.video, got.audio, test.video, test.audio)
			}
		})
	}
}

func TestTargetLatencyOverrideAppliesJointLead(t *testing.T) {
	SetTargetLatency(120 * time.Millisecond)
	t.Cleanup(func() {
		SetTargetLatency(0)
	})

	for _, hint := range []connectionLatencyHint{connectionLatencyLow, connectionLatencyNormal, connectionLatencyHigh} {
		got := screenLatenciesForHint(hint)
		if got.video != 120*time.Millisecond || got.audio != 120*time.Millisecond {
			t.Fatalf("hint %d override = video %v audio %v, want 120ms/120ms", hint, got.video, got.audio)
		}
	}
	if got := TargetLatency(); got != 120*time.Millisecond {
		t.Fatalf("TargetLatency = %v, want 120ms", got)
	}
	if !HasExplicitTargetLatency() {
		t.Fatal("explicit target latency was not reported")
	}
	if got, want := targetLatencySamples44k1(), samplesFor44k1(120*time.Millisecond); got != want {
		t.Fatalf("audio latency = %d samples, want %d", got, want)
	}
}

func TestTargetLatencyAutomaticAndClamp(t *testing.T) {
	SetTargetLatency(0)
	if HasExplicitTargetLatency() {
		t.Fatal("automatic latency policy was reported as explicit")
	}
	t.Cleanup(func() {
		SetTargetLatency(0)
	})

	SetTargetLatency(time.Millisecond)
	if got := screenLatenciesForHint(connectionLatencyNormal); got.video != 5*time.Millisecond || got.audio != 5*time.Millisecond {
		t.Fatalf("minimum override = video %v audio %v, want 5ms/5ms", got.video, got.audio)
	}

	SetTargetLatency(3 * time.Second)
	if got := screenLatenciesForHint(connectionLatencyNormal); got.video != 2*time.Second || got.audio != 2*time.Second {
		t.Fatalf("maximum override = video %v audio %v, want 2s/2s", got.video, got.audio)
	}

	SetTargetLatency(0)
	if got := screenLatenciesForHint(connectionLatencyNormal); got.video != 75*time.Millisecond || got.audio != 85*time.Millisecond {
		t.Fatalf("automatic policy = video %v audio %v, want 75ms/85ms", got.video, got.audio)
	}
}

func TestSamplesFor44k1TruncatesFractionalSamples(t *testing.T) {
	if got := samplesFor44k1(85 * time.Millisecond); got != 3748 {
		t.Fatalf("85ms = %d samples, want truncation to 3748", got)
	}
}

func TestMinimumVideoLeadPreservesAutomaticAudioVideoDelta(t *testing.T) {
	targets := screenLatencyTargets{
		video: defaultVideoLatencyNormal,
		audio: defaultAudioLatencyNormal,
	}
	if got := targets.withMinimumVideoLead(50 * time.Millisecond); got != targets {
		t.Fatalf("unneeded minimum changed targets to %#v", got)
	}
	got := targets.withMinimumVideoLead(150 * time.Millisecond)
	if got.video != 150*time.Millisecond || got.audio != 160*time.Millisecond {
		t.Fatalf("calibrated targets = video %v audio %v, want 150ms/160ms", got.video, got.audio)
	}
}

func TestConnectionLatencyHintFollowsLocalInterface(t *testing.T) {
	classPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(classPath, "wlan0", "wireless"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(classPath, "wlan1", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}

	interfaces := []localNetworkInterface{
		{name: "eth0", addrs: []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)}}},
		{name: "wlan0", addrs: []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.20"), Mask: net.CIDRMask(24, 32)}}},
		{name: "wlan1", addrs: []net.Addr{&net.IPAddr{IP: net.ParseIP("2001:db8::20")}}},
	}

	tests := []struct {
		name      string
		ip        net.IP
		wantHint  connectionLatencyHint
		wantIface string
	}{
		{name: "wired", ip: net.ParseIP("192.0.2.10"), wantHint: connectionLatencyNormal, wantIface: "eth0"},
		{name: "wireless marker", ip: net.ParseIP("192.0.2.20"), wantHint: connectionLatencyHigh, wantIface: "wlan0"},
		{name: "phy marker", ip: net.ParseIP("2001:db8::20"), wantHint: connectionLatencyHigh, wantIface: "wlan1"},
		{name: "unknown", ip: net.ParseIP("192.0.2.30"), wantHint: connectionLatencyNormal, wantIface: ""},
		{name: "nil", ip: nil, wantHint: connectionLatencyNormal, wantIface: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotHint, gotIface := connectionLatencyHintForLocalIP(test.ip, interfaces, classPath)
			if gotHint != test.wantHint || gotIface != test.wantIface {
				t.Fatalf("hint/interface = %s/%q, want %s/%q",
					connectionLatencyHintName(gotHint), gotIface,
					connectionLatencyHintName(test.wantHint), test.wantIface)
			}
		})
	}
}

func TestConnectionLatencyHintName(t *testing.T) {
	tests := map[connectionLatencyHint]string{
		connectionLatencyLow:     "low",
		connectionLatencyNormal:  "normal",
		connectionLatencyHigh:    "high",
		connectionLatencyHint(9): "normal",
	}
	for hint, want := range tests {
		if got := connectionLatencyHintName(hint); got != want {
			t.Fatalf("hint %d name = %q, want %q", hint, got, want)
		}
	}
}

func TestSessionLatencyOverrideBeatsTheProcessWideOne(t *testing.T) {
	SetTargetLatency(120 * time.Millisecond)
	t.Cleanup(func() { SetTargetLatency(0) })

	got, explicit := sessionScreenLatencies(connectionLatencyNormal, 250*time.Millisecond)
	if got.video != 250*time.Millisecond || got.audio != 250*time.Millisecond || !explicit {
		t.Fatalf("session override = %+v explicit=%v, want 250ms/250ms explicit", got, explicit)
	}
	got, explicit = sessionScreenLatencies(connectionLatencyNormal, 0)
	if got.video != 120*time.Millisecond || !explicit {
		t.Fatalf("no session override = %+v explicit=%v, want the process-wide 120ms", got, explicit)
	}
	SetTargetLatency(0)
	got, explicit = sessionScreenLatencies(connectionLatencyNormal, 0)
	if got.video != 75*time.Millisecond || got.audio != 85*time.Millisecond || explicit {
		t.Fatalf("automatic = %+v explicit=%v, want 75ms/85ms automatic", got, explicit)
	}
	if got, _ := sessionScreenLatencies(connectionLatencyNormal, time.Millisecond); got.video != 5*time.Millisecond {
		t.Fatalf("tiny session override = %v, want clamped to 5ms", got.video)
	}
}

// Windows: Wi-Fi определяется по IANA IfType 71, а не по /sys/class/net.
func TestConnectionLatencyHintUsesWindowsIfType(t *testing.T) {
	classPath := t.TempDir() // пустой: файловых маркеров нет, как на Windows
	ip := func(s string) []net.Addr {
		return []net.Addr{&net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)}}
	}
	interfaces := []localNetworkInterface{
		{name: "Ethernet", index: 3, addrs: ip("192.0.2.10"), ifType: 6, description: "Intel I219"},
		{name: "Wi-Fi", index: 4, addrs: ip("192.0.2.20"), ifType: 71, description: "Intel AX201", linkMbps: 866},
		{name: "vEthernet (WSL)", index: 5, addrs: ip("192.0.2.30"), ifType: 6},
		{name: "OpenVPN", index: 6, addrs: ip("192.0.2.40"), ifType: 53},
		{name: "Loopback", index: 1, addrs: ip("192.0.2.50"), ifType: 24},
		{name: "NoType", index: 7, addrs: ip("192.0.2.60")},
	}
	tests := []struct {
		ip       string
		wantHint connectionLatencyHint
		wantName string
		wantKind NetworkKind
	}{
		{"192.0.2.10", connectionLatencyNormal, "Ethernet", NetworkWired},
		{"192.0.2.20", connectionLatencyHigh, "Wi-Fi", NetworkWiFi},
		{"192.0.2.30", connectionLatencyNormal, "vEthernet (WSL)", NetworkWired},
		{"192.0.2.40", connectionLatencyNormal, "OpenVPN", NetworkOther},
		{"192.0.2.50", connectionLatencyNormal, "Loopback", NetworkOther},
		{"192.0.2.60", connectionLatencyNormal, "NoType", NetworkUnknown},
		{"198.51.100.1", connectionLatencyNormal, "", NetworkUnknown},
	}
	for _, test := range tests {
		t.Run(test.ip, func(t *testing.T) {
			hint, name := connectionLatencyHintForLocalIP(net.ParseIP(test.ip), interfaces, classPath)
			if hint != test.wantHint || name != test.wantName {
				t.Fatalf("hint/interface = %s/%q, want %s/%q", connectionLatencyHintName(hint), name,
					connectionLatencyHintName(test.wantHint), test.wantName)
			}
			info, found := routeInterfaceForLocalIP(net.ParseIP(test.ip), interfaces, classPath)
			if found != (test.wantName != "") || info.Kind != test.wantKind && found {
				t.Fatalf("route = %+v found=%v, want kind %s", info, found, test.wantKind)
			}
		})
	}
	info, _ := routeInterfaceForLocalIP(net.ParseIP("192.0.2.20"), interfaces, classPath)
	if info.Description != "Intel AX201" || info.LinkMbps != 866 || info.IfType != 71 {
		t.Fatalf("Wi-Fi route details = %+v", info)
	}
}

// IfType 71 на Wi-Fi даёт те запасы, что задуманы для беспроводной сети: 100/170 мс.
func TestWiFiRouteGetsWirelessLatencyMargins(t *testing.T) {
	SetTargetLatency(0)
	hint, _ := connectionLatencyHintForLocalIP(net.ParseIP("192.0.2.20"), []localNetworkInterface{
		{name: "Wi-Fi", addrs: []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.20"), Mask: net.CIDRMask(24, 32)}}, ifType: 71},
	}, t.TempDir())
	got, explicit := sessionScreenLatencies(hint, 0)
	if explicit || got.video != 100*time.Millisecond || got.audio != 170*time.Millisecond {
		t.Fatalf("Wi-Fi latencies = %+v explicit=%v, want 100ms/170ms automatic", got, explicit)
	}
}

func TestNetworkKindForIfType(t *testing.T) {
	tests := map[uint32]NetworkKind{
		0: NetworkUnknown, 71: NetworkWiFi, 6: NetworkWired, 62: NetworkWired, 69: NetworkWired,
		117: NetworkWired, 24: NetworkOther, 53: NetworkOther, 131: NetworkOther, 243: NetworkOther,
	}
	for ifType, want := range tests {
		if got := networkKindForIfType(ifType); got != want {
			t.Errorf("IfType %d = %s, want %s", ifType, got, want)
		}
	}
}

func TestRouteInterfaceToLoopback(t *testing.T) {
	info, err := RouteInterfaceTo("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !info.LocalIP.IsLoopback() {
		t.Fatalf("route to loopback uses %v", info.LocalIP)
	}
}
