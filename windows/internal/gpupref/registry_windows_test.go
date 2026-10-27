//go:build windows

package gpupref

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// Настоящий реестр, но в отдельном разделе HKCU, который тест сам создаёт и удаляет:
// настоящие предпочтения видеокарты пользователя не затрагиваются.
func TestRegistryStoreRoundTrip(t *testing.T) {
	const key = `Software\AirSpoonTest\GpuPref`
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, key)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\AirSpoonTest`)
	})
	st := RegistryStore{Key: key}
	const exe = `C:\Test Dir\AirSpoonSender.exe`
	if _, ok, err := st.Get(exe); ok || err != nil {
		t.Fatalf("раздела нет: ok=%v err=%v", ok, err)
	}
	if err := st.Set(exe, "GpuPreference=1;"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get(exe)
	if err != nil || !ok || got != "GpuPreference=1;" {
		t.Fatalf("Get: %q %v %v", got, ok, err)
	}
	// Тот же путь в другом регистре — то же значение, дубль не создаётся.
	if err := st.Set(`c:\test dir\airspoonsender.exe`, "GpuPreference=2;"); err != nil {
		t.Fatal(err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	if len(names) != 1 {
		t.Fatalf("значений в разделе %d (%v), ожидали одно", len(names), names)
	}
	if v, _, _ := k.GetStringValue(names[0]); v != "GpuPreference=2;" {
		t.Fatalf("данные: %q", v)
	}
	p, set, err := Current(st, exe)
	if err != nil || !set || p != HighPerformance {
		t.Fatalf("Current: %v %v %v", p, set, err)
	}
}
