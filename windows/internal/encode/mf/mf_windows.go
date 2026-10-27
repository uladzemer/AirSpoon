//go:build windows && (amd64 || arm64)

package mf

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/win/com"
)

const (
	mfVersion = 0x00020070 // MF_VERSION: SDK 2, API 0x70

	// MFT_ENUM_FLAG_*
	mftEnumFlagSyncMFT       = 0x01
	mftEnumFlagAsyncMFT      = 0x02
	mftEnumFlagHardware      = 0x04
	mftEnumFlagSortAndFilter = 0x40

	// MFT_MESSAGE_TYPE
	mftMessageCommandFlush           = 0x00000000
	mftMessageCommandDrain           = 0x00000001
	mftMessageSetD3DManager          = 0x00000002
	mftMessageNotifyBeginStreaming   = 0x10000000
	mftMessageNotifyEndStreaming     = 0x10000001
	mftMessageNotifyEndOfStream      = 0x10000002
	mftMessageNotifyStartOfStream    = 0x10000003
	mftOutputStreamProvidesSamples   = 0x00000100
	mftOutputStreamCanProvideSamples = 0x00000200

	// Типы событий Media Foundation.
	meError              = 1
	meTransformNeedInput = 601
	meTransformHaveOut   = 602
	meTransformDrainDone = 603

	mfEventFlagNoWait = 1

	// Значения атрибутов типа носителя.
	mfVideoInterlaceProgressive = 2
	mfNominalRange16235         = 2
	mfTransferMatrixBT709       = 1
	mfPrimariesBT709            = 2
	mfTransferFunc709           = 5
	h264ProfileMain             = 77
)

// HRESULT Media Foundation, которые мы различаем.
const (
	hrNeedMoreInput  uint32 = 0xC00D6D72 // MF_E_TRANSFORM_NEED_MORE_INPUT
	hrStreamChange   uint32 = 0xC00D6D61 // MF_E_TRANSFORM_STREAM_CHANGE
	hrNotAccepting   uint32 = 0xC00D36B5 // MF_E_NOTACCEPTING
	hrNoEvents       uint32 = 0xC00D3E80 // MF_E_NO_EVENTS_AVAILABLE
	hrInvalidMediaTy uint32 = 0xC00D36B4 // MF_E_INVALIDMEDIATYPE
)

// Слоты таблиц методов (см. mfobjects.h, mftransform.h, mfidl.h, icodecapi.h).
const (
	// IMFAttributes (унаследовано IMFMediaType, IMFSample, IMFActivate, IMFMediaEvent)
	slotAttrGetUINT32          = 7
	slotAttrGetGUID            = 10
	slotAttrGetAllocatedString = 13
	slotAttrGetBlobSize        = 14
	slotAttrGetBlob            = 15
	slotAttrSetUINT32          = 21
	slotAttrSetUINT64          = 22
	slotAttrSetGUID            = 24
	slotAttrSetBlob            = 26

	// IMFActivate
	slotActivateObject = 33
	slotActivateShut   = 34

	// IMFSample
	slotSampleGetTime         = 35
	slotSampleSetTime         = 36
	slotSampleSetDuration     = 38
	slotSampleToContiguousBuf = 41
	slotSampleAddBuffer       = 42

	// IMFMediaBuffer
	slotBufLock             = 3
	slotBufUnlock           = 4
	slotBufGetCurrentLength = 5
	slotBufSetCurrentLength = 6

	// IMFMediaEvent
	slotEventGetType   = 33
	slotEventGetStatus = 35

	// IMFMediaEventGenerator
	slotGenGetEvent = 3

	// IMFTransform
	slotMFTGetOutputStreamInfo = 7
	slotMFTGetAttributes       = 8
	slotMFTGetInputAvailable   = 13
	slotMFTGetOutputAvailable  = 14
	slotMFTSetInputType        = 15
	slotMFTSetOutputType       = 16
	slotMFTGetOutputCurrent    = 18
	slotMFTProcessMessage      = 23
	slotMFTProcessInput        = 24
	slotMFTProcessOutput       = 25

	// IMFDXGIDeviceManager
	slotDevMgrResetDevice = 7

	// ICodecAPI
	slotCodecIsSupported = 3
	slotCodecGetValue    = 8
	slotCodecSetValue    = 9
)

