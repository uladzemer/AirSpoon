//go:build windows && (amd64 || arm64)

// Пакет gpudiag собирает диагностику видеокарт для разбора ошибки «не удалось
// захватить экран»: какие видеокарты видит программа (название, LUID,
// выходы, на какой карте каждый монитор), какое предпочтение видеокарты записано
// для программы, на какой карте работает устройство D3D11 и какие кодеры H.264
// (Media Foundation) найдены. Тот же текст печатает команда
// «AirSpoonSender.exe diag» и пишет в sender.log при ошибке захвата.
package gpudiag

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/mf"
	"airspoon/internal/gpupref"
	"airspoon/internal/win/com"
)

// Options — что собирать.
type Options struct {
	// ExePath — путь к программе (ключ предпочтения видеокарты); пусто — os.Executable.
	ExePath string
	// Store — хранилище предпочтений; nil — реестр.
	Store gpupref.Store
	// Probe — сколько пытаться открыть дублирование экрана; 0 — не пробовать.
	Probe time.Duration
	// Display — номер монитора для пробы.
	Display int
	// RetryEnv — значение переменной «перезапуск для смены видеокарты уже был».
	RetryEnv string

	// Extended включает расширенную диагностику (A12): пробы видеокарт и
	// мониторов, пробное кодирование, сеть и звук. Без неё отчёт короткий (для
	// журнала при ошибке захвата): система, Media Foundation, карты, кодеры.
	Extended bool
	// EncodeProbe — длительность каждой пробы кодирования (0 — 2 с; меньше нуля —
	// пробное кодирование пропустить, например пока идёт показ экрана).
	EncodeProbe time.Duration
	// MeasureCapture — сколько измерять захват каждого монитора (0 — 5 с; меньше
	// нуля — не измерять).
	MeasureCapture time.Duration
	// Peer — адрес приёмника (host или host:port): по нему определяется тип сети.
	// Пусто — маршрут по умолчанию.
	Peer string
	// Progress получает короткие сообщения о ходе долгой диагностики; может быть nil.
	Progress func(string)
}

// Report — собранные сведения.
type Report struct {
	At        time.Time
	Exe       string
	Windows   string
	Pref      string // описание предпочтения из реестра
	Retried   bool
	AdaptersE error
	Adapters  []dxgi.AdapterInfo
	Hybrid    bool
	Encoders  EncoderInfo
	Probe     *dxgi.ProbeResult

	System SystemInfo
	// MFErr — Media Foundation не запустилась (errors.Is(err, mf.ErrUnavailable));
	// SoftwareEncoder — результат создания программного кодера (Err — не создан).
	MFErr           error
	SoftwareEncoder mf.EncoderInfo
	// Ext заполнен в расширенном режиме (Options.Extended).
	Ext *Extended
}

// EncoderInfo — найденные кодеры H.264.
type EncoderInfo struct {
	All        []string
	AllErr     error
	PerAdapter []AdapterEncoders
}

// AdapterEncoders — кодеры, найденные для одной видеокарты (MFTEnum2 с LUID).
type AdapterEncoders struct {
	Index int
	Name  string
	LUID  dxgi.LUID
	Names []string
	Err   error
}

