//go:build windows && (amd64 || arm64)

package gpudiag

import (
	"errors"
	"strings"
	"testing"
	"time"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/win/com"
)

type memStore map[string]string

func (m memStore) Get(exe string) (string, bool, error) { v, ok := m[exe]; return v, ok, nil }
func (m memStore) Set(exe, data string) error           { m[exe] = data; return nil }

func TestCollectReportsAdaptersEncodersAndPreference(t *testing.T) {
	const exe = `C:\Apps\AirSpoonSender.exe`
	st := memStore{exe: "AutoHDREnable=0;GpuPreference=1;"}
	rep := Collect(Options{ExePath: exe, Store: st, Probe: 300 * time.Millisecond, RetryEnv: "1"})
	text := rep.String()
	t.Log("\n" + text)
	for _, want := range []string{
		"Диагностика видеокарт AirSpoon Sender",
		exe,
		"GpuPreference=1;",
		"энергосбережение",
		"AIRSPOON_GPU_RETRY",
		"Аппаратные кодеры H.264",
		"Программный кодер",
		"Проба дублирования монитора 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
	if !rep.Retried {
		t.Error("RetryEnv=1 должен давать Retried")
	}
	if rep.AdaptersE == nil && len(rep.Adapters) > 0 {
		for _, a := range rep.Adapters {
			if !strings.Contains(text, a.Name) || !strings.Contains(text, a.LUID.String()) {
				t.Errorf("в отчёте нет видеокарты %q с LUID %s", a.Name, a.LUID)
			}
		}
	}
}

func TestReportWithoutPreferenceAndProbe(t *testing.T) {
	rep := Collect(Options{ExePath: `C:\Apps\x.exe`, Store: memStore{}})
	if rep.Probe != nil {
		t.Fatal("проба не запрашивалась")
	}
	if !strings.Contains(rep.String(), "не задано") {
		t.Fatalf("предпочтение не задано, а в отчёте: %s", rep.String())
	}
}

// Строки отчёта о неудачной пробе содержат HRESULT и пояснение про видеокарты.
func TestLinesDescribeFailedProbe(t *testing.T) {
	probeErr := &com.Error{Op: "DuplicateOutput", HR: 0x887A0004}
	rep := Report{
		At: time.Now(), Exe: `C:\Apps\x.exe`, Pref: "не задано",
		Probe: &dxgi.ProbeResult{Display: 0, Monitor: `\\.\DISPLAY1`, Adapter: "NVIDIA GeForce", DeviceAdapter: "NVIDIA GeForce",
			Attempts: 4, Elapsed: time.Second, Err: probeErr},
	}
	text := rep.String()
	for _, want := range []string{`\\.\DISPLAY1`, "0x887A0004", "не открылось", "двумя видеокартами"} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, text)
		}
	}
	rep.Probe = &dxgi.ProbeResult{Display: 3, Err: errors.New("монитор не найден")}
	if !strings.Contains(rep.String(), "монитор не найден") {
		t.Errorf("проба без монитора:\n%s", rep.String())
	}
}
