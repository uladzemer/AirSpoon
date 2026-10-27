//go:build windows && (amd64 || arm64)

package mf

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"airspoon/internal/win/com"
)

// Packet — один закодированный кадр в формате Annex-B.
type Packet struct {
	AnnexB []byte
	// SampleTime — время образца в единицах по 100 нс, как оно было передано
	// на вход (кодеры сохраняют его).
	SampleTime int64
	// Key — в кадре есть IDR-слайс.
	Key bool
}

// Config — параметры кодера.
type Config struct {
	Width, Height, FPS int
	BitrateKbps        int
	// Device — устройство D3D11 (ID3D11Device) захвата; нужно аппаратному кодеру
	// для приёма текстур NV12. Без него (или если MFT не поддерживает D3D11)
	// кодер принимает NV12 из системной памяти.
	Device *com.Object
	// AdapterLUID — видеокарта, на которой работает захват. Аппаратные кодеры
	// ищутся для неё (MFTEnum2 с MFT_ENUM_ADAPTER_LUID); если на ней кодера нет —
	// берётся любой аппаратный, но кадры ему передаются через системную память
	// (текстуры другой видеокарты принять он не может). nil — без привязки.
	AdapterLUID *AdapterLUID
	// Output получает готовые кадры. Вызывается из потока кодера: обработчик не
	// должен блокироваться надолго.
	Output func(Packet)
	// Logf — сообщения для пользователя, Debugf — подробности (оба могут быть nil).
	Logf, Debugf func(format string, args ...any)
}

func (c *Config) validate() error {
	if c.Width < 2 || c.Height < 2 || c.Width%2 != 0 || c.Height%2 != 0 {
		return fmt.Errorf("недопустимый размер кадра %dx%d (нужны чётные размеры)", c.Width, c.Height)
	}
	if c.FPS < 1 || c.FPS > 240 {
		return fmt.Errorf("недопустимая частота кадров %d", c.FPS)
	}
	if c.BitrateKbps < 100 {
		return fmt.Errorf("недопустимый битрейт %d кбит/с", c.BitrateKbps)
	}
	if c.Output == nil {
		return errors.New("не задан обработчик выхода кодера")
	}
	return nil
}

// Encoder — H.264 кодер на Media Foundation.
type Encoder struct {
	cfg      Config
	name     string
	hardware bool
	async    bool
	textures bool // вход — текстуры D3D11

	activate *com.Object // IMFActivate аппаратного MFT (nil для программного)
	mft      *com.Object
	codec    *com.Object
	events   *com.Object
	devMgr   *com.Object

	providesSamples bool
	outBuf          *com.Object
	outSample       *com.Object

	mu     sync.Mutex // сериализует вызовы MFT
	params paramSets

	needInput atomic.Int32
	keys      *keyGate  // запрос ключевого кадра: не сбрасывается до успеха SetValue
	wd        *watchdog // зависание кодера и ожидание ключевого кадра
	closing   atomic.Bool
	inputs    atomic.Uint64
	outputs   atomic.Uint64
	failMu    sync.Mutex
	failure   error
	evDone    chan struct{}
	drainDone chan struct{} // получает сигнал от потока событий по METransformDrainComplete
	closeOnce sync.Once
	warnedPS  bool

	// Состояние emit (вызывается под e.mu).
	lastST        int64
	haveST        bool
	warnedReorder bool
	reorders      atomic.Uint64
	keyWarned     bool // отказ ForceKeyFrame уже показан пользователю
	spsMu         sync.Mutex
	spsRaw        []byte  // SPS, который уже разобран и записан в журнал
	spsInfo       SPSInfo // его разбор (spsOK — разбор удался)
	spsOK         bool
	variant       typeVariant      // вариант типа выхода, который приняла MFT
	rejected      []ParamRejection // свойства ICodecAPI, не принятые кодером
	level         int              // level_idc, который мы запросили
}

// ParamRejection — свойство ICodecAPI, которое кодер не принял.
type ParamRejection struct {
	Name string
	// HR — код ошибки SetValue; 0, если IsSupported ответил «не поддерживается».
	HR     uint32
	Reason string
}

func (p ParamRejection) String() string {
	if p.HR == 0 {
		return fmt.Sprintf("%s (%s)", p.Name, p.Reason)
	}
	return fmt.Sprintf("%s (0x%08X)", p.Name, p.HR)
}

// typeVariant — какие необязательные атрибуты положены в тип выхода/входа.
type typeVariant struct{ profile, level, color bool }

func (v typeVariant) String() string {
	var parts []string
	if v.profile {
		parts = append(parts, "профиль")
	}
	if v.level {
		parts = append(parts, "уровень")
	}
	if v.color {
		parts = append(parts, "цвет BT.709")
	}
	if len(parts) == 0 {
		return "только размер и битрейт"
	}
	return strings.Join(parts, "+")
}

type candidate struct {
	name     string
	activate *com.Object // nil: создать по CLSID_MSH264EncoderMFT
}

func (c *Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Config) debugf(format string, args ...any) {
	if c.Debugf != nil {
		c.Debugf(format, args...)
	}
}

