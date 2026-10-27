//go:build windows && (amd64 || arm64)

package dxgi

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/gpupref"
	"airspoon/internal/win/com"
)

// Описание видеокарт, мониторов и попытки открыть дублирование для диагностики
// (команда diag, журнал при ошибке захвата) и выбора видеокарты на гибридных
// ноутбуках (Intel + NVIDIA).

// slotAdapter1GetDesc1 — IDXGIAdapter1::GetDesc1 (после трёх методов IUnknown,
// четырёх методов IDXGIObject и трёх методов IDXGIAdapter).
const slotAdapter1GetDesc1 = 10

// slotDXGIDeviceGetAdapter — IDXGIDevice::GetAdapter.
const slotDXGIDeviceGetAdapter = 7

const (
	dxgiAdapterFlagSoftware = 2
	displayDeviceAttached   = 0x1
	displayDevicePrimary    = 0x4
)

var (
	user32DLL               = windows.NewLazySystemDLL("user32.dll")
	procEnumDisplayDevicesW = user32DLL.NewProc("EnumDisplayDevicesW")
	procGetSystemMetrics    = user32DLL.NewProc("GetSystemMetrics")
)

// smRemoteSession — SM_REMOTESESSION: процесс работает в сеансе удалённого стола.
const smRemoteSession = 0x1000

// isRemoteSession сообщает, что процесс работает в сеансе RDP.
func isRemoteSession() bool {
	r, _, _ := procGetSystemMetrics.Call(smRemoteSession)
	return r != 0
}

// LUID — локальный идентификатор видеокарты (действует до перезагрузки).
type LUID struct {
	Low  uint32
	High int32
}

func (l LUID) String() string { return fmt.Sprintf("%08X:%08X", uint32(l.High), l.Low) }

// IsZero — идентификатор не задан.
func (l LUID) IsZero() bool { return l.Low == 0 && l.High == 0 }

// OutputDetail — монитор (выход) видеокарты.
type OutputDetail struct {
	Name     string // \\.\DISPLAY1
	Bounds   Rect
	Attached bool // входит в рабочий стол
	Rotation uint32
	// GDIAdapter — видеокарта, к которой монитор подключён по данным GDI
	// (EnumDisplayDevices). Процесс на «чужой» видеокарте видит выход под собой,
	// а настоящую принадлежность показывает только это поле.
	GDIAdapter string
	Primary    bool
	// ColorSpace — DXGI_COLOR_SPACE_TYPE монитора по IDXGIOutput6::GetDesc1;
	// ColorSpaceKnown ложно, если интерфейса нет (Windows старше 10 1703) или вызов не удался.
	ColorSpace      int32
	ColorSpaceKnown bool
	BitsPerColor    uint32
	RefreshHz       int // текущая частота обновления по GDI (0 — не определена)
}

// ColorSpaceName — читаемое название DXGI_COLOR_SPACE_TYPE.
func ColorSpaceName(cs int32) string {
	switch cs {
	case 0:
		return "RGB_FULL_G22_NONE_P709 (SDR)"
	case 1:
		return "RGB_FULL_G10_NONE_P709 (scRGB, HDR)"
	case 2:
		return "RGB_STUDIO_G22_NONE_P709 (SDR)"
	case 12:
		return "RGB_FULL_G2084_NONE_P2020 (HDR10)"
	case 13, 14, 16:
		return fmt.Sprintf("G2084_P2020 код %d (HDR10)", cs)
	}
	return fmt.Sprintf("код %d", cs)
}

// ColorSpaceIsHDR — цветовое пространство монитора относится к HDR
// (PQ/ST.2084 или линейное scRGB).
func ColorSpaceIsHDR(cs int32) bool {
	switch cs {
	case 1, 12, 13, 14, 16:
		return true
	}
	return false
}

// outputDesc1 — DXGI_OUTPUT_DESC1.
type outputDesc1 struct {
	DeviceName                 [32]uint16
	DesktopCoordinates         rect32
	AttachedToDesktop          int32
	Rotation                   uint32
	Monitor                    uintptr
	BitsPerColor               uint32
	ColorSpace                 int32
	RedPrimary, GreenPrimary   [2]float32
	BluePrimary, WhitePoint    [2]float32
	MinLuminance, MaxLuminance float32
	MaxFullFrameLuminance      float32
}

// slotOutput6GetDesc1 — IDXGIOutput6::GetDesc1 (после IDXGIOutput1: 19..22,
// IDXGIOutput2: SupportsOverlays 23, Output3: 24, Output4: 25, Output5: 26).
const slotOutput6GetDesc1 = 27

