package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"airspoon/internal/applog"
	"airspoon/internal/capture/dxgi"
	"airspoon/internal/gpudiag"
	"airspoon/internal/gpupref"
	"airspoon/internal/trayctl"
)

// Гибридные видеокарты (ноутбук Intel + NVIDIA). Захват экрана (Desktop
// Duplication) не работает, если процесс выполняется на дискретной видеокарте,
// а монитор подключён к встроенной (DXGI_ERROR_UNSUPPORTED, см. пакет gpupref).
// Здесь — склейка: предпочтение видеокарты для самой программы записывается при
// старте режима трея, а если захват всё же не открылся, программа один раз
// перезапускается с другим предпочтением.

const (
	// envGPURetry=1 — рабочий процесс запущен после перезапуска для смены
	// видеокарты; повторно перезапускаться нельзя (защита от цикла).
	envGPURetry = "AIRSPOON_GPU_RETRY"
	// exitRestartGPU — код выхода рабочего процесса «перезапустите меня: изменено
	// предпочтение видеокарты». Это не авария.
	exitRestartGPU = 4
	// resumeMaxAge — запись о показе, прерванном перезапуском, действует столько.
	resumeMaxAge = 2 * time.Minute
)

// gpuEnv — окружение решений о видеокарте; в тестах подменяется подделками.
type gpuEnv struct {
	store    gpupref.Store
	adapters func() ([]dxgi.AdapterInfo, error)
	exe      func() (string, error)
	getenv   func(string) string
	// attemptFile — файл памяти о предпочтениях, при которых захват уже не
	// открылся; пустое имя — без памяти. now — часы (nil — time.Now).
	attemptFile string
	now         func() time.Time
}

func newGPUEnv() gpuEnv {
	e := gpuEnv{store: gpupref.RegistryStore{}, adapters: dxgi.ListAdapters, exe: os.Executable, getenv: os.Getenv,
		attemptFile: filepath.Join(filepath.Dir(applog.DefaultPath()), "gpu-attempts.json")}
	overrideGPUEnv(&e)
	return e
}

// retried — процесс уже запущен после перезапуска для смены видеокарты.
func (e gpuEnv) retried() bool { return e.getenv(envGPURetry) == "1" }

func (e gpuEnv) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// envGPUPreflight=1 — «я короткий дочерний процесс: только записать
// предпочтение видеокарты и выйти». Драйверы (DXGI, реестр) опрашивает он, а не
// наблюдатель: сбой или зависание драйвера не должны убить наблюдателя.
const envGPUPreflight = "AIRSPOON_GPU_PREFLIGHT"

// ensureStartup записывает «энергосбережение» для этой программы на ноутбуках с
// несколькими видеокартами (экран на встроенной карте), если предпочтение для её
// пути ещё не задано. Выполняется в дочернем процессе envGPUPreflight (см. supervisor_windows.go) до запуска рабочего процесса:
// видеокарту процесс выбирает при старте, поэтому рабочий процесс уже стартует с
// нужным предпочтением. На настольном ПК с монитором на дискретной карте и при
// неизвестном владельце монитора ничего не пишется (см. gpupref.EnsureDefault).
// Ошибки не мешают работе: пишутся в журнал.
func (e gpuEnv) ensureStartup(logf func(string, ...any)) {
	exe, err := e.exe()
	if err != nil {
		logf("видеокарта: путь к программе не определён: %v", err)
		return
	}
	list, err := e.adapters()
	if err != nil {
		logf("видеокарта: список видеокарт не получен: %v", err)
		return
	}
	changed, err := gpupref.EnsureDefault(e.store, exe, dxgi.GPUAdapters(list), dxgi.OwnerOfMonitor(list, ""))
	switch {
	case err != nil:
		logf("видеокарта: предпочтение для %s не записано: %v", exe, err)
	case changed:
		logf("видеокарта: экран подключён к встроенной видеокарте, а видеокарт несколько; для %s записано предпочтение «энергосбережение» (встроенная видеокарта): захват экрана работает только на той карте, к которой подключён экран", exe)
	}
}

// unsupportedPlan решает, что делать после DXGI_ERROR_UNSUPPORTED: unsupp — ошибка
// захвата (из неё берётся монитор).
func (e gpuEnv) unsupportedPlan(unsupp error) (gpupref.Plan, error) {
	exe, err := e.exe()
	if err != nil {
		return gpupref.Plan{Reason: "путь к программе не определён"}, err
	}
	list, err := e.adapters()
	if err != nil {
		return gpupref.Plan{Reason: "список видеокарт не получен"}, err
	}
	cur, set, err := gpupref.Current(e.store, exe)
	if err != nil {
		return gpupref.Plan{Reason: "предпочтение не прочитано"}, err
	}
	monitor := ""
	var ue *dxgi.UnsupportedError
	if errors.As(unsupp, &ue) {
		monitor = ue.Monitor
	}
	owner := dxgi.OwnerOfMonitor(list, monitor)
	plan := gpupref.PlanAfterUnsupportedTried(cur, set, dxgi.GPUAdapters(list), owner, e.retried(), e.triedPrefs(exe))
	return plan, nil
}

// apply записывает предпочтение из плана для этой программы и запоминает, при
// каких предпочтениях захват уже не открылся.
func (e gpuEnv) apply(plan gpupref.Plan) error {
	exe, err := e.exe()
	if err != nil {
		return err
	}
	old, _, _ := e.store.Get(exe)
	if err := e.store.Set(exe, gpupref.FormatData(old, plan.Pref)); err != nil {
		return err
	}
	e.saveTried(exe, plan.Failed)
	return nil
}

// --- память о неудачных предпочтениях ---

