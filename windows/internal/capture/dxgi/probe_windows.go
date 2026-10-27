//go:build windows && (amd64 || arm64)

package dxgi

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/win/com"
)

// Пробы для команды diag: что умеет каждая видеокарта и как на самом деле
// захватывается каждый монитор. Ничего не меняют в системе, кроме коротких
// пробных устройств D3D11 и дублирования экрана.

const (
	vendorMicrosoft = 0x1414
	// slotAdapterCheckInterfaceSupport — IDXGIAdapter::CheckInterfaceSupport.
	slotAdapterCheckInterfaceSupport = 9
)

// CreateAttempt — итог одного вызова D3D11CreateDevice.
type CreateAttempt struct {
	Tried        bool
	OK           bool
	HR           uint32 // HRESULT вызова (S_OK = 0)
	FeatureLevel uint32 // D3D_FEATURE_LEVEL, например 0xB100 — 11_1
}

// FeatureLevelName — читаемый уровень возможностей: 0xB100 -> "11_1".
func FeatureLevelName(fl uint32) string {
	if fl == 0 {
		return "-"
	}
	return fmt.Sprintf("%d_%d", fl>>12, (fl>>8)&0xF)
}

// VideoProcSupport — поддержка BGRA -> NV12 видеопроцессором.
type VideoProcSupport struct {
	Checked           bool
	InHR, OutHR       uint32
	InFlags, OutFlags uint32
	// BGRAToNV12 — видеопроцессор принимает BGRA и выдаёт NV12.
	BGRAToNV12 bool
	Err        error // почему проверить не удалось или формат не поддерживается
}

// AdapterProbe — результат пробы одной видеокарты.
type AdapterProbe struct {
	Index    int
	Name     string
	VendorID uint32
	DeviceID uint32
	LUID     LUID
	// Software — программный адаптер (флаг DXGI); BasicDisplay — Microsoft Basic
	// Display/Render Driver (драйвер видеокарты не установлен или не работает).
	Software, BasicDisplay bool

	WithVideo    CreateAttempt // D3D11CreateDevice с BGRA_SUPPORT | VIDEO_SUPPORT
	WithoutVideo CreateAttempt // то же без VIDEO_SUPPORT
	// HasVideoDevice — у созданного устройства есть ID3D11VideoDevice и контекст.
	HasVideoDevice bool
	VideoProc      VideoProcSupport

	// Версия драйвера пользовательского режима (IDXGIAdapter::CheckInterfaceSupport).
	UMDOK      bool
	UMDHR      uint32
	UMDVersion uint64
	UMDText    string // "31.0.15.4601"
}

// FormatUMDVersion переводит версию драйвера из CheckInterfaceSupport
// (LARGE_INTEGER: по 16 бит на часть) в вид "31.0.15.4601".
func FormatUMDVersion(v uint64) string {
	return fmt.Sprintf("%d.%d.%d.%d", v>>48, (v>>32)&0xFFFF, (v>>16)&0xFFFF, v&0xFFFF)
}

// IsBasicDisplay определяет Microsoft Basic Display/Render Driver по
// производителю и флагам адаптера.
func IsBasicDisplay(vendorID, flags uint32) bool {
	return vendorID == vendorMicrosoft || flags&dxgiAdapterFlagSoftware != 0
}

// createRaw — один вызов D3D11CreateDevice; при успехе возвращает объекты.
func createRaw(adapter *com.Object, flags uintptr) (device, ctx *com.Object, a CreateAttempt) {
	a.Tried = true
	if procD3D11CreateDevice.Find() != nil {
		a.HR = 0x8007007E // ERROR_MOD_NOT_FOUND
		return nil, nil, a
	}
	var fl uint32
	hr, _, _ := procD3D11CreateDevice.Call(
		uintptr(unsafe.Pointer(adapter)), d3dDriverTypeUnknown, 0, flags,
		0, 0, d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)), uintptr(unsafe.Pointer(&fl)), uintptr(unsafe.Pointer(&ctx)))
	a.HR, a.FeatureLevel = uint32(hr), fl
	a.OK = int32(hr) >= 0
	if !a.OK {
		device, ctx = nil, nil
	}
	return device, ctx, a
}