var iidOutput6 = com.ParseGUID("068346e8-aaec-4b84-add7-137f513f77a1")

// outputColorSpace читает цветовое пространство выхода (IDXGIOutput6::GetDesc1).
func outputColorSpace(output *com.Object) (cs int32, bits uint32, ok bool) {
	o6, err := output.QueryInterface(&iidOutput6)
	if err != nil {
		return 0, 0, false
	}
	defer o6.Release()
	var d outputDesc1
	if int32(o6.Call(slotOutput6GetDesc1, uintptr(unsafe.Pointer(&d)))) < 0 {
		return 0, 0, false
	}
	return d.ColorSpace, d.BitsPerColor, true
}

// devMode — DEVMODEW (220 байт); нужны только размер и частота.
type devMode struct {
	DeviceName         [32]uint16
	SpecVersion        uint16
	DriverVersion      uint16
	Size               uint16
	DriverExtra        uint16
	Fields             uint32
	PositionX          int32
	PositionY          int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

var procEnumDisplaySettingsW = user32DLL.NewProc("EnumDisplaySettingsW")

// refreshRate возвращает текущую частоту обновления монитора name по GDI.
func refreshRate(name string) int {
	if procEnumDisplaySettingsW.Find() != nil {
		return 0
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	var dm devMode
	dm.Size = uint16(unsafe.Sizeof(dm))
	const enumCurrentSettings = 0xFFFFFFFF // ENUM_CURRENT_SETTINGS = (DWORD)-1
	r, _, _ := procEnumDisplaySettingsW.Call(uintptr(unsafe.Pointer(p)), enumCurrentSettings, uintptr(unsafe.Pointer(&dm)))
	if r == 0 {
		return 0
	}
	return int(dm.DisplayFrequency)
}

// AdapterInfo — видеокарта и её выходы в порядке перечисления EnumAdapters1.
type AdapterInfo struct {
	Index         int
	Name          string
	VendorID      uint32
	DeviceID      uint32
	LUID          LUID
	Flags         uint32 // DXGI_ADAPTER_FLAG
	DedicatedVRAM uint64
	SharedMemory  uint64
	Outputs       []OutputDetail
}

// Software — программный адаптер (Microsoft Basic Render Driver).
func (a AdapterInfo) Software() bool { return a.Flags&dxgiAdapterFlagSoftware != 0 }

type adapterDesc1 struct {
	adapterDesc
	Flags uint32
}

type displayDevice struct {
	Cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

type gdiDisplay struct {
	adapter string
	primary bool
}

// gdiDisplays возвращает для каждого \\.\DISPLAYn название видеокарты по GDI.
func gdiDisplays() map[string]gdiDisplay {
	out := map[string]gdiDisplay{}
	if procEnumDisplayDevicesW.Find() != nil {
		return out
	}
	for i := uint32(0); i < 64; i++ {
		var dd displayDevice
		dd.Cb = uint32(unsafe.Sizeof(dd))
		r, _, _ := procEnumDisplayDevicesW.Call(0, uintptr(i), uintptr(unsafe.Pointer(&dd)), 0)
		if r == 0 {
			break
		}
		out[windows.UTF16ToString(dd.DeviceName[:])] = gdiDisplay{
			adapter: windows.UTF16ToString(dd.DeviceString[:]),
			primary: dd.StateFlags&displayDevicePrimary != 0,
		}
	}
	return out
}

// ListAdapters перечисляет видеокарты и мониторы, которые видит этот процесс.
func ListAdapters() ([]AdapterInfo, error) {
	cleanup, err := com.Init()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	factory, err := createFactory()
	if err != nil {
		return nil, err
	}
	defer factory.Release()
	gdi := gdiDisplays()
	var list []AdapterInfo
	for ai := uint32(0); ; ai++ {
		var adapter *com.Object
		hr := factory.Call(slotFactoryEnumAdapters1, uintptr(ai), uintptr(unsafe.Pointer(&adapter)))
		if uint32(hr) == hrNotFound {
			break
		}
		if err := com.Check("EnumAdapters1", hr); err != nil {
			return list, err
		}
		info := describeAdapter(adapter, int(ai), gdi)
		adapter.Release()
		list = append(list, info)
	}
	return list, nil
}

// describeAdapter читает описание адаптера и перечисляет его выходы.
func describeAdapter(adapter *com.Object, index int, gdi map[string]gdiDisplay) AdapterInfo {
	var ad adapterDesc1
	if hr := adapter.Call(slotAdapter1GetDesc1, uintptr(unsafe.Pointer(&ad))); int32(hr) < 0 {
		adapter.Call(slotAdapterGetDesc, uintptr(unsafe.Pointer(&ad.adapterDesc)))
	}
	info := AdapterInfo{
		Index:         index,
		Name:          windows.UTF16ToString(ad.Description[:]),
		VendorID:      ad.VendorID,
		DeviceID:      ad.DeviceID,
		LUID:          LUID{Low: ad.LuidLow, High: ad.LuidHigh},
		Flags:         ad.Flags,
		DedicatedVRAM: uint64(ad.DedicatedVideoMemory),
		SharedMemory:  uint64(ad.SharedSystemMemory),
	}
	for oi := uint32(0); ; oi++ {
		var output *com.Object
		hr := adapter.Call(slotAdapterEnumOutputs, uintptr(oi), uintptr(unsafe.Pointer(&output)))
		if uint32(hr) == hrNotFound || int32(hr) < 0 {
			break
		}
		var od outputDesc
		output.Call(slotOutputGetDesc, uintptr(unsafe.Pointer(&od)))
		cs, bits, csOK := outputColorSpace(output)
		output.Release()
		b := od.DesktopCoordinates
		name := windows.UTF16ToString(od.DeviceName[:])
		g := gdi[name]
		info.Outputs = append(info.Outputs, OutputDetail{
			Name:            name,
			Bounds:          Rect{X: int(b.Left), Y: int(b.Top), W: int(b.Right - b.Left), H: int(b.Bottom - b.Top)},
			Attached:        od.AttachedToDesktop != 0,
			Rotation:        od.Rotation,
			GDIAdapter:      g.adapter,
			Primary:         g.primary,
			ColorSpace:      cs,
			ColorSpaceKnown: csOK,
			BitsPerColor:    bits,
			RefreshHz:       refreshRate(name),
		})
	}
	return info
}

// OwnerOfMonitor возвращает название видеокарты, к которой по данным GDI
// подключён монитор с именем monitor (\.\DISPLAY1); если имени нет — монитора,
// который захват берёт по умолчанию (основной монитор GDI, иначе первый
// подключённый к рабочему столу). Пусто — определить не удалось.
func OwnerOfMonitor(list []AdapterInfo, monitor string) string {
	first, primary := "", ""
	havePrimary := false
	for _, a := range list {
		for _, o := range a.Outputs {
			if monitor != "" && o.Name == monitor {
				return o.GDIAdapter
			}
			if !o.Attached {
				continue
			}
			if first == "" {
				first = o.GDIAdapter
			}
			if o.Primary && !havePrimary {
				primary, havePrimary = o.GDIAdapter, true
			}
		}
	}
	if monitor != "" {
		return ""
	}
	if havePrimary && primary != "" {
		return primary
	}
	return first
}

// --- попытка открыть дублирование ---

// ErrDuplicateUnsupported — DuplicateOutput вернул DXGI_ERROR_UNSUPPORTED
// (0x887A0004): на гибридном ноутбуке так бывает, если процесс работает на
// одной видеокарте, а монитор подключён к другой.
var ErrDuplicateUnsupported = errors.New("дублирование экрана не поддерживается на этой видеокарте")

// UnsupportedError — ErrDuplicateUnsupported с подробностями для журнала.
type UnsupportedError struct {
	Adapter string // видеокарта, под которой процесс видит монитор
	Monitor string
	Hybrid  bool  // в системе больше одной настоящей видеокарты
	Err     error // исходная ошибка с HRESULT
}

func (e *UnsupportedError) Error() string {
	if !e.Hybrid {
		// Одна видеокарта: смена карты ничего не даст, причина в сеансе.
		return fmt.Sprintf("захват экрана недоступен в этом сеансе (удалённый рабочий стол, виртуальная машина или другая видеокарта; монитор %s на %s): %v", e.Monitor, e.Adapter, e.Err)
	}
	return fmt.Sprintf("%v (монитор %s на %s): %v", ErrDuplicateUnsupported, e.Monitor, e.Adapter, e.Err)
}

func (e *UnsupportedError) Unwrap() error { return e.Err }

// Is делает errors.Is(err, ErrDuplicateUnsupported) истинным.
func (e *UnsupportedError) Is(target error) bool { return target == ErrDuplicateUnsupported }

// DesktopWaitError — на системе с одной видеокартой дублирование экрана не
// открывается (DXGI_ERROR_UNSUPPORTED), но это не RDP и не Basic Render: так
// бывает временно, например на экране блокировки или при смене режима. Показ
// нужно начать заново через паузу. Не совпадает с ErrDuplicateUnsupported,
// чтобы не запускать смену видеокарты.
type DesktopWaitError struct {
	Adapter string
	Monitor string
	Err     error // исходная ошибка с HRESULT
}

func (e *DesktopWaitError) Error() string {
	return fmt.Sprintf("рабочий стол пока недоступен для захвата (монитор %s на %s): %v", e.Monitor, e.Adapter, e.Err)
}

func (e *DesktopWaitError) Unwrap() error { return e.Err }

// WaitingForDesktop помечает ошибку как временную: повторять, а не сдаваться
// (trayctl проверяет это через интерфейс, без импорта этого пакета).
func (e *DesktopWaitError) WaitingForDesktop() bool { return true }

// ProbeResult — итог пробного открытия дублирования.
type ProbeResult struct {
	Display       int
	Monitor       string
	Adapter       string // видеокарта, под которой DXGI показывает монитор
	AdapterLUID   LUID
	DeviceAdapter string // видеокарта, на которой создано устройство D3D11
	DeviceLUID    LUID
	Attempts      int
	Elapsed       time.Duration
	OK            bool
	Err           error // последняя ошибка (с HRESULT), если открыть не удалось
}

// deviceAdapter возвращает видеокарту, на которой создано устройство D3D11
// (спрашиваем у самого устройства, а не берём из переменной).
func deviceAdapter(device *com.Object) (name string, luid LUID, err error) {
	dd, err := device.QueryInterface(&iidDXGIDevice)
	if err != nil {
		return "", LUID{}, err
	}
	defer dd.Release()
	var adapter *com.Object
	if err := com.Check("IDXGIDevice::GetAdapter", dd.Call(slotDXGIDeviceGetAdapter, uintptr(unsafe.Pointer(&adapter)))); err != nil {
		return "", LUID{}, err
	}
	defer adapter.Release()
	var ad adapterDesc
	adapter.Call(slotAdapterGetDesc, uintptr(unsafe.Pointer(&ad)))
	return windows.UTF16ToString(ad.Description[:]), LUID{Low: ad.LuidLow, High: ad.LuidHigh}, nil
}

// ProbeDuplication пробует открыть дублирование монитора display не дольше wait
// (повтор каждые 250 мс), затем сразу закрывает его. Для команды diag.
func ProbeDuplication(display int, wait time.Duration) (res ProbeResult) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	res = ProbeResult{Display: display}
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()
	cleanup, err := com.Init()
	if err != nil {
		res.Err = err
		return res
	}
	defer cleanup()
	found, err := findOutput(display)
	if err != nil {
		res.Err = err
		return res
	}
	defer found.release()
	res.Monitor, res.Adapter, res.AdapterLUID = found.info.Name, found.info.AdapterName, found.info.AdapterLUID
	device, ctx, err := createDevice(found.adapter)
	if err != nil {
		res.Err = err
		return res
	}
	defer func() { ctx.Release(); device.Release() }()
	if n, l, derr := deviceAdapter(device); derr == nil {
		res.DeviceAdapter, res.DeviceLUID = n, l
	}
	out1, err := found.output.QueryInterface(&iidOutput1)
	if err != nil {
		res.Err = fmt.Errorf("IDXGIOutput1: %w", err)
		return res
	}
	defer out1.Release()
	for {
		res.Attempts++
		var dup *com.Object
		hr := out1.Call(slotOutput1DuplicateOutput, uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&dup)))
		if err := com.Check("DuplicateOutput", hr); err == nil {
			dup.Release()
			res.OK, res.Err = true, nil
			return res
		} else {
			res.Err = err
		}
		if time.Since(start) >= wait {
			return res
		}
		time.Sleep(dupRetryInterval)
	}
}

// hybridSystem сообщает, что в системе больше одной настоящей (не программной и
// не виртуальной Microsoft) видеокарты.
func hybridSystem() bool {
	list, err := ListAdapters()
	return err == nil && gpupref.Hybrid(GPUAdapters(list))
}

// GPUAdapters переводит список видеокарт в вид, нужный пакету gpupref.
func GPUAdapters(list []AdapterInfo) []gpupref.Adapter {
	out := make([]gpupref.Adapter, 0, len(list))
	for _, a := range list {
		out = append(out, gpupref.Adapter{
			Name: a.Name, VendorID: a.VendorID, DeviceID: a.DeviceID,
			Software: a.Software(), VideoMemory: a.DedicatedVRAM, Outputs: len(a.Outputs),
		})
	}
	return out
}

// DescribeError возвращает понятное пояснение к ошибке открытия дублирования
// (для журнала и команды diag).
func DescribeError(err error) string {
	if err == nil {
		return ""
	}
	return describeDupError(err)
}
