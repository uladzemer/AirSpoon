package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/trayctl"
)

func seedReceiverKeyCredentials(t *testing.T, path, deviceID string) []byte {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	key := bytes.Repeat([]byte{0x23}, ed25519.PublicKeySize)
	data, err := json.Marshal(map[string]*airplay.SavedCredentials{
		deviceID: {
			PairingID: "sender", Ed25519Public: private.Public().(ed25519.PublicKey), Ed25519Seed: private.Seed(),
			ReceiverID: deviceID, ReceiverPublic: key, PairingProtocol: airplay.PairingProtocolRaw,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestTrayReceiverKeyConfirmation(t *testing.T) {
	for _, answer := range []string{"yes", "no", "cancel", "no dialog"} {
		t.Run(answer, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			oldKey := seedReceiverKeyCredentials(t, path, "selected")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runs, confirmations := 0, 0
			s := &mirrorStreamer{credFile: path}
			s.run = func(_ context.Context, o mirrorOptions) error {
				runs++
				if runs == 1 {
					return &receiverKeyMismatchError{name: "Гостиная", deviceID: "selected", err: airplay.ErrReceiverIdentityChanged}
				}
				store, err := airplay.NewCredentialStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if !o.forcePair || store.Lookup("selected").ReceiverID != "" || len(store.Lookup("selected").ReceiverPublic) != 0 {
					t.Fatal("retry did not forget the pin and request fresh pairing")
				}
				return nil
			}
			if answer != "no dialog" {
				s.confirmReceiverKeyChange = func(title, text string) bool {
					confirmations++
					if title != trayctl.AppName || !strings.Contains(text, "«Гостиная»") ||
						!strings.Contains(text, "кто-то в сети может выдавать себя за телевизор") ||
						!strings.Contains(text, "Подключиться и запомнить новый ключ?") {
						t.Fatalf("unexpected confirmation: %s: %s", title, text)
					}
					if answer == "cancel" {
						cancel()
					}
					return answer != "no"
				}
			}
			err := s.Stream(ctx, trayctl.Target{Device: trayctl.Device{Addr: "127.0.0.1:7000"}}, trayctl.Hooks{})
			if answer == "yes" {
				if err != nil || runs != 2 || confirmations != 1 {
					t.Fatalf("approved replacement: err=%v runs=%d confirmations=%d", err, runs, confirmations)
				}
				return
			}
			want := trayctl.ErrPINCanceled
			if answer == "cancel" {
				want = context.Canceled
			} else if answer == "no dialog" {
				want = airplay.ErrReceiverIdentityChanged
			}
			if !errors.Is(err, want) || runs != 1 {
				t.Fatalf("declined replacement: err=%v runs=%d", err, runs)
			}
			store, err := airplay.NewCredentialStore(path)
			if err != nil || !bytes.Equal(store.Lookup("selected").ReceiverPublic, oldKey) {
				t.Fatal("unapproved replacement changed the receiver pin", err)
			}
		})
	}
}

func TestTrayReceiverKeyReplacementPairsAndPinsAgain(t *testing.T) {
	withFakeVideo(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay})
	path := filepath.Join(t.TempDir(), "credentials.json")
	oldKey := seedReceiverKeyCredentials(t, path, testReceiverDeviceID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	confirmations := 0
	s := &mirrorStreamer{
		credFile: path, encoder: "auto", run: mirror,
		confirmReceiverKeyChange: func(_, _ string) bool { confirmations++; return true },
	}
	err := s.Stream(ctx, trayctl.Target{Device: trayctl.Device{Addr: server.Addr().String()}}, trayctl.Hooks{Streaming: cancel})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if confirmations != 1 || server.Stats().SetupRequests == 0 {
		t.Fatalf("replacement did not reconnect: confirmations=%d stats=%+v", confirmations, server.Stats())
	}
	store, err := airplay.NewCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Lookup(testReceiverDeviceID)
	if saved == nil || len(saved.ReceiverPublic) != ed25519.PublicKeySize || bytes.Equal(saved.ReceiverPublic, oldKey) || saved.ReceiverID != testReceiverDeviceID {
		t.Fatal("replacement receiver was not pinned")
	}
}
