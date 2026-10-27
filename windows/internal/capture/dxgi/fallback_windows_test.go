//go:build windows && (amd64 || arm64)

package dxgi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"airspoon/internal/win/com"
)

// createDevice повторяет без VIDEO_SUPPORT при ЛЮБОЙ ошибке (не только
// DXGI_ERROR_UNSUPPORTED) и запоминает исходный HRESULT и признак «без видео».
func TestCreateWithFallbackRetriesOnAnyError(t *testing.T) {
	const eFail = 0x80004005
	for _, first := range []uint32{hrUnsupported, eFail, 0x80070057, hrDeviceRemoved} {
		var flags []uintptr
		res, hr := createWithFallback(func(f uintptr) (uintptr, uint32) {
			flags = append(flags, f)
			if f&d3d11CreateVideoSupport != 0 {
				return uintptr(first), 0
			}
			return 0, 0xB000
		})
		if int32(hr) < 0 {
			t.Fatalf("HRESULT 0x%08X: повтор без видео должен удаться, получен 0x%08X", first, uint32(hr))
		}
		if !res.NoVideo || res.VideoHR != first || res.FeatureLevel != 0xB000 {
			t.Fatalf("HRESULT 0x%08X: итог %+v", first, res)
		}
		if len(flags) != 2 || flags[1]&d3d11CreateVideoSupport != 0 || flags[1]&d3d11CreateBGRASupport == 0 {
			t.Fatalf("второй вызов должен быть с BGRA без видео: %#x", flags)
		}
	}

	// Первая попытка удалась: повтора нет, флага нет.
	calls := 0
	res, hr := createWithFallback(func(uintptr) (uintptr, uint32) { calls++; return 1 /* S_FALSE */, 0xB100 })
	if calls != 1 || res.NoVideo || res.VideoHR != 0 || int32(hr) < 0 || res.FeatureLevel != 0xB100 {
		t.Fatalf("успех с первого раза: calls=%d res=%+v hr=0x%X", calls, res, hr)
	}

	// Обе попытки неудачны: наружу идёт HRESULT второй, а первый сохранён.
	res, hr = createWithFallback(func(f uintptr) (uintptr, uint32) {
		if f&d3d11CreateVideoSupport != 0 {
			return uintptr(hrUnsupported), 0
		}
		return uintptr(eFail), 0
	})
	if uint32(hr) != eFail || res.VideoHR != hrUnsupported || !res.NoVideo {
		t.Fatalf("обе неудачны: hr=0x%X res=%+v", uint32(hr), res)
	}
}

func TestVPFormatSupportCheck(t *testing.T) {
	ok := vpFormatSupport{InFlags: vpFormatSupportInput, OutFlags: vpFormatSupportOutput}
	if err := ok.check(); err != nil {
		t.Fatalf("BGRA->NV12 поддержан, а ошибка %v", err)
	}
	for name, s := range map[string]vpFormatSupport{
		"HRESULT входа":   {InHR: 0x80004005, OutFlags: vpFormatSupportOutput},
		"HRESULT выхода":  {OutHR: 0x887A0001, InFlags: vpFormatSupportInput},
		"нет BGRA входа":  {InFlags: 0, OutFlags: vpFormatSupportOutput},
		"нет NV12 выхода": {InFlags: vpFormatSupportInput, OutFlags: vpFormatSupportInput},
	} {
		if err := s.check(); err == nil {
			t.Errorf("%s: ошибка не возвращена", name)
		}
	}
	if err := (vpFormatSupport{InHR: 0x80004005}).check(); err == nil || !strings.Contains(err.Error(), "0x80004005") {
		t.Errorf("HRESULT должен быть в тексте: %v", err)
	}
}

