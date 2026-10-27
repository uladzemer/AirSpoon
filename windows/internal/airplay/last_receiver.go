package airplay

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LastReceiver — последний телевизор, на который успешно пошёл показ. Хранится
// рядом с журналом (%LOCALAPPDATA%\AirSpoon\last-receiver.json), чтобы телевизор
// появлялся в списке, даже если mDNS в этой сети не доходит.
type LastReceiver struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	DeviceID string `json:"deviceid,omitempty"`
}

// LoadLastReceiver читает файл; ok == false, если файла нет или он повреждён.
func LoadLastReceiver(path string) (LastReceiver, bool) {
	var r LastReceiver
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &r) != nil {
		return LastReceiver{}, false
	}
	if r.Address == "" || r.Port <= 0 || r.Port > 65535 {
		return LastReceiver{}, false
	}
	return r, true
}

// SaveLastReceiver записывает файл целиком (через временный файл). Если там уже
// то же самое, диск не трогается.
func SaveLastReceiver(path string, r LastReceiver) error {
	if old, ok := LoadLastReceiver(path); ok && old == r {
		return nil
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