var (
	mfplatDLL                = windows.NewLazySystemDLL("mfplat.dll")
	ole32DLL                 = windows.NewLazySystemDLL("ole32.dll")
	procMFStartup            = mfplatDLL.NewProc("MFStartup")
	procMFShutdown           = mfplatDLL.NewProc("MFShutdown")
	procMFCreateSample       = mfplatDLL.NewProc("MFCreateSample")
	procMFCreateMemoryBuffer = mfplatDLL.NewProc("MFCreateMemoryBuffer")
	procMFCreateMediaType    = mfplatDLL.NewProc("MFCreateMediaType")
	procMFCreateDXGIManager  = mfplatDLL.NewProc("MFCreateDXGIDeviceManager")
	procMFCreateDXGISurface  = mfplatDLL.NewProc("MFCreateDXGISurfaceBuffer")
	procMFTEnumEx            = mfplatDLL.NewProc("MFTEnumEx")
	procMFTEnum2             = mfplatDLL.NewProc("MFTEnum2") // Windows 10: перечисление с атрибутами (LUID видеокарты)
	procMFCreateAttributes   = mfplatDLL.NewProc("MFCreateAttributes")
	procCoTaskMemFree        = ole32DLL.NewProc("CoTaskMemFree")
	procCoCreateInstance     = ole32DLL.NewProc("CoCreateInstance")

	iidIMFTransform          = com.ParseGUID("bf94c121-5b05-4e6f-8000-ba598961414d")
	iidIMFMediaEventGen      = com.ParseGUID("2cd0bd52-bcd5-4b89-b62c-eadc0c031e7d")
	iidICodecAPI             = com.ParseGUID("901db4c7-31ce-41a2-85dc-8fa0bf41b8da")
	iidTexture2D             = com.ParseGUID("6f15aaf2-d208-4e89-9ab4-489535d34f9c")
	clsidMSH264EncoderMFT    = com.ParseGUID("6ca50344-051a-4ded-9779-a43305165e35")
	mftCategoryVideoEncoder  = com.ParseGUID("f79eac7d-e545-4387-bdee-d647d7bde42a")
	mediaTypeVideo           = com.ParseGUID("73646976-0000-0010-8000-00aa00389b71")
	videoFormatNV12          = com.ParseGUID("3231564e-0000-0010-8000-00aa00389b71")
	videoFormatH264          = com.ParseGUID("34363248-0000-0010-8000-00aa00389b71")
	attrMTMajorType          = com.ParseGUID("48eba18e-f8c9-4687-bf11-0a74c9f96a8f")
	attrMTSubtype            = com.ParseGUID("f7e34c9a-42e8-4714-b74b-cb29d72c35e5")
	attrMTFrameSize          = com.ParseGUID("1652c33d-d6b2-4012-b834-72030849a37d")
	attrMTFrameRate          = com.ParseGUID("c459a2e8-3d2c-4e44-b132-fee5156c7bb0")
	attrMTPixelAspect        = com.ParseGUID("c6376a1e-8d0a-4027-be45-6d9a0ad39bb6")
	attrMTInterlace          = com.ParseGUID("e2724bb8-e676-4806-b4b2-a8d6efb44ccd")
	attrMTAvgBitrate         = com.ParseGUID("20332624-fb0d-4d9e-bd0d-cbf6786c102e")
	attrMTMPEG2Profile       = com.ParseGUID("ad76a80b-2d5c-4e0b-b375-64e520137036")
	attrMTMPEG2Level         = com.ParseGUID("96f66574-11c5-4015-8666-bff516436da7")
	attrMTSeqHeader          = com.ParseGUID("3c036de7-3ad0-4c9e-9216-ee6d6ac21cb3")
	attrMTYUVMatrix          = com.ParseGUID("3e23d450-2c75-4d25-a00e-b91670d12327")
	attrMTPrimaries          = com.ParseGUID("dbfbe4d7-0740-4ee0-8192-850ab0e21935")
	attrMTTransferFunc       = com.ParseGUID("5fb0fce9-be5c-4935-a811-ec838f8eed93")
	attrMTNominalRange       = com.ParseGUID("c21b8ee5-b956-4071-8daf-325edf5cab11")
	attrMTMaxKeyframeSpacing = com.ParseGUID("c16eb52b-73a1-476f-8d62-839d6a020652")
	attrTransformAsync       = com.ParseGUID("f81a699a-649a-497d-8c73-29f8fed6ad7a")
	attrAsyncUnlock          = com.ParseGUID("e5666d6b-3422-4eb6-a421-da7db1f8e207")
	attrD3D11Aware           = com.ParseGUID("206b4fc8-fcf9-4c51-afe3-9764369e33a0")
	attrFriendlyName         = com.ParseGUID("314ffbae-5b41-4c95-9c19-4e7d586face3")
	attrLowLatency           = com.ParseGUID("9c27891a-ed7a-40e1-88e8-b22727a024ee")
	// MFT_ENUM_ADAPTER_LUID (mfapi.h): LUID видеокарты для MFTEnum2.
	attrEnumAdapterLUID = com.ParseGUID("1d39518c-e220-4da8-a07f-ba172552d6b1")
	// MFT_ENUM_HARDWARE_VENDOR_ID_Attribute: строка вида "VEN_10DE" у IMFActivate аппаратного MFT.
	attrEnumVendorID = com.ParseGUID("3aecb0cc-035b-4bcc-8185-2b8d551ef3af")

	// Свойства ICodecAPI (codecapi.h).
	apiLowLatencyMode  = com.ParseGUID("9c27891a-ed7a-40e1-88e8-b22727a024ee")
	apiCommonLowLat    = com.ParseGUID("9d3ecd55-89e8-490a-970a-0c9548d5a56e")
	apiRateControlMode = com.ParseGUID("1c0608e9-370c-4710-8a58-cb6181c42423")
	apiMeanBitRate     = com.ParseGUID("f7222374-2144-4815-b550-a37f8e12ee52")
	apiGOPSize         = com.ParseGUID("95f31b26-95a4-41aa-9303-246a7fc6eef1")
	apiBPictureCount   = com.ParseGUID("8d390aac-dc5c-4200-b57f-814d04babab2")
	apiForceKeyFrame   = com.ParseGUID("398c1b98-8353-475a-9ef2-8f265d260345")
	apiRealTime        = com.ParseGUID("143a0ff6-a131-43da-b81e-98fbb8ec378e")
)