// Collect собирает диагностику. Вызывать можно из любой горутины: поток ОС
// закрепляется на время сбора.
func Collect(opts Options) Report {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r := Report{At: time.Now(), Retried: opts.RetryEnv == "1"}
	r.Exe = opts.ExePath
	if r.Exe == "" {
		r.Exe, _ = os.Executable()
	}
	r.Windows = windowsVersion()
	r.System = collectSystem()
	if cleanup, err := com.Init(); err == nil {
		r.MFErr = mf.Startup()
		if r.MFErr == nil {
			r.SoftwareEncoder = mf.SoftwareEncoderInfo()
		}
		cleanup()
	}
	st := opts.Store
	if st == nil {
		st = gpupref.RegistryStore{}
	}
	switch p, set, err := gpupref.Current(st, r.Exe); {
	case err != nil:
		r.Pref = "не прочитано: " + err.Error()
	case !set:
		r.Pref = "не задано (решает Windows)"
	default:
		data, _, _ := st.Get(r.Exe)
		r.Pref = fmt.Sprintf("%s (%s)", strings.TrimSpace(data), p)
	}
	r.Adapters, r.AdaptersE = dxgi.ListAdapters()
	r.Hybrid = gpupref.Hybrid(dxgi.GPUAdapters(r.Adapters))

	if cleanup, err := com.Init(); err == nil {
		defer cleanup()
		r.Encoders.All, r.Encoders.AllErr = mf.HardwareEncoderNames(nil)
		for _, a := range r.Adapters {
			if a.Software() {
				continue
			}
			l := mf.AdapterLUID{Low: a.LUID.Low, High: a.LUID.High}
			names, err := mf.HardwareEncoderNames(&l)
			r.Encoders.PerAdapter = append(r.Encoders.PerAdapter, AdapterEncoders{Index: a.Index, Name: a.Name, LUID: a.LUID, Names: names, Err: err})
		}
	} else {
		r.Encoders.AllErr = err
	}
	if opts.Probe > 0 {
		p := dxgi.ProbeDuplication(opts.Display, opts.Probe)
		r.Probe = &p
	}
	if opts.Extended {
		if cleanup, err := com.Init(); err == nil {
			r.collectExtended(opts)
			cleanup()
		}
	}
	return r
}

func windowsVersion() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("Windows %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

func yesNo(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}

func list(names []string) string {
	if len(names) == 0 {
		return "нет"
	}
	return strings.Join(names, "; ")
}

// Lines возвращает отчёт построчно (для журнала и консоли).
func (r Report) Lines() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	add("=== Диагностика видеокарт AirSpoon Sender (%s) ===", r.At.Format("2006-01-02 15:04:05"))
	add("Версия ОС по RtlGetVersion: %s, процесс pid %d", r.Windows, os.Getpid())
	out = append(out, r.System.Lines()...)
	out = append(out, r.mfLines()...)
	add("Программа: %s", r.Exe)
	add("Предпочтение видеокарты для программы (HKCU\\%s): %s", gpupref.KeyPath, r.Pref)
	add("Перезапуск для смены видеокарты уже был (AIRSPOON_GPU_RETRY): %s", yesNo(r.Retried))
	if r.AdaptersE != nil {
		add("Видеокарты: ошибка перечисления: %v", r.AdaptersE)
	}
	add("Видеокарт: %d, гибридная система (несколько разных настоящих карт): %s", len(r.Adapters), yesNo(r.Hybrid))
	for _, a := range r.Adapters {
		kind := ""
		if a.Software() {
			kind = ", программный"
		}
		add("  [%d] %s: vendor=%04X device=%04X, LUID=%s, видеопамять=%d МБ, общая=%d МБ, флаги=0x%X%s, выходов: %d",
			a.Index, a.Name, a.VendorID, a.DeviceID, a.LUID, a.DedicatedVRAM>>20, a.SharedMemory>>20, a.Flags, kind, len(a.Outputs))
		for _, o := range a.Outputs {
			owner := o.GDIAdapter
			if owner == "" {
				owner = "неизвестно"
			}
			prim := ""
			if o.Primary {
				prim = ", основной"
			}
			add("      монитор %s %dx%d в (%d,%d), подключён к рабочему столу: %s; по данным GDI принадлежит карте «%s»%s",
				o.Name, o.Bounds.W, o.Bounds.H, o.Bounds.X, o.Bounds.Y, yesNo(o.Attached), owner, prim)
		}
		if r.Ext != nil {
			for _, p := range r.Ext.AdapterProbes {
				if p.LUID == a.LUID {
					out = append(out, adapterProbeLines(p)...)
				}
			}
		}
	}
	if r.Ext != nil && r.Ext.AdapterProbesErr != nil {
		add("Проба видеокарт: ошибка: %v", r.Ext.AdapterProbesErr)
	}
	if r.Encoders.AllErr != nil {
		add("Аппаратные кодеры H.264 (все видеокарты): ошибка: %v", r.Encoders.AllErr)
	} else {
		add("Аппаратные кодеры H.264 (все видеокарты): %s", list(r.Encoders.All))
	}
	for _, e := range r.Encoders.PerAdapter {
		if e.Err != nil {
			add("  на видеокарте [%d] %s (LUID %s): %v", e.Index, e.Name, e.LUID, e.Err)
			continue
		}
		add("  на видеокарте [%d] %s (LUID %s): %s", e.Index, e.Name, e.LUID, list(e.Names))
	}
	add("%s", r.softwareLine())
	if p := r.Probe; p != nil {
		if p.Monitor == "" {
			add("Проба дублирования монитора %d: %v", p.Display, p.Err)
		} else {
			add("Проба дублирования монитора %d (%s) на карте «%s» (LUID %s): устройство D3D11 создано на карте «%s» (LUID %s)",
				p.Display, p.Monitor, p.Adapter, p.AdapterLUID, p.DeviceAdapter, p.DeviceLUID)
			if p.OK {
				add("  DuplicateOutput: успешно с попытки %d (%v)", p.Attempts, p.Elapsed.Round(time.Millisecond))
			} else {
				add("  DuplicateOutput: не открылось за %v (%d попыток): %v — %s",
					p.Elapsed.Round(time.Millisecond), p.Attempts, p.Err, dxgi.DescribeError(p.Err))
			}
		}
	}
	if e := r.Ext; e != nil {
		out = append(out, e.captureLines()...)
		out = append(out, e.trialLines()...)
		add("Сеть и звук:")
		add("  %s", e.Network.line())
		add("  %s", e.Audio.line())
		add("ИТОГ: %s", e.Recommendation)
	}
	return out
}

