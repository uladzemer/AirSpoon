//go:build windows && (amd64 || arm64)

package dxgi

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/win/com"
)

// Коды ошибок DXGI и Win32, которые мы различаем.
const (
	hrAccessLost            uint32 = 0x887A0026 // DXGI_ERROR_ACCESS_LOST
	hrWaitTimeout           uint32 = 0x887A0027 // DXGI_ERROR_WAIT_TIMEOUT
	hrNotCurrentlyAvailable uint32 = 0x887A0022 // DXGI_ERROR_NOT_CURRENTLY_AVAILABLE
	hrUnsupported           uint32 = 0x887A0004 // DXGI_ERROR_UNSUPPORTED
	hrDeviceRemoved         uint32 = 0x887A0005 // DXGI_ERROR_DEVICE_REMOVED
	hrDeviceReset           uint32 = 0x887A0007 // DXGI_ERROR_DEVICE_RESET
	hrSessionDisconnected   uint32 = 0x887A0028 // DXGI_ERROR_SESSION_DISCONNECTED
	hrNotFound              uint32 = 0x887A0002 // DXGI_ERROR_NOT_FOUND
	hrAccessDenied          uint32 = 0x80070005 // E_ACCESSDENIED
	dxgiFormatB8G8R8A8UNorm        = 87
	dxgiFormatNV12                 = 103
	d3d11UsageDefault              = 0
	d3d11UsageStaging              = 3
	d3d11BindShaderResource        = 0x8
	d3d11BindRenderTarget          = 0x20
	d3d11CPUAccessRead             = 0x20000
	d3d11MapRead                   = 1
	d3d11CreateBGRASupport         = 0x20
	d3d11CreateVideoSupport        = 0x800
	d3d11SDKVersion                = 7
	d3dDriverTypeUnknown           = 0
	d3dDriverTypeHardware          = 1
)

// Слоты таблиц методов (см. заголовки d3d11.h, dxgi.h, dxgi1_2.h): сначала три
// метода IUnknown, затем унаследованные, затем собственные в порядке объявления.
const (
	// IDXGIFactory1
	slotFactoryEnumAdapters1 = 12
	// IDXGIAdapter
	slotAdapterEnumOutputs = 7
	slotAdapterGetDesc     = 8
	// IDXGIOutput
	slotOutputGetDesc = 7
	// IDXGIOutput1
	slotOutput1DuplicateOutput = 22
	// IDXGIOutputDuplication
	slotDupGetDesc            = 7
	slotDupAcquireNextFrame   = 8
	slotDupGetFramePtrShape   = 11
	slotDupReleaseFrame       = 14
	slotDeviceCreateTexture2D = 5
	// ID3D11Device
	slotDeviceGetRemovedReason = 39
	// ID3D11Texture2D
	slotTextureGetDesc = 10
	// ID3D11DeviceContext
	slotCtxMap                  = 14
	slotCtxUnmap                = 15
	slotCtxCopySubresourceRegn  = 46
	slotCtxCopyResource         = 47
	slotCtxUpdateSubresource    = 48
	slotCtxFlush                = 111
	slotMultithreadSetProtected = 5 // ID3D10Multithread
)

var (
	d3d11DLL               = windows.NewLazySystemDLL("d3d11.dll")
	procD3D11CreateDevice  = d3d11DLL.NewProc("D3D11CreateDevice")
	dxgiDLL                = windows.NewLazySystemDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
	iidFactory1            = com.ParseGUID("770aae78-f26f-4dba-a829-253c83d1b387")
	iidOutput1             = com.ParseGUID("00cddea8-939b-4b83-a340-a685226666cc")
	iidDXGIDevice          = com.ParseGUID("54ec77fa-1377-44e6-8c32-88fd5f44c84c")
	iidTexture2D           = com.ParseGUID("6f15aaf2-d208-4e89-9ab4-489535d34f9c")
	iidVideoDevice         = com.ParseGUID("10ec4d5b-975a-4689-b9e4-d0aac30fe333")
	iidVideoContext        = com.ParseGUID("61f21c45-3c0e-4a74-9cea-67100d9ad5e4")
	iidMultithread         = com.ParseGUID("9b7e4e00-342c-4106-a19f-4f2704f689f0")
	errNoDisplay           = errors.New("монитор не найден")
	errDeviceLost          = errors.New("устройство D3D11 потеряно")
	ErrStopped             = errors.New("захват остановлен")
	// ErrDeviceLost — устройство D3D11 потеряно, источник нужно создать заново.
	ErrDeviceLost = errDeviceLost
)