// NewHardware перебирает аппаратные кодеры H.264 (MFTEnumEx с флагами
// HARDWARE|SORTANDFILTER) и возвращает первый, который удалось настроить.
func NewHardware(cfg Config) (*Encoder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := startup(); err != nil {
		return nil, err
	}
	var failures []string
	// Сначала кодеры видеокарты захвата (с текстурами), затем любые аппаратные
	// через системную память.
	if cfg.AdapterLUID != nil {
		cands, err := enumerateHardware(cfg.AdapterLUID)
		if err != nil {
			cfg.debugf("MFTEnum2 для видеокарты %s: %v", cfg.AdapterLUID, err)
			failures = append(failures, "MFTEnum2: "+err.Error())
		}
		if len(cands) == 0 && err == nil {
			cfg.logf("на видеокарте захвата (%s) аппаратных кодеров H.264 нет", cfg.AdapterLUID)
		}
		if enc, f := tryCandidates(cfg, cands); enc != nil {
			return enc, nil
		} else {
			failures = append(failures, f...)
		}
		// Другая видеокарта не примет текстуры этого устройства D3D11.
		cfg.Device = nil
		cfg.logf("пробуем аппаратные кодеры других видеокарт: кадры пойдут через системную память")
	}
	cands, err := enumerateHardware(nil)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		if len(failures) > 0 {
			return nil, fmt.Errorf("аппаратных кодеров H.264 не удалось запустить (%s)", strings.Join(failures, "; "))
		}
		return nil, errors.New("аппаратных кодеров H.264 в системе нет")
	}
	enc, f := tryCandidates(cfg, cands)
	if enc != nil {
		return enc, nil
	}
	failures = append(failures, f...)
	return nil, fmt.Errorf("ни один аппаратный кодер H.264 не удалось запустить (%s)", strings.Join(failures, "; "))
}

// tryCandidates запускает первый подошедший кодер из списка (остальные объекты
// освобождаются). Возвращает nil и причины отказов, если не подошёл ни один.
func tryCandidates(cfg Config, cands []candidate) (*Encoder, []string) {
	var failures []string
	for i, cand := range cands {
		enc, err := newEncoder(cfg, cand, true)
		if err == nil {
			for _, rest := range cands[i+1:] {
				rest.activate.Release()
			}
			return enc, nil
		}
		// newEncoder уже освободил объект кандидата.
		cfg.debugf("аппаратный кодер %q не подошёл: %v", cand.name, err)
		failures = append(failures, fmt.Sprintf("%s: %v", cand.name, err))
	}
	return nil, failures
}

// NewSoftware создаёт программный Microsoft H.264 Encoder MFT.
func NewSoftware(cfg Config) (*Encoder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := startup(); err != nil {
		return nil, err
	}
	return newEncoder(cfg, candidate{name: "Microsoft H.264 Encoder MFT"}, false)
}

// AdapterLUID — локальный идентификатор видеокарты (структура LUID).
type AdapterLUID struct {
	Low  uint32
	High int32
}

func (l *AdapterLUID) String() string {
	if l == nil {
		return "<нет>"
	}
	return fmt.Sprintf("%08X:%08X", uint32(l.High), l.Low)
}

// enumerateHardware перечисляет аппаратные кодеры H.264. С luid — только
// кодеры этой видеокарты (MFTEnum2, нужен Windows 10), без — все.
func enumerateHardware(luid *AdapterLUID) ([]candidate, error) {
	out := registerTypeInfo{Major: mediaTypeVideo, Subtype: videoFormatH264}
	var arr **com.Object
	var n uint32
	if luid == nil {
		if err := procMFTEnumEx.Find(); err != nil {
			return nil, fmt.Errorf("MFTEnumEx недоступна: %w", err)
		}
		hr, _, _ := procMFTEnumEx.Call(uintptr(unsafe.Pointer(&mftCategoryVideoEncoder)),
			mftEnumFlagHardware|mftEnumFlagSortAndFilter, 0, uintptr(unsafe.Pointer(&out)),
			uintptr(unsafe.Pointer(&arr)), uintptr(unsafe.Pointer(&n)))
		if err := com.Check("MFTEnumEx", hr); err != nil {
			return nil, err
		}
	} else {
		if err := procMFTEnum2.Find(); err != nil {
			return nil, fmt.Errorf("MFTEnum2 недоступна: %w", err)
		}
		attrs, err := newLUIDAttributes(luid)
		if err != nil {
			return nil, err
		}
		defer attrs.Release()
		// Флаг HARDWARE при LUID обязателен, иначе E_INVALIDARG.
		hr, _, _ := procMFTEnum2.Call(uintptr(unsafe.Pointer(&mftCategoryVideoEncoder)),
			mftEnumFlagHardware|mftEnumFlagSortAndFilter, 0, uintptr(unsafe.Pointer(&out)),
			uintptr(unsafe.Pointer(attrs)), uintptr(unsafe.Pointer(&arr)), uintptr(unsafe.Pointer(&n)))
		if err := com.Check("MFTEnum2", hr); err != nil {
			return nil, err
		}
	}
	if n == 0 || arr == nil {
		return nil, nil
	}
	defer coTaskMemFree(unsafe.Pointer(arr))
	var list []candidate
	for _, act := range unsafe.Slice(arr, n) {
		name, err := getString(act, &attrFriendlyName)
		if err != nil || name == "" {
			name = "аппаратный кодер H.264"
		}
		list = append(list, candidate{name: name, activate: act})
	}
	return list, nil
}

