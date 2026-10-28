package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/applog"
)

// Присмотр за рабочим процессом. Программа без окна консоли, и если она вдруг
// закрывается из-за сбоя в нативном коде (драйвер видеокарты, Media Foundation),
// человек видит только пропавший значок. Поэтому процесс, запущенный
// пользователем, лишь следит за единственным экземпляром «рабочего» процесса
// (тот же файл с переменной окружения AIRSPOON_CHILD=1): если он закрылся
// аварийно, причина записывается в журнал, программа запускается заново и
// сообщает об этом уведомлением.
//
// В диспетчере задач видны два процесса с одним именем. Чтобы «Снять задачу» с
// любого из них закрывало всю программу целиком, рабочий процесс привязан к
// заданию Windows (Job Object) наблюдателя: закрылся наблюдатель — закрылся и
// рабочий процесс. Обратно: закрыли рабочий (код выхода 1) — наблюдатель выходит.
const (
	envChild     = "AIRSPOON_CHILD"     // «я — рабочий процесс под присмотром»
	envRestarted = "AIRSPOON_RESTARTED" // сколько раз программу уже перезапускали после аварий
	envUpdated   = "AIRSPOON_UPDATED"   // версия, до которой программа была обновлена

	// Коды выхода рабочего процесса: 0 — обычный выход, 1 — показана ошибка
	// запуска (перезапускать нечего), exitCrash — перехваченная авария.
	exitCrash = 3
	// exitRestartGPU (4, см. gpu.go) — просьба перезапустить программу: не авария.
	exitUpdate = 5 // просьба завершить процесс для применения обновления: не авария.

	maxCrashes  = 3                // столько аварий подряд за crashWindow — и перезапуски прекращаются
	crashWindow = 10 * time.Minute // окно подсчёта
	restartWait = time.Second
	// crashLogMax — crash.log старше этого размера очищается при запуске.
	crashLogMax = 1 << 20

	statusControlCExit = 0xC000013A // STATUS_CONTROL_C_EXIT: закрытие консоли, выход сеанса
)

// gpuPreflightTimeout — сколько ждём дочерний процесс, записывающий
// предпочтение видеокарты; по истечении он убивается, рабочий процесс стартует
// как есть.
const gpuPreflightTimeout = 10 * time.Second

// runGPUPreflight — тело дочернего процесса: записать предпочтение видеокарты и
// выйти. Сбой здесь гасит только этот процесс.
func runGPUPreflight() (code int) {
	defer func() {
		if r := recover(); r != nil {
			appendLog("видеокарта: проверка предпочтения упала: %v", r)
			code = exitCrash
		}
	}()
	newGPUEnv().ensureStartup(appendLog)
	return 0
}

// preflightGPU запускает exe как дочерний процесс envGPUPreflight и ждёт его
// не дольше timeout (см. waitPreflight).
func preflightGPU(exe string, timeout time.Duration) bool {
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), envGPUPreflight+"=1")
	return waitPreflight(cmd, timeout)
}

// waitPreflight запускает cmd и ждёт не дольше timeout. Предпочтение должно быть
// записано до старта рабочего процесса (видеокарту процесс выбирает при старте).
// Любой исход, кроме успеха, пишется в журнал и не мешает запуску: по таймауту
// процесс убивается. Возвращает true, если процесс завершился штатно.
func waitPreflight(cmd *exec.Cmd, timeout time.Duration) bool {
	if err := cmd.Start(); err != nil {
		appendLog("видеокарта: проверка предпочтения не запущена: %v", err)
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			appendLog("видеокарта: проверка предпочтения закончилась ошибкой: %v", err)
			return false
		}
		return true
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		appendLog("видеокарта: проверка предпочтения не уложилась в %v и прервана; рабочий процесс запускается без неё", timeout)
		return false
	}
}

// isCrashExit сообщает, что код выхода рабочего процесса — авария, а не выход
// по воле человека или Windows. Код 1 дают «Завершить задачу» и taskkill /F.
func isCrashExit(code uint32) bool {
	switch code {
	case 0, 1, exitRestartGPU, exitUpdate, statusControlCExit:
		return false
	}
	return true
}

// crashLogPath — файл с трассировкой, если программа упала в коде Go.
func crashLogPath() string {
	return filepath.Join(filepath.Dir(applog.DefaultPath()), "crash.log")
}

// setupCrashOutput направляет в crash.log трассировку любой аварии Go
// (паника в любой горутине, fatal error): у оконной программы stderr никуда не
// идёт. Возвращает функцию закрытия.
func setupCrashOutput() func() {
	path := crashLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > crashLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return func() {}
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		_ = f.Close()
	}
}

// appendLog дописывает строку в журнал программы из процесса-наблюдателя
// (рабочий процесс, который обычно пишет в журнал, к этому моменту уже закрыт).
func appendLog(format string, args ...any) {
	w, err := applog.Open(applog.DefaultPath(), applog.DefaultMaxBytes)
	if err != nil {
		return
	}
	defer w.Close()
	fmt.Fprintf(w, "%s %s\n", time.Now().Format("2006/01/02 15:04:05.000000"), fmt.Sprintf(format, args...))
}