type rect32 struct{ Left, Top, Right, Bottom int32 }

type box32 struct{ Left, Top, Front, Right, Bottom, Back uint32 }

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize uint32
	Format                              uint32
	SampleCount, SampleQuality          uint32
	Usage, BindFlags, CPUAccessFlags    uint32
	MiscFlags                           uint32
}

type mappedSubresource struct {
	Data       unsafe.Pointer
	RowPitch   uint32
	DepthPitch uint32
}

type outputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates rect32
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type adapterDesc struct {
	Description           [128]uint16
	VendorID, DeviceID    uint32
	SubSysID, Revision    uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	LuidLow               uint32
	LuidHigh              int32
}

type outduplFrameInfo struct {
	LastPresentTime       int64
	LastMouseUpdateTime   int64
	AccumulatedFrames     uint32
	RectsCoalesced        int32
	ProtectedContentMask  int32
	PointerX, PointerY    int32
	PointerVisible        int32
	TotalMetadataBufSize  uint32
	PointerShapeBufferLen uint32
}

// DXGI_OUTDUPL_DESC, including the 28-byte DXGI_MODE_DESC.
type outduplDesc struct {
	Width, Height                     uint32
	RefreshNum, RefreshDen            uint32
	Format, ScanlineOrdering, Scaling uint32
	Rotation                          uint32
	DesktopImageInSystemMemory        int32
}

type outduplPointerShapeInfo struct {
	Type, Width, Height, Pitch uint32
	HotX, HotY                 int32
}

// OutputInfo описывает монитор, доступный для захвата.
type OutputInfo struct {
	Index       int    // номер в списке показа: 0 — основной монитор GDI, далее остальные подключённые к рабочему столу (отключённые выходы не нумеруются)
	AdapterName string // например "NVIDIA GeForce RTX 2080 Ti"
	AdapterLUID LUID   // локальный идентификатор этой видеокарты
	Name        string // например \\.\DISPLAY1
	Bounds      Rect   // положение на виртуальном рабочем столе
	Rotation    uint32
	Attached    bool // входит в рабочий стол
	Primary     bool // основной монитор по данным GDI
}

// foundOutput — найденный выход вместе с живыми COM-объектами.
type foundOutput struct {
	info    OutputInfo
	adapter *com.Object
	output  *com.Object // IDXGIOutput
}

func (f *foundOutput) release() {
	if f == nil {
		return
	}
	f.output.Release()
	f.adapter.Release()
}

func createFactory() (*com.Object, error) {
	if err := procCreateDXGIFactory1.Find(); err != nil {
		return nil, fmt.Errorf("загрузка CreateDXGIFactory1: %w", err)
	}
	var f *com.Object
	hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidFactory1)), uintptr(unsafe.Pointer(&f)))
	if err := com.Check("CreateDXGIFactory1", hr); err != nil {
		return nil, err
	}
	return f, nil
}

// outputCand — выход видеокарты, как его видит упорядочивание списка мониторов.
type outputCand struct {
	Name     string
	Attached bool // входит в рабочий стол
	Primary  bool // основной монитор по данным GDI
}

