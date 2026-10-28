package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		input string
		want  [3]int
		ok    bool
	}{
		{"0.2.0", [3]int{0, 2, 0}, true},
		{"v0.2.0", [3]int{0, 2, 0}, true},
		{"V1.10.3", [3]int{1, 10, 3}, true},
		{"dev", [3]int{}, false},
		{"", [3]int{}, false},
		{"0.2", [3]int{}, false},
		{"0.2.0.1", [3]int{}, false},
		{"0.2.0-rc1", [3]int{}, false},
		{"v-1.0.0", [3]int{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseVersion(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseVersion(%q) = (%v, %v), ожидалось (%v, %v)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		rel, cur [3]int
		want     bool
	}{
		{[3]int{0, 3, 0}, [3]int{0, 2, 0}, true},
		{[3]int{1, 0, 0}, [3]int{0, 9, 9}, true},
		{[3]int{0, 2, 1}, [3]int{0, 2, 0}, true},
		{[3]int{0, 10, 0}, [3]int{0, 9, 0}, true},
		{[3]int{0, 2, 0}, [3]int{0, 2, 0}, false},
		{[3]int{0, 1, 9}, [3]int{0, 2, 0}, false},
		{[3]int{1, 0, 0}, [3]int{2, 0, 0}, false},
	}
	for _, tc := range cases {
		if got := IsNewer(tc.rel, tc.cur); got != tc.want {
			t.Errorf("IsNewer(%v, %v) = %v, ожидалось %v", tc.rel, tc.cur, got, tc.want)
		}
	}
}

func TestCheckDevSkipsNetwork(t *testing.T) {
	serverCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	rel, newer, err := Check(context.Background(), ts.Client(), ts.URL, "dev")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if newer {
		t.Error("для dev не может быть найдено обновление")
	}
	if rel.TagName != "" {
		t.Errorf("неожиданный релиз: %+v", rel)
	}
	if serverCalled {
		t.Error("Check не должен делать сетевых запросов при current = 'dev'")
	}
}

func TestCheckUnrecognizedCurrentSkipsNetwork(t *testing.T) {
	serverCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	for _, invalid := range []string{"", "invalid", "1.0", "1.2.3.4"} {
		rel, newer, err := Check(context.Background(), ts.Client(), ts.URL, invalid)
		if err != nil {
			t.Errorf("версия %q: неожиданная ошибка: %v", invalid, err)
		}
		if newer {
			t.Errorf("версия %q: newer = true", invalid)
		}
		if rel.TagName != "" {
			t.Errorf("версия %q: неожиданный релиз: %+v", invalid, rel)
		}
	}
	if serverCalled {
		t.Error("сеть не должна вызываться для нераспознанных версий")
	}
}

func TestCheckFindsNewerRelease(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("неверный заголовок Accept: %s", r.Header.Get("Accept"))
		}
		if r.Header.Get("User-Agent") != "AirSpoon-Sender/0.2.0" {
			t.Errorf("неверный заголовок User-Agent: %s", r.Header.Get("User-Agent"))
		}
		resp := Release{
			TagName: "v0.3.0",
			HTMLURL: "https://github.com/uladzemer/AirSpoon/releases/tag/v0.3.0",
			Assets: []Asset{
				{
					Name:               "airspoon-debug-armeabi-v7a.apk",
					Size:               1024,
					BrowserDownloadURL: "https://example.com/airspoon.apk",
				},
				{
					Name:               ExeAssetName,
					Size:               2048,
					BrowserDownloadURL: "https://example.com/AirSpoonSender.exe",
					Digest:             "sha256:abcd",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	rel, newer, err := Check(context.Background(), ts.Client(), ts.URL, "0.2.0")
	if err != nil {
		t.Fatalf("ошибка Check: %v", err)
	}
	if !newer {
		t.Fatal("ожидалось newer = true для 0.3.0 > 0.2.0")
	}
	if rel.Version != "0.3.0" {
		t.Errorf("rel.Version = %q, ожидалось 0.3.0", rel.Version)
	}
	if rel.Asset.Name != ExeAssetName || rel.Asset.Size != 2048 {
		t.Errorf("неверный ассет: %+v", rel.Asset)
	}
}

func TestCheckReleaseNotNewer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := Release{
			TagName: "v0.2.0",
			HTMLURL: "https://github.com/uladzemer/AirSpoon/releases/tag/v0.2.0",
			Assets: []Asset{
				{
					Name:               ExeAssetName,
					Size:               2048,
					BrowserDownloadURL: "https://example.com/AirSpoonSender.exe",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	rel, newer, err := Check(context.Background(), ts.Client(), ts.URL, "0.2.0")
	if err != nil {
		t.Fatalf("ошибка Check: %v", err)
	}
	if newer {
		t.Error("ожидалось newer = false для одинаковых версий")
	}
	if rel.Version != "0.2.0" {
		t.Errorf("rel.Version = %q", rel.Version)
	}
}

func TestCheckMissingAssetReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := Release{
			TagName: "v0.3.0",
			Assets: []Asset{
				{
					Name: "other.exe",
					Size: 100,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	_, _, err := Check(context.Background(), ts.Client(), ts.URL, "0.2.0")
	if err == nil {
		t.Fatal("ожидалась ошибка из-за отсутствия AirSpoonSender.exe")
	}
}

func TestDownloadSuccess(t *testing.T) {
	payload := []byte("fake-new-executable-binary-data")
	sum := sha256.Sum256(payload)
	hexDigest := "sha256:" + hex.EncodeToString(sum[:])

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "AirSpoonSender.exe.new")

	asset := Asset{
		Name:               ExeAssetName,
		Size:               int64(len(payload)),
		BrowserDownloadURL: ts.URL + "/download",
		Digest:             hexDigest,
	}

	err := Download(context.Background(), ts.Client(), asset, dst)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("не удалось прочитать скачанный файл: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("содержимое не совпадает: got %q, want %q", data, payload)
	}
}

func TestDownloadSizeExceeded(t *testing.T) {
	payload := []byte("this payload is longer than expected")

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "file.exe")

	asset := Asset{
		Name:               ExeAssetName,
		Size:               10, // меньше, чем len(payload)
		BrowserDownloadURL: ts.URL,
	}

	err := Download(context.Background(), ts.Client(), asset, dst)
	if err == nil {
		t.Fatal("ожидалась ошибка превышения размера")
	}
	if _, serr := os.Stat(dst); serr == nil {
		t.Fatal("файл назначения не должен был создаться при ошибке")
	}
}

func TestDownloadInvalidDigest(t *testing.T) {
	payload := []byte("executable binary data")

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "file.exe")

	asset := Asset{
		Name:               ExeAssetName,
		Size:               int64(len(payload)),
		BrowserDownloadURL: ts.URL,
		Digest:             "sha256:00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
	}

	err := Download(context.Background(), ts.Client(), asset, dst)
	if err == nil {
		t.Fatal("ожидалась ошибка несовпадения контрольной суммы")
	}
	if _, serr := os.Stat(dst); serr == nil {
		t.Fatal("файл назначения не должен был создаться при ошибке")
	}
}

func TestDownloadRejectsNonHTTPS(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()

	asset := Asset{
		Name:               ExeAssetName,
		Size:               10,
		BrowserDownloadURL: ts.URL, // http://
	}

	err := Download(context.Background(), ts.Client(), asset, "target.exe")
	if err == nil {
		t.Fatal("ожидалась ошибка для протокола HTTP")
	}
}
