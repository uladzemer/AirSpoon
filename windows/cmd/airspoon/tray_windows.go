package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"

	"airspoon/internal/airplay"
	"airspoon/internal/applog"
	"airspoon/internal/settings"
	"airspoon/internal/trayctl"
	"airspoon/internal/traywin"
	"airspoon/internal/update"
)

// runTray — режим трея (запуск без аргументов): один значок у часов, меню со
// списком телевизоров, показ экрана. Возвращает код выхода процесса.
func runTray() int {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
	if os.Getenv(envGPUPreflight) == "1" {
		// Короткий дочерний процесс наблюдателя: без значка и мьютекса.
		return runGPUPreflight()
	}
	release, first, err := traywin.AcquireSingleInstance()
	if err != nil {
		showFatal("мьютекс «один экземпляр»: " + err.Error())
		return 1
	}
	defer release()
	if os.Getenv(envChild) == "1" {
		// Рабочий процесс под присмотром: мьютекс «один экземпляр» держит и
		// наблюдатель, и мы (если наблюдателя закроют, мьютекс не пропадёт).
		return runTrayApp()
	}
	if !first {
		if os.Getenv(envUpdated) != "" {
			// Новый процесс после обновления: прошлый процесс-наблюдатель мог ещё
			// не успеть закрыть свой дескриптор. Повторяем попытки до ~15 с.
			deadline := time.Now().Add(15 * time.Second)
			for !first && time.Now().Before(deadline) {
				release()
				time.Sleep(100 * time.Millisecond)
				release, first, err = traywin.AcquireSingleInstance()
				if err != nil {
					showFatal("мьютекс «один экземпляр»: " + err.Error())
					return 1
				}
			}
		}
		if !first {
			// Уже запущена: просим первый экземпляр показать меню у значка. Это делает
			// именно запущенный человеком процесс: у него есть право на передний план.
			// Если вместо значка открыто окно об аварии, поднимаем его.
			if !traywin.FocusMessageBox(trayctl.AppName) {
				traywin.SignalRunning()
			}
			return 0
		}
	}
	return supervise(release)
}

