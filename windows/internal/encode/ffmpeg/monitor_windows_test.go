//go:build windows

package ffmpeg

import (
	"testing"

	"airspoon/internal/capture/dxgi"
)

// requireMonitor пропускает тест, если в этом сеансе нет монитора, который можно
// захватить (отключённый удалённый рабочий стол, служба, сервер без экрана):
// ddagrab в ffmpeg там падает с «Failed to enumerate DXGI output». Так же
// ведут себя тесты пакетов dxgi и native. Если монитор есть, а захват не
// работает, тест должен падать, а не пропускаться.
func requireMonitor(t *testing.T) {
	t.Helper()
	list, err := dxgi.ListOutputs()
	if err != nil {
		t.Skipf("перечисление мониторов недоступно в этом окружении: %v", err)
	}
	if len(list) == 0 {
		t.Skip("в этом сеансе нет монитора для захвата (рабочий стол недоступен): проверять нечего")
	}
}