// variant — VARIANT (24 байта на amd64) для ICodecAPI.
type variant struct {
	vt  uint16
	_   [3]uint16
	val uint64
	_   uint64
}

const (
	vtBool = 11
	vtI4   = 3
	vtUI4  = 19
)

// outputDataBuffer — MFT_OUTPUT_DATA_BUFFER.
type outputDataBuffer struct {
	StreamID uint32
	Sample   *com.Object
	Status   uint32
	Events   *com.Object
}

// outputStreamInfo — MFT_OUTPUT_STREAM_INFO.
type outputStreamInfo struct {
	Flags, Size, Alignment uint32
}

// registerTypeInfo — MFT_REGISTER_TYPE_INFO.
type registerTypeInfo struct {
	Major, Subtype com.GUID
}

var (
	startupOnce sync.Once
	startupErr  error
)

// startup запускает Media Foundation один раз на процесс; MFShutdown не
// вызывается до выхода. Раньше платформа включалась и выключалась вместе с
// каждым кодером, а при смене кодера (вход текстурами -> вход через память)
// старая платформа выключалась, пока рабочие потоки драйвера ещё завершали
// работу: изредка это кончалось нарушением доступа (0xC0000005) в нативном коде.
func startup() error {
	startupOnce.Do(func() {
		if err := procMFStartup.Find(); err != nil {
			startupErr = markUnavailable(fmt.Errorf("Media Foundation недоступна: %w", err))
			return
		}
		hr, _, _ := procMFStartup.Call(mfVersion, 1)
		startupErr = markUnavailable(com.Check("MFStartup", hr))
	})
	return startupErr
}

func coTaskMemFree(p unsafe.Pointer) {
	if p != nil {
		procCoTaskMemFree.Call(uintptr(p))
	}
}

// --- атрибуты ---

func setU32(o *com.Object, key *com.GUID, v uint32) error {
	return com.Check("SetUINT32", o.Call(slotAttrSetUINT32, uintptr(unsafe.Pointer(key)), uintptr(v)))
}

func setU64(o *com.Object, key *com.GUID, v uint64) error {
	return com.Check("SetUINT64", o.Call(slotAttrSetUINT64, uintptr(unsafe.Pointer(key)), uintptr(v)))
}

