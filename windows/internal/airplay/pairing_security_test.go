package airplay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

func TestHAPVerifyReceiverKeyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		pinned, infoKey, wrongKey    bool
		badSignature, badTag, badTLV bool
		wantError, wantPin, wantWarn bool
	}{
		{name: "no pin no info key", wantWarn: true},
		{name: "TOFU from info", infoKey: true, wantPin: true},
		{name: "info key mismatch", infoKey: true, wrongKey: true, wantError: true},
		{name: "info signature mismatch", infoKey: true, badSignature: true, wantError: true},
		{name: "pinned without info", pinned: true, wantPin: true},
		{name: "pinned with inconsistent info", pinned: true, infoKey: true, wrongKey: true, wantPin: true},
		{name: "pinned signature mismatch", pinned: true, badSignature: true, wantError: true},
		{name: "no key invalid tag", badTag: true, wantError: true},
		{name: "no key malformed identity", badTLV: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			oldWriter := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldWriter)
			state := newReceiverPairingTestState(t, "", newReceiverControllerStore())
			client := newReceiverPairingTestClient(t)
			client.PairKeys.Transient = true
			client.info = &ReceiverInfo{}
			if tc.infoKey {
				client.info.PK = append([]byte(nil), state.publicKey...)
				if tc.wrongKey {
					client.info.PK[0] ^= 1
				}
			}
			if tc.pinned {
				client.PairKeys.ReceiverID = state.identifier
				client.PairKeys.ReceiverPublic = append(ed25519.PublicKey(nil), state.publicKey...)
			}
			if err := state.controllers.remember(client.PairingID, client.PairKeys.Ed25519Public); err != nil {
				t.Fatal(err)
			}
			// Two verifies also check that the compatibility warning is logged once.
			for attempt := 0; attempt < 2; attempt++ {
				conn, _, closePair := runReceiverPairingServer(t, state, 0, 2, func(_ rtspTestRequest, response []byte) []byte {
					message := tlv8Decode(response)
					if !bytes.Equal(message[tlvState], []byte{2}) {
						return response
					}
					if tc.badTag {
						message[tlvEncryptedData][0] ^= 1
					} else if tc.badSignature || tc.badTLV {
						aead, err := chacha20poly1305.New(state.verify.verifyKey)
						if err != nil {
							t.Error(err)
							return response
						}
						plaintext, err := aead.Open(nil, receiverPairingNonce("PV-Msg02"), message[tlvEncryptedData], nil)
						if err != nil {
							t.Error(err)
							return response
						}
						if tc.badTLV {
							plaintext = append(plaintext, tlvIdentifier)
						} else {
							identity := tlv8Decode(plaintext)
							identity[tlvSignature][0] ^= 1
							plaintext = tlv8Encode(identity)
						}
						message[tlvEncryptedData] = aead.Seal(nil, receiverPairingNonce("PV-Msg02"), plaintext, nil)
					}
					return tlv8Encode(message)
				})
				client.conn, client.encrypted = conn, false
				err := client.PairVerify(context.Background())
				closePair()
				if (err != nil) != tc.wantError {
					t.Fatalf("verify error=%v, wantError=%v", err, tc.wantError)
				}
				if tc.pinned && tc.wantError && !errors.Is(err, ErrReceiverIdentityChanged) {
					t.Fatalf("pinned verification error=%v", err)
				}
				if !tc.wantError && (client.PairKeys.receiverVerified != tc.wantPin || !client.encrypted) {
					t.Fatalf("verified=%v encrypted=%v", client.PairKeys.receiverVerified, client.encrypted)
				}
				if !tc.pinned && !tc.wantPin && len(client.PairKeys.ReceiverPublic) != 0 {
					t.Fatal("unverified receiver was pinned")
				}
			}
			wantWarnings := 0
			if tc.wantWarn {
				wantWarnings = 1
			}
			if count := strings.Count(logs.String(), "подпись V2 не проверена"); count != wantWarnings {
				t.Fatalf("warnings=%d, want %d", count, wantWarnings)
			}
		})
	}
}

