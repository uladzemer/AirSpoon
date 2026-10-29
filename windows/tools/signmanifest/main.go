// signmanifest создаёт ключ и подписанный манифест обновлений. Секреты не выводятся.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"airspoon/internal/update"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("нужна команда keygen или sign")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard) // Не отражаем в ошибке неизвестные аргументы с возможными секретами.
	switch args[0] {
	case "keygen":
		out := f.String("out", "", "файл для base64 seed")
		if err := f.Parse(args[1:]); err != nil || *out == "" || f.NArg() != 0 {
			return errors.New("использование: keygen -out <file>")
		}
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return errors.New("не удалось создать ключ обновлений")
		}
		if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := io.WriteString(file, base64.StdEncoding.EncodeToString(private.Seed()))
		closeErr := file.Close()
		if writeErr != nil {
			return errors.New("не удалось записать ключ обновлений")
		}
		if closeErr != nil {
			return errors.New("не удалось сохранить ключ обновлений")
		}
		_, err = fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(public))
		return err
	case "sign":
		version := f.String("version", "", "версия X.Y.Z")
		keyEnv := f.String("key-env", "AIRSPOON_UPDATE_SIGNING_KEY", "имя переменной с base64 seed")
		if err := f.Parse(args[1:]); err != nil || f.NArg() == 0 {
			return errors.New("использование: sign -version X.Y.Z -key-env AIRSPOON_UPDATE_SIGNING_KEY <files...>")
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv(*keyEnv)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("нет корректного ключа подписи обновлений в переменной окружения (нужен base64 seed 32 байта)")
		}
		m := update.Manifest{Version: *version}
		for _, path := range f.Args() {
			asset, err := hashAsset(path)
			if err != nil {
				return err
			}
			m.Assets = append(m.Assets, asset)
		}
		data, err := m.CanonicalJSON()
		if err != nil {
			return err
		}
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
		if err := os.WriteFile(update.ManifestName, data, 0o644); err != nil {
			return err
		}
		return os.WriteFile(update.ManifestSignatureName, []byte(base64.StdEncoding.EncodeToString(sig)), 0o644)
	default:
		return errors.New("нужна команда keygen или sign")
	}
}

func hashAsset(path string) (update.ManifestAsset, error) {
	f, err := os.Open(path)
	if err != nil {
		return update.ManifestAsset{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return update.ManifestAsset{}, errors.New("ассет должен быть обычным файлом")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	return update.ManifestAsset{Name: filepath.Base(path), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}
