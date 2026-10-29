//go:build windows && (amd64 || arm64)

package dxgi

import (
	"fmt"
	"unsafe"

	"airspoon/internal/win/com"
)

// Слоты ID3D11VideoDevice и ID3D11VideoContext.
const (
	slotVDevCreateVideoProcessor = 4
	slotVDevCreateInputView      = 8
	slotVDevCreateOutputView     = 9
	slotVDevCreateEnumerator     = 10

	slotVCtxSetOutputTargetRect  = 13
	slotVCtxSetOutputBackground  = 14
	slotVCtxSetOutputColorSpace  = 15
	slotVCtxSetStreamFrameFormat = 27
	slotVCtxSetStreamColorSpace  = 28
	slotVCtxSetStreamSourceRect  = 30
	slotVCtxSetStreamDestRect    = 31
	slotVCtxBlt                  = 53
	slotVCtxSetStreamRotation    = 63
	slotVEnumGetCaps             = 9
)

// Цветовое пространство видеопроцессора (D3D11_VIDEO_PROCESSOR_COLOR_SPACE):
// биты Usage, RGB_Range, YCbCr_Matrix, YCbCr_xvYCC, затем Nominal_Range (2 бита).
const (
	vpColorSpaceInputRGBFull       = 0           // RGB 0..255
	vpColorSpaceOutputBT709Limited = 1<<2 | 1<<4 // BT.709, YCbCr 16..235
	vpViewDimensionTexture2D       = 1           // D3D11_VPIV/VPOV_DIMENSION_TEXTURE2D
	vpFrameFormatProgressive       = 0           // D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE
	vpFeatureCapsRotation          = 0x40
)

type vpRational struct{ Num, Den uint32 }

type vpContentDesc struct {
	InputFrameFormat uint32
	InputFrameRate   vpRational
	InputWidth       uint32
	InputHeight      uint32
	OutputFrameRate  vpRational
	OutputWidth      uint32
	OutputHeight     uint32
	Usage            uint32
}

type vpStream struct {
	Enable              int32
	OutputIndex         uint32
	InputFrameOrField   uint32
	PastFrames          uint32
	FutureFrames        uint32
	PastSurfaces        **com.Object
	InputSurface        *com.Object
	FutureSurfaces      **com.Object
	PastSurfacesRight   **com.Object
	InputSurfaceRight   *com.Object
	FutureSurfacesRight **com.Object
}

type vpColor struct{ R, G, B, A float32 }

type vpCaps struct {
	Device, Feature, Filter, InputFormat, AutoStream, Stereo uint32
	RateConversionCount, MaxInputStreams, MaxStreamStates    uint32
}

// videoProc — обёртка над ID3D11VideoProcessor: BGRA -> NV12 с масштабированием
// на GPU. Один экземпляр рассчитан на фиксированные размеры источника и холста.
type videoProc struct {
	vdev, vctx *com.Object
	enum, proc *com.Object
}

func newVideoProc(device, ctx *com.Object) (*videoProc, error) {
	vdev, err := device.QueryInterface(&iidVideoDevice)
	if err != nil {
		return nil, fmt.Errorf("ID3D11VideoDevice: %w", err)
	}
	vctx, err := ctx.QueryInterface(&iidVideoContext)
	if err != nil {
		vdev.Release()
		return nil, fmt.Errorf("ID3D11VideoContext: %w", err)
	}
	if sup, err := probeVPFormats(vdev); err != nil {
		vctx.Release()
		vdev.Release()
		return nil, err
	} else if err := sup.check(); err != nil {
		vctx.Release()
		vdev.Release()
		return nil, err
	}
	return &videoProc{vdev: vdev, vctx: vctx}, nil
}

// Флаги D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT.
const (
	vpFormatSupportInput  = 0x1
	vpFormatSupportOutput = 0x2
)

// slotVEnumCheckFormat — ID3D11VideoProcessorEnumerator::CheckVideoProcessorFormat.
// Интерфейс наследует ID3D11DeviceChild (после IUnknown ещё GetDevice,
// GetPrivateData, SetPrivateData, SetPrivateDataInterface), затем идут
// GetVideoProcessorContentDesc (7) и CheckVideoProcessorFormat (8); слот проверен
// на живой RTX 2080 Ti (S_OK, флаги 3 для BGRA и NV12).
const slotVEnumCheckFormat = 8