// mfLines — Media Foundation: результат MFStartup, а не предположение.
func (r Report) mfLines() []string {
	if r.MFErr != nil {
		return []string{fmt.Sprintf("Media Foundation: MFStartup НЕ удался: %v (%s)", r.MFErr, mfAdvice(r.System.IsN))}
	}
	return []string{"Media Foundation: MFStartup выполнен успешно"}
}

// softwareLine — программный кодер по результату создания MFT.
func (r Report) softwareLine() string {
	if r.MFErr != nil {
		return "Программный кодер: проверить нельзя, Media Foundation недоступна"
	}
	e := r.SoftwareEncoder
	if e.Name == "" {
		return "Программный кодер: не проверялся"
	}
	if e.Err != "" {
		return fmt.Sprintf("Программный кодер: %s НЕ создаётся: %s (%s)", e.Name, e.Err, mfAdvice(r.System.IsN))
	}
	return fmt.Sprintf("Программный кодер: %s создаётся (async=%s, D3D11=%s)", e.Name, yesNo(e.Async), yesNo(e.D3D11Aware))
}

// captureLines — мониторы, захват и путь преобразования цвета (расширенный режим).
func (e *Extended) captureLines() []string {
	if e.MonitorsErr != nil {
		return []string{fmt.Sprintf("Мониторы (проба захвата): ошибка: %v", e.MonitorsErr)}
	}
	if len(e.Monitors) == 0 {
		return nil
	}
	out := []string{"Мониторы и захват (поворот, цветовое пространство, частота, формат первого кадра, путь цвета, замер):"}
	for _, m := range e.Monitors {
		out = append(out, monitorLines(m, e.MonitorsMeasured)...)
	}
	return out
}

// String — отчёт одним текстом.
func (r Report) String() string { return strings.Join(r.Lines(), "\n") }
