package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"airspoon/internal/update"
)

func TestKeygenAndSign(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	keyPath := filepath.Join("keys", "test.key")
	if err := run([]string{"keygen", "-out", keyPath}, &stdout); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil || len(seed) != 32 {
		t.Fatal("invalid seed")
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil || len(pub) != 32 || !bytes.Equal(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), pub) {
		t.Fatal("stdout must contain only the public key")
	}
	if strings.Contains(stdout.String(), string(encoded)) {
		t.Fatal("seed leaked to stdout")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(keyPath)
		if info.Mode().Perm() != 0o600 {
			t.Fatal("key permissions must be 0600")
		}
	}
	if err := run([]string{"keygen", "-out", keyPath}, &stdout); err == nil {
		t.Fatal("must not overwrite an existing key")
	}
	t.Setenv("TEST_SIGNING_KEY", string(encoded))
	for _, name := range []string{"z.apk", "a.exe"} {
		if err := os.WriteFile(name, []byte("test asset"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout.Reset()
	args := []string{"sign", "-version", "1.2.3", "-key-env", "TEST_SIGNING_KEY", "z.apk", "a.exe"}
	if err := run(args, &stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatal("sign must not print secrets")
	}
	data, _ := os.ReadFile(update.ManifestName)
	signature, _ := os.ReadFile(update.ManifestSignatureName)
	sig, _ := base64.StdEncoding.DecodeString(string(signature))
	if !ed25519.Verify(pub, data, sig) {
		t.Fatal("signature does not verify")
	}
	var m update.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("test asset"))
	if m.Version != "1.2.3" || len(m.Assets) != 2 || m.Assets[0].Name != "a.exe" || m.Assets[0].Size != 10 || m.Assets[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("incorrect manifest contents")
	}
	// Порядок входных файлов не меняет ни JSON, ни подпись.
	args[len(args)-2], args[len(args)-1] = args[len(args)-1], args[len(args)-2]
	if err := run(args, &stdout); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(update.ManifestName)
	if !bytes.Equal(again, data) {
		t.Fatal("manifest is not deterministic")
	}
	data[0] ^= 1
	if ed25519.Verify(pub, data, sig) {
		t.Fatal("accepted tampering")
	}
}

func TestInvalidSigningInput(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("asset.exe", []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(make([]byte, 64))} {
		t.Setenv("TEST_SIGNING_KEY", value)
		if err := run([]string{"sign", "-version", "1.2.3", "-key-env", "TEST_SIGNING_KEY", "asset.exe"}, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted invalid seed")
		}
	}
	t.Setenv("TEST_SIGNING_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, args := range [][]string{
		{"sign", "-version", "v1.2.3", "-key-env", "TEST_SIGNING_KEY", "asset.exe"},
		{"sign", "-version", "1.2.3", "-key-env", "TEST_SIGNING_KEY", "asset.exe", "asset.exe"},
		{"sign", "-version", "1.2.3", "-key-env", "TEST_SIGNING_KEY", "missing.exe"},
		{"keygen"}, {"sign"}, {}, {"unknown"},
	} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	if _, err := os.Stat(update.ManifestName); !os.IsNotExist(err) {
		t.Fatal("manifest created on failure")
	}
}