// runTrayApp — сама программа в трее (рабочий процесс). Аварии Go пишутся в
// crash.log, перехваченная паника даёт код exitCrash, и наблюдатель перезапускает
// программу.
func runTrayApp() (code int) {
	closeCrash := setupCrashOutput()
	defer closeCrash()
	logFile := setupTrayLog()
	defer logFile()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("авария программы: %v\n%s", r, debug.Stack())
			code = exitCrash
		}
	}()
	log.Printf("AirSpoon Sender %s запущен (pid %d)", version, os.Getpid())

	app, err := traywin.New(traywin.Options{Tooltip: trayctl.AppName, Logf: log.Printf})
	if err != nil {
		log.Printf("не удалось создать значок: %v", err)
		showFatal("значок в области уведомлений не создан: " + err.Error())
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var discoverer trayctl.Discoverer = &mdnsDiscoverer{}
	if fake := fakeDevicesFromEnv(); fake != nil {
		discoverer = fake
	}
	gpu := newGPUEnv()
	var restartGPU atomic.Bool
	var updating atomic.Bool
	var updateBusy atomic.Bool

	streamer := newMirrorStreamer()
	streamer.onUnsupported = func(t trayctl.Target, cause error) bool {
		return requestGPURestart(gpu, t, cause, resumePath(), app.Quit, &restartGPU)
	}

	st := settings.Load(settings.DefaultPath())

	var ctl *trayctl.Controller
	var reportBusy atomic.Bool
	reporter := newSupportReporter()
	ctl = trayctl.New(trayctl.Config{
		Discoverer:   discoverer,
		Streamer:     streamer,
		UI:           app,
		Audio:        true,
		Version:      version,
		CheckUpdates: st.CheckUpdates,
		SaveCheckUpdates: func(on bool) {
			st.CheckUpdates = on
			if err := settings.Save(settings.DefaultPath(), st); err != nil {
				log.Printf("настройки: не удалось сохранить: %v", err)
			}
		},
		InstallUpdate: func(rel update.Release) {
			installUpdateAction(app, rel, &updateBusy, &updating)
		},
		Logf: log.Printf,
		// «Сохранить отчёт для поддержки»: диагностика идёт около минуты, поэтому в
		// своей горутине, а итог показывается уведомлением.
		SupportReport: func() {
			go supportReportAction(reporter, ctl.Snapshot,
				func(kind trayctl.NoticeKind, text string) { app.Notify(kind, trayctl.AppName, text) },
				&reportBusy, os.Getenv(envGPURetry), lastReceiverHost())
		},
	})
	app.Attach(ctl)
	ctl.Start(ctx)
	runTestHooks(ctl)
	switch {
	// Авария проверяется первой: признак смены видеокарты остаётся в окружении
	// и после аварийных перезапусков.
	case restartedAfterCrash():
		app.Notify(trayctl.NoticeInfo, trayctl.AppName,
			"Программа неожиданно закрылась и запущена снова. Если показ экрана был включён, выберите телевизор ещё раз.")
	case gpu.retried():
		// Программа перезапущена, чтобы работать на той видеокарте, к которой
		// подключён экран: продолжаем прерванный показ.
		if d, ok := takeResume(resumePath(), time.Now()); ok {
			log.Printf("после смены видеокарты продолжаем показ на %q (%s)", d.Name, d.Addr)
			ctl.Select(d)
		} else {
			app.Notify(trayctl.NoticeInfo, trayctl.AppName,
				"Программа перезапущена, чтобы показывать экран на ноутбуке с двумя видеокартами. Выберите телевизор в меню ещё раз.")
		}
	case os.Getenv(envUpdated) != "":
		app.Notify(trayctl.NoticeInfo, trayctl.AppName,
			fmt.Sprintf("AirSpoon Sender обновлён до версии %s", os.Getenv(envUpdated)))
	default:
		firstRunHint(app)
	}

	app.Run()

	if !ctl.Shutdown(5 * time.Second) {
		log.Printf("показ не успел корректно завершиться за 5 с")
	}
	if updating.Load() {
		if os.Getenv(envChild) != "1" {
			// Без наблюдателя перезапускать некому: запускаем новый exe сами, он
			// дождётся нашего мьютекса (AIRSPOON_UPDATED).
			if err := startUpdated(); err != nil {
				log.Printf("обновление: новый exe не запущен: %v", err)
				return 1
			}
			log.Printf("AirSpoon Sender завершён, обновлённый запущен отдельно")
			return 0
		}
		log.Printf("AirSpoon Sender завершён для установки обновления")
		return exitUpdate
	}
	if restartGPU.Load() {
		log.Printf("AirSpoon Sender завершён для перезапуска с другой видеокартой")
		return exitRestartGPU
	}
	log.Printf("AirSpoon Sender завершён")
	return 0
}