func setPair(o *com.Object, key *com.GUID, hi, lo uint32) error {
	return setU64(o, key, uint64(hi)<<32|uint64(lo))
}

func setGUID(o *com.Object, key, v *com.GUID) error {
	return com.Check("SetGUID", o.Call(slotAttrSetGUID, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(v))))
}

func getU32(o *com.Object, key *com.GUID) (uint32, error) {
	var v uint32
	err := com.Check("GetUINT32", o.Call(slotAttrGetUINT32, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v))))
	return v, err
}

func getBlob(o *com.Object, key *com.GUID) ([]byte, error) {
	var size uint32
	if err := com.Check("GetBlobSize", o.Call(slotAttrGetBlobSize, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&size)))); err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	var got uint32
	hr := o.Call(slotAttrGetBlob, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&got)))
	if err := com.Check("GetBlob", hr); err != nil {
		return nil, err
	}
	return buf[:got], nil
}

func getString(o *com.Object, key *com.GUID) (string, error) {
	var p *uint16
	var n uint32
	hr := o.Call(slotAttrGetAllocatedString, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n)))
	if err := com.Check("GetAllocatedString", hr); err != nil {
		return "", err
	}
	defer coTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p), nil
}

// --- ICodecAPI ---

func setCodec(codec *com.Object, api *com.GUID, vt uint16, v uint64) error {
	val := variant{vt: vt, val: v}
	return com.Check("ICodecAPI::SetValue", codec.Call(slotCodecSetValue, uintptr(unsafe.Pointer(api)), uintptr(unsafe.Pointer(&val))))
}

// getCodec читает текущее значение свойства ICodecAPI: тип VARIANT и значение
// (для чисел и логических — в младших битах).
func getCodec(codec *com.Object, api *com.GUID) (vt uint16, v uint64, err error) {
	var val variant
	hr := codec.Call(slotCodecGetValue, uintptr(unsafe.Pointer(api)), uintptr(unsafe.Pointer(&val)))
	if err := com.Check("ICodecAPI::GetValue", hr); err != nil {
		return 0, 0, err
	}
	return val.vt, val.val, nil
}

// codecSupported спрашивает ICodecAPI::IsSupported: S_OK — свойство есть, S_FALSE —
// нет. Ошибка (в том числе E_NOTIMPL) значит «кодер не отвечает на вопрос»:
// вызывающий считает свойство поддерживаемым и всё равно пробует SetValue.
func codecSupported(codec *com.Object, api *com.GUID) (supported bool, err error) {
	hr := codec.Call(slotCodecIsSupported, uintptr(unsafe.Pointer(api)))
	if int32(hr) < 0 {
		return true, com.Check("ICodecAPI::IsSupported", hr)
	}
	return hr == 0, nil
}

func setCodecBool(codec *com.Object, api *com.GUID, v bool) error {
	var b uint64
	if v {
		b = 0xFFFF // VARIANT_TRUE
	}
	return setCodec(codec, api, vtBool, b)
}

// --- образцы и буферы ---

func createSample() (*com.Object, error) {
	var s *com.Object
	hr, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(&s)))
	if err := com.Check("MFCreateSample", hr); err != nil {
		return nil, err
	}
	return s, nil
}

func createMemoryBuffer(size int) (*com.Object, error) {
	var b *com.Object
	hr, _, _ := procMFCreateMemoryBuffer.Call(uintptr(size), uintptr(unsafe.Pointer(&b)))
	if err := com.Check("MFCreateMemoryBuffer", hr); err != nil {
		return nil, err
	}
	return b, nil
}

// readSample копирует содержимое образца (все буферы) в новый срез.
func readSample(sample *com.Object) ([]byte, error) {
	var buf *com.Object
	if err := com.Check("ConvertToContiguousBuffer", sample.Call(slotSampleToContiguousBuf, uintptr(unsafe.Pointer(&buf)))); err != nil {
		return nil, err
	}
	defer buf.Release()
	var p *byte
	var maxLen, curLen uint32
	hr := buf.Call(slotBufLock, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&curLen)))
	if err := com.Check("IMFMediaBuffer::Lock", hr); err != nil {
		return nil, err
	}
	out := make([]byte, curLen)
	if curLen > 0 {
		copy(out, unsafe.Slice(p, curLen))
	}
	buf.Call(slotBufUnlock)
	return out, nil
}