// orderOutputs возвращает индексы кандидатов в порядке показа: основной монитор
// GDI первым, затем остальные подключённые к рабочему столу в порядке
// перечисления. Выходы, не подключённые к рабочему столу, пропускаются: их
// дублировать нельзя, а номер 0 на них давал чёрный кадр. Если подключённых нет
// вовсе (сеанс, где AttachedToDesktop не заполняется), берутся все по порядку,
// чтобы не остаться без монитора.
func orderOutputs(c []outputCand) []int {
	var order []int
	for i, o := range c {
		if o.Attached {
			order = append(order, i)
		}
	}
	if len(order) == 0 {
		for i := range c {
			order = append(order, i)
		}
	}
	for pos, i := range order {
		if c[i].Primary {
			copy(order[1:pos+1], order[:pos])
			order[0] = i
			break
		}
	}
	return order
}

// enumOutputs перебирает выходы всех адаптеров в порядке orderOutputs. visit
// возвращает true, если нужный выход найден: тогда его COM-объекты
// передаются вызывающему, иначе освобождаются.
func enumOutputs(visit func(f *foundOutput) bool) (*foundOutput, error) {
	factory, err := createFactory()
	if err != nil {
		return nil, err
	}
	defer factory.Release()
	gdi := gdiDisplays()
	var all []*foundOutput
	defer func() {
		for _, f := range all {
			f.release()
		}
	}()
	for ai := uint32(0); ; ai++ {
		var adapter *com.Object
		hr := factory.Call(slotFactoryEnumAdapters1, uintptr(ai), uintptr(unsafe.Pointer(&adapter)))
		if uint32(hr) == hrNotFound {
			break
		}
		if err := com.Check("EnumAdapters1", hr); err != nil {
			return nil, err
		}
		var ad adapterDesc
		adapter.Call(slotAdapterGetDesc, uintptr(unsafe.Pointer(&ad)))
		adapterName := windows.UTF16ToString(ad.Description[:])
		for oi := uint32(0); ; oi++ {
			var output *com.Object
			hr := adapter.Call(slotAdapterEnumOutputs, uintptr(oi), uintptr(unsafe.Pointer(&output)))
			if uint32(hr) == hrNotFound {
				break
			}
			if com.Check("EnumOutputs", hr) != nil {
				break
			}
			var od outputDesc
			output.Call(slotOutputGetDesc, uintptr(unsafe.Pointer(&od)))
			b := od.DesktopCoordinates
			name := windows.UTF16ToString(od.DeviceName[:])
			adapter.AddRef()
			all = append(all, &foundOutput{
				info: OutputInfo{
					AdapterName: adapterName,
					AdapterLUID: LUID{Low: ad.LuidLow, High: ad.LuidHigh},
					Name:        name,
					Bounds:      Rect{X: int(b.Left), Y: int(b.Top), W: int(b.Right - b.Left), H: int(b.Bottom - b.Top)},
					Rotation:    od.Rotation,
					Attached:    od.AttachedToDesktop != 0,
					Primary:     gdi[name].primary,
				},
				adapter: adapter,
				output:  output,
			})
		}
		adapter.Release()
	}
	cands := make([]outputCand, len(all))
	for i, f := range all {
		cands[i] = outputCand{Name: f.info.Name, Attached: f.info.Attached, Primary: f.info.Primary}
	}
	for pos, i := range orderOutputs(cands) {
		f := all[i]
		f.info.Index = pos
		if visit(f) {
			all[i] = nil // владение COM-объектами переходит вызывающему
			return f, nil
		}
	}
	return nil, nil
}

// ListOutputs возвращает все мониторы, к которым можно применить захват.
func ListOutputs() ([]OutputInfo, error) {
	cleanup, err := com.Init()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var list []OutputInfo
	_, err = enumOutputs(func(f *foundOutput) bool {
		list = append(list, f.info)
		return false
	})
	return list, err
}

func findOutput(display int) (*foundOutput, error) {
	f, err := enumOutputs(func(f *foundOutput) bool { return f.info.Index == display })
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("%w: номер %d", errNoDisplay, display)
	}
	return f, nil
}