// ProbeAdapters проверяет каждую видеокарту: создание устройства D3D11 с
// поддержкой видео и без неё (HRESULT и уровень возможностей), наличие
// ID3D11VideoDevice, поддержку BGRA -> NV12 видеопроцессором и версию драйвера.
func ProbeAdapters() ([]AdapterProbe, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
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
	var out []AdapterProbe
	for ai := uint32(0); ; ai++ {
		var adapter *com.Object
		hr := factory.Call(slotFactoryEnumAdapters1, uintptr(ai), uintptr(unsafe.Pointer(&adapter)))
		if uint32(hr) == hrNotFound {
			break
		}
		if err := com.Check("EnumAdapters1", hr); err != nil {
			return out, err
		}
		out = append(out, probeAdapter(adapter, int(ai)))
		adapter.Release()
	}
	return out, nil
}

func probeAdapter(adapter *com.Object, index int) AdapterProbe {
	var ad adapterDesc1
	if hr := adapter.Call(slotAdapter1GetDesc1, uintptr(unsafe.Pointer(&ad))); int32(hr) < 0 {
		adapter.Call(slotAdapterGetDesc, uintptr(unsafe.Pointer(&ad.adapterDesc)))
	}
	p := AdapterProbe{
		Index:        index,
		Name:         windows.UTF16ToString(ad.Description[:]),
		VendorID:     ad.VendorID,
		DeviceID:     ad.DeviceID,
		LUID:         LUID{Low: ad.LuidLow, High: ad.LuidHigh},
		Software:     ad.Flags&dxgiAdapterFlagSoftware != 0,
		BasicDisplay: IsBasicDisplay(ad.VendorID, ad.Flags),
	}

	// Версия драйвера: для программных адаптеров вернёт DXGI_ERROR_UNSUPPORTED.
	var ver int64
	hr := adapter.Call(slotAdapterCheckInterfaceSupport, uintptr(unsafe.Pointer(&iidDXGIDevice)), uintptr(unsafe.Pointer(&ver)))
	p.UMDHR = uint32(hr)
	if p.UMDOK = int32(hr) >= 0; p.UMDOK {
		p.UMDVersion = uint64(ver)
		p.UMDText = FormatUMDVersion(p.UMDVersion)
	}

	dev, ctx, a := createRaw(adapter, d3d11CreateBGRASupport|d3d11CreateVideoSupport)
	p.WithVideo = a
	if dev != nil {
		p.probeVideo(dev, ctx)
		ctx.Release()
		dev.Release()
	}
	dev, ctx, a = createRaw(adapter, d3d11CreateBGRASupport)
	p.WithoutVideo = a
	if dev != nil {
		ctx.Release()
		dev.Release()
	}
	return p
}

// probeVideo проверяет видеоинтерфейсы устройства, созданного с VIDEO_SUPPORT.
func (p *AdapterProbe) probeVideo(dev, ctx *com.Object) {
	vdev, err := dev.QueryInterface(&iidVideoDevice)
	if err != nil {
		p.VideoProc = VideoProcSupport{Err: fmt.Errorf("ID3D11VideoDevice: %w", err)}
		return
	}
	defer vdev.Release()
	vctx, err := ctx.QueryInterface(&iidVideoContext)
	if err != nil {
		p.VideoProc = VideoProcSupport{Err: fmt.Errorf("ID3D11VideoContext: %w", err)}
		return
	}
	vctx.Release()
	p.HasVideoDevice = true
	sup, err := probeVPFormats(vdev)
	p.VideoProc = VideoProcSupport{Checked: err == nil}
	if err != nil {
		p.VideoProc.Err = err
		return
	}
	p.VideoProc.InHR, p.VideoProc.OutHR = sup.InHR, sup.OutHR
	p.VideoProc.InFlags, p.VideoProc.OutFlags = sup.InFlags, sup.OutFlags
	if cerr := sup.check(); cerr != nil {
		p.VideoProc.Err = cerr
		return
	}
	p.VideoProc.BGRAToNV12 = true
}

