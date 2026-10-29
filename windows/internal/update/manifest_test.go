package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManifest(t *testing.T) (Manifest, []byte, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("trusted executable")
	sum := sha256.Sum256(payload)
	m := Manifest{Version: "1.2.3", Assets: []ManifestAsset{{ExeAssetName, int64(len(payload)), hex.EncodeToString(sum[:])}}}
	return m, payload, pub, private
}

func TestVerifyManifest(t *testing.T) {
	m, _, pub, private := testManifest(t)
	data, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, data)))
	key := base64.StdEncoding.EncodeToString(pub)
	for _, tc := range []struct {
		name, version, key string
		data, sig          []byte
		valid              bool
	}{
		{"valid", m.Version, key, data, sig, true},
		{"bytes changed", m.Version, key, append(append([]byte{}, data...), '\n'), sig, false},
		{"signature changed", m.Version, key, data, []byte(strings.Repeat("A", 88)), false},
		{"signature truncated", m.Version, key, data, sig[:20], false},
		{"version changed", "9.9.9", key, data, sig, false},
		{"unconfigured key", m.Version, UpdatePublicKey, data, sig, false},
		{"wrong key", m.Version, base64.StdEncoding.EncodeToString(make([]byte, 32)), data, sig, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifyManifest(tc.data, tc.sig, tc.key, tc.version)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
	// Даже корректно подписанные повреждённые/неоднозначные документы запрещены.
	for _, invalid := range []string{
		`{`, `{"version":"1.2.3","assets":[]}`,
		string(data) + "\n",
		strings.Replace(string(data), `"version":"1.2.3"`, `"version":"1.2.3","version":"1.2.3"`, 1),
		strings.Replace(string(data), `"size":18`, `"size":0`, 1),
	} {
		signed := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(invalid))))
		if _, err := verifyManifest([]byte(invalid), signed, key, m.Version); err == nil {
			t.Fatal("accepted malformed signed manifest")
		}
	}
}

func TestManifestCanonicalAndMatch(t *testing.T) {
	m, _, _, _ := testManifest(t)
	asset := m.Assets[0]
	m.Assets = append(m.Assets, ManifestAsset{"a.apk", 1, asset.SHA256})
	data, err := m.CanonicalJSON()
	if err != nil || bytes.Index(data, []byte(ExeAssetName)) > bytes.Index(data, []byte("a.apk")) {
		t.Fatalf("canonical order: %v", err)
	}
	for _, a := range []Asset{{Name: "other.exe", Size: asset.Size}, {Name: asset.Name, Size: asset.Size + 1}} {
		if _, err := m.match(a); err == nil {
			t.Fatal("accepted mismatched asset")
		}
	}
	for _, change := range []func(*Manifest){
		func(m *Manifest) { m.Version = "v1.2.3" },
		func(m *Manifest) { m.Assets = append(m.Assets, m.Assets[0]) },
		func(m *Manifest) { m.Assets[0].SHA256 = "bad" },
		func(m *Manifest) { m.Assets[0].Size = -1 },
		func(m *Manifest) { m.Assets[0].Name = "../file.exe" },
	} {
		copy := Manifest{Version: m.Version, Assets: append([]ManifestAsset(nil), m.Assets...)}
		change(&copy)
		if _, err := copy.CanonicalJSON(); err == nil {
			t.Fatal("accepted invalid manifest")
		}
	}
}

func TestDownloadSignedRelease(t *testing.T) {
	m, payload, pub, private := testManifest(t)
	data, _ := m.CanonicalJSON()
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, data)))
	for _, kind := range []string{"valid", "tampered exe", "tampered manifest", "missing signature", "wrong version", "wrong size", "duplicate manifest", "github digest"} {
		t.Run(kind, func(t *testing.T) {
			files := map[string][]byte{ManifestName: data, ManifestSignatureName: sig, ExeAssetName: append([]byte{}, payload...)}
			if kind == "tampered exe" {
				files[ExeAssetName][0] ^= 1
			}
			if kind == "tampered manifest" {
				files[ManifestName] = bytes.ReplaceAll(data, []byte("1.2.3"), []byte("1.2.4"))
			}
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(files[strings.TrimPrefix(r.URL.Path, "/")])
			}))
			defer ts.Close()
			rel := Release{TagName: "v1.2.3", Version: "1.2.3"}
			for _, name := range []string{ManifestName, ManifestSignatureName, ExeAssetName} {
				a := Asset{Name: name, Size: int64(len(files[name])), BrowserDownloadURL: ts.URL + "/" + name}
				if name == ExeAssetName {
					rel.Asset = a
				}
				if kind != "missing signature" || name != ManifestSignatureName {
					rel.Assets = append(rel.Assets, a)
				}
			}
			if kind == "wrong version" {
				rel.Version, rel.TagName = "9.9.9", "v9.9.9"
			}
			if kind == "wrong size" {
				rel.Asset.Size++
			}
			if kind == "duplicate manifest" {
				rel.Assets = append(rel.Assets, rel.Assets[0])
			}
			if kind == "github digest" {
				rel.Asset.Digest = "sha256:" + strings.Repeat("0", 64)
			}
			dst := filepath.Join(t.TempDir(), "update.exe")
			err := downloadRelease(context.Background(), ts.Client(), rel, dst, base64.StdEncoding.EncodeToString(pub))
			if (err == nil) != (kind == "valid") {
				t.Fatalf("unexpected result: %v", err)
			}
			if kind != "valid" {
				if _, err := os.Stat(dst); !os.IsNotExist(err) {
					t.Fatal("untrusted executable left at destination")
				}
			}
		})
	}
}

func TestDownloadRejectsHTTPSDowngrade(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/update", http.StatusFound)
	}))
	defer ts.Close()
	err := Download(context.Background(), ts.Client(), Asset{Size: 10, BrowserDownloadURL: ts.URL}, filepath.Join(t.TempDir(), "update"))
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS rejection: %v", err)
	}
}
