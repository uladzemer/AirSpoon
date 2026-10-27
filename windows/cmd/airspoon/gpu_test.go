package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/capture/dxgi"
	"airspoon/internal/trayctl"
	"airspoon/internal/win/com"
)

// fakeStore — подделка реестра с предпочтениями видеокарты.
type fakeStore struct {
	m    map[string]string
	sets int
}

func (s *fakeStore) Get(exe string) (string, bool, error) {
	v, ok := s.m[exe]
	return v, ok, nil
}

func (s *fakeStore) Set(exe, data string) error {
	s.sets++
	s.m[exe] = data
	return nil
}

const fakeExe = `C:\Apps\AirSpoon\AirSpoonSender.exe`

var (
	fakeNVIDIA = dxgi.AdapterInfo{Index: 0, Name: "NVIDIA GeForce RTX 3060 Laptop GPU", VendorID: 0x10DE, DeviceID: 0x2560, DedicatedVRAM: 6 << 30}
	fakeIntel  = dxgi.AdapterInfo{Index: 1, Name: "Intel(R) UHD Graphics", VendorID: 0x8086, DeviceID: 0x9A60, DedicatedVRAM: 128 << 20,
		Outputs: []dxgi.OutputDetail{{Name: `\\.\DISPLAY1`, Attached: true, GDIAdapter: "Intel(R) UHD Graphics"}}}
	// Процесс на NVIDIA видит монитор ноутбука под NVIDIA, а по GDI он на Intel.
	fakeNVIDIAWithLaptopPanel = dxgi.AdapterInfo{Index: 0, Name: fakeNVIDIA.Name, VendorID: 0x10DE, DeviceID: 0x2560, DedicatedVRAM: 6 << 30,
		Outputs: []dxgi.OutputDetail{{Name: `\\.\DISPLAY1`, Attached: true, GDIAdapter: "Intel(R) UHD Graphics"}}}
)

func newFakeGPUEnv(list []dxgi.AdapterInfo, env map[string]string) (gpuEnv, *fakeStore) {
	st := &fakeStore{m: map[string]string{}}
	return gpuEnv{
		store:    st,
		adapters: func() ([]dxgi.AdapterInfo, error) { return list, nil },
		exe:      func() (string, error) { return fakeExe, nil },
		getenv:   func(k string) string { return env[k] },
	}, st
}

// supervised — окружение рабочего процесса под присмотром наблюдателя.
var supervised = map[string]string{envChild: "1"}

func unsupportedErr() error {
	return captureError{&dxgi.UnsupportedError{Adapter: fakeNVIDIA.Name, Monitor: `\\.\DISPLAY1`, Hybrid: true,
		Err: &com.Error{Op: "DuplicateOutput", HR: 0x887A0004}}}
}

// (а) При старте на гибридной системе записывается «энергосбережение» для пути
// самой программы — и только один раз.
func TestEnsureStartupWritesPowerSavingOnHybrid(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIA, fakeIntel}, nil)
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	e.ensureStartup(logf)
	if st.m[fakeExe] != "GpuPreference=1;" || len(st.m) != 1 {
		t.Fatalf("реестр: %v", st.m)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "энергосбережение") {
		t.Fatalf("журнал: %v", logs)
	}
	e.ensureStartup(logf)
	if st.sets != 1 {
		t.Fatalf("повторный старт записал ещё раз: %d записей", st.sets)
	}
}

func TestEnsureStartupDoesNothingOnSingleGPUOrWithUserChoice(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIA}, nil)
	e.ensureStartup(func(string, ...any) {})
	if st.sets != 0 {
		t.Fatal("одна видеокарта: писать ничего нельзя")
	}
	e, st = newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIA, fakeIntel}, nil)
	st.m[fakeExe] = "GpuPreference=2;"
	e.ensureStartup(func(string, ...any) {})
	if st.sets != 0 || st.m[fakeExe] != "GpuPreference=2;" {
		t.Fatalf("выбор человека перезаписан: %v", st.m)
	}
}