// MonitorProbe — результат пробы одного монитора.
type MonitorProbe struct {
	Display  int // номер для Config.Display
	Name     string
	Adapter  string
	LUID     LUID
	Primary  bool
	Attached bool
	Bounds   Rect
	Rotation uint32 // DXGI_MODE_ROTATION: 1 — без поворота, 2 — 90, 3 — 180, 4 — 270

	ColorSpace      int32
	ColorSpaceKnown bool
	BitsPerColor    uint32
	HDR             bool // ColorSpaceIsHDR(ColorSpace)
	RefreshHz       int  // текущая частота по GDI

	// Первый кадр.
	FrameFormat     uint32 // DXGI_FORMAT; 0 — кадра не было
	FrameFormatName string

	// Путь преобразования цвета, который выбрал бы захват, и причина.
	GPUConvert bool
	PathReason string
	VideoOK    bool   // устройство создано с поддержкой видео
	VideoHR    uint32 // исходный HRESULT попытки с VIDEO_SUPPORT

	// Замер: выдача кадров захватом (1280x720, 30 к/с) за Measured.
	Measured    time.Duration
	Frames      int // кадров выдано
	FreshFrames int // из них новых (рабочий стол менялся)
	Captured    uint64
	AvgInterval time.Duration // средний интервал между кадрами
	MaxInterval time.Duration // наибольший интервал
	Err         error         // захват не открылся или остановился (в том числе ErrFrameFormat)
}

// intervalStats считает средний и наибольший интервал между отметками времени.
func intervalStats(t []time.Time) (avg, max time.Duration) {
	if len(t) < 2 {
		return 0, 0
	}
	for i := 1; i < len(t); i++ {
		d := t[i].Sub(t[i-1])
		if d > max {
			max = d
		}
	}
	return t[len(t)-1].Sub(t[0]) / time.Duration(len(t)-1), max
}

// ProbeMonitors открывает захват каждого монитора на measure (по умолчанию 4 с,
// не меньше 1 с и не больше 10 с) и возвращает по каждому поворот, цветовое
// пространство, частоту, формат первого кадра, выбранный путь преобразования и
// интервалы между кадрами. Мониторы проверяются по очереди.
func ProbeMonitors(measure time.Duration) ([]MonitorProbe, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch {
	case measure <= 0:
		measure = 4 * time.Second
	case measure < time.Second:
		measure = time.Second
	case measure > 10*time.Second:
		measure = 10 * time.Second
	}
	cleanup, err := com.Init()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	list, err := ListOutputs()
	if err != nil {
		return nil, err
	}
	details, _ := ListAdapters() // для цветового пространства; ошибка не мешает
	var out []MonitorProbe
	for _, o := range list {
		p := MonitorProbe{
			Display: o.Index, Name: o.Name, Adapter: o.AdapterName, LUID: o.AdapterLUID,
			Primary: o.Primary, Attached: o.Attached, Bounds: o.Bounds, Rotation: o.Rotation,
		}
		for _, a := range details {
			for _, d := range a.Outputs {
				if d.Name == o.Name && a.LUID == o.AdapterLUID {
					p.ColorSpace, p.ColorSpaceKnown, p.BitsPerColor = d.ColorSpace, d.ColorSpaceKnown, d.BitsPerColor
					p.HDR = d.ColorSpaceKnown && ColorSpaceIsHDR(d.ColorSpace)
					p.RefreshHz = d.RefreshHz
				}
			}
		}
		probeMonitor(&p, measure)
		out = append(out, p)
	}
	return out, nil
}

func probeMonitor(p *MonitorProbe, measure time.Duration) {
	c, err := New(Config{Display: p.Display, Width: 1280, Height: 720, FPS: 30})
	if err != nil {
		p.Err = err
		return
	}
	defer c.Close()
	p.VideoOK, p.VideoHR = c.DeviceVideoSupport()
	stop := make(chan struct{})
	timer := time.AfterFunc(measure, func() { close(stop) })
	defer timer.Stop()
	var times []time.Time
	start := time.Now()
	for {
		f, err := c.Grab(stop)
		if err != nil {
			if err != ErrStopped {
				p.Err = err
			}
			break
		}
		times = append(times, time.Now())
		p.Frames++
		if f.Fresh {
			p.FreshFrames++
		}
	}
	p.Measured = time.Since(start)
	p.Captured = c.Stats().Captured
	p.AvgInterval, p.MaxInterval = intervalStats(times)
	p.FrameFormat = c.FrameFormat()
	p.FrameFormatName = FormatName(p.FrameFormat)
	p.GPUConvert, p.PathReason = c.ConvertPath()
}
