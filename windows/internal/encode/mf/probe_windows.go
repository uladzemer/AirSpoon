//go:build windows && (amd64 || arm64)

package mf

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"airspoon/internal/win/com"
)

// EncoderInfo описывает один MFT-кодер H.264 (для diag).
type EncoderInfo struct {
	Name       string
	Vendor     string // «VEN_10DE» у аппаратных (из реестра MFT), «Microsoft» у программного
	Hardware   bool
	Async      bool
	D3D11Aware bool
	// Err — почему не удалось прочитать флаги (MFT не активируется).
	Err string
}

func (i EncoderInfo) String() string {
	kind := "программный"
	if i.Hardware {
		kind = "аппаратный"
	}
	s := fmt.Sprintf("%q (%s, производитель %s, async=%t, D3D11=%t)", i.Name, kind, i.Vendor, i.Async, i.D3D11Aware)
	if i.Err != "" {
		s += ": " + i.Err
	}
	return s
}

// ListH264Encoders перечисляет аппаратные кодеры H.264 видеокарты luid (nil — все)
// и читает у каждого флаги async и D3D11-aware. Для этого MFT приходится
// создавать и сразу освобождать. Нужен com.Init в вызывающем потоке.
func ListH264Encoders(luid *AdapterLUID) ([]EncoderInfo, error) {
	if err := startup(); err != nil {
		return nil, err
	}
	cands, err := enumerateHardware(luid)
	if err != nil {
		return nil, err
	}
	var out []EncoderInfo
	for _, c := range cands {
		info := EncoderInfo{Name: c.name, Hardware: true, Vendor: "?"}
		if v, err := getString(c.activate, &attrEnumVendorID); err == nil && v != "" {
			info.Vendor = v
		}
		var mft *com.Object
		hr := c.activate.Call(slotActivateObject, uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&mft)))
		if err := com.Check("ActivateObject", hr); err != nil {
			info.Err = "не активируется: " + err.Error()
		} else {
			info.Async, info.D3D11Aware = readMFTFlags(mft)
			c.activate.Call(slotActivateShut)
			mft.Release()
		}
		c.activate.Release()
		out = append(out, info)
	}
	return out, nil
}

// SoftwareEncoderInfo описывает программный Microsoft H.264 Encoder MFT; Err
// заполнен, если его не удалось создать (например, N-редакция Windows без
// Media Feature Pack).
func SoftwareEncoderInfo() EncoderInfo {
	info := EncoderInfo{Name: "Microsoft H.264 Encoder MFT", Vendor: "Microsoft"}
	if err := startup(); err != nil {
		info.Err = err.Error()
		return info
	}
	const clsctxInprocServer = 1
	var mft *com.Object
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMSH264EncoderMFT)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&mft)))
	if err := checkSoftwareCreate(hr); err != nil {
		info.Err = err.Error()
		return info
	}
	info.Async, info.D3D11Aware = readMFTFlags(mft)
	mft.Release()
	return info
}

// readMFTFlags читает MF_TRANSFORM_ASYNC и MF_SA_D3D11_AWARE из атрибутов MFT.
func readMFTFlags(mft *com.Object) (async, d3d bool) {
	var attrs *com.Object
	if hr := mft.Call(slotMFTGetAttributes, uintptr(unsafe.Pointer(&attrs))); int32(hr) < 0 || attrs == nil {
		return false, false
	}
	defer attrs.Release()
	a, _ := getU32(attrs, &attrTransformAsync)
	d, _ := getU32(attrs, &attrD3D11Aware)
	return a == 1, d == 1
}

// ProbeConfig — параметры пробного кодирования для diag.
type ProbeConfig struct {
	Width, Height, FPS int
	BitrateKbps        int
	// Frames — сколько кадров подать (с темпом FPS); 0 — две секунды.
	Frames int
	// Software — пробовать программный кодер; иначе аппаратный.
	Software bool
	// EncoderName — аппаратный кодер по имени из ListH264Encoders; "" — первый, что запустится.
	EncoderName string
	// AdapterLUID — искать аппаратные кодеры только этой видеокарты (nil — любые).
	AdapterLUID *AdapterLUID
	// Device и MakeTexture включают вход текстурами D3D11: Device — устройство
	// видеокарты захвата, MakeTexture(i) отдаёт текстуру NV12 кадра i и функцию
	// освобождения (вызывается после передачи кадра кодеру). Без них — вход из
	// системной памяти.
	Device      *com.Object
	MakeTexture func(frame int) (tex *com.Object, release func(), err error)
	Logf        func(format string, args ...any)
}