// (б) DXGI_ERROR_UNSUPPORTED: один раз записать предпочтение и перезапуститься.
func TestRequestGPURestartWritesPrefSavesResumeAndQuits(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, supervised)
	resume := filepath.Join(t.TempDir(), "gpu-resume.json")
	quits := 0
	var flag atomic.Bool
	target := trayctl.Target{Device: trayctl.Device{ID: "AA:01", Name: "Телевизор", Addr: "192.168.1.20:7000"}}

	if !requestGPURestart(e, target, unsupportedErr(), resume, func() { quits++ }, &flag) {
		t.Fatal("перезапуск должен быть начат")
	}
	if st.m[fakeExe] != "GpuPreference=1;" || quits != 1 || !flag.Load() {
		t.Fatalf("реестр %v, выходов %d, флаг %v", st.m, quits, flag.Load())
	}
	d, ok := takeResume(resume, time.Now())
	if !ok || d.Name != "Телевизор" || d.Addr != "192.168.1.20:7000" {
		t.Fatalf("запись о показе: %+v %v", d, ok)
	}
	if _, again := takeResume(resume, time.Now()); again {
		t.Fatal("запись должна читаться один раз")
	}
}

// Защита от цикла: в процессе, запущенном после перезапуска, повторного перезапуска нет.
func TestRequestGPURestartIsOneShot(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, map[string]string{envChild: "1", envGPURetry: "1"})
	var flag atomic.Bool
	quits := 0
	if requestGPURestart(e, trayctl.Target{}, unsupportedErr(), filepath.Join(t.TempDir(), "r.json"), func() { quits++ }, &flag) {
		t.Fatal("после перезапуска повторный перезапуск запрещён")
	}
	if st.sets != 0 || quits != 0 || flag.Load() {
		t.Fatalf("побочные действия при отказе: записей %d, выходов %d", st.sets, quits)
	}
}

func TestRequestGPURestartNotOnSingleGPU(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIA}, supervised)
	var flag atomic.Bool
	if requestGPURestart(e, trayctl.Target{}, unsupportedErr(), filepath.Join(t.TempDir(), "r.json"), func() {}, &flag) || st.sets != 0 {
		t.Fatal("на одной видеокарте перезапуск не поможет")
	}
}

// Энергосбережение уже записано при старте (а), но не помогло: перезапуск с «решает система».
func TestRequestGPURestartTriesNextPreference(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, supervised)
	st.m[fakeExe] = "GpuPreference=1;"
	var flag atomic.Bool
	if !requestGPURestart(e, trayctl.Target{Device: trayctl.Device{Addr: "10.0.0.2:7000"}}, unsupportedErr(), filepath.Join(t.TempDir(), "r.json"), func() {}, &flag) {
		t.Fatal("ожидался перезапуск с другим предпочтением")
	}
	if st.m[fakeExe] != "GpuPreference=0;" {
		t.Fatalf("после энергосбережения пробуем «решает система»: %q", st.m[fakeExe])
	}
}

func TestResumeExpiresAndRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gpu-resume.json")
	now := time.Now()
	d := trayctl.Device{Name: "ТВ", Addr: "10.0.0.2:7000"}
	if err := saveResume(path, d, now.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok := takeResume(path, now); ok {
		t.Fatal("устаревшая запись должна игнорироваться")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("устаревшая запись должна удаляться")
	}
	_ = os.WriteFile(path, []byte("не json"), 0o600)
	if _, ok := takeResume(path, now); ok {
		t.Fatal("мусор должен игнорироваться")
	}
	if err := saveResume(path, trayctl.Device{Name: "без адреса"}, now); err == nil {
		t.Fatal("без адреса сохранять нечего")
	}
}

// Показ через трей: ошибка UNSUPPORTED превращается либо в тихий перезапуск, либо
// в понятное сообщение про видеокарту.
func TestStreamerMapsUnsupportedToRestartOrGPUMessage(t *testing.T) {
	run := func(context.Context, mirrorOptions) error { return unsupportedErr() }
	hooks := trayctl.Hooks{Streaming: func() {}, AskPIN: func(context.Context, bool) (string, error) { return "", nil }}
	target := trayctl.Target{Device: trayctl.Device{Name: "ТВ", Addr: "10.0.0.2:7000"}}

	s := &mirrorStreamer{credFile: "c.json", encoder: "auto", run: run, onUnsupported: func(trayctl.Target, error) bool { return true }}
	err := s.Stream(context.Background(), target, hooks)
	if !errors.Is(err, trayctl.ErrRestarting) || !errors.Is(err, dxgi.ErrDuplicateUnsupported) {
		t.Fatalf("перезапуск: %v", err)
	}
	if errors.Is(err, trayctl.ErrCapture) {
		t.Fatal("при перезапуске ошибку захвата показывать нельзя")
	}

	s.onUnsupported = func(trayctl.Target, error) bool { return false }
	err = s.Stream(context.Background(), target, hooks)
	if !errors.Is(err, trayctl.ErrCaptureGPU) || !errors.Is(err, trayctl.ErrCapture) {
		t.Fatalf("без перезапуска: %v", err)
	}
	if msg := trayctl.UserMessage(err, "ТВ"); !strings.Contains(msg, "другой видеокарте") {
		t.Fatalf("сообщение пользователю: %q", msg)
	}

	s.onUnsupported = nil
	if err := s.Stream(context.Background(), target, hooks); !errors.Is(err, trayctl.ErrCaptureGPU) {
		t.Fatalf("без обработчика: %v", err)
	}
}