// Ошибка Blt: кадры в память — видеопроцессор отключается, кадр идёт через CPU;
// кадры текстурой — особая ошибка, по которой источник переходит на память;
// потеря устройства остаётся потерей устройства.
func TestOnBltFailure(t *testing.T) {
	bltErr := &com.Error{Op: "VideoProcessorBlt", HR: 0x80004005}
	newCap := func(wantTexture bool, removed bool) *Capturer {
		return &Capturer{
			vp: &videoProc{}, ring: []*com.Object{nil, nil}, wantTexture: wantTexture,
			gpuPath: true, deviceRemovedFn: func() bool { return removed },
			cfg: Config{Logf: func(string, ...any) {}},
		}
	}

	c := newCap(false, false)
	if err := c.onBltFailure(bltErr); err != nil {
		t.Fatalf("кадры в память: ожидался переход на CPU, получено %v", err)
	}
	if c.vp != nil || c.ring != nil {
		t.Fatal("видеопроцессор и кольцо должны быть освобождены")
	}
	if gpu, reason := c.ConvertPath(); gpu || !strings.Contains(reason, "0x80004005") {
		t.Fatalf("путь %v, причина %q: ожидался CPU с HRESULT", gpu, reason)
	}

	c = newCap(true, false)
	err := c.onBltFailure(bltErr)
	var vpe *VideoProcError
	if !errors.Is(err, ErrVideoProcessor) || !errors.As(err, &vpe) {
		t.Fatalf("кадры текстурой: ожидалась VideoProcError, получено %v", err)
	}
	if hr, ok := com.HR(err); !ok || hr != 0x80004005 {
		t.Fatalf("HRESULT должен сохраняться в цепочке ошибок: %v", err)
	}
	if c.vp == nil {
		t.Fatal("при текстурах видеопроцессор не снимаем: решает источник")
	}
	// После SetWantTexture(false) следующий отказ уводит на CPU.
	if err := c.SetWantTexture(false); err != nil {
		t.Fatal(err)
	}
	if err := c.onBltFailure(bltErr); err != nil || c.vp != nil {
		t.Fatalf("после перехода на память: err=%v vp=%v", err, c.vp)
	}

	c = newCap(false, true)
	if err := c.onBltFailure(bltErr); !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("потеря устройства: %v", err)
	}
}

// Формат кадра не BGRA8 (HDR) — остановка с понятным текстом, а не мусор.
func TestCheckFrameFormat(t *testing.T) {
	if err := checkFrameFormat(dxgiFormatB8G8R8A8UNorm); err != nil {
		t.Fatal(err)
	}
	for _, f := range []uint32{10 /* R16G16B16A16_FLOAT */, 24 /* R10G10B10A2 */, 28} {
		err := checkFrameFormat(f)
		if !errors.Is(err, ErrFrameFormat) {
			t.Fatalf("формат %d: ожидалась ErrFrameFormat, получено %v", f, err)
		}
		if !strings.Contains(err.Error(), "Выключите HDR") {
			t.Errorf("формат %d: в тексте нет подсказки про HDR: %v", f, err)
		}
	}
}

func TestOrderOutputs(t *testing.T) {
	// Основной монитор GDI идёт первым, независимо от порядка перечисления;
	// выход, не подключённый к рабочему столу, пропускается.
	c := []outputCand{
		{Name: `\\.\DISPLAY3`, Attached: false},
		{Name: `\\.\DISPLAY1`, Attached: true},
		{Name: `\\.\DISPLAY2`, Attached: true, Primary: true},
		{Name: `\\.\DISPLAY4`, Attached: true},
	}
	if got, want := orderOutputs(c), []int{2, 1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ожидалось %v", got, want)
	}
	// Основной уже первый — порядок не меняется.
	c = []outputCand{{Attached: true, Primary: true}, {Attached: true}}
	if got, want := orderOutputs(c), []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ожидалось %v", got, want)
	}
	// Данных GDI нет (основного не видно): порядок перечисления.
	c = []outputCand{{Attached: true}, {Attached: false}, {Attached: true}}
	if got, want := orderOutputs(c), []int{0, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ожидалось %v", got, want)
	}
	// Основной помечен, но не подключён к рабочему столу: он не выбирается.
	c = []outputCand{{Attached: true}, {Attached: false, Primary: true}}
	if got, want := orderOutputs(c), []int{0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ожидалось %v", got, want)
	}
	// Подключённых нет вовсе (AttachedToDesktop не заполняется): берём все.
	c = []outputCand{{}, {Primary: true}}
	if got, want := orderOutputs(c), []int{1, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ожидалось %v", got, want)
	}
	if got := orderOutputs(nil); len(got) != 0 {
		t.Fatalf("пустой список: %v", got)
	}
}

// Видеокарта по умолчанию считается по основному монитору, а не по первому
// подключённому.
func TestOwnerOfMonitorDefaultsToPrimary(t *testing.T) {
	list := []AdapterInfo{
		{Name: "Intel", Outputs: []OutputDetail{{Name: `\\.\DISPLAY1`, Attached: true, GDIAdapter: "Intel"}}},
		{Name: "NVIDIA", Outputs: []OutputDetail{
			{Name: `\\.\DISPLAY2`, Attached: false, Primary: true, GDIAdapter: "Ghost"},
			{Name: `\\.\DISPLAY3`, Attached: true, Primary: true, GDIAdapter: "NVIDIA"},
		}},
	}
	if got := OwnerOfMonitor(list, ""); got != "NVIDIA" {
		t.Fatalf("владелец основного монитора: %q", got)
	}
	if got := OwnerOfMonitor(list, `\\.\DISPLAY1`); got != "Intel" {
		t.Fatalf("владелец DISPLAY1: %q", got)
	}
	// Основного нет — первый подключённый, как раньше.
	list[1].Outputs[1].Primary = false
	if got := OwnerOfMonitor(list, ""); got != "Intel" {
		t.Fatalf("без основного: %q", got)
	}
}

