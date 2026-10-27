//go:build windows && (amd64 || arm64)

package gpudiag

import (
	"errors"
	"strings"
	"testing"
	"time"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/mf"
)

func TestIsNEdition(t *testing.T) {
	tests := []struct {
		edition, product string
		want             bool
	}{
		{"Professional", "Windows 10 Pro", false},
		{"ProfessionalN", "Windows 10 Pro N", true},
		{"CoreN", "Windows 11 Home N", true},
		{"EnterpriseN", "", true},
		{"Core", "Windows 11 Home", false},
		{"Education", "Windows 10 Education", false},
		{"", "Windows 10 Pro KN", true},
		{"Professional", "Windows 10 Pro N for Workstations", true},
		{"", "", false},
	}
	for _, tc := range tests {
		if got := isNEdition(tc.edition, tc.product); got != tc.want {
			t.Errorf("isNEdition(%q, %q) = %v", tc.edition, tc.product, got)
		}
	}
}

func TestMachineName(t *testing.T) {
	if got := machineName("LENOVO", "20KH002JRT"); got != "LENOVO 20KH002JRT" {
		t.Error(got)
	}
	if got := machineName("VMware, Inc.", "VMware, Inc."); got != "VMware, Inc." {
		t.Error(got)
	}
	if machineName("", "") != "" {
		t.Error("пусто")
	}
}

func TestPowerText(t *testing.T) {
	if got := powerText(systemPowerStatus{ACLineStatus: 1, BatteryLifePercent: 80}); got != "от сети, заряд 80%" {
		t.Error(got)
	}
	if got := powerText(systemPowerStatus{ACLineStatus: 0, BatteryLifePercent: 255}); got != "от батареи" {
		t.Error(got)
	}
	if got := powerText(systemPowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}); !strings.Contains(got, "батареи нет") {
		t.Error(got)
	}
}

func okTrial(adapter, name string, hardware bool, p trialProfile, input string) EncodeTrial {
	return EncodeTrial{
		AdapterName: adapter, Encoder: name, Hardware: hardware, Profile: p,
		Report: mf.ProbeReport{Encoder: name, Hardware: hardware, Input: input, Offered: 60, Accepted: 60, Packets: 60, KeyPackets: 1,
			Elapsed: 2 * time.Second},
	}
}

func TestTrialPassed(t *testing.T) {
	good := okTrial("GPU", "NVIDIA H.264 Encoder MFT", true, trialProfiles[1], "системная память")
	if !good.Passed() {
		t.Fatal("целая проба должна проходить")
	}
	slow := good
	slow.Report.Skipped, slow.Report.Accepted = 7, 53 // больше 10% пропущено
	if slow.Passed() {
		t.Error("пропуск 7 кадров из 60 (11%) не должен проходить")
	}
	edge := good
	edge.Report.Skipped, edge.Report.Accepted = 6, 54 // ровно 10%
	if !edge.Passed() {
		t.Error("ровно 10% пропусков допустимо")
	}
	for name, mut := range map[string]func(*EncodeTrial){
		"ошибка пробы":  func(x *EncodeTrial) { x.Err = "не запустился" },
		"ошибка отчёта": func(x *EncodeTrial) { x.Report.Err = "кадр 3" },
		"нет пакетов":   func(x *EncodeTrial) { x.Report.Packets = 0 },
		"нет кадров":    func(x *EncodeTrial) { x.Report.Offered = 0 },
	} {
		x := good
		mut(&x)
		if x.Passed() {
			t.Errorf("%s: не должна проходить", name)
		}
	}
}

func p720() trialProfile  { return trialProfiles[0] }
func p1080() trialProfile { return trialProfiles[1] }
func p60() trialProfile   { return trialProfiles[2] }

func TestRecommendHardwarePrefersHeaviestProfileThenTextures(t *testing.T) {
	r := &Report{System: SystemInfo{LogicalCores: 8}, Ext: &Extended{
		EncoderLists: []AdapterEncoderList{{Index: 0, Name: "NVIDIA GeForce", Infos: []mf.EncoderInfo{{Name: "NVIDIA H.264 Encoder MFT", Hardware: true}}}},
		Trials: []EncodeTrial{
			okTrial("NVIDIA GeForce", "NVIDIA H.264 Encoder MFT", true, p720(), "системная память"),
			okTrial("NVIDIA GeForce", "NVIDIA H.264 Encoder MFT", true, p60(), "системная память"),
			okTrial("NVIDIA GeForce", "NVIDIA H.264 Encoder MFT", true, p60(), "текстуры D3D11"),
			okTrial("процессор", "Microsoft H.264 Encoder MFT", false, p720(), "системная память"),
		},
	}}
	got := recommend(r)
	for _, want := range []string{"аппаратный кодер", "NVIDIA H.264 Encoder MFT", "NVIDIA GeForce", "текстуры D3D11", "1080p60"} {
		if !strings.Contains(got, want) {
			t.Errorf("в итоге %q нет %q", got, want)
		}
	}
}