// ProbeReport — итог пробного кодирования.
type ProbeReport struct {
	Encoder  string
	Hardware bool
	Async    bool
	// Input — какой вход сработал: «текстуры D3D11» или «системная память».
	Input                           string
	Width, Height, FPS, BitrateKbps int
	// Offered — кадров предложено; Accepted — принято кодером; Skipped — кодер был занят.
	Offered, Accepted, Skipped int
	Packets, KeyPackets        int
	Elapsed                    time.Duration
	// AvgFrameTime и MaxFrameTime — время вызова подачи кадра (для аппаратного
	// кодера с текстурами это в основном копирование и постановка в очередь).
	AvgFrameTime, MaxFrameTime time.Duration
	// Reorders — сколько раз кодер выдал кадры не по порядку времени (B-кадры).
	Reorders uint64
	// Settings — итоговые параметры одной строкой (тип выхода + GetValue ICodecAPI).
	Settings string
	// Rejected — свойства ICodecAPI, которые кодер не принял, с HRESULT.
	Rejected []ParamRejection
	// SPS — разбор SPS потока; HasSPS=false, если ключевого кадра не было.
	SPS    SPSInfo
	HasSPS bool
	// Err — ошибка в ходе кодирования или при запуске кодера.
	Err string
}

// Summary — отчёт одной-двумя строками для diag.
func (r ProbeReport) Summary() string {
	var b strings.Builder
	kind := "программный"
	if r.Hardware {
		kind = "аппаратный"
	}
	fmt.Fprintf(&b, "%q (%s, async=%t), вход: %s, %dx%d@%d, %d кбит/с: подано %d, принято %d, пропущено %d, пакетов %d (ключевых %d) за %v, "+
		"среднее время подачи кадра %v, максимум %v",
		r.Encoder, kind, r.Async, r.Input, r.Width, r.Height, r.FPS, r.BitrateKbps, r.Offered, r.Accepted, r.Skipped,
		r.Packets, r.KeyPackets, r.Elapsed.Round(10*time.Millisecond),
		r.AvgFrameTime.Round(10*time.Microsecond), r.MaxFrameTime.Round(10*time.Microsecond))
	if r.Settings != "" {
		b.WriteString("; " + r.Settings)
	}
	if r.HasSPS {
		b.WriteString("; " + r.SPS.String())
	}
	if r.Reorders > 0 {
		fmt.Fprintf(&b, "; кадров не по порядку: %d", r.Reorders)
	}
	if r.Err != "" {
		b.WriteString("; ошибка: " + r.Err)
	}
	return b.String()
}

// probeFrame рисует кадр NV12 с движущимся градиентом.
func probeFrame(w, h, n int) []byte {
	data := make([]byte, w*h*3/2)
	for y := 0; y < h; y++ {
		row := data[y*w : (y+1)*w]
		for x := range row {
			row[x] = byte(16 + (x+3*n+y/4)%200)
		}
	}
	uv := data[w*h:]
	for i := 0; i < len(uv); i += 2 {
		uv[i], uv[i+1] = byte(100+n%50), byte(140-n%40)
	}
	return data
}