// installUpdateAction вызывается по пункту меню «Обновить до версии X.Y.Z».
func installUpdateAction(app *traywin.App, rel update.Release, busy, updating *atomic.Bool) {
	if !busy.CompareAndSwap(false, true) {
		return
	}
	defer busy.Store(false)

	msg := fmt.Sprintf("Установить AirSpoon Sender %s? Программа перезапустится; если идёт показ экрана, он прервётся.", rel.Version)
	if !traywin.Confirm(trayctl.AppName, msg) {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("обновление: не удалось определить путь к exe: %v", err)
		return
	}
	if realExe, err := filepath.EvalSymlinks(exe); err == nil {
		exe = realExe
	}

	exeNew := exe + ".new"
	exeOld := exe + ".old"

	onDownloadFailed := func(cause error) {
		log.Printf("обновление: сбой скачивания %s: %v", rel.Asset.BrowserDownloadURL, cause)
		app.Notify(trayctl.NoticeError, trayctl.AppName,
			"Не удалось скачать обновление. Открываю страницу релиза в браузере.")
		// Открываем только страницу GitHub: ShellExecute запустил бы и путь к программе.
		if strings.HasPrefix(rel.HTMLURL, "https://github.com/") {
			_ = windows.ShellExecute(0, windows.StringToUTF16Ptr("open"),
				windows.StringToUTF16Ptr(rel.HTMLURL), nil, nil, windows.SW_SHOWNORMAL)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := update.Download(ctx, nil, rel.Asset, exeNew); err != nil {
		onDownloadFailed(err)
		return
	}

	_ = os.Remove(exeOld)
	if err := os.Rename(exe, exeOld); err != nil {
		_ = os.Remove(exeNew)
		onDownloadFailed(fmt.Errorf("не удалось переименовать текущий файл: %w", err))
		return
	}
	if err := os.Rename(exeNew, exe); err != nil {
		_ = os.Rename(exeOld, exe)
		_ = os.Remove(exeNew)
		onDownloadFailed(fmt.Errorf("не удалось заменить exe: %w", err))
		return
	}

	if err := saveUpdateVersion(rel.Version); err != nil {
		log.Printf("обновление: не удалось сохранить номер версии: %v", err)
	}

	updating.Store(true)
	app.Quit()
}

// requestGPURestart вызывается, когда захват экрана не открылся из-за
// DXGI_ERROR_UNSUPPORTED. Если есть что изменить (и перезапуск ещё не
// выполнялся), записывает предпочтение видеокарты, запоминает телевизор и просит
// программу выйти с кодом exitRestartGPU: наблюдатель запустит её заново.
// Возвращает true, если перезапуск начат.
func requestGPURestart(gpu gpuEnv, t trayctl.Target, cause error, resumeFile string, quit func(), flag *atomic.Bool) bool {
	if gpu.getenv(envChild) != "1" {
		// Без наблюдателя (он не смог запустить рабочий процесс) выход с кодом 4
		// был бы тихим закрытием программы: перезапускать некому.
		log.Printf("видеокарта: перезапуск невозможен, программа работает без присмотра")
		return false
	}
	plan, err := gpu.unsupportedPlan(cause)
	if err != nil {
		log.Printf("видеокарта: решение о перезапуске не принято (%s): %v", plan.Reason, err)
		return false
	}
	if !plan.Restart {
		log.Printf("видеокарта: перезапуск не нужен или невозможен: %s", plan.Reason)
		return false
	}
	if err := gpu.apply(plan); err != nil {
		log.Printf("видеокарта: предпочтение не записано: %v", err)
		return false
	}
	if err := saveResume(resumeFile, t.Device, time.Now()); err != nil {
		log.Printf("видеокарта: телевизор для продолжения не сохранён: %v", err)
	}
	log.Printf("видеокарта: %s; программа перезапускается", plan.Reason)
	flag.Store(true)
	quit()
	return true
}

// setupTrayLog направляет стандартный лог в %LOCALAPPDATA%\AirSpoon\sender.log
// (ротация около 5 МБ). Возвращает функцию закрытия. Если файл недоступен,
// лог отбрасывается: программа без консоли не должна падать из-за журнала.
func setupTrayLog() (closeLog func()) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetPrefix("")
	airplay.SetDebugMode(false)
	w, err := applog.Open(applog.DefaultPath(), applog.DefaultMaxBytes)
	if err != nil {
		log.SetOutput(io.Discard)
		return func() {}
	}
	log.SetOutput(w)
	return func() { _ = w.Close() }
}

// firstRunHint при самом первом запуске подсказывает, где искать значок.
func firstRunHint(app *traywin.App) {
	marker := filepath.Join(filepath.Dir(defaultCredentialsFile()), "first-run-done")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(marker), 0o700)
	_ = os.WriteFile(marker, []byte("1\n"), 0o600)
	app.Notify(trayctl.NoticeInfo, trayctl.AppName,
		"Программа запущена. Нажмите на значок рядом с часами (если его не видно — на стрелку «^»), чтобы выбрать телевизор.")
}

// showFatal показывает человеку окно аварии (крупное, поверх остальных окон,
// с кнопкой «Открыть журнал»), а причину для того, кто поможет, записывает в
// журнал: в окне технических слов нет.
func showFatal(reason string) {
	appendLog("окно аварии: %s", reason)
	traywin.ShowFatal(traywin.FatalStartText, applog.DefaultPath())
}
