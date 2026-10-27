//go:build windows && (amd64 || arm64)

package dxgi

import (
	"runtime"
	"testing"
	"unsafe"

	"airspoon/internal/win/com"
)

// На адаптере без драйвера видеокарты (Microsoft Basic Render Driver) устройство
// с поддержкой видео не создаётся (DXGI_ERROR_UNSUPPORTED); createDevice должна
// создать его без неё. Так бывает на ноутбуке, где драйвер Intel не установлен
// и монитор обслуживает Microsoft Basic Display Driver.
func TestCreateDeviceOnBasicRenderAdapter(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	factory, err := createFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Release()
	for ai := uint32(0); ; ai++ {
		var adapter *com.Object
		hr := factory.Call(slotFactoryEnumAdapters1, uintptr(ai), uintptr(unsafe.Pointer(&adapter)))
		if uint32(hr) == hrNotFound {
			break
		}
		if err := com.Check("EnumAdapters1", hr); err != nil {
			t.Fatal(err)
		}
		var ad adapterDesc1
		adapter.Call(slotAdapter1GetDesc1, uintptr(unsafe.Pointer(&ad)))
		if ad.VendorID != 0x1414 {
			adapter.Release()
			continue
		}
		device, ctx, err := createDevice(adapter)
		adapter.Release()
		if err != nil {
			t.Fatalf("устройство на Microsoft Basic Render Driver не создано: %v", err)
		}
		ctx.Release()
		device.Release()
		return
	}
	t.Skip("адаптера Microsoft Basic Render Driver нет")
}