func TestRecommendHardwareFailedFallsBackToSoftwareCap(t *testing.T) {
	bad := okTrial("Intel HD", "Intel Quick Sync", true, p720(), "системная память")
	bad.Report.Packets = 0
	soft := func(p trialProfile) EncodeTrial {
		return okTrial("процессор", "Microsoft H.264 Encoder MFT", false, p, "системная память")
	}
	mk := func(cores int) *Report {
		return &Report{System: SystemInfo{LogicalCores: cores}, Ext: &Extended{
			EncoderLists: []AdapterEncoderList{{Index: 0, Name: "Intel HD", Infos: []mf.EncoderInfo{{Name: "Intel Quick Sync", Hardware: true}}}},
			Trials:       []EncodeTrial{bad, soft(p720()), soft(p1080()), soft(p60())},
		}}
	}
	// Потолок программного кодера: 720p30 на малом числе ядер, 1080p30 при шести и больше,
	// 1080p60 не рекомендуется никогда, даже если проба прошла.
	if got := recommend(mk(4)); !strings.Contains(got, "программный кодер") || !strings.Contains(got, "720p30") || !strings.Contains(got, "аппаратные кодеры не прошли пробу") {
		t.Errorf("4 ядра: %q", got)
	}
	if got := recommend(mk(8)); !strings.Contains(got, "1080p30") || strings.Contains(got, "1080p60") {
		t.Errorf("8 ядер: %q", got)
	}
}

func TestRecommendNoHardwareEncoders(t *testing.T) {
	r := &Report{System: SystemInfo{LogicalCores: 2}, Ext: &Extended{
		Trials: []EncodeTrial{okTrial("процессор", "Microsoft H.264 Encoder MFT", false, p720(), "системная память")},
	}}
	if got := recommend(r); !strings.Contains(got, "аппаратных кодеров нет") || !strings.Contains(got, "качество снижено") {
		t.Errorf("%q", got)
	}
}

func TestRecommendNothingWorks(t *testing.T) {
	bad := okTrial("процессор", "Microsoft H.264 Encoder MFT", false, p720(), "системная память")
	bad.Err = "кодер не запустился"
	r := &Report{System: SystemInfo{LogicalCores: 4}, Ext: &Extended{Trials: []EncodeTrial{bad}}}
	if got := recommend(r); !strings.Contains(got, "ни один кодер не прошёл пробу") {
		t.Errorf("%q", got)
	}
}

func TestRecommendMediaFoundationMissing(t *testing.T) {
	r := &Report{MFErr: errors.New("Media Foundation недоступна"), System: SystemInfo{IsN: true}, Ext: &Extended{}}
	got := recommend(r)
	if !strings.Contains(got, "показ невозможен") || !strings.Contains(got, "Пакет мультимедийных функций") {
		t.Errorf("%q", got)
	}
	r.System.IsN = false
	if got := recommend(r); strings.Contains(got, "Пакет мультимедийных функций") {
		t.Errorf("совет про пакет только для N-редакций: %q", got)
	}
}

func TestRecommendCaptureFailureWins(t *testing.T) {
	r := &Report{System: SystemInfo{LogicalCores: 4}, Ext: &Extended{
		Monitors: []dxgi.MonitorProbe{{Display: 0, Primary: true, Err: errors.New("захват недоступен в этом сеансе")}},
		Trials:   []EncodeTrial{okTrial("GPU", "X", true, p60(), "системная память")},
	}}
	if got := recommend(r); !strings.Contains(got, "захват экрана не работает") {
		t.Errorf("%q", got)
	}
}

func TestRecommendSkippedTrials(t *testing.T) {
	r := &Report{Ext: &Extended{TrialsSkipped: "идёт показ"}}
	if got := recommend(r); !strings.Contains(got, "не выполнялось") {
		t.Errorf("%q", got)
	}
}