func TestPairingRejectsUnauthenticatedReceiver(t *testing.T) {
	for _, test := range []struct {
		name, path string
		state      byte
		field      byte
		mode       string
		want       string
	}{
		{"missing M4 proof", "/pair-setup", 4, tlvProof, "omit", "server proof"},
		{"wrong M4 proof", "/pair-setup", 4, tlvProof, "flip", "server proof"},
		{"empty M4 error", "/pair-setup", 4, tlvError, "empty", "TLV error length"},
		{"truncated M4", "/pair-setup", 4, 0, "truncate", "exceeds remaining"},
		{"missing M6 ciphertext", "/pair-setup", 6, tlvEncryptedData, "omit", "decrypt M6"},
		{"wrong M6 tag", "/pair-setup", 6, tlvEncryptedData, "flip", "decrypt M6"},
		{"wrong M6 signature", "/pair-setup", 6, tlvSignature, "identity", "signature verification"},
		{"missing M6 identifier", "/pair-setup", 6, tlvIdentifier, "identity", "signature verification"},
		{"wrong M6 key", "/pair-setup", 6, tlvPublicKey, "identity", "signature verification"},
		{"empty M6 error", "/pair-setup", 6, tlvError, "empty", "TLV error length"},
		{"missing V2 ciphertext", "/pair-verify", 2, tlvEncryptedData, "omit", "decrypt V2"},
		{"wrong V2 tag", "/pair-verify", 2, tlvEncryptedData, "flip", "decrypt V2"},
		{"wrong V2 signature", "/pair-verify", 2, tlvSignature, "identity", "signature verification"},
		{"wrong V2 identifier", "/pair-verify", 2, tlvIdentifier, "identity", "идентификатор"},
		{"empty V2 error", "/pair-verify", 2, tlvError, "empty", "TLV error length"},
		{"empty V4 error", "/pair-verify", 4, tlvError, "empty", "TLV error length"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newReceiverPairingTestState(t, "4827", newReceiverControllerStore())
			client := newReceiverPairingTestClient(t)
			var sharedKey []byte
			conn, _, closePair := runReceiverPairingServer(t, state, 3, 2, func(request rtspTestRequest, response []byte) []byte {
				message := tlv8Decode(response)
				if request.uri == "/pair-setup" && bytes.Equal(message[tlvState], []byte{4}) {
					sharedKey = append([]byte(nil), state.setup.sharedKey...)
				}
				if request.uri != test.path || !bytes.Equal(message[tlvState], []byte{test.state}) {
					return response
				}
				switch test.mode {
				case "omit":
					delete(message, test.field)
				case "flip":
					message[test.field][0] ^= 1
				case "empty":
					message[test.field] = nil
				case "truncate":
					return append(response, tlvSignature, 3, 1)
				case "identity":
					key := hkdfSHA512(sharedKey, []byte("Pair-Setup-Encrypt-Salt"), []byte("Pair-Setup-Encrypt-Info"), 32)
					label := "PS-Msg06"
					if test.path == "/pair-verify" {
						key, label = state.verify.verifyKey, "PV-Msg02"
					}
					aead, err := chacha20poly1305.New(key)
					if err != nil {
						t.Fatal(err)
					}
					plaintext, err := aead.Open(nil, receiverPairingNonce(label), message[tlvEncryptedData], nil)
					if err != nil {
						t.Fatal(err)
					}
					identity := tlv8Decode(plaintext)
					if test.field == tlvIdentifier {
						delete(identity, test.field)
					} else {
						identity[test.field][0] ^= 1
					}
					message[tlvEncryptedData] = aead.Seal(nil, receiverPairingNonce(label), tlv8Encode(identity), nil)
				}
				return tlv8Encode(message)
			})
			defer closePair()
			client.conn = conn
			err := client.pairSetup(context.Background(), "4827")
			if err == nil {
				err = client.PairVerify(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("pairing error = %v, want %q", err, test.want)
			}
			if client.encrypted {
				t.Fatal("unauthenticated peer enabled control encryption")
			}
		})
	}
}

func TestTLV8RejectsTruncatedMessages(t *testing.T) {
	valid := tlv8EncodeOrdered([]tlv8Item{{tlvPublicKey, bytes.Repeat([]byte{0x42}, 384)}})
	if !bytes.Equal(tlv8Decode(valid)[tlvPublicKey], bytes.Repeat([]byte{0x42}, 384)) {
		t.Fatal("fragmented key did not round trip")
	}
	for _, truncated := range [][]byte{{1}, {1, 3, 1}, append(valid, 1), append(valid, 1, 3, 1)} {
		if got := tlv8Decode(truncated); got != nil {
			t.Fatalf("accepted truncated TLV: %x", truncated)
		}
	}
	_, err := exchangePairSetupM1(context.Background(), func() ([]byte, error) { return []byte{tlvError, 0}, nil })
	if err == nil {
		t.Fatal("empty M2 error accepted")
	}
}