// ProbeEncode запускает выбранный кодер, подаёт Frames кадров с темпом FPS и
// возвращает отчёт. Ошибка возвращается, только если кодер не запустился; сбой
// в ходе кодирования попадает в ProbeReport.Err. Нужен com.Init в вызывающем потоке.
func ProbeEncode(pc ProbeConfig) (ProbeReport, error) {
	rep := ProbeReport{Width: pc.Width, Height: pc.Height, FPS: pc.FPS, BitrateKbps: pc.BitrateKbps}
	if pc.Frames <= 0 {
		pc.Frames = 2 * pc.FPS
	}
	var mu sync.Mutex
	var packets, keys int
	cfg := Config{
		Width: pc.Width, Height: pc.Height, FPS: pc.FPS, BitrateKbps: pc.BitrateKbps,
		Device: pc.Device, AdapterLUID: pc.AdapterLUID, Logf: pc.Logf, Debugf: pc.Logf,
		Output: func(p Packet) {
			mu.Lock()
			packets++
			if p.Key {
				keys++
			}
			mu.Unlock()
		},
	}
	if pc.MakeTexture == nil {
		cfg.Device = nil // без источника текстур вход только через память
	}
	if err := cfg.validate(); err != nil {
		rep.Err = err.Error()
		return rep, err
	}
	enc, err := openForProbe(cfg, pc)
	if err != nil {
		rep.Err = err.Error()
		return rep, err
	}
	defer enc.Close()
	rep.Encoder, rep.Hardware, rep.Async = enc.Name(), enc.Hardware(), enc.async
	rep.Input = "системная память"
	useTex := enc.Textures() && pc.MakeTexture != nil
	if useTex {
		rep.Input = "текстуры D3D11"
	}

	frames := make([][]byte, 4)
	if !useTex {
		for i := range frames {
			frames[i] = probeFrame(pc.Width, pc.Height, i*7)
		}
	}
	interval := time.Second / time.Duration(pc.FPS)
	start := time.Now()
	next := start
	var sum time.Duration
loop:
	for i := 0; i < pc.Frames; i++ {
		st := int64(i) * 10_000_000 / int64(pc.FPS)
		t0 := time.Now()
		var accepted bool
		var err error
		if useTex {
			var tex *com.Object
			var release func()
			if tex, release, err = pc.MakeTexture(i); err == nil {
				accepted, err = enc.EncodeTexture(tex, st)
				if release != nil {
					release()
				}
			}
		} else {
			accepted, err = enc.EncodeNV12(frames[i%len(frames)], st)
		}
		d := time.Since(t0)
		sum += d
		if d > rep.MaxFrameTime {
			rep.MaxFrameTime = d
		}
		rep.Offered++
		switch {
		case err != nil:
			rep.Err = fmt.Sprintf("кадр %d: %v", i, err)
			break loop
		case accepted:
			rep.Accepted++
		default:
			rep.Skipped++
		}
		if err := enc.Err(); err != nil {
			rep.Err = err.Error()
			break
		}
		next = next.Add(interval)
		time.Sleep(time.Until(next))
	}
	rep.Elapsed = time.Since(start)
	if rep.Offered > 0 {
		rep.AvgFrameTime = sum / time.Duration(rep.Offered)
	}
	// Дадим кодеру закончить выдачу принятого.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && enc.Outputs() < enc.Inputs(); {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	rep.Packets, rep.KeyPackets = packets, keys
	mu.Unlock()
	rep.Reorders = enc.Reorders()
	rep.Settings = enc.SettingsSummary()
	rep.Rejected = append([]ParamRejection(nil), enc.rejected...)
	rep.SPS, rep.HasSPS = enc.SPS()
	if rep.Err == "" && rep.Accepted > 0 && rep.Packets == 0 {
		rep.Err = "кодер принял кадры, но не выдал ни одного пакета"
	}
	return rep, nil
}

// openForProbe создаёт кодер, который просит проба.
func openForProbe(cfg Config, pc ProbeConfig) (*Encoder, error) {
	if err := startup(); err != nil {
		return nil, err
	}
	if pc.Software {
		cfg.Device = nil
		return newEncoder(cfg, candidate{name: "Microsoft H.264 Encoder MFT"}, false)
	}
	cands, err := enumerateHardware(pc.AdapterLUID)
	if err != nil {
		return nil, err
	}
	var use []candidate
	for _, c := range cands {
		if pc.EncoderName == "" || c.name == pc.EncoderName {
			use = append(use, c)
		} else {
			c.activate.Release()
		}
	}
	if len(use) == 0 {
		if pc.EncoderName != "" {
			return nil, fmt.Errorf("аппаратный кодер %q не найден", pc.EncoderName)
		}
		return nil, errors.New("аппаратных кодеров H.264 нет")
	}
	enc, failures := tryCandidates(cfg, use)
	if enc == nil {
		return nil, fmt.Errorf("кодер не запустился (%s)", strings.Join(failures, "; "))
	}
	return enc, nil
}