func TestCLIUnsupportedAdvice(t *testing.T) {
	var out []string
	say := func(s string) { out = append(out, s) }

	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, nil)
	cliUnsupportedAdvice(e, unsupportedErr(), say)
	text := strings.Join(out, "\n")
	if st.m[fakeExe] != "GpuPreference=1;" || !strings.Contains(text, "энергосбережение") || !strings.Contains(text, "ещё раз") {
		t.Fatalf("реестр %v, сообщение:\n%s", st.m, text)
	}

	out = nil
	e, st = newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, map[string]string{envChild: "1", envGPURetry: "1"})
	cliUnsupportedAdvice(e, unsupportedErr(), say)
	text = strings.Join(out, "\n")
	if st.sets != 0 || !strings.Contains(text, "diag") {
		t.Fatalf("после перезапуска записывать ничего нельзя, нужна подсказка про diag:\n%s", text)
	}
}

func TestExitRestartGPUIsNotCrash(t *testing.T) {
	if isCrashExit(exitRestartGPU) {
		t.Fatal("просьба перезапуска не авария")
	}
	if exitRestartGPU == exitCrash {
		t.Fatal("коды выхода должны различаться")
	}
}

// Настольный ПК: монитор подключён к NVIDIA, встроенная графика Intel простаивает.
// Уводить процесс на Intel нельзя: при старте предпочтение не записывается.
func TestEnsureStartupSkipsDesktopWithMonitorOnDiscreteGPU(t *testing.T) {
	nvidiaWithMonitor := fakeNVIDIA
	nvidiaWithMonitor.Outputs = []dxgi.OutputDetail{{Name: `\.\DISPLAY1`, Attached: true, GDIAdapter: fakeNVIDIA.Name}}
	intelNoMonitor := fakeIntel
	intelNoMonitor.Outputs = nil
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{nvidiaWithMonitor, intelNoMonitor}, nil)
	e.ensureStartup(func(string, ...any) {})
	if st.sets != 0 || len(st.m) != 0 {
		t.Fatalf("монитор на дискретной карте: писать нельзя, реестр %v", st.m)
	}
	// Владелец монитора неизвестен (нет выходов): тоже не пишем.
	nvidiaNoOut, intelNoOut := fakeNVIDIA, fakeIntel
	nvidiaNoOut.Outputs, intelNoOut.Outputs = nil, nil
	e, st = newFakeGPUEnv([]dxgi.AdapterInfo{nvidiaNoOut, intelNoOut}, nil)
	e.ensureStartup(func(string, ...any) {})
	if st.sets != 0 {
		t.Fatalf("владелец неизвестен: писать нельзя, реестр %v", st.m)
	}
}

// Ручные команды mirror и новые запуски трея не гоняют предпочтение по кругу
// (1 -> 0 -> 1 ...): о неудачных предпочтениях остаётся память.
func TestCLIUnsupportedAdviceDoesNotFlipPreferenceBackAndForth(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, nil)
	e.attemptFile = filepath.Join(t.TempDir(), "gpu-attempts.json")
	var out []string
	say := func(s string) { out = append(out, s) }

	cliUnsupportedAdvice(e, unsupportedErr(), say) // запуск 1: «решает система» не подошло -> энергосбережение
	if st.m[fakeExe] != "GpuPreference=1;" || st.sets != 1 {
		t.Fatalf("запуск 1: реестр %v, записей %d", st.m, st.sets)
	}
	out = nil
	cliUnsupportedAdvice(e, unsupportedErr(), say) // запуск 2: энергосбережение тоже не подошло
	text := strings.Join(out, "\n")
	if st.sets != 1 || st.m[fakeExe] != "GpuPreference=1;" {
		t.Fatalf("запуск 2 не должен менять реестр: %v, записей %d", st.m, st.sets)
	}
	if strings.Contains(text, "ещё раз") || !strings.Contains(text, "diag") {
		t.Fatalf("запуск 2: нужен совет про diag, а не «запустите ещё раз»:\n%s", text)
	}
	for i := 0; i < 3; i++ { // и дальше реестр не гуляет
		cliUnsupportedAdvice(e, unsupportedErr(), say)
	}
	if st.sets != 1 {
		t.Fatalf("реестр менялся повторно: записей %d", st.sets)
	}

	// Захват открылся — память стёрта, при новой беде перебор начинается заново.
	e.forgetAttempts()
	st.m[fakeExe] = "GpuPreference=0;"
	cliUnsupportedAdvice(e, unsupportedErr(), say)
	if st.m[fakeExe] != "GpuPreference=1;" {
		t.Fatalf("после forgetAttempts перебор должен начаться заново: %v", st.m)
	}
}