func newEncoder(cfg Config, cand candidate, hardware bool) (*Encoder, error) {
	if err := startup(); err != nil {
		cand.activate.Release()
		return nil, err
	}
	e := &Encoder{cfg: cfg, name: cand.name, hardware: hardware, activate: cand.activate}
	e.keys = newKeyGate(keyRequestInterval)
	// Первый кадр должен быть ключевым.
	e.keys.request()
	if err := e.setup(); err != nil {
		e.release(false)
		return nil, err
	}
	// Сторож заводим после setup: создание аппаратного MFT занимает секунды, и
	// это время не должно входить в пороги старта и первого ключевого кадра.
	// Сами часы пойдут с первого offer().
	now := time.Now()
	e.wd = newWatchdog(now)
	e.wd.requestKey(now)
	return e, nil
}

// setup создаёт MFT и настраивает его: тип выхода, тип входа, параметры кодирования.
func (e *Encoder) setup() error {
	if e.activate != nil {
		hr := e.activate.Call(slotActivateObject, uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&e.mft)))
		if err := com.Check("ActivateObject", hr); err != nil {
			return err
		}
	} else {
		const clsctxInprocServer = 1
		hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMSH264EncoderMFT)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&e.mft)))
		if err := checkSoftwareCreate(hr); err != nil {
			return err
		}
	}

	d3dAware := false
	var attrs *com.Object
	if hr := e.mft.Call(slotMFTGetAttributes, uintptr(unsafe.Pointer(&attrs))); int32(hr) >= 0 && attrs != nil {
		if async, _ := getU32(attrs, &attrTransformAsync); async == 1 {
			e.async = true
			if err := setU32(attrs, &attrAsyncUnlock, 1); err != nil {
				attrs.Release()
				return fmt.Errorf("разблокировка асинхронного MFT: %w", err)
			}
			ev, err := e.mft.QueryInterface(&iidIMFMediaEventGen)
			if err != nil {
				attrs.Release()
				return fmt.Errorf("IMFMediaEventGenerator: %w", err)
			}
			e.events = ev
		}
		_ = setU32(attrs, &attrLowLatency, 1)
		if aware, _ := getU32(attrs, &attrD3D11Aware); aware == 1 {
			d3dAware = true
		}
		attrs.Release()
	}
	if e.hardware && !e.async {
		return errors.New("аппаратный MFT не асинхронный: такой режим не поддерживается")
	}

	// D3D11: текстуры на входе, если MFT это умеет.
	if e.hardware && e.cfg.Device != nil && d3dAware {
		if err := e.attachDeviceManager(); err != nil {
			e.cfg.debugf("%s: текстуры D3D11 недоступны (%v), вход через системную память", e.name, err)
		} else {
			e.textures = true
		}
	}

	if codec, err := e.mft.QueryInterface(&iidICodecAPI); err == nil {
		e.codec = codec
	}
	// Часть кодеров ждёт свойства до типов, часть сбрасывает их при смене типа:
	// ставим до типов, затем повторно после, перед BEGIN_STREAMING. В итоговый
	// отчёт попадает результат второго прохода.
	e.applyCodecSettings()

	if err := e.setTypes(); err != nil {
		return err
	}
	e.rejected = e.applyCodecSettings()
	for _, r := range e.rejected {
		e.cfg.debugf("%s: свойство %s не принято", e.name, r)
	}
	var info outputStreamInfo
	if hr := e.mft.Call(slotMFTGetOutputStreamInfo, 0, uintptr(unsafe.Pointer(&info))); int32(hr) >= 0 {
		e.providesSamples = info.Flags&(mftOutputStreamProvidesSamples|mftOutputStreamCanProvideSamples) != 0
		if !e.providesSamples {
			size := int(info.Size)
			if size < 1<<20 {
				size = 1 << 20
			}
			if err := e.allocOutputSample(size); err != nil {
				return err
			}
		}
	}
	e.refreshSequenceHeader()
	if err := com.Check("BEGIN_STREAMING", e.mft.Call(slotMFTProcessMessage, mftMessageNotifyBeginStreaming, 0)); err != nil {
		return err
	}
	if err := com.Check("START_OF_STREAM", e.mft.Call(slotMFTProcessMessage, mftMessageNotifyStartOfStream, 0)); err != nil {
		return err
	}
	e.logSettings()
	if e.async {
		e.evDone = make(chan struct{})
		e.drainDone = make(chan struct{}, 1)
		go e.eventLoop()
	}
	return nil
}