// attemptMaxAge — как долго помним неудачные предпочтения. По истечении перебор
// разрешён снова (могли сменить монитор, драйвер или способ подключения).
const attemptMaxAge = 24 * time.Hour

type attemptRecord struct {
	Exe    string    `json:"exe"`
	Failed []int     `json:"failed"`
	At     time.Time `json:"at"`
}

// triedPrefs возвращает предпочтения, при которых захват уже не открылся и о
// которых запись свежая. Без неё каждая ручная команда mirror и каждый новый запуск
// трея меняли бы предпочтение туда-обратно: окружение (envGPURetry) ограничивает
// только один запуск.
func (e gpuEnv) triedPrefs(exe string) []gpupref.Preference {
	if e.attemptFile == "" {
		return nil
	}
	b, err := os.ReadFile(e.attemptFile)
	if err != nil {
		return nil
	}
	var r attemptRecord
	if json.Unmarshal(b, &r) != nil || !strings.EqualFold(r.Exe, exe) || e.clock().Sub(r.At) > attemptMaxAge || r.At.After(e.clock().Add(time.Minute)) {
		return nil
	}
	var out []gpupref.Preference
	for _, n := range r.Failed {
		if n >= 0 && n <= int(gpupref.HighPerformance) {
			out = append(out, gpupref.Preference(n))
		}
	}
	return out
}

func (e gpuEnv) saveTried(exe string, failed []gpupref.Preference) {
	if e.attemptFile == "" {
		return
	}
	r := attemptRecord{Exe: exe, At: e.clock()}
	for _, p := range failed {
		r.Failed = append(r.Failed, int(p))
	}
	b, err := json.Marshal(r)
	if err != nil || os.MkdirAll(filepath.Dir(e.attemptFile), 0o700) != nil {
		return
	}
	_ = os.WriteFile(e.attemptFile, b, 0o600)
}

// forgetAttempts стирает память: захват открылся, прежние неудачи неактуальны.
func (e gpuEnv) forgetAttempts() {
	if e.attemptFile != "" {
		_ = os.Remove(e.attemptFile)
	}
}

// logCaptureDiagnostics пишет в журнал диагностику видеокарт при ошибке
// захвата; подменяется в тестах.
// display — номер монитора, который захватывался.
var logCaptureDiagnostics = func(logf func(string, ...any), cause error, display int) {
	logf("ошибка захвата экрана: %v", cause)
	// Источник захвата к этому моменту закрыт, поэтому короткая проба открытия
	// дублирования (на новом устройстве D3D11) не мешает и показывает HRESULT и
	// видеокарту устройства.
	rep := gpudiag.Collect(gpudiag.Options{Display: display, Probe: 400 * time.Millisecond, RetryEnv: os.Getenv(envGPURetry)})
	for _, l := range rep.Lines() {
		logf("%s", l)
	}
}

// cliUnsupportedAdvice объясняет человеку в консоли, что делать после
// DXGI_ERROR_UNSUPPORTED; при необходимости записывает предпочтение видеокарты.
func cliUnsupportedAdvice(e gpuEnv, err error, out func(string)) {
	plan, perr := e.unsupportedPlan(err)
	if perr != nil {
		out("Экран подключён к другой видеокарте, чем та, на которой работает программа (ноутбук с двумя видеокартами), и не захватывается.")
		out("Не удалось изменить настройку видеокарты: " + perr.Error())
		out("Запустите «AirSpoonSender.exe diag» и покажите вывод тому, кто вам помогает.")
		return
	}
	if plan.Restart {
		if aerr := e.apply(plan); aerr != nil {
			out("Экран подключён к другой видеокарте, и записать настройку не удалось: " + aerr.Error())
			return
		}
		out(fmt.Sprintf("Экран подключён к другой видеокарте, чем та, на которой работала программа. Для AirSpoonSender.exe записана настройка видеокарты «%s» (только для вашей учётной записи Windows).", plan.Pref))
		out("Запустите команду ещё раз: настройка действует на новый запуск.")
		return
	}
	out("Экран подключён к другой видеокарте, чем та, на которой работает программа, и не захватывается. " + plan.Reason + ".")
	out("Откройте «Параметры Windows > Система > Дисплей > Графика», добавьте AirSpoonSender.exe и выберите другую видеокарту; запустите «AirSpoonSender.exe diag» и покажите вывод тому, кто вам помогает.")
}

// --- показ, прерванный перезапуском ---

type resumeTarget struct {
	Name string    `json:"name"`
	Addr string    `json:"addr"`
	At   time.Time `json:"at"`
}

func resumePath() string {
	return filepath.Join(filepath.Dir(applog.DefaultPath()), "gpu-resume.json")
}

// saveResume запоминает телевизор, на который шёл показ, чтобы после перезапуска
// продолжить его без выбора в меню.
func saveResume(path string, d trayctl.Device, now time.Time) error {
	if d.Addr == "" {
		return errors.New("у телевизора нет адреса")
	}
	b, err := json.Marshal(resumeTarget{Name: d.Name, Addr: d.Addr, At: now})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// takeResume читает и удаляет запись о прерванном показе; старше resumeMaxAge
// или повреждённая запись игнорируется.
func takeResume(path string, now time.Time) (trayctl.Device, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return trayctl.Device{}, false
	}
	_ = os.Remove(path)
	var r resumeTarget
	if json.Unmarshal(b, &r) != nil || r.Addr == "" || now.Sub(r.At) > resumeMaxAge || r.At.After(now.Add(time.Minute)) {
		return trayctl.Device{}, false
	}
	if _, err := trayctl.NormalizeAddress(r.Addr); err != nil {
		return trayctl.Device{}, false
	}
	return trayctl.Device{ID: "resume:" + r.Addr, Name: r.Name, Addr: r.Addr}, true
}