// vpFormatSupport — что видеопроцессор заявляет про BGRA на входе и NV12 на выходе.
type vpFormatSupport struct {
	InHR, OutHR       uint32 // HRESULT вызовов CheckVideoProcessorFormat
	InFlags, OutFlags uint32 // D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_*
}

// check возвращает ошибку, если драйвер не умеет BGRA -> NV12; в тексте оба
// HRESULT и флаги — по ним в журнале видно, что именно не поддерживается.
func (s vpFormatSupport) check() error {
	if int32(s.InHR) < 0 || int32(s.OutHR) < 0 {
		return fmt.Errorf("CheckVideoProcessorFormat: HRESULT вход 0x%08X, выход 0x%08X", s.InHR, s.OutHR)
	}
	if s.InFlags&vpFormatSupportInput == 0 {
		return fmt.Errorf("видеопроцессор не принимает BGRA на входе (флаги 0x%X)", s.InFlags)
	}
	if s.OutFlags&vpFormatSupportOutput == 0 {
		return fmt.Errorf("видеопроцессор не выдаёт NV12 (флаги 0x%X)", s.OutFlags)
	}
	return nil
}

// probeVPFormats создаёт пробный перечислитель и спрашивает поддержку форматов.
func probeVPFormats(vdev *com.Object) (vpFormatSupport, error) {
	desc := vpContentDesc{
		InputFrameFormat: vpFrameFormatProgressive,
		InputFrameRate:   vpRational{30, 1},
		InputWidth:       1280, InputHeight: 720,
		OutputFrameRate: vpRational{30, 1},
		OutputWidth:     1280, OutputHeight: 720,
	}
	var enum *com.Object
	hr := vdev.Call(slotVDevCreateEnumerator, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&enum)))
	if err := com.Check("CreateVideoProcessorEnumerator", hr); err != nil {
		return vpFormatSupport{}, err
	}
	defer enum.Release()
	var s vpFormatSupport
	s.InHR = uint32(enum.Call(slotVEnumCheckFormat, dxgiFormatB8G8R8A8UNorm, uintptr(unsafe.Pointer(&s.InFlags))))
	s.OutHR = uint32(enum.Call(slotVEnumCheckFormat, dxgiFormatNV12, uintptr(unsafe.Pointer(&s.OutFlags))))
	return s, nil
}

// configure создаёт видеопроцессор для размеров srcW x srcH -> dstW x dstH и
// задаёт вписывание fit, цветовые пространства и чёрный фон полей.
func (v *videoProc) configure(srcW, srcH, dstW, dstH, fps int, fit Rect, rotation uint32) error {
	v.releaseProc()
	desc := vpContentDesc{
		InputFrameFormat: vpFrameFormatProgressive,
		InputFrameRate:   vpRational{uint32(fps), 1},
		InputWidth:       uint32(srcW),
		InputHeight:      uint32(srcH),
		OutputFrameRate:  vpRational{uint32(fps), 1},
		OutputWidth:      uint32(dstW),
		OutputHeight:     uint32(dstH),
	}
	hr := v.vdev.Call(slotVDevCreateEnumerator, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&v.enum)))
	if err := com.Check("CreateVideoProcessorEnumerator", hr); err != nil {
		return err
	}
	// Rotation is optional. Refuse the GPU path if unavailable so the existing
	// system-memory/CPU fallback is used instead of silently streaming sideways.
	var caps vpCaps
	hr = v.enum.Call(slotVEnumGetCaps, uintptr(unsafe.Pointer(&caps)))
	if err := com.Check("GetVideoProcessorCaps", hr); err != nil {
		return err
	}
	if err := checkVPRotation(caps.Feature, rotation); err != nil {
		return err
	}
	hr = v.vdev.Call(slotVDevCreateVideoProcessor, uintptr(unsafe.Pointer(v.enum)), 0, uintptr(unsafe.Pointer(&v.proc)))
	if err := com.Check("CreateVideoProcessor", hr); err != nil {
		v.releaseProc()
		return err
	}
	inCS := uint32(vpColorSpaceInputRGBFull)
	outCS := uint32(vpColorSpaceOutputBT709Limited)
	full := rect32{0, 0, int32(dstW), int32(dstH)}
	src := rect32{0, 0, int32(srcW), int32(srcH)}
	dst := rect32{int32(fit.X), int32(fit.Y), int32(fit.Right()), int32(fit.Bottom())}
	// Explicit limited-range YCbCr black, matching FillBlackNV12. Some drivers
	// interpret RGB black as Y=0 even with limited-range output selected.
	black := vpColor{16.0 / 255, 128.0 / 255, 128.0 / 255, 1}
	v.vctx.Call(slotVCtxSetStreamFrameFormat, uintptr(unsafe.Pointer(v.proc)), 0, vpFrameFormatProgressive)
	v.vctx.Call(slotVCtxSetStreamColorSpace, uintptr(unsafe.Pointer(v.proc)), 0, uintptr(unsafe.Pointer(&inCS)))
	v.vctx.Call(slotVCtxSetOutputColorSpace, uintptr(unsafe.Pointer(v.proc)), uintptr(unsafe.Pointer(&outCS)))
	v.vctx.Call(slotVCtxSetStreamSourceRect, uintptr(unsafe.Pointer(v.proc)), 0, 1, uintptr(unsafe.Pointer(&src)))
	v.vctx.Call(slotVCtxSetStreamDestRect, uintptr(unsafe.Pointer(v.proc)), 0, 1, uintptr(unsafe.Pointer(&dst)))
	v.vctx.Call(slotVCtxSetOutputTargetRect, uintptr(unsafe.Pointer(v.proc)), 1, uintptr(unsafe.Pointer(&full)))
	v.vctx.Call(slotVCtxSetOutputBackground, uintptr(unsafe.Pointer(v.proc)), 1, uintptr(unsafe.Pointer(&black)))
	if caps.Feature&vpFeatureCapsRotation != 0 {
		// D3D11 rotation values are DXGI values minus one. Source rect is in
		// native coordinates; destination rect is in upright canvas coordinates.
		rot := uint32(0)
		if rotation >= dxgiModeRotationIdentity && rotation <= dxgiModeRotation270 {
			rot = rotation - dxgiModeRotationIdentity
		}
		enable := uintptr(0)
		if rot != 0 {
			enable = 1
		}
		v.vctx.Call(slotVCtxSetStreamRotation, uintptr(unsafe.Pointer(v.proc)), 0, enable, uintptr(rot))
	}
	return nil
}