func (e *Encoder) attachDeviceManager() error {
	var token uint32
	var mgr *com.Object
	hr, _, _ := procMFCreateDXGIManager.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&mgr)))
	if err := com.Check("MFCreateDXGIDeviceManager", hr); err != nil {
		return err
	}
	if err := com.Check("ResetDevice", mgr.Call(slotDevMgrResetDevice, uintptr(unsafe.Pointer(e.cfg.Device)), uintptr(token))); err != nil {
		mgr.Release()
		return err
	}
	hr2 := e.mft.Call(slotMFTProcessMessage, mftMessageSetD3DManager, uintptr(unsafe.Pointer(mgr)))
	if err := com.Check("SET_D3D_MANAGER", hr2); err != nil {
		mgr.Release()
		return err
	}
	e.devMgr = mgr
	return nil
}

func (e *Encoder) allocOutputSample(size int) error {
	buf, err := createMemoryBuffer(size)
	if err != nil {
		return err
	}
	s, err := createSample()
	if err != nil {
		buf.Release()
		return err
	}
	if err := com.Check("AddBuffer", s.Call(slotSampleAddBuffer, uintptr(unsafe.Pointer(buf)))); err != nil {
		s.Release()
		buf.Release()
		return err
	}
	e.outBuf, e.outSample = buf, s
	return nil
}

// codecParam — одно свойство ICodecAPI, которое мы хотим выставить.
type codecParam struct {
	name string
	api  *com.GUID
	vt   uint16
	val  uint64
}

const variantTrue = 0xFFFF // VARIANT_TRUE

func (e *Encoder) codecParams() []codecParam {
	return []codecParam{
		{"AVLowLatencyMode", &apiLowLatencyMode, vtBool, variantTrue},
		{"AVEncCommonLowLatency", &apiCommonLowLat, vtBool, variantTrue},
		{"AVEncCommonRealTime", &apiRealTime, vtBool, variantTrue},
		{"RateControlMode=CBR", &apiRateControlMode, vtUI4, 0},
		{"MeanBitRate", &apiMeanBitRate, vtUI4, uint64(e.cfg.BitrateKbps) * 1000},
		{"GOPSize", &apiGOPSize, vtUI4, uint64(2 * e.cfg.FPS)},
		{"DefaultBPictureCount=0", &apiBPictureCount, vtUI4, 0},
	}
}

// hrOf достаёт HRESULT из ошибки COM (0, если это не она).
func hrOf(err error) uint32 {
	var ce *com.Error
	if errors.As(err, &ce) {
		return ce.HR
	}
	return 0
}

// applyCodecSettings задаёт параметры через ICodecAPI и возвращает не принятые.
// Перед SetValue спрашивает IsSupported: если кодер честно отвечает «нет»,
// свойство пропускается. Отказ отдельного свойства не фатален: не каждый кодер
// знает каждое. Профиль и уровень сюда не входят: они задаются типом выхода
// (setTypes), а не MPVProfile/MPVLevel — те принимают не все кодеры.
func (e *Encoder) applyCodecSettings() []ParamRejection {
	if e.codec == nil {
		e.cfg.debugf("%s: ICodecAPI недоступен, параметры по умолчанию", e.name)
		return nil
	}
	var rejected []ParamRejection
	for _, p := range e.codecParams() {
		if ok, _ := codecSupported(e.codec, p.api); !ok {
			rejected = append(rejected, ParamRejection{Name: p.name, Reason: "IsSupported: нет"})
			continue
		}
		if err := setCodec(e.codec, p.api, p.vt, p.val); err != nil {
			rejected = append(rejected, ParamRejection{Name: p.name, HR: hrOf(err), Reason: err.Error()})
		}
	}
	return rejected
}

// rateControlName — название eAVEncCommonRateControlMode.
func rateControlName(v uint64) string {
	names := []string{"CBR", "PeakConstrainedVBR", "UnconstrainedVBR", "Quality", "LowDelayVBR", "GlobalVBR", "GlobalLowDelayVBR"}
	if v < uint64(len(names)) {
		return names[v]
	}
	return fmt.Sprintf("режим %d", v)
}

// codecReadback читает фактические значения свойств ICodecAPI одной строкой.
func (e *Encoder) codecReadback() string {
	if e.codec == nil {
		return "ICodecAPI недоступен"
	}
	read := func(api *com.GUID, format func(v uint64) string) string {
		_, v, err := getCodec(e.codec, api)
		if err != nil {
			if hr := hrOf(err); hr != 0 {
				return fmt.Sprintf("н/д (0x%08X)", hr)
			}
			return "н/д"
		}
		return format(v)
	}
	num := func(v uint64) string { return fmt.Sprint(uint32(v)) }
	boolean := func(v uint64) string {
		if uint16(v) != 0 {
			return "да"
		}
		return "нет"
	}
	return fmt.Sprintf("RateControl=%s, битрейт=%s кбит/с, GOP=%s, B-кадров=%s, малая задержка=%s",
		read(&apiRateControlMode, func(v uint64) string { return rateControlName(uint64(uint32(v))) }),
		read(&apiMeanBitRate, func(v uint64) string { return fmt.Sprint(uint32(v) / 1000) }),
		read(&apiGOPSize, num),
		read(&apiBPictureCount, num),
		read(&apiLowLatencyMode, boolean))
}

