//go:build !testhooks

package main

import (
	"context"

	"airspoon/internal/trayctl"
)

// В обычной сборке тестовых переменных окружения (AIRSPOON_TEST_*) нет: любая
// другая программа могла бы с их помощью запустить показ экрана без участия
// человека. Включаются сборкой с тегом testhooks (см. testhooks.go).

func fakeDevicesFromEnv() trayctl.Discoverer { return nil }

func runTestHooks(*trayctl.Controller) {}

// overrideGPUEnv в обычной сборке ничего не подменяет: настоящий реестр и
// настоящие видеокарты.
func overrideGPUEnv(*gpuEnv) {}

// wrapTestRun в обычной сборке возвращает показ без изменений.
func wrapTestRun(run func(context.Context, mirrorOptions) error) func(context.Context, mirrorOptions) error {
	return run
}
