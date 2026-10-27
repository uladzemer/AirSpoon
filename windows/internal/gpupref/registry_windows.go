//go:build windows

package gpupref

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// KeyPath — раздел HKCU с предпочтениями видеокарты по программам.
const KeyPath = `Software\Microsoft\DirectX\UserGpuPreferences`

// RegistryStore хранит предпочтения в реестре текущего пользователя (HKCU):
// прав администратора не нужно, чужие пользователи и записи других программ
// не затрагиваются. Имя значения — полный путь к .exe, данные — «GpuPreference=N;».
type RegistryStore struct {
	// Key — раздел внутри HKCU; пусто — KeyPath (нестандартный нужен тестам).
	Key string
}

func (s RegistryStore) path() string {
	if s.Key == "" {
		return KeyPath
	}
	return s.Key
}

// find возвращает имя существующего значения для exe (путь Windows сравнивает
// без учёта регистра, поэтому ищем так же, чтобы не завести дубль).
func (s RegistryStore) find(k registry.Key, exe string) (string, bool) {
	if _, _, err := k.GetStringValue(exe); err == nil {
		return exe, true
	}
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return "", false
	}
	for _, n := range names {
		if strings.EqualFold(n, exe) {
			return n, true
		}
	}
	return "", false
}

// Get читает данные значения для exe.
func (s RegistryStore) Get(exe string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, s.path(), registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer k.Close()
	name, ok := s.find(k, exe)
	if !ok {
		return "", false, nil
	}
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Set записывает данные значения для exe (раздел создаётся при необходимости).
func (s RegistryStore) Set(exe, data string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, s.path(), registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	name := exe
	if existing, ok := s.find(k, exe); ok {
		name = existing
	}
	return k.SetStringValue(name, data)
}
