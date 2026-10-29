package airplay

import (
	"bytes"
	"crypto/ed25519"
	"path/filepath"
	"testing"
)

func TestForgetReceiverIdentityPreservesOtherCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	creds := &SavedCredentials{
		PairingID: "sender", Ed25519Public: private.Public().(ed25519.PublicKey), Ed25519Seed: private.Seed(),
		ReceiverID: "receiver", ReceiverPublic: bytes.Repeat([]byte{0x51}, ed25519.PublicKeySize),
		PairingProtocol: PairingProtocolHAP, RestoreToken: "restore-token",
	}
	for _, id := range []string{"selected", "other"} {
		if err := store.backend.Save(id, creds); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ForgetReceiverIdentity("selected"); err != nil {
		t.Fatal(err)
	}
	if err := store.ForgetReceiverIdentity("missing"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	selected := reloaded.Lookup("selected")
	if selected.ReceiverID != "" || len(selected.ReceiverPublic) != 0 {
		t.Fatal("receiver pin was not removed from disk")
	}
	if selected.PairingID != creds.PairingID || !bytes.Equal(selected.Ed25519Seed, creds.Ed25519Seed) ||
		!bytes.Equal(selected.Ed25519Public, creds.Ed25519Public) ||
		selected.PairingProtocol != creds.PairingProtocol || selected.RestoreToken != creds.RestoreToken {
		t.Fatal("forgetting receiver pin changed other device state")
	}
	if other := reloaded.Lookup("other"); other.ReceiverID != creds.ReceiverID || !bytes.Equal(other.ReceiverPublic, creds.ReceiverPublic) {
		t.Fatal("another device's pin was changed")
	}
	if reloaded.Len() != 2 || creds.ReceiverID == "" || len(creds.ReceiverPublic) == 0 {
		t.Fatal("forget modified the original entry or created a missing device")
	}
}