// findOutputByIdentity ищет выход по имени монитора и LUID видеокарты, а не по
// номеру: номера сдвигаются, когда монитор отключают или меняют основной (Win+P),
// а дублирование должно открываться на том же мониторе и той же карте, на
// которой создано устройство D3D11.
func findOutputByIdentity(name string, luid LUID) (*foundOutput, error) {
	f, err := enumOutputs(func(f *foundOutput) bool {
		return f.info.Name == name && (luid.IsZero() || f.info.AdapterLUID == luid)
	})
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("%w: %s (видеокарта %s)", errNoDisplay, name, luid)
	}
	return f, nil
}

// deviceCreateResult — как создавалось устройство D3D11.
type deviceCreateResult struct {
	// NoVideo — устройство создано без D3D11_CREATE_DEVICE_VIDEO_SUPPORT:
	// видеопроцессора GPU на нём не будет, преобразование цвета идёт на CPU.
	NoVideo bool
	// VideoHR — исходный HRESULT попытки с VIDEO_SUPPORT (0 — она удалась).
	VideoHR uint32
	// FeatureLevel — уровень возможностей созданного устройства.
	FeatureLevel uint32
}

// createWithFallback пробует создать устройство с поддержкой видео, а при любой
// ошибке повторяет без неё. Возвращает итог и HRESULT последней попытки.
// Видео не умеет адаптер без драйвера видеокарты (Microsoft Basic Display
// Driver, когда драйвер Intel/AMD не установлен или сломан), но бывают и другие
// отказы (E_INVALIDARG, E_FAIL, ошибки старых драйверов): захват работает и без
// видео, преобразование цвета уходит на CPU.
func createWithFallback(try func(flags uintptr) (hr uintptr, featureLevel uint32)) (deviceCreateResult, uintptr) {
	hr, fl := try(d3d11CreateBGRASupport | d3d11CreateVideoSupport)
	if int32(hr) >= 0 {
		return deviceCreateResult{FeatureLevel: fl}, hr
	}
	res := deviceCreateResult{NoVideo: true, VideoHR: uint32(hr)}
	hr, fl = try(d3d11CreateBGRASupport)
	res.FeatureLevel = fl
	return res, hr
}

// createDevice создаёт устройство D3D11 на адаптере выхода с поддержкой BGRA и видео.
func createDevice(adapter *com.Object) (device, ctx *com.Object, err error) {
	device, ctx, _, err = createDeviceInfo(adapter)
	return device, ctx, err
}

// createDeviceInfo — createDevice с описанием того, как устройство создано.
func createDeviceInfo(adapter *com.Object) (device, ctx *com.Object, res deviceCreateResult, err error) {
	if err := procD3D11CreateDevice.Find(); err != nil {
		return nil, nil, res, fmt.Errorf("загрузка D3D11CreateDevice: %w", err)
	}
	res, hr := createWithFallback(func(flags uintptr) (uintptr, uint32) {
		var featureLevel uint32
		device, ctx = nil, nil
		hr, _, _ := procD3D11CreateDevice.Call(
			uintptr(unsafe.Pointer(adapter)), d3dDriverTypeUnknown, 0, flags,
			0, 0, d3d11SDKVersion,
			uintptr(unsafe.Pointer(&device)), uintptr(unsafe.Pointer(&featureLevel)), uintptr(unsafe.Pointer(&ctx)))
		return hr, featureLevel
	})
	if err := com.Check("D3D11CreateDevice", hr); err != nil {
		if res.NoVideo {
			return nil, nil, res, fmt.Errorf("%w (с поддержкой видео: HRESULT 0x%08X)", err, res.VideoHR)
		}
		return nil, nil, res, err
	}
	// MFT может обращаться к устройству из своих потоков.
	if mt, qerr := device.QueryInterface(&iidMultithread); qerr == nil {
		mt.Call(slotMultithreadSetProtected, 1)
		mt.Release()
	}
	return device, ctx, res, nil
}

func createTexture2D(device *com.Object, d *texture2DDesc) (*com.Object, error) {
	var t *com.Object
	hr := device.Call(slotDeviceCreateTexture2D, uintptr(unsafe.Pointer(d)), 0, uintptr(unsafe.Pointer(&t)))
	if err := com.Check("CreateTexture2D", hr); err != nil {
		return nil, err
	}
	return t, nil
}