// Расширенный отчёт содержит все разделы плана: система, Media Foundation,
// карты, мониторы, кодеры, захват, итог, сеть и звук.
func TestLinesExtendedSections(t *testing.T) {
	luid := dxgi.LUID{Low: 1, High: 0}
	rep := Report{
		At: time.Now(), Exe: `C:\Apps\x.exe`, Pref: "не задано", Windows: "Windows 10.0.19045",
		System: SystemInfo{Windows: "Windows 10 Pro N 22H2, сборка 10.0.19045.1", Edition: "ProfessionalN", IsN: true,
			ProcessArch: "x64 (amd64)", SystemArch: "x64 (amd64)", CPU: "Intel(R) Core(TM) i5", LogicalCores: 4,
			RAMTotalMB: 8000, RAMFreeMB: 3000, Power: "от батареи, заряд 50%"},
		Adapters:        []dxgi.AdapterInfo{{Index: 0, Name: "Microsoft Basic Render Driver", VendorID: 0x1414, LUID: luid, Flags: 2}},
		SoftwareEncoder: mf.EncoderInfo{Name: "Microsoft H.264 Encoder MFT", Err: "REGDB_E_CLASSNOTREG"},
		Ext: &Extended{
			AdapterProbes: []dxgi.AdapterProbe{{Index: 0, Name: "Microsoft Basic Render Driver", LUID: luid, BasicDisplay: true,
				WithVideo: dxgi.CreateAttempt{Tried: true, HR: 0x887A0004}, WithoutVideo: dxgi.CreateAttempt{Tried: true, OK: true, FeatureLevel: 0xB100}}},
			Monitors: []dxgi.MonitorProbe{{Display: 0, Name: `\\.\DISPLAY1`, Adapter: "Microsoft Basic Render Driver", Primary: true,
				ColorSpaceKnown: true, HDR: true, RefreshHz: 60, FrameFormat: 10, FrameFormatName: "R16G16B16A16_FLOAT",
				PathReason: "устройство D3D11 создано без поддержки видео", Frames: 100, FreshFrames: 40, Measured: 5 * time.Second,
				AvgInterval: 33 * time.Millisecond, MaxInterval: 90 * time.Millisecond}},
			MonitorsMeasured: true,
			Trials:           []EncodeTrial{okTrial("процессор", "Microsoft H.264 Encoder MFT", false, p720(), "системная память")},
			Recommendation:   "на этой машине будет программный кодер",
		},
	}
	text := rep.String()
	for _, want := range []string{
		"Система: Windows 10 Pro N 22H2", "N/KN: ДА", "Архитектура: программа x64", "логических ядер: 4", "Память: всего 8000 МБ",
		"Питание: от батареи",
		"Media Foundation: MFStartup выполнен успешно",
		"Программный кодер: Microsoft H.264 Encoder MFT НЕ создаётся",
		"Basic Display/Render Driver", "D3D11CreateDevice с поддержкой видео: ошибка 0x887A0004", "без неё: успешно (уровень 11_1)",
		"HDR ВКЛЮЧЁН", "формат первого кадра: R16G16B16A16_FLOAT", "повторов 60%", "создано без поддержки видео",
		"720p30", "ПРОШЛА",
		"Сеть и звук:", "ИТОГ: на этой машине будет программный кодер",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
}

func TestLinesMediaFoundationMissing(t *testing.T) {
	rep := Report{At: time.Now(), Pref: "-", MFErr: errors.New("Media Foundation недоступна: mfplat.dll"), System: SystemInfo{IsN: true}}
	text := rep.String()
	for _, want := range []string{"MFStartup НЕ удался", "Пакет мультимедийных функций", "Программный кодер: проверить нельзя"} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, text)
		}
	}
}

func TestOptionsDefaults(t *testing.T) {
	var o Options
	if o.encodeProbe() != 2*time.Second || o.measure() != 5*time.Second {
		t.Error("значения по умолчанию: 2 с и 5 с")
	}
	o = Options{EncodeProbe: -1, MeasureCapture: -1}
	if o.encodeProbe() >= 0 || o.measure() >= 0 {
		t.Error("отрицательные значения означают «пропустить»")
	}
}

func TestCollectNetworkStripsPort(t *testing.T) {
	n := collectNetwork("127.0.0.1:7000")
	if n.Target != "127.0.0.1" || !n.Peer {
		t.Errorf("цель %q peer=%v", n.Target, n.Peer)
	}
	if n.Err != nil {
		t.Fatalf("маршрут к 127.0.0.1: %v", n.Err)
	}
}

func TestIsDuplicateAdapter(t *testing.T) {
	out := []dxgi.OutputDetail{{Name: `\.\DISPLAY1`}}
	list := []dxgi.AdapterInfo{
		{Name: "NVIDIA GeForce RTX 2080 Ti", VendorID: 0x10DE, DeviceID: 0x1E07, Outputs: out},
		{Name: "NVIDIA GeForce RTX 2080 Ti", VendorID: 0x10DE, DeviceID: 0x1E07}, // копия из сеанса RDP
		{Name: "Intel(R) UHD Graphics", VendorID: 0x8086, DeviceID: 0x3E92},
		{Name: "NVIDIA GeForce RTX 2080 Ti", VendorID: 0x10DE, DeviceID: 0x1E07, Outputs: out}, // вторая настоящая карта с монитором
	}
	want := []bool{false, true, false, false}
	for i, w := range want {
		if got := isDuplicateAdapter(list, i); got != w {
			t.Errorf("адаптер %d: %v, ждали %v", i, got, w)
		}
	}
}

func TestTrialLinesRejectedOnlyWhenSettingsMissing(t *testing.T) {
	tr := okTrial("GPU", "X", true, p720(), "системная память")
	tr.Report.Rejected = []mf.ParamRejection{{Name: "AVEncCommonLowLatency", HR: 0x80070057}, {Name: "AVEncCommonRealTime"}}
	tr.Report.Settings = "ICodecAPI: RateControl=CBR; не принято: AVEncCommonLowLatency (0x80070057)"
	if n := len(tr.lines()); n != 1 {
		t.Fatalf("при прочитанных параметрах отвергнутые уже в Summary: %d строк", n)
	}
	tr.Report.Settings = ""
	lines := tr.lines()
	if len(lines) != 3 || !strings.Contains(lines[1], "0x80070057") || !strings.Contains(lines[2], "IsSupported ответил «нет»") {
		t.Fatalf("строки: %q", lines)
	}
}