func TestUnsupportedErrorTextDependsOnHybrid(t *testing.T) {
	base := &com.Error{Op: "DuplicateOutput", HR: hrUnsupported}
	single := (&UnsupportedError{Adapter: "Intel", Monitor: `\\.\DISPLAY1`, Err: base}).Error()
	if !strings.Contains(single, "в этом сеансе") || !strings.Contains(single, "0x887A0004") {
		t.Errorf("одна видеокарта: %q", single)
	}
	hybrid := (&UnsupportedError{Adapter: "Intel", Monitor: `\\.\DISPLAY1`, Hybrid: true, Err: base}).Error()
	if !strings.Contains(hybrid, ErrDuplicateUnsupported.Error()) {
		t.Errorf("гибрид: %q", hybrid)
	}
}

func TestProbeHelpers(t *testing.T) {
	if got := FormatUMDVersion(31<<48 | 0<<32 | 15<<16 | 4601); got != "31.0.15.4601" {
		t.Errorf("версия драйвера NVIDIA: %q", got)
	}
	if FeatureLevelName(0xB100) != "11_1" || FeatureLevelName(0xA000) != "10_0" || FeatureLevelName(0) != "-" {
		t.Error("названия уровней возможностей")
	}
	if !IsBasicDisplay(vendorMicrosoft, 0) || !IsBasicDisplay(0x10DE, dxgiAdapterFlagSoftware) || IsBasicDisplay(0x10DE, 0) {
		t.Error("определение Basic Display")
	}
	t0 := time.Unix(100, 0)
	avg, max := intervalStats([]time.Time{t0, t0.Add(30 * time.Millisecond), t0.Add(60 * time.Millisecond), t0.Add(160 * time.Millisecond)})
	if max != 100*time.Millisecond || avg != 160*time.Millisecond/3 {
		t.Errorf("интервалы: средний %v, наибольший %v", avg, max)
	}
	if a, m := intervalStats([]time.Time{t0}); a != 0 || m != 0 {
		t.Error("один кадр интервалов не даёт")
	}
	if !ColorSpaceIsHDR(12) || !ColorSpaceIsHDR(1) || ColorSpaceIsHDR(0) {
		t.Error("ColorSpaceIsHDR")
	}
	if !strings.Contains(ColorSpaceName(12), "HDR10") || !strings.Contains(ColorSpaceName(0), "SDR") {
		t.Error("ColorSpaceName")
	}
}

// Раскладка структур Win32/DXGI, которые читаются напрямую из памяти.
func TestStructLayouts(t *testing.T) {
	if got := unsafe.Sizeof(devMode{}); got != 220 {
		t.Errorf("DEVMODEW: %d байт, ожидалось 220", got)
	}
	if got := unsafe.Offsetof(devMode{}.DisplayFrequency); got != 184 {
		t.Errorf("DEVMODEW.dmDisplayFrequency: смещение %d, ожидалось 184", got)
	}
	if got := unsafe.Sizeof(outputDesc1{}); got != 152 {
		t.Errorf("DXGI_OUTPUT_DESC1: %d байт, ожидалось 152 (x64)", got)
	}
	if got := unsafe.Offsetof(outputDesc1{}.ColorSpace); got != 100 {
		t.Errorf("DXGI_OUTPUT_DESC1.ColorSpace: смещение %d, ожидалось 100", got)
	}
}

// --- проверки на живом окружении (пропускаются, если GPU/рабочего стола нет) ---

