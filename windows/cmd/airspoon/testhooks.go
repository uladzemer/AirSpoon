//go:build testhooks

package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"airspoon/internal/trayctl"
)

// Тестовые переменные окружения режима трея. Нужны для проверок без кликов
// (скриншоты меню, показ на testreceiver, корректное завершение). Работают
// только в особой сборке с тегом testhooks (build.ps1 -TestHooks): в обычном
// AirSpoonSender.exe их нет, поэтому чужая программа не сможет заставить его
// показывать экран по заданному адресу (см. testhooks_off.go).
//
//	AIRSPOON_TEST_DEVICES="Гостиная=127.0.0.1:7100;Спальня=127.0.0.1:7101"
//	    вместо поиска по mDNS в меню показываются эти телевизоры;
//	AIRSPOON_TEST_CONNECT=127.0.0.1:7100
//	    сразу после запуска начать показ по адресу (как «Подключить по адресу…»);
//	AIRSPOON_TEST_EXIT_AFTER=15s
//	    через указанное время выйти так же, как по пункту «Выход».
const (
	envTestDevices   = "AIRSPOON_TEST_DEVICES"
	envTestConnect   = "AIRSPOON_TEST_CONNECT"
	envTestExitAfter = "AIRSPOON_TEST_EXIT_AFTER"
)

// fixedDevices — поиск, всегда возвращающий заданный список.
type fixedDevices []trayctl.Device

func (f fixedDevices) Discover(context.Context) ([]trayctl.Device, error) {
	return append([]trayctl.Device(nil), f...), nil
}

// parseFakeDevices разбирает "Имя=адрес;Имя=адрес".
func parseFakeDevices(spec string) fixedDevices {
	var out fixedDevices
	for _, part := range strings.Split(spec, ";") {
		name, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		name, addr = strings.TrimSpace(name), strings.TrimSpace(addr)
		if !ok || name == "" || addr == "" {
			continue
		}
		norm, err := trayctl.NormalizeAddress(addr)
		if err != nil {
			continue
		}
		out = append(out, trayctl.Device{ID: "test:" + norm, Name: name, Addr: norm})
	}
	return out
}

func fakeDevicesFromEnv() trayctl.Discoverer {
	spec := os.Getenv(envTestDevices)
	if spec == "" {
		return nil
	}
	return parseFakeDevices(spec)
}

// runTestHooks выполняет действия из тестовых переменных окружения.
func runTestHooks(ctl *trayctl.Controller) {
	if addr := os.Getenv(envTestConnect); addr != "" {
		log.Printf("тестовый хук: подключение по адресу %s", addr)
		if err := ctl.ConnectAddress(addr); err != nil {
			log.Printf("тестовый хук: %v", err)
		}
	}
	if s := os.Getenv(envTestExitAfter); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			time.AfterFunc(d, func() {
				log.Printf("тестовый хук: выход через %s", d)
				ctl.Exit()
			})
		}
	}
}

// Имитация аварий (только при первом запуске рабочего процесса, чтобы
// перезапущенная программа не падала снова):
//
//	AIRSPOON_TEST_KILL_AFTER=3s
//	    процесс внезапно завершается с кодом 0xC0000005, как при сбое в драйвере
//	    (без единой строки в журнале);
//	AIRSPOON_TEST_PANIC_AFTER=3s
//	    паника в посторонней горутине (должна попасть в crash.log).
const (
	envTestKillAfter  = "AIRSPOON_TEST_KILL_AFTER"
	envTestPanicAfter = "AIRSPOON_TEST_PANIC_AFTER"
)

func init() {
	if os.Getenv(envRestarted) != "0" {
		return
	}
	if d := testDuration(envTestKillAfter); d > 0 {
		time.AfterFunc(d, func() {
			h, _ := windows.GetCurrentProcess()
			_ = windows.TerminateProcess(h, 0xC0000005)
		})
	}
	if d := testDuration(envTestPanicAfter); d > 0 {
		time.AfterFunc(d, func() { panic("тестовая паника (AIRSPOON_TEST_PANIC_AFTER)") })
	}
}

func testDuration(env string) time.Duration {
	d, err := time.ParseDuration(os.Getenv(env))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
