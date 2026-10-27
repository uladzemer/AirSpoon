package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain уводит LOCALAPPDATA/APPDATA во временный каталог: тесты запускают
// настоящий mirror, который пишет last-receiver.json, журнал и учётные данные,
// и в профиль разработчика (и тем более владельца) они попадать не должны.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "airspoon-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "временный каталог:", err)
		os.Exit(1)
	}
	os.Setenv("LOCALAPPDATA", dir)
	os.Setenv("APPDATA", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
