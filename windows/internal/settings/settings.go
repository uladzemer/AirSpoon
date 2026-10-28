// Пакет settings — загрузка и атомарное сохранение настроек AirSpoon Sender.
// Хранятся в %LOCALAPPDATA%\AirSpoon\settings.json в формате JSON.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"airspoon/internal/applog"
)

// Settings описывает пользовательские настройки программы.
type Settings struct {
	CheckUpdates bool `json:"checkUpdates"`
}

// Default возвращает настройки по умолчанию.
func Default() Settings {
	return Settings{
		CheckUpdates: true,
	}
}

// DefaultPath возвращает путь к файлу настроек: %LOCALAPPDATA%\AirSpoon\settings.json.
func DefaultPath() string {
	return filepath.Join(filepath.Dir(applog.DefaultPath()), "settings.json")
}

// Load читает настройки из файла path. Если path пуст, используется DefaultPath().
// Если файл отсутствует, не читается или содержит повреждённый JSON — возвращаются
// настройки по умолчанию (CheckUpdates = true).
func Load(path string) Settings {
	if path == "" {
		path = DefaultPath()
	}
	s := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Default()
	}
	return s
}

// Save атомарно сохраняет настройки в path через временный файл и Rename.
// Если path пуст, используется DefaultPath().
func Save(path string, s Settings) error {
	if path == "" {
		path = DefaultPath()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("создание временного файла: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	tmpFile = nil

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("замена файла настроек: %w", err)
	}
	return nil
}
