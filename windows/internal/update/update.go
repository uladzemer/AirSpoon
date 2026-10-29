// Пакет update — проверка и скачивание обновлений AirSpoon Sender с GitHub Releases.
// Чистый Go, без системных зависимостей, покрыт модульными тестами.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIURL — адрес последнего релиза репозитория на GitHub.
const DefaultAPIURL = "https://api.github.com/repos/uladzemer/AirSpoon/releases/latest"

// ExeAssetName — имя файла сборки AirSpoon Sender для Windows в релизе.
const ExeAssetName = "AirSpoonSender.exe"

// Asset описывает файл релиза GitHub.
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest,omitempty"`
}

// Release описывает релиз GitHub с исполняемым файлом программы.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`

	// Asset — найденный файл ExeAssetName в релизе.
	Asset Asset `json:"-"`
	// Version — номер версии релиза без префикса "v" (например, "0.3.0").
	Version string `json:"-"`
}

// ParseVersion разбирает строку версии вида "v1.2.3" или "1.2.3" в тройку [3]int.
// Если версия не распознана (включая "dev", пустую строку, пререлизы), возвращает ok = false.
func ParseVersion(s string) (v [3]int, ok bool) {
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// IsNewer сообщает, строго ли версия rel новее cur (числовое сравнение трёх компонент).
func IsNewer(rel, cur [3]int) bool {
	for i := 0; i < 3; i++ {
		if rel[i] > cur[i] {
			return true
		}
		if rel[i] < cur[i] {
			return false
		}
	}
	return false
}

// Check запрашивает последний релиз по apiURL (по умолчанию DefaultAPIURL) и
// проверяет, новее ли он текущей версии current.
// Если current равен "dev" или не распознан, Check сразу возвращает false без сети.
func Check(ctx context.Context, client *http.Client, apiURL, current string) (Release, bool, error) {
	curVer, ok := ParseVersion(current)
	if !ok {
		// "dev" или нераспознанная версия: проверки в сеть не выполняются.
		return Release{}, false, nil
	}
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AirSpoon-Sender/"+current)

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("github api: %s", resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, false, fmt.Errorf("разбор ответа github: %w", err)
	}

	relVer, ok := ParseVersion(rel.TagName)
	if !ok {
		return Release{}, false, fmt.Errorf("нераспознанный тег релиза %q", rel.TagName)
	}

	var foundAsset *Asset
	for i := range rel.Assets {
		if rel.Assets[i].Name == ExeAssetName {
			foundAsset = &rel.Assets[i]
			break
		}
	}
	if foundAsset == nil {
		return Release{}, false, fmt.Errorf("файл %s не найден в релизе %s", ExeAssetName, rel.TagName)
	}
	rel.Asset = *foundAsset
	rel.Version = fmt.Sprintf("%d.%d.%d", relVer[0], relVer[1], relVer[2])

	newer := IsNewer(relVer, curVer)
	return rel, newer, nil
}

// Download скачивает asset во временный файл рядом с dstPath, проверяет размер
// и контрольную сумму SHA-256 (если задан digest sha256:<hex>), а затем атомарно
// переименовывает в dstPath. Разрешены только HTTPS-ссылки.
func Download(ctx context.Context, client *http.Client, asset Asset, dstPath string) (err error) {
	u, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("недопустимый адрес скачивания: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("небезопасный протокол %q: скачивание разрешено только по HTTPS", u.Scheme)
	}
	if asset.Size <= 0 {
		return errors.New("недопустимый размер файла ассета (<= 0)")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	// Проверяем также каждый редирект, включая переход на сервер хранения GitHub.
	safeClient := *client
	safeClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Scheme, "https") {
			return errors.New("скачивание обновления разрешено только по HTTPS")
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("слишком много перенаправлений при скачивании обновления")
		}
		return nil
	}

	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(dstPath)+".tmp*")
	if err != nil {
		return fmt.Errorf("создание временного файла: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
		}
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AirSpoon-Sender")
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := safeClient.Do(req)
	if err != nil {
		return fmt.Errorf("скачивание: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер вернул статус: %s", resp.Status)
	}

	hasher := sha256.New()
	// Ограничиваем чтение asset.Size + 1 байт, чтобы обнаружить превышение лимита.
	w := io.MultiWriter(tmpFile, hasher)
	copied, err := io.Copy(w, io.LimitReader(resp.Body, asset.Size+1))
	if err != nil {
		return fmt.Errorf("ошибка при записи файла: %w", err)
	}
	if copied > asset.Size {
		return fmt.Errorf("размер файла превышает ожидаемый (%d > %d байт)", copied, asset.Size)
	}
	if copied < asset.Size {
		return fmt.Errorf("скачанный файл не полон (%d из %d байт)", copied, asset.Size)
	}

	// Проверка контрольной суммы (если в ассете есть digest вида sha256:<hex>).
	if strings.HasPrefix(strings.ToLower(asset.Digest), "sha256:") {
		expected := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(asset.Digest), "sha256:"))
		actual := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(expected, actual) {
			return fmt.Errorf("контрольная сумма SHA-256 не совпадает: ожидалась %s, получена %s", expected, actual)
		}
	}

	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	tmpFile = nil // закрыт успешно

	if err := os.Rename(tmpName, dstPath); err != nil {
		return fmt.Errorf("переименование во временный файл: %w", err)
	}
	return nil
}
