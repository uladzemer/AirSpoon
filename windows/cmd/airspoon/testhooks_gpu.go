//go:build testhooks

package main

import (
	"context"
	"os"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/gpupref"
	"airspoon/internal/win/com"
)

// Проверка перезапуска при DXGI_ERROR_UNSUPPORTED на машине без гибридных
// видеокарт (настоящий захват на такой машине не воспроизвести):
//
//	AIRSPOON_TEST_ADAPTERS=hybrid
//	    программа считает, что в системе Intel (к ней подключён экран) и NVIDIA;
//	AIRSPOON_TEST_GPUPREF_KEY=Software\AirSpoonTest\GpuPref
//	    предпочтения видеокарты пишутся в этот раздел HKCU, а не в настоящий
//	    UserGpuPreferences;
//	AIRSPOON_TEST_UNSUPPORTED=1
//	    первый показ завершается ошибкой DXGI_ERROR_UNSUPPORTED (после
//	    перезапуска показ идёт нормально).
const (
	envTestAdapters    = "AIRSPOON_TEST_ADAPTERS"
	envTestGPUKey      = "AIRSPOON_TEST_GPUPREF_KEY"
	envTestUnsupported = "AIRSPOON_TEST_UNSUPPORTED"
)

func fakeAdapters(kind string) []dxgi.AdapterInfo {
	nvidia := dxgi.AdapterInfo{Index: 0, Name: "NVIDIA GeForce RTX 3060 Laptop GPU", VendorID: 0x10DE, DeviceID: 0x2560, DedicatedVRAM: 6 << 30}
	intel := dxgi.AdapterInfo{Index: 1, Name: "Intel(R) UHD Graphics", VendorID: 0x8086, DeviceID: 0x9A60, DedicatedVRAM: 128 << 20,
		Outputs: []dxgi.OutputDetail{{Name: `\\.\DISPLAY1`, Attached: true, GDIAdapter: "Intel(R) UHD Graphics", Primary: true}}}
	if kind == "single" {
		return []dxgi.AdapterInfo{nvidia}
	}
	return []dxgi.AdapterInfo{nvidia, intel}
}

func overrideGPUEnv(e *gpuEnv) {
	if key := os.Getenv(envTestGPUKey); key != "" {
		e.store = gpupref.RegistryStore{Key: key}
	}
	if kind := os.Getenv(envTestAdapters); kind != "" {
		list := fakeAdapters(kind)
		e.adapters = func() ([]dxgi.AdapterInfo, error) { return list, nil }
	}
}

func wrapTestRun(run func(context.Context, mirrorOptions) error) func(context.Context, mirrorOptions) error {
	if os.Getenv(envTestUnsupported) != "1" || os.Getenv(envGPURetry) == "1" {
		return run
	}
	return func(ctx context.Context, o mirrorOptions) error {
		return captureError{&dxgi.UnsupportedError{Adapter: "NVIDIA GeForce RTX 3060 Laptop GPU", Monitor: `\\.\DISPLAY1`,
			Err: &com.Error{Op: "DuplicateOutput", HR: 0x887A0004}}}
	}
}