func checkVPRotation(features, rotation uint32) error {
	if rotation > dxgiModeRotationIdentity && features&vpFeatureCapsRotation == 0 {
		return fmt.Errorf("видеопроцессор не поддерживает поворот (DXGI Rotation=%d)", rotation)
	}
	return nil
}

// inputView создаёт представление BGRA-текстуры как входа процессора.
func (v *videoProc) inputView(tex *com.Object) (*com.Object, error) {
	desc := [4]uint32{0, vpViewDimensionTexture2D, 0, 0} // FourCC, ViewDimension, MipSlice, ArraySlice
	var view *com.Object
	hr := v.vdev.Call(slotVDevCreateInputView, uintptr(unsafe.Pointer(tex)), uintptr(unsafe.Pointer(v.enum)),
		uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&view)))
	if err := com.Check("CreateVideoProcessorInputView", hr); err != nil {
		return nil, err
	}
	return view, nil
}

// outputView создаёт представление NV12-текстуры как выхода процессора.
func (v *videoProc) outputView(tex *com.Object) (*com.Object, error) {
	desc := [4]uint32{vpViewDimensionTexture2D, 0, 0, 0} // ViewDimension, MipSlice (остальное — union)
	var view *com.Object
	hr := v.vdev.Call(slotVDevCreateOutputView, uintptr(unsafe.Pointer(tex)), uintptr(unsafe.Pointer(v.enum)),
		uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&view)))
	if err := com.Check("CreateVideoProcessorOutputView", hr); err != nil {
		return nil, err
	}
	return view, nil
}

// blt выполняет преобразование входного представления в выходное.
func (v *videoProc) blt(in, out *com.Object) error {
	stream := vpStream{Enable: 1, InputSurface: in}
	hr := v.vctx.Call(slotVCtxBlt, uintptr(unsafe.Pointer(v.proc)), uintptr(unsafe.Pointer(out)), 0, 1,
		uintptr(unsafe.Pointer(&stream)))
	return com.Check("VideoProcessorBlt", hr)
}

func (v *videoProc) releaseProc() {
	v.proc.Release()
	v.enum.Release()
	v.proc, v.enum = nil, nil
}

func (v *videoProc) release() {
	v.releaseProc()
	v.vctx.Release()
	v.vdev.Release()
	v.vctx, v.vdev = nil, nil
}