// newSystemMemorySample создаёт образец с буфером в системной памяти и копирует в него data.
func newSystemMemorySample(data []byte) (*com.Object, error) {
	buf, err := createMemoryBuffer(len(data))
	if err != nil {
		return nil, err
	}
	defer buf.Release()
	var p *byte
	var maxLen, curLen uint32
	hr := buf.Call(slotBufLock, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&curLen)))
	if err := com.Check("IMFMediaBuffer::Lock", hr); err != nil {
		return nil, err
	}
	copy(unsafe.Slice(p, maxLen), data)
	buf.Call(slotBufUnlock)
	if err := com.Check("SetCurrentLength", buf.Call(slotBufSetCurrentLength, uintptr(len(data)))); err != nil {
		return nil, err
	}
	s, err := createSample()
	if err != nil {
		return nil, err
	}
	if err := com.Check("AddBuffer", s.Call(slotSampleAddBuffer, uintptr(unsafe.Pointer(buf)))); err != nil {
		s.Release()
		return nil, err
	}
	return s, nil
}

// newTextureSample создаёт образец, оборачивающий текстуру D3D11 (NV12).
func newTextureSample(tex *com.Object, nv12Size int) (*com.Object, error) {
	var buf *com.Object
	hr, _, _ := procMFCreateDXGISurface.Call(uintptr(unsafe.Pointer(&iidTexture2D)), uintptr(unsafe.Pointer(tex)), 0, 0, uintptr(unsafe.Pointer(&buf)))
	if err := com.Check("MFCreateDXGISurfaceBuffer", hr); err != nil {
		return nil, err
	}
	defer buf.Release()
	// Для буферов DXGI длину нужно задать явно: часть MFT проверяет её.
	buf.Call(slotBufSetCurrentLength, uintptr(nv12Size))
	s, err := createSample()
	if err != nil {
		return nil, err
	}
	if err := com.Check("AddBuffer", s.Call(slotSampleAddBuffer, uintptr(unsafe.Pointer(buf)))); err != nil {
		s.Release()
		return nil, err
	}
	return s, nil
}

// newLUIDAttributes создаёт хранилище атрибутов с MFT_ENUM_ADAPTER_LUID для MFTEnum2.
func newLUIDAttributes(luid *AdapterLUID) (*com.Object, error) {
	if err := procMFCreateAttributes.Find(); err != nil {
		return nil, fmt.Errorf("MFCreateAttributes недоступна: %w", err)
	}
	if err := startup(); err != nil {
		return nil, err
	}
	var attrs *com.Object
	hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrs)), 1)
	if err := com.Check("MFCreateAttributes", hr); err != nil {
		return nil, err
	}
	// LUID в памяти: DWORD LowPart, LONG HighPart.
	if err := com.Check("SetBlob(MFT_ENUM_ADAPTER_LUID)", attrs.Call(slotAttrSetBlob,
		uintptr(unsafe.Pointer(&attrEnumAdapterLUID)), uintptr(unsafe.Pointer(luid)), unsafe.Sizeof(*luid))); err != nil {
		attrs.Release()
		return nil, err
	}
	return attrs, nil
}

// HardwareEncoderNames возвращает названия аппаратных кодеров H.264 (для
// диагностики): для видеокарты luid или, если luid == nil, для всех.
func HardwareEncoderNames(luid *AdapterLUID) ([]string, error) {
	if err := startup(); err != nil {
		return nil, err
	}
	cands, err := enumerateHardware(luid)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range cands {
		names = append(names, c.name)
		c.activate.Release()
	}
	return names, nil
}

// Startup запускает Media Foundation (один раз на процесс) и возвращает итог:
// для diag. Ошибка совпадает с ErrUnavailable.
func Startup() error { return startup() }

// checkSoftwareCreate превращает HRESULT создания программного кодера в ошибку;
// «класс не зарегистрирован» помечается как ErrUnavailable.
func checkSoftwareCreate(hr uintptr) error {
	err := com.Check("CoCreateInstance(CLSID_MSH264EncoderMFT)", hr)
	if err != nil && uint32(hr) == hrClassNotRegistered {
		return markUnavailable(err)
	}
	return err
}