// supervise запускает рабочий процесс и перезапускает его после аварий.
// Возвращает код выхода программы.
func supervise(releaseMutex func()) int {
	exe, err := os.Executable()
	if err != nil {
		appendLog("присмотр за программой отключён (путь к файлу не определён: %v)", err)
		return runTrayApp()
	}
	job, jobErr := newKillOnCloseJob()
	if jobErr != nil {
		appendLog("задание Windows для рабочего процесса не создано (%v); если закрыть только присматривающий процесс, рабочий останется", jobErr)
	} else {
		defer windows.CloseHandle(job)
	}
	// Рабочий процесс выбирает видеокарту при старте, поэтому предпочтение для
	// ноутбуков с двумя видеокартами записывается до его запуска.
	// Опрос драйверов — в коротком дочернем процессе с таймаутом, не в наблюдателе.
	preflightGPU(exe, gpuPreflightTimeout)
	var crashes []time.Time
	restarts := 0    // перезапуски после аварий (в окружение рабочего процесса)
	gpuRestarts := 0 // перезапуски для смены видеокарты (не больше одного)
	for {
		started := time.Now()
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), envChild+"=1", envRestarted+"="+strconv.Itoa(restarts))
		if gpuRestarts > 0 {
			cmd.Env = append(cmd.Env, envGPURetry+"=1")
		}
		if err := cmd.Start(); err != nil {
			// Процесс не удалось даже запустить: работаем в этом же процессе, без присмотра.
			appendLog("рабочий процесс не запущен (%v); программа работает без присмотра", err)
			return runTrayApp()
		}
		if jobErr == nil {
			if err := addToJob(job, cmd.Process.Pid); err != nil {
				appendLog("рабочий процесс не привязан к заданию Windows: %v", err)
			}
		}
		err := cmd.Wait()
		var code uint32
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				appendLog("ожидание рабочего процесса прервалось (%v)", err)
				return 1
			}
			code = uint32(exitErr.ExitCode())
		}
		if code == exitRestartGPU {
			// Рабочий процесс сам попросил перезапуск: изменено предпочтение
			// видеокарты. Это не авария; повторять можно только один раз.
			if gpuRestarts >= 1 {
				appendLog("повторная просьба сменить видеокарту отклонена: перезапуск уже выполнялся")
				return 1
			}
			gpuRestarts++
			appendLog("перезапуск программы для работы на другой видеокарте (проработала %v)", time.Since(started).Round(time.Second))
			continue
		}
		if code == exitUpdate {
			// Рабочий процесс обновил exe и попросил перезапуск: освобождаем
			// мьютекс одного экземпляра и запускаем exe заново как отдельный
			// процесс (обычный запуск, без envChild) с AIRSPOON_UPDATED=<версия>.
			appendLog("программа обновлена; перезапуск отдельным процессом")
			if releaseMutex != nil {
				releaseMutex()
			}
			if err := startUpdated(); err != nil {
				appendLog("не удалось запустить обновлённый процесс (%v); программа завершена", err)
				return 1
			}
			return 0
		}
		if !isCrashExit(code) {
			return int(code)
		}
		now := time.Now()
		crashes = append(recentCrashes(crashes, now), now)
		hint := ""
		if st, serr := os.Stat(crashLogPath()); serr == nil && st.Size() > 0 && st.ModTime().After(started) {
			hint = "; трассировка Go записана в " + crashLogPath()
		}
		appendLog("программа неожиданно закрылась (код 0x%08X, проработала %v)%s", code, now.Sub(started).Round(time.Second), hint)
		if len(crashes) >= maxCrashes {
			appendLog("программа закрывалась аварийно %d раза за %v: перезапуски прекращены", len(crashes), crashWindow)
			showFatal(fmt.Sprintf("программа закрывалась аварийно %d раза за %v", len(crashes), crashWindow))
			return 1
		}
		appendLog("перезапуск программы (%d из %d)", len(crashes), maxCrashes-1)
		time.Sleep(restartWait)
		restarts++
	}
}

// startUpdated запускает заменённый exe обычным процессом (без envChild) с
// AIRSPOON_UPDATED=<версия>: новый экземпляр дождётся мьютекса и сообщит об обновлении.
func startUpdated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ver := takeUpdateVersion()
	if ver == "" {
		ver = "latest"
	}
	cmd := exec.Command(exe)
	cmd.Env = append(filterEnv(os.Environ(), envChild), envUpdated+"="+ver)
	return cmd.Start()
}

func filterEnv(env []string, removeKey string) []string {
	prefix := removeKey + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

func updateVersionPath() string {
	return filepath.Join(filepath.Dir(applog.DefaultPath()), "update-version")
}

func saveUpdateVersion(ver string) error {
	path := updateVersionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(ver), 0o600)
}

func takeUpdateVersion() string {
	path := updateVersionPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_ = os.Remove(path)
	return strings.TrimSpace(string(data))
}

// newKillOnCloseJob создаёт задание Windows, которое убивает все свои процессы,
// когда закрывается его дескриптор (в том числе при завершении процесса-владельца).
func newKillOnCloseJob() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return h, nil
}

// addToJob привязывает запущенный процесс к заданию.
func addToJob(job windows.Handle, pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

func recentCrashes(list []time.Time, now time.Time) []time.Time {
	var out []time.Time
	for _, t := range list {
		if now.Sub(t) < crashWindow {
			out = append(out, t)
		}
	}
	return out
}

// restartedAfterCrash сообщает, что этот рабочий процесс запущен вместо
// аварийно закрывшегося.
func restartedAfterCrash() bool {
	n, err := strconv.Atoi(os.Getenv(envRestarted))
	return err == nil && n > 0
}
