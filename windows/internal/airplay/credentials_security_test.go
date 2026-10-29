package airplay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsCorruptFileIsQuarantined(t *testing.T) {
	for _, data := range []string{"{broken", "null", "[]", `{"device":null}`} {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		store, err := NewCredentialStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if store.Len() != 0 || store.Lookup("device") != nil {
			t.Fatal("corrupt file not treated as empty")
		}
		backup, err := os.ReadFile(path + ".bad")
		if err != nil || string(backup) != data {
			t.Fatalf("quarantine = %q, %v", backup, err)
		}
		if err := store.SaveRestoreToken("device", "fresh"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialsAtomicReplacementAndFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	store, err := NewCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"first", "second"} {
		if err := store.SaveRestoreToken("device", token); err != nil {
			t.Fatal(err)
		}
		reloaded, err := NewCredentialStore(path)
		if err != nil || reloaded.Lookup("device").RestoreToken != token {
			t.Fatalf("reload: %v", err)
		}
	}
	// Rename cannot replace a nonempty directory. It must leave that target
	// intact, clean up the temporary file, and roll back the in-memory update.
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	fb := store.backend.(*fileBackend)
	fb.path = blocked
	if err := store.SaveRestoreToken("device", "failed"); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	if got := store.Lookup("device").RestoreToken; got != "second" {
		t.Fatalf("failed save changed memory to %q", got)
	}
	if keep, err := os.ReadFile(filepath.Join(blocked, "keep")); err != nil || string(keep) != "keep" {
		t.Fatal("failed replacement damaged target")
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".credentials-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files left behind: %v, %v", temps, err)
	}
}

func TestCredentialsPlaintextMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x63}, ed25519.SeedSize))
	creds := &SavedCredentials{PairingID: "sender", Ed25519Seed: private.Seed(), Ed25519Public: private.Public().(ed25519.PublicKey), RestoreToken: "restore-secret"}
	data, err := json.Marshal(map[string]*SavedCredentials{"device": creds})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		store, err := NewCredentialStore(path)
		if err != nil {
			t.Fatal(err)
		}
		got := store.Lookup("device")
		if got == nil || !bytes.Equal(got.Ed25519Seed, private.Seed()) || got.RestoreToken != creds.RestoreToken {
			t.Fatal("credentials lost during migration")
		}
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if protectCredentials {
		if bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(private.Seed()))) || bytes.Contains(data, []byte("restore-secret")) || bytes.Contains(data, []byte("ed25519_seed")) {
			t.Fatal("secret fields remain in plaintext")
		}
		if !bytes.Contains(data, []byte("protected_secrets")) {
			t.Fatal("DPAPI envelope missing")
		}
	} else if !bytes.Contains(data, []byte("ed25519_seed")) {
		t.Fatal("non-Windows credentials no longer plaintext")
	}
}