func TestAttemptMemoryExpiresAndIsPerExecutable(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, nil)
	e.attemptFile = filepath.Join(t.TempDir(), "gpu-attempts.json")
	now := time.Now()
	e.now = func() time.Time { return now }
	say := func(string) {}
	cliUnsupportedAdvice(e, unsupportedErr(), say)
	cliUnsupportedAdvice(e, unsupportedErr(), say)
	if st.sets != 1 {
		t.Fatalf("память должна работать: записей %d", st.sets)
	}
	now = now.Add(attemptMaxAge + time.Minute) // сутки прошли
	cliUnsupportedAdvice(e, unsupportedErr(), say)
	if st.sets != 2 {
		t.Fatalf("после срока давности перебор разрешён снова: записей %d", st.sets)
	}
	if tried := e.triedPrefs(`C:\Other\AirSpoonSender.exe`); len(tried) != 0 {
		t.Fatalf("память другого файла не должна действовать: %v", tried)
	}
}

// Корень задержки быстрого отказа: ошибка захвата, дошедшая до primeSource через
// источник, сохраняет ErrDuplicateUnsupported (по ней запускается перезапуск).
func TestPrimeSourceKeepsUnsupportedFromSource(t *testing.T) {
	_, err := primeSource(context.Background(), failingSource{err: fmt.Errorf("захват экрана: %w", unsupportedErr())})
	if !errors.Is(err, dxgi.ErrDuplicateUnsupported) {
		t.Fatalf("ErrDuplicateUnsupported потеряна: %v", err)
	}
}

type failingSource struct{ err error }

func (f failingSource) ReadAccessUnit(context.Context) (airplay.VideoAccessUnit, error) {
	return airplay.VideoAccessUnit{}, f.err
}
func (failingSource) Close() error { return nil }

// Без наблюдателя (рабочий процесс не запущен, программа работает сама) выход с
// кодом 4 закрыл бы программу молча: реестр не трогаем, перезапуска нет.
func TestRequestGPURestartNotWithoutSupervisor(t *testing.T) {
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIAWithLaptopPanel, fakeIntel}, nil)
	var flag atomic.Bool
	quits := 0
	if requestGPURestart(e, trayctl.Target{}, unsupportedErr(), filepath.Join(t.TempDir(), "r.json"), func() { quits++ }, &flag) {
		t.Fatal("без наблюдателя перезапуск невозможен")
	}
	if st.sets != 0 || quits != 0 || flag.Load() {
		t.Fatalf("побочные действия: записей %d, выходов %d, флаг %v", st.sets, quits, flag.Load())
	}
}

// Владелец монитора не опознан (удалённый рабочий стол: «Microsoft Remote Display
// Adapter»): реестр не меняется, перезапуска нет.
func TestRequestGPURestartNotWhenMonitorOwnerUnknown(t *testing.T) {
	rdp := dxgi.AdapterInfo{Index: 2, Name: "Microsoft Remote Display Adapter", VendorID: 0x1414,
		Outputs: []dxgi.OutputDetail{{Name: `\.\DISPLAY1`, Attached: true, GDIAdapter: "Microsoft Remote Display Adapter"}}}
	intel := dxgi.AdapterInfo{Index: 1, Name: fakeIntel.Name, VendorID: 0x8086, DeviceID: 0x9A60, DedicatedVRAM: 128 << 20}
	e, st := newFakeGPUEnv([]dxgi.AdapterInfo{fakeNVIDIA, intel, rdp}, supervised)
	var flag atomic.Bool
	if requestGPURestart(e, trayctl.Target{}, unsupportedErr(), filepath.Join(t.TempDir(), "r.json"), func() {}, &flag) {
		t.Fatal("владелец монитора не опознан: перезапускать нельзя")
	}
	if st.sets != 0 || flag.Load() {
		t.Fatalf("реестр изменён: %v", st.m)
	}
}
