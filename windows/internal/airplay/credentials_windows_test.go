//go:build windows

package airplay

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCredentialDPAPIRoundTrip(t *testing.T) {
	secret := []byte("private seed and restore token")
	protected, err := protectCredentialSecrets(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(protected, secret) || bytes.Contains(protected, secret) {
		t.Fatal("DPAPI did not encrypt secret")
	}
	plain, err := unprotectCredentialSecrets(protected)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatalf("DPAPI round trip: %q, %v", plain, err)
	}
	protected[len(protected)/2] ^= 0x80
	if _, err := unprotectCredentialSecrets(protected); err == nil {
		t.Fatal("DPAPI accepted tampered ciphertext")
	}
}

func TestCredentialsAtomicWriteKeepsOldFileOnRenameFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRestoreToken("device", "old"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// Deny delete sharing to force the final Windows rename to fail.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := store.SaveRestoreToken("device", "new"); err == nil {
		t.Fatal("rename should fail on locked destination")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed save truncated existing file", err)
	}
	if store.Lookup("device").RestoreToken != "old" {
		t.Fatal("failed save changed in-memory credentials")
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".credentials-*"))
	if err != nil || len(temps) > 0 {
		t.Fatalf("temporary files left: %v, %v", temps, err)
	}
}

func TestCredentialsCorruptDPAPIIsQuarantined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"device":{"protected_secrets":"YmFk"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewCredentialStore(path)
	if err != nil || store.Len() != 0 {
		t.Fatalf("corrupt DPAPI file: %v", err)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatal(err)
	}
}