func TestProbeAdaptersLive(t *testing.T) {
	list, err := ProbeAdapters()
	if err != nil || len(list) == 0 {
		t.Skipf("видеокарты недоступны: %v", err)
	}
	for _, a := range list {
		t.Logf("%d %s ven=%04X dev=%04X basic=%t: с видео hr=0x%08X fl=%s, без видео hr=0x%08X fl=%s, VideoDevice=%t, BGRA->NV12=%t (in 0x%X out 0x%X, err=%v), драйвер %q (ok=%t hr=0x%08X)",
			a.Index, a.Name, a.VendorID, a.DeviceID, a.BasicDisplay,
			a.WithVideo.HR, FeatureLevelName(a.WithVideo.FeatureLevel),
			a.WithoutVideo.HR, FeatureLevelName(a.WithoutVideo.FeatureLevel),
			a.HasVideoDevice, a.VideoProc.BGRAToNV12, a.VideoProc.InFlags, a.VideoProc.OutFlags, a.VideoProc.Err,
			a.UMDText, a.UMDOK, a.UMDHR)
		if !a.WithVideo.Tried || !a.WithoutVideo.Tried {
			t.Errorf("%s: обе попытки должны быть выполнены", a.Name)
		}
		if a.WithVideo.OK && !a.HasVideoDevice {
			t.Logf("%s: устройство с видео создано, но ID3D11VideoDevice нет", a.Name)
		}
		if a.HasVideoDevice && !a.VideoProc.Checked {
			t.Errorf("%s: видеоустройство есть, но форматы не проверены: %v", a.Name, a.VideoProc.Err)
		}
		// Страж номера слота CheckVideoProcessorFormat: при неверном слоте
		// драйвер отвечает NOT_FOUND и GPU-преобразование отключалось бы везде.
		if a.HasVideoDevice && !a.Software && (int32(a.VideoProc.InHR) < 0 || int32(a.VideoProc.OutHR) < 0) {
			t.Errorf("%s: CheckVideoProcessorFormat вернул HRESULT 0x%08X / 0x%08X", a.Name, a.VideoProc.InHR, a.VideoProc.OutHR)
		}
	}
}

func TestProbeMonitorsLive(t *testing.T) {
	list, err := ProbeMonitors(time.Second)
	if err != nil || len(list) == 0 {
		t.Skipf("мониторы недоступны: %v", err)
	}
	for _, m := range list {
		t.Logf("монитор %d %s на %s primary=%t rot=%d cs=%s(known=%t) %d Гц формат=%s путь gpu=%t (%s) кадров=%d новых=%d средний=%v макс=%v err=%v",
			m.Display, m.Name, m.Adapter, m.Primary, m.Rotation, ColorSpaceName(m.ColorSpace), m.ColorSpaceKnown, m.RefreshHz,
			m.FrameFormatName, m.GPUConvert, m.PathReason, m.Frames, m.FreshFrames, m.AvgInterval, m.MaxInterval, m.Err)
	}
	if list[0].Display != 0 {
		t.Errorf("первым в списке должен быть монитор 0, а не %d", list[0].Display)
	}
}

// Монитор 0 — основной по GDI, и он находится и по номеру, и по имени с LUID.
func TestDefaultOutputIsPrimaryAndFoundByIdentity(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	list, err := ListOutputs()
	if err != nil || len(list) == 0 {
		t.Skipf("мониторов нет: %v", err)
	}
	for i, o := range list {
		if o.Index != i {
			t.Errorf("индекс %d у позиции %d", o.Index, i)
		}
		if !o.Attached && i < len(list)-1 {
			t.Logf("монитор %s не подключён к рабочему столу", o.Name)
		}
	}
	anyPrimary := false
	for _, o := range list {
		anyPrimary = anyPrimary || o.Primary
	}
	if anyPrimary && !list[0].Primary {
		t.Errorf("монитор 0 (%s) не основной", list[0].Name)
	}
	f, err := findOutputByIdentity(list[0].Name, list[0].AdapterLUID)
	if err != nil {
		t.Fatalf("поиск по имени и LUID: %v", err)
	}
	f.release()
	if _, err := findOutputByIdentity(`\\.\DISPLAY999`, list[0].AdapterLUID); !errors.Is(err, errNoDisplay) {
		t.Errorf("несуществующее имя: %v", err)
	}
}

// Отказ видеопроцессора при настройке под новый размер (кадры текстурой) не
// оставляет Capturer полусконфигурированным: vp снят, кольцо освобождено, размер
// сброшен, чтобы следующий ensureSize не вышел раньше времени, а produce не упал
// по индексу ringViews.
func TestFailVideoProcSetupResetsConfiguration(t *testing.T) {
	c := &Capturer{
		vp: &videoProc{}, ring: []*com.Object{nil, nil}, wantTexture: true, gpuPath: true,
		srcW: 1920, srcH: 1080, cfg: Config{Logf: func(string, ...any) {}},
	}
	err := c.failVideoProcSetup(&com.Error{Op: "VideoProcessorSetStreamOutputRate", HR: 0x80004005})
	var vpe *VideoProcError
	if !errors.As(err, &vpe) || !errors.Is(err, ErrVideoProcessor) {
		t.Fatalf("ожидалась VideoProcError, получено %v", err)
	}
	if c.vp != nil || c.ring != nil || c.ringViews != nil {
		t.Fatal("видеопроцессор и кольцо должны быть сняты")
	}
	if c.srcW != 0 || c.srcH != 0 {
		t.Fatalf("размер должен быть сброшен, получено %dx%d", c.srcW, c.srcH)
	}
	if gpu, _ := c.ConvertPath(); gpu {
		t.Fatal("путь преобразования должен стать CPU")
	}
}