func TestReceiverKeyPinningAndLegacyTOFUMigration(t *testing.T) {
	for _, protocol := range []PairingProtocol{PairingProtocolRaw, PairingProtocolHAP} {
		t.Run(string(protocol), func(t *testing.T) {
			store, err := NewCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			first := newReceiverPairingTestClient(t)
			if err := store.SavePairing("device", first.PairingID, first.PairKeys.Ed25519Public, first.PairKeys.Ed25519Private, protocol); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 3; attempt++ {
				controllers := newReceiverControllerStore()
				state := newReceiverPairingTestState(t, "", controllers)
				if attempt == 2 {
					state.privateKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x23}, 32))
					state.publicKey = state.privateKey.Public().(ed25519.PublicKey)
				}
				client := newReceiverPairingTestClient(t)
				state.rawControllerKey = append(ed25519.PublicKey(nil), client.PairKeys.Ed25519Public...)
				client.info = &ReceiverInfo{DeviceID: "device", PK: append([]byte(nil), state.publicKey...)}
				if err := client.RestorePairingCredentials(store.Lookup("device")); err != nil {
					t.Fatal(err)
				}
				if err := controllers.remember(client.PairingID, client.PairKeys.Ed25519Public); err != nil {
					t.Fatal(err)
				}
				conn, _, closePair := runReceiverPairingServer(t, state, 0, 2)
				client.conn = conn
				err := client.PairVerify(context.Background())
				closePair()
				if attempt == 2 {
					// HAP has no LTPK in V2: a changed key fails the signature
					// against the pin; raw also rejects /info's replacement key.
					if err == nil {
						t.Fatal("replacement receiver key accepted")
					}
					if !errors.Is(err, ErrReceiverIdentityChanged) {
						t.Fatal(err)
					}
					if bytes.Equal(store.Lookup("device").ReceiverPublic, state.publicKey) {
						t.Fatal("persisted pin replaced")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SaveClientPairing("device", client); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(store.Lookup("device").ReceiverPublic, state.publicKey) {
					t.Fatal("TOFU receiver pin not saved")
				}
			}
		})
	}
}

func TestTransientPairingPreservesReceiverPinAcrossFreshSetup(t *testing.T) {
	for _, protocol := range []PairingProtocol{PairingProtocolRaw, PairingProtocolHAP} {
		t.Run(string(protocol), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			var saved *SavedCredentials
			for attempt := 0; attempt < 3; attempt++ {
				state := newReceiverPairingTestState(t, "", newReceiverControllerStore())
				if attempt == 2 {
					state.privateKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x24}, 32))
					state.publicKey = state.privateKey.Public().(ed25519.PublicKey)
				}
				client := newReceiverPairingTestClient(t)
				client.info = &ReceiverInfo{DeviceID: "device", Features: FeatureLegacyPairing}
				setupRequests := 1
				if protocol == PairingProtocolHAP {
					client.info.Features, setupRequests = testModernPairingFeatures, 3
				}
				if err := client.RestoreReceiverIdentity(saved); err != nil {
					t.Fatal(err)
				}
				conn, _, closePair := runReceiverPairingServer(t, state, setupRequests, 2)
				client.conn = conn
				err := client.pairTransient(context.Background())
				closePair()
				if attempt == 2 {
					if !errors.Is(err, ErrReceiverIdentityChanged) {
						t.Fatalf("replacement key: %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				store, err := NewCredentialStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SaveClientPairing("device", client); err != nil {
					t.Fatal(err)
				}
				reloaded, err := NewCredentialStore(path)
				if err != nil {
					t.Fatal(err)
				}
				saved = reloaded.Lookup("device")
				if saved.HasPairingCredentials() || !bytes.Equal(saved.ReceiverPublic, state.publicKey) {
					t.Fatal("transient receiver pin was lost or marked as reusable controller credentials")
				}
			}
		})
	}
}
