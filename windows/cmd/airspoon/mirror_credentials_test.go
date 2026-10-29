package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"airspoon/internal/airplay"
)

type failingPairingBackend struct {
	saved *airplay.SavedCredentials
	saves int
}

func (b *failingPairingBackend) Lookup(string) *airplay.SavedCredentials {
	return b.saved
}

func (b *failingPairingBackend) SaveClientPairing(string, *airplay.AirPlayClient) error {
	b.saves++
	return errors.New("credentials locked")
}

func TestSaveMirrorPairingPolicy(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	pub := priv.Public().(ed25519.PublicKey)
	receiverKey := bytes.Repeat([]byte{2}, ed25519.PublicKeySize)
	for _, tc := range []struct {
		name                      string
		transient, pinned, newPIN bool
		change                    string
		wantError, wantWarning    bool
		wantSaves                 int
	}{
		{name: "unchanged persistent", pinned: true},
		{name: "unchanged PIN identity", pinned: true, newPIN: true},
		{name: "unchanged transient pin", transient: true, pinned: true, change: "controller"},
		{name: "transient without receiver key", transient: true, change: "no receiver key"},
		{name: "first TOFU pin", transient: true, wantError: true, wantSaves: 1},
		{name: "old credentials TOFU migration", wantError: true, wantSaves: 1},
		{name: "new PIN keys", pinned: true, newPIN: true, change: "controller", wantError: true, wantSaves: 1},
		{name: "new PIN keys without receiver key", newPIN: true, change: "new keys without receiver key", wantError: true, wantSaves: 1},
		{name: "optional paired identity update", pinned: true, change: "controller", wantWarning: true, wantSaves: 1},
		{name: "optional protocol migration", pinned: true, change: "protocol", wantWarning: true, wantSaves: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			backend := &failingPairingBackend{saved: &airplay.SavedCredentials{
				PairingID: "controller", Ed25519Public: pub, Ed25519Seed: priv.Seed(),
				PairingProtocol: airplay.PairingProtocolHAP, Transient: tc.transient,
			}}
			if tc.pinned {
				backend.saved.ReceiverID, backend.saved.ReceiverPublic = "receiver", receiverKey
			}
			client := airplay.NewAirPlayClient("localhost", 7000)
			if !tc.transient {
				if err := client.RestorePairingCredentials(backend.saved); err != nil {
					t.Fatal(err)
				}
			}
			client.PairingID = "controller"
			client.PairKeys = &airplay.PairKeys{Ed25519Public: pub, Ed25519Private: priv,
				ReceiverID: "receiver", ReceiverPublic: receiverKey, Transient: tc.transient}
			switch tc.change {
			case "controller":
				client.PairingID = "new controller"
			case "protocol":
				backend.saved.PairingProtocol = airplay.PairingProtocolUnknown
			case "no receiver key":
				client.PairKeys.ReceiverID, client.PairKeys.ReceiverPublic = "", nil
			case "new keys without receiver key":
				client.PairingID = "new controller"
				client.PairKeys.ReceiverID, client.PairKeys.ReceiverPublic = "", nil
			}
			err := saveMirrorPairing(backend, "tv", client, tc.newPIN)
			if (err != nil) != tc.wantError || backend.saves != tc.wantSaves {
				t.Fatalf("error=%v backend saves=%d", err, backend.saves)
			}
			warned := strings.Contains(logs.String(), "предупреждение: ключи сопряжения не сохранены")
			if warned != tc.wantWarning {
				t.Fatalf("warning=%v, log=%s", warned, logs.String())
			}
		})
	}
}
