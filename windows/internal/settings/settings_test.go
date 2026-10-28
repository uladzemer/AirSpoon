package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.json")

	s := Load(path)
	if !s.CheckUpdates {
		t.Error("при отсутствии файла checkUpdates должен быть true")
	}
}

func TestLoadCorruptFileReturnsDefault(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(path, []byte("{invalid json..."), 0o600); err != nil {
		t.Fatal(err)
	}

	s := Load(path)
	if !s.CheckUpdates {
		t.Error("при повреждённом файле checkUpdates должен быть true")
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sub", "settings.json")

	// Сохраняем CheckUpdates = false
	if err := Save(path, Settings{CheckUpdates: false}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded := Load(path)
	if loaded.CheckUpdates {
		t.Error("ожидалось CheckUpdates = false")
	}

	// Перезаписываем CheckUpdates = true
	if err := Save(path, Settings{CheckUpdates: true}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded = Load(path)
	if !loaded.CheckUpdates {
		t.Error("ожидалось CheckUpdates = true")
	}
}