// outputTypeProfileLevel читает profile и level текущего типа выхода (0 — не задан).
func (e *Encoder) outputTypeProfileLevel() (profile, level uint32) {
	var t *com.Object
	if hr := e.mft.Call(slotMFTGetOutputCurrent, 0, uintptr(unsafe.Pointer(&t))); int32(hr) < 0 || t == nil {
		return 0, 0
	}
	defer t.Release()
	profile, _ = getU32(t, &attrMTMPEG2Profile)
	level, _ = getU32(t, &attrMTMPEG2Level)
	return profile, level
}

// SettingsSummary — итоговые параметры кодера одной строкой: что приняла MFT по
// типу выхода и что ответил ICodecAPI на GetValue. Вызывать можно только
// до Close.
func (e *Encoder) SettingsSummary() string {
	profile, level := e.outputTypeProfileLevel()
	typ := fmt.Sprintf("тип выхода: профиль %s, уровень %s (запрошен %s; вариант типа: %s)",
		orNone(profile, h264ProfileName), orNone(level, h264LevelName), h264LevelName(e.level), e.variant)
	s := fmt.Sprintf("%dx%d@%d; %s; ICodecAPI: %s", e.cfg.Width, e.cfg.Height, e.cfg.FPS, typ, e.codecReadback())
	if len(e.rejected) > 0 {
		parts := make([]string, len(e.rejected))
		for i, r := range e.rejected {
			parts[i] = r.String()
		}
		s += "; не принято: " + strings.Join(parts, ", ")
	}
	return s
}

func orNone(v uint32, name func(int) string) string {
	if v == 0 {
		return "не задан"
	}
	return name(int(v))
}

// logSettings пишет в обычный журнал итоговые параметры кодера (одна строка).
func (e *Encoder) logSettings() {
	e.cfg.logf("кодер %q: %s", e.name, e.SettingsSummary())
}

// makeType строит тип носителя для выхода (H.264) или входа (NV12).
func (e *Encoder) makeType(output bool, v typeVariant) (*com.Object, error) {
	var t *com.Object
	hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&t)))
	if err := com.Check("MFCreateMediaType", hr); err != nil {
		return nil, err
	}
	sub := &videoFormatNV12
	if output {
		sub = &videoFormatH264
	}
	steps := []error{
		setGUID(t, &attrMTMajorType, &mediaTypeVideo),
		setGUID(t, &attrMTSubtype, sub),
		setPair(t, &attrMTFrameSize, uint32(e.cfg.Width), uint32(e.cfg.Height)),
		setPair(t, &attrMTFrameRate, uint32(e.cfg.FPS), 1),
		setPair(t, &attrMTPixelAspect, 1, 1),
		setU32(t, &attrMTInterlace, mfVideoInterlaceProgressive),
	}
	if output {
		steps = append(steps, setU32(t, &attrMTAvgBitrate, uint32(e.cfg.BitrateKbps)*1000))
		if v.profile {
			steps = append(steps, setU32(t, &attrMTMPEG2Profile, h264ProfileMain))
		}
		if v.level {
			steps = append(steps, setU32(t, &attrMTMPEG2Level, uint32(e.level)))
		}
	}
	if v.color {
		steps = append(steps,
			setU32(t, &attrMTYUVMatrix, mfTransferMatrixBT709),
			setU32(t, &attrMTPrimaries, mfPrimariesBT709),
			setU32(t, &attrMTTransferFunc, mfTransferFunc709),
			setU32(t, &attrMTNominalRange, mfNominalRange16235))
	}
	for _, err := range steps {
		if err != nil {
			t.Release()
			return nil, err
		}
	}
	return t, nil
}

