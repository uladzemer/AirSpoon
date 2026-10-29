package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// UpdatePublicKey — base64 от 32 байт Ed25519. Заменить перед первым релизом.
const UpdatePublicKey = "QDIpKAuvK0T7meBr/55aHsuDpFPmUMQhBLPEZ/UhjHQ="

const ManifestName = "airspoon-update.json"
const ManifestSignatureName = ManifestName + ".sig"
const maxManifestSize = 64 * 1024

type ManifestAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version string          `json:"version"`
	Assets  []ManifestAsset `json:"assets"`
}

var manifestVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// CanonicalJSON задаёт единственный формат: порядок полей структуры, ассеты по имени,
// без пробелов и завершающего перевода строки. Подписываются именно эти байты.
func (m Manifest) CanonicalJSON() ([]byte, error) {
	if !manifestVersion.MatchString(m.Version) || len(m.Assets) == 0 {
		return nil, errors.New("недопустимая версия или пустой манифест обновления")
	}
	m.Assets = append([]ManifestAsset(nil), m.Assets...)
	sort.Slice(m.Assets, func(i, j int) bool { return m.Assets[i].Name < m.Assets[j].Name })
	for i, a := range m.Assets {
		digest, err := hex.DecodeString(a.SHA256)
		if a.Name == "" || strings.ContainsAny(a.Name, `/\`) || a.Size <= 0 ||
			err != nil || len(digest) != sha256.Size || a.SHA256 != strings.ToLower(a.SHA256) ||
			(i > 0 && m.Assets[i-1].Name == a.Name) {
			return nil, errors.New("недопустимый файл или повтор имени в манифесте обновления")
		}
	}
	data, err := json.Marshal(m)
	if len(data) > maxManifestSize {
		return nil, errors.New("манифест обновления слишком велик")
	}
	return data, err
}

func verifyManifest(data, signature []byte, publicKey, version string) (Manifest, error) {
	key, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Manifest{}, errors.New("ключ проверки обновлений не настроен")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(sig) != ed25519.SignatureSize || len(data) > maxManifestSize ||
		!ed25519.Verify(key, data, sig) {
		return Manifest{}, errors.New("подпись манифеста обновления недействительна")
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, errors.New("не удалось разобрать манифест обновления")
	}
	canonical, err := m.CanonicalJSON()
	if err != nil || !bytes.Equal(data, canonical) {
		return Manifest{}, errors.New("недопустимый формат манифеста обновления")
	}
	if m.Version != version {
		return Manifest{}, errors.New("версия манифеста не совпадает с версией релиза")
	}
	return m, nil
}

func (m Manifest) match(asset Asset) (ManifestAsset, error) {
	for _, a := range m.Assets {
		if a.Name == asset.Name && a.Size == asset.Size {
			return a, nil
		}
	}
	return ManifestAsset{}, errors.New("имя или размер файла не совпадает с подписанным манифестом")
}

func (a ManifestAsset) verifyFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("файл обновления не соответствует подписанному манифесту (размер или SHA-256)")
	}
	return nil
}

// DownloadRelease обязателен перед установкой: GitHub metadata не является источником доверия.
func DownloadRelease(ctx context.Context, client *http.Client, rel Release, dst string) error {
	return downloadRelease(ctx, client, rel, dst, UpdatePublicKey)
}

func downloadRelease(ctx context.Context, client *http.Client, rel Release, dst, publicKey string) (err error) {
	if rel.Asset.Name != ExeAssetName || strings.TrimPrefix(rel.TagName, "v") != rel.Version {
		return errors.New("недопустимая версия или имя файла обновления")
	}
	dir, err := os.MkdirTemp("", "airspoon-manifest-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	readAsset := func(name string, limit int64) ([]byte, error) {
		var found []Asset
		for _, a := range rel.Assets {
			if a.Name == name {
				found = append(found, a)
			}
		}
		if len(found) != 1 || found[0].Size <= 0 || found[0].Size > limit {
			return nil, fmt.Errorf("отсутствует или недопустим файл подписи обновления %s", name)
		}
		path := filepath.Join(dir, name)
		if err := Download(ctx, client, found[0], path); err != nil {
			return nil, fmt.Errorf("скачивание %s: %w", name, err)
		}
		return os.ReadFile(path)
	}
	data, err := readAsset(ManifestName, maxManifestSize)
	if err != nil {
		return err
	}
	sig, err := readAsset(ManifestSignatureName, 256)
	if err != nil {
		return err
	}
	m, err := verifyManifest(data, sig, publicKey, rel.Version)
	if err != nil {
		return err
	}
	a, err := m.match(rel.Asset)
	if err != nil {
		return err
	}
	if err := Download(ctx, client, rel.Asset, dst); err != nil {
		return err
	}
	if err := a.verifyFile(dst); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