// setTypes задаёт тип выхода (до типа входа: так требуют кодеры H.264) и тип входа.
// Профиль и уровень (по макроблокам в секунду, см. chooseH264Level) ставятся на
// типе выхода: с профилем, уровнем и цветовыми атрибутами BT.709, затем проще,
// последним — вариант без уровня (кодер, не знающий MF_MT_MPEG2_LEVEL, выберет
// его сам) и совсем без необязательных атрибутов.
func (e *Encoder) setTypes() error {
	e.level = chooseH264Level(e.cfg.Width, e.cfg.Height, e.cfg.FPS)
	variants := []typeVariant{
		{profile: true, level: true, color: true},
		{profile: true, level: true},
		{profile: true, color: true},
		{profile: true},
		{},
	}
	var lastErr error
	for _, v := range variants {
		out, err := e.makeType(true, v)
		if err != nil {
			return err
		}
		hr := e.mft.Call(slotMFTSetOutputType, 0, uintptr(unsafe.Pointer(out)), 0)
		out.Release()
		if lastErr = com.Check("SetOutputType", hr); lastErr == nil {
			e.variant = v
			break
		}
		e.cfg.debugf("%s: тип выхода (%s) не принят: %v", e.name, v, lastErr)
	}
	if lastErr != nil {
		return lastErr
	}
	for _, color := range []bool{true, false} {
		in, err := e.makeType(false, typeVariant{color: color})
		if err != nil {
			return err
		}
		hr := e.mft.Call(slotMFTSetInputType, 0, uintptr(unsafe.Pointer(in)), 0)
		in.Release()
		if lastErr = com.Check("SetInputType(NV12)", hr); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// refreshSequenceHeader читает SPS/PPS из MF_MT_MPEG_SEQUENCE_HEADER текущего типа выхода.
func (e *Encoder) refreshSequenceHeader() {
	var t *com.Object
	if hr := e.mft.Call(slotMFTGetOutputCurrent, 0, uintptr(unsafe.Pointer(&t))); int32(hr) < 0 || t == nil {
		return
	}
	defer t.Release()
	if blob, err := getBlob(t, &attrMTSeqHeader); err == nil && len(blob) > 0 {
		e.params.learnSequenceHeader(blob)
	}
}

// --- публичный интерфейс ---

// Name — имя выбранного MFT.
func (e *Encoder) Name() string { return e.name }

// Hardware — кодер аппаратный.
func (e *Encoder) Hardware() bool { return e.hardware }

// Textures — кодер принимает текстуры D3D11 (иначе NV12 из памяти).
func (e *Encoder) Textures() bool { return e.textures }

// Inputs и Outputs — счётчики принятых кадров и выданных пакетов.
func (e *Encoder) Inputs() uint64  { return e.inputs.Load() }
func (e *Encoder) Outputs() uint64 { return e.outputs.Load() }

// Err возвращает фатальную ошибку кодера, если она произошла.
func (e *Encoder) Err() error {
	e.failMu.Lock()
	defer e.failMu.Unlock()
	return e.failure
}

func (e *Encoder) fail(err error) {
	e.failMu.Lock()
	if e.failure == nil {
		e.failure = err
	}
	e.failMu.Unlock()
}

// ForceKeyFrame просит выдать IDR на ближайшем кадре. Запрос живёт, пока кодер
// не примет SetValue, и уходит к кодеру не чаще раза в секунду. Если ключевой
// кадр не пришёл за несколько секунд, Health вернёт *StallError.
func (e *Encoder) ForceKeyFrame() {
	e.keys.request()
	e.wd.requestKey(time.Now())
}

// Health проверяет, что кодер не завис: кадры подаются, а пакетов нет дольше 2 с
// (3 с до первого пакета), либо запрошенный ключевой кадр не приходит дольше 3 с.
// Возвращает *StallError (см. IsStall); вызывать из цикла подачи кадров.
func (e *Encoder) Health() error { return e.wd.check(time.Now()) }

// applyForceKey (под e.mu) отправляет кодеру ожидающий запрос IDR.
func (e *Encoder) applyForceKey() {
	seq, ok := e.keys.take(time.Now())
	if !ok {
		return
	}
	if e.codec == nil {
		// Принудительно запросить IDR нечем: остаётся периодический.
		e.keys.done(seq, true)
		return
	}
	err := setCodec(e.codec, &apiForceKeyFrame, vtUI4, 1)
	e.keys.done(seq, err == nil)
	if err == nil {
		return
	}
	// Первый отказ показываем пользователю, повторы (раз в секунду) — в подробный журнал.
	if !e.keyWarned {
		e.keyWarned = true
		e.cfg.logf("%s: принудительный IDR не принят (%v), запрос будет повторяться", e.name, err)
		return
	}
	e.cfg.debugf("%s: принудительный IDR не принят: %v", e.name, err)
}

// Reorders — сколько раз кодер выдал кадр, время образца которого не больше, чем у
// предыдущего выданного: признак B-кадров (см. emit).
func (e *Encoder) Reorders() uint64 { return e.reorders.Load() }

// SPS возвращает разбор последнего SPS потока (ok=false, если SPS ещё не было
// или не разобрался).
func (e *Encoder) SPS() (SPSInfo, bool) {
	e.spsMu.Lock()
	defer e.spsMu.Unlock()
	return e.spsInfo, e.spsOK
}

// EncodeTexture подаёт кадр NV12 в виде текстуры D3D11. accepted=false — кодер
// сейчас занят, кадр нужно отбросить (задержка не копится).
func (e *Encoder) EncodeTexture(tex *com.Object, sampleTime int64) (accepted bool, err error) {
	if !e.textures {
		return false, errors.New("кодер не принимает текстуры")
	}
	if err := e.Err(); err != nil {
		return false, err
	}
	e.wd.offer(time.Now())
	if e.async && e.needInput.Load() <= 0 {
		return false, nil
	}
	s, err := newTextureSample(tex, e.cfg.Width*e.cfg.Height*3/2)
	if err != nil {
		return false, err
	}
	defer s.Release()
	return e.feed(s, sampleTime)
}

// EncodeNV12 подаёт кадр NV12 из системной памяти (строки без выравнивания).
func (e *Encoder) EncodeNV12(data []byte, sampleTime int64) (accepted bool, err error) {
	if want := e.cfg.Width * e.cfg.Height * 3 / 2; len(data) != want {
		return false, fmt.Errorf("кадр NV12 %d байт, ожидалось %d", len(data), want)
	}
	if err := e.Err(); err != nil {
		return false, err
	}
	e.wd.offer(time.Now())
	if e.async && e.needInput.Load() <= 0 {
		return false, nil
	}
	s, err := newSystemMemorySample(data)
	if err != nil {
		return false, err
	}
	defer s.Release()
	return e.feed(s, sampleTime)
}

func (e *Encoder) feed(s *com.Object, sampleTime int64) (bool, error) {
	dur := int64(10_000_000 / e.cfg.FPS)
	s.Call(slotSampleSetTime, uintptr(sampleTime))
	s.Call(slotSampleSetDuration, uintptr(dur))
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applyForceKey()
	hr := e.mft.Call(slotMFTProcessInput, 0, uintptr(unsafe.Pointer(s)), 0)
	if uint32(hr) == hrNotAccepting && !e.async {
		// Синхронный MFT просит сначала забрать готовое.
		if err := e.drainLocked(); err != nil {
			return false, err
		}
		hr = e.mft.Call(slotMFTProcessInput, 0, uintptr(unsafe.Pointer(s)), 0)
		if uint32(hr) == hrNotAccepting {
			return false, nil
		}
	}
	if err := com.Check("ProcessInput", hr); err != nil {
		return false, err
	}
	e.inputs.Add(1)
	if e.async {
		e.needInput.Add(-1)
		return true, nil
	}
	return true, e.drainLocked()
}

// drainLocked забирает все готовые выходы синхронного MFT (e.mu удерживается).
func (e *Encoder) drainLocked() error {
	for i := 0; i < 64; i++ {
		got, err := e.processOutputLocked()
		if err != nil {
			e.fail(err)
			return err
		}
		if !got {
			return nil
		}
	}
	return nil
}

// processOutputLocked читает один выход; got=false — выходов пока нет.
func (e *Encoder) processOutputLocked() (got bool, err error) {
	var out outputDataBuffer
	if !e.providesSamples {
		out.Sample = e.outSample
	}
	var status uint32
	hr := e.mft.Call(slotMFTProcessOutput, 0, 1, uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&status)))
	if out.Events != nil {
		out.Events.Release()
	}
	switch uint32(hr) {
	case hrNeedMoreInput:
		return false, nil
	case hrStreamChange:
		if out.Sample != nil && e.providesSamples {
			out.Sample.Release()
		}
		if err := e.renegotiateOutput(); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := com.Check("ProcessOutput", hr); err != nil {
		if out.Sample != nil && e.providesSamples {
			out.Sample.Release()
		}
		return false, err
	}
	if out.Sample == nil {
		return false, nil
	}
	data, rerr := readSample(out.Sample)
	var st int64
	out.Sample.Call(slotSampleGetTime, uintptr(unsafe.Pointer(&st)))
	if e.providesSamples {
		out.Sample.Release()
	} else {
		e.outBuf.Call(slotBufSetCurrentLength, 0)
	}
	if rerr != nil {
		return false, rerr
	}
	e.emit(data, st)
	return true, nil
}

func (e *Encoder) emit(data []byte, sampleTime int64) {
	if len(data) == 0 {
		return
	}
	au, key, complete := e.params.prepare(data)
	if key && !complete {
		// Кодер не положил SPS/PPS, а запомненных нет: пробуем заголовок последовательности.
		e.refreshSequenceHeader()
		au, key, complete = e.params.prepare(data)
		if !complete && !e.warnedPS {
			e.warnedPS = true
			e.cfg.logf("предупреждение: %s не выдал SPS/PPS перед IDR и в заголовке последовательности их нет", e.name)
		}
	}
	if key {
		e.noteSPS()
	}
	// Порядок выдачи: время образца, не большее предыдущего, значит кодер
	// переставляет кадры, то есть выдаёт B-кадры (на входе время строго растёт).
	if e.haveST && sampleTime <= e.lastST {
		e.reorders.Add(1)
		if !e.warnedReorder {
			e.warnedReorder = true
			e.cfg.logf("предупреждение: %s выдаёт кадры не по порядку времени (%d после %d): кодер выдаёт B-кадры, "+
				"приёмник с малой задержкой может их не принять", e.name, sampleTime, e.lastST)
		}
	}
	e.lastST, e.haveST = sampleTime, true
	e.outputs.Add(1)
	e.wd.output(time.Now(), key)
	e.cfg.Output(Packet{AnnexB: au, SampleTime: sampleTime, Key: key})
}

// noteSPS разбирает SPS потока и один раз (и при каждой смене) пишет в журнал
// профиль, уровень, размер и признаки B-кадров.
func (e *Encoder) noteSPS() {
	sps := e.params.sps
	if len(sps) == 0 {
		return
	}
	e.spsMu.Lock()
	same := bytes.Equal(sps, e.spsRaw)
	e.spsMu.Unlock()
	if same {
		return
	}
	info, err := ParseSPS(sps)
	e.spsMu.Lock()
	e.spsRaw = append(e.spsRaw[:0], sps...)
	e.spsInfo, e.spsOK = info, err == nil
	e.spsMu.Unlock()
	if err != nil {
		e.cfg.logf("%s: SPS потока не разобран: %v", e.name, err)
		return
	}
	e.cfg.logf("%s: %s", e.name, info)
}

// renegotiateOutput выбирает тип выхода заново после MF_E_TRANSFORM_STREAM_CHANGE.
func (e *Encoder) renegotiateOutput() error {
	for i := 0; i < 16; i++ {
		var t *com.Object
		hr := e.mft.Call(slotMFTGetOutputAvailable, 0, uintptr(i), uintptr(unsafe.Pointer(&t)))
		if int32(hr) < 0 {
			break
		}
		hr = e.mft.Call(slotMFTSetOutputType, 0, uintptr(unsafe.Pointer(t)), 0)
		t.Release()
		if int32(hr) >= 0 {
			e.refreshSequenceHeader()
			return nil
		}
	}
	return errors.New("не удалось выбрать тип выхода после смены потока")
}

// eventLoop обслуживает события асинхронного MFT: готовность к входу и выход.
func (e *Encoder) eventLoop() {
	defer close(e.evDone)
	cleanup, err := com.Init()
	if err != nil {
		e.fail(err)
		return
	}
	defer cleanup()
	for !e.closing.Load() {
		var ev *com.Object
		hr := e.events.Call(slotGenGetEvent, mfEventFlagNoWait, uintptr(unsafe.Pointer(&ev)))
		if uint32(hr) == hrNoEvents {
			time.Sleep(500 * time.Microsecond)
			continue
		}
		if err := com.Check("GetEvent", hr); err != nil {
			if !e.closing.Load() {
				e.fail(err)
			}
			return
		}
		var typ uint32
		ev.Call(slotEventGetType, uintptr(unsafe.Pointer(&typ)))
		switch typ {
		case meTransformNeedInput:
			e.needInput.Add(1)
		case meTransformHaveOut:
			e.mu.Lock()
			_, perr := e.processOutputLocked()
			e.mu.Unlock()
			if perr != nil && !e.closing.Load() {
				e.fail(perr)
				ev.Release()
				return
			}
		case meTransformDrainDone:
			select {
			case e.drainDone <- struct{}{}:
			default:
			}
		case meError:
			var status uint32
			ev.Call(slotEventGetStatus, uintptr(unsafe.Pointer(&status)))
			e.fail(&com.Error{Op: "событие MEError кодера", HR: status})
			ev.Release()
			return
		}
		ev.Release()
	}
}

// drainTimeout — сколько ждать, пока MFT выдаст всё, что у него в работе, при остановке.
const drainTimeout = time.Second

// Close останавливает кодер и освобождает все объекты COM. Повторный вызов безопасен.
//
// Остановка идёт в порядке, который требует Media Foundation для асинхронных
// MFT: сначала MFT просят закончить начатую работу (DRAIN) и ждут
// METransformDrainComplete, пока поток событий забирает готовые пакеты; затем
// END_OF_STREAM, END_STREAMING, ShutdownObject и только после него
// освобождаются указатели. Раньше объекты освобождались, пока внутренние потоки
// драйвера NVIDIA ещё кодировали принятые кадры: изредка это кончалось
// нарушением доступа (0xC0000005) уже после смены кодера.
func (e *Encoder) Close() {
	e.closeOnce.Do(func() {
		drained := e.drain()
		e.closing.Store(true)
		if e.evDone != nil {
			<-e.evDone
		}
		e.release(drained)
	})
}

// drain просит MFT закончить работу над принятыми кадрами и ждёт завершения.
// Возвращает false, если дождаться не удалось (тогда release сбрасывает MFT).
func (e *Encoder) drain() bool {
	if e.mft == nil || e.Err() != nil {
		return false
	}
	e.mu.Lock()
	hr := e.mft.Call(slotMFTProcessMessage, mftMessageCommandDrain, 0)
	var err error
	if int32(hr) >= 0 && !e.async {
		err = e.drainLocked()
	}
	e.mu.Unlock()
	if int32(hr) < 0 || err != nil {
		e.cfg.debugf("%s: остановка: DRAIN не принят (0x%08X, %v)", e.name, uint32(hr), err)
		return false
	}
	if !e.async {
		return true
	}
	select {
	case <-e.drainDone:
		return true
	case <-e.evDone: // поток событий завершился сам (сбой): ждать нечего
		return false
	case <-time.After(drainTimeout):
		e.cfg.debugf("%s: остановка: MFT не завершил DRAIN за %v", e.name, drainTimeout)
		return false
	}
}

func (e *Encoder) release(drained bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.mft != nil {
		e.mft.Call(slotMFTProcessMessage, mftMessageNotifyEndOfStream, 0)
		e.mft.Call(slotMFTProcessMessage, mftMessageNotifyEndStreaming, 0)
		if !drained {
			e.mft.Call(slotMFTProcessMessage, mftMessageCommandFlush, 0)
		}
		if e.devMgr != nil {
			e.mft.Call(slotMFTProcessMessage, mftMessageSetD3DManager, 0)
		}
	}
	if e.activate != nil {
		// ShutdownObject раньше Release: после него объект больше не работает,
		// остаётся только отпустить ссылки.
		e.activate.Call(slotActivateShut)
	}
	for _, o := range []**com.Object{&e.outSample, &e.outBuf, &e.codec, &e.events, &e.mft, &e.devMgr, &e.activate} {
		(*o).Release()
		*o = nil
	}
}
