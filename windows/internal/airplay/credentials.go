package airplay

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SavedCredentials holds the persistent pairing credentials and optional
// screencast restore state for a single device.
type SavedCredentials struct {
	PairingID       string          `json:"pairing_id"`
	Ed25519Public   []byte          `json:"ed25519_public"`
	Ed25519Seed     []byte          `json:"ed25519_seed,omitempty"` // 32-byte seed (private key is derived from this)
	ReceiverID      string          `json:"receiver_id,omitempty"`
	ReceiverPublic  []byte          `json:"receiver_public,omitempty"`
	Transient       bool            `json:"transient,omitempty"`
	PairingProtocol PairingProtocol `json:"pairing_protocol,omitempty"`
	RestoreToken    string          `json:"restore_token,omitempty"`
}

// PairingProtocol is the pair-verify wire protocol negotiated when credentials
// were created. It must be restored before reusing the keys: HAP pair-verify
// enables encrypted control framing, while raw AirPlay pair-verify does not.
type PairingProtocol string

const (
	PairingProtocolUnknown PairingProtocol = ""
	PairingProtocolHAP     PairingProtocol = "hap"
	PairingProtocolRaw     PairingProtocol = "raw"
)

// DefaultCredentialsPath returns ~/.config/open-airplay/credentials.json.
func DefaultCredentialsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "open-airplay", "credentials.json")
}

// Ed25519Keys reconstructs the key pair from saved credentials.
func (c *SavedCredentials) Ed25519Keys() (ed25519.PublicKey, ed25519.PrivateKey) {
	if len(c.Ed25519Seed) != ed25519.SeedSize {
		return nil, nil
	}
	priv := ed25519.NewKeyFromSeed(c.Ed25519Seed)
	return ed25519.PublicKey(c.Ed25519Public), priv
}

// HasPairingCredentials reports whether the saved entry contains a usable
// AirPlay pairing identity.
func (c *SavedCredentials) HasPairingCredentials() bool {
	return c != nil && !c.Transient && c.PairingID != "" &&
		len(c.Ed25519Public) == ed25519.PublicKeySize &&
		len(c.Ed25519Seed) == ed25519.SeedSize
}

// RestorePairingCredentials installs a saved identity and its negotiated wire
// protocol on the client. Call this before PairVerify.
func (c *AirPlayClient) RestorePairingCredentials(saved *SavedCredentials) error {
	if saved == nil || !saved.HasPairingCredentials() {
		return fmt.Errorf("saved credentials contain no usable pairing identity")
	}
	if len(saved.ReceiverPublic) != 0 && len(saved.ReceiverPublic) != ed25519.PublicKeySize {
		return fmt.Errorf("saved credentials contain invalid receiver public key; pair again")
	}
	protocol := saved.PairingProtocol
	if protocol == PairingProtocolUnknown {
		// Old credential files predate explicit protocol persistence. Preserve the
		// previous capability-driven behavior: legacy pairing profiles used raw
		// pair-verify, while CoreUtils/HAP profiles used encrypted verification.
		protocol = PairingProtocolHAP
		if c.info != nil && c.info.PrefersLegacyPairing() {
			protocol = PairingProtocolRaw
		}
	}
	switch protocol {
	case PairingProtocolHAP:
		c.pairingProtocol = pairingProtocolHAP
	case PairingProtocolRaw:
		c.pairingProtocol = pairingProtocolRaw
	default:
		return fmt.Errorf("saved credentials contain unknown pairing protocol %q", protocol)
	}

	pub, priv := saved.Ed25519Keys()
	c.PairingID = saved.PairingID
	c.PairKeys = &PairKeys{
		Ed25519Public:  append(ed25519.PublicKey(nil), pub...),
		Ed25519Private: append(ed25519.PrivateKey(nil), priv...),
		ReceiverID:     saved.ReceiverID,
		ReceiverPublic: append(ed25519.PublicKey(nil), saved.ReceiverPublic...),
	}
	return nil
}

// CredentialBackend is the storage interface for pairing credentials.
type CredentialBackend interface {
	Lookup(deviceID string) (*SavedCredentials, error)
	Save(deviceID string, creds *SavedCredentials) error
}

// CredentialStore manages per-device pairing credentials using a pluggable backend.
type CredentialStore struct {
	mu      sync.Mutex
	backend CredentialBackend
}

// NewCredentialStore creates a credential store backed by a JSON file at path.
func NewCredentialStore(path string) (*CredentialStore, error) {
	fb, err := newFileBackend(path)
	if err != nil {
		return nil, err
	}
	return &CredentialStore{backend: fb}, nil
}

// NewCredentialStoreWithBackend creates a credential store with the given backend.
func NewCredentialStoreWithBackend(b CredentialBackend) *CredentialStore {
	return &CredentialStore{backend: b}
}

// Lookup returns saved credentials for a device, or nil if not found.
func (cs *CredentialStore) Lookup(deviceID string) *SavedCredentials {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	creds, err := cs.backend.Lookup(deviceID)
	if err != nil {
		return nil
	}
	return creds
}

// Len returns the number of stored credential entries.
// Only supported by the file backend; returns 0 for other backends.
func (cs *CredentialStore) Len() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if fb, ok := cs.backend.(*fileBackend); ok {
		return len(fb.devices)
	}
	return 0
}

// Save stores credentials for a device.
func (cs *CredentialStore) Save(deviceID string, pairingID string, pub ed25519.PublicKey, priv ed25519.PrivateKey) error {
	return cs.savePairing(deviceID, pairingID, pub, priv, PairingProtocolUnknown)
}

// SavePairing stores credentials together with the protocol which actually
// completed. New production call sites should use this instead of Save.
func (cs *CredentialStore) SavePairing(deviceID string, pairingID string, pub ed25519.PublicKey, priv ed25519.PrivateKey, protocol PairingProtocol) error {
	if protocol != PairingProtocolHAP && protocol != PairingProtocolRaw {
		return fmt.Errorf("cannot save unknown pairing protocol %q", protocol)
	}
	return cs.savePairing(deviceID, pairingID, pub, priv, protocol)
}

func (cs *CredentialStore) savePairing(deviceID string, pairingID string, pub ed25519.PublicKey, priv ed25519.PrivateKey, protocol PairingProtocol) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.savePairingLocked(deviceID, pairingID, pub, priv, protocol, nil)
}

// SaveClientPairing persists the receiver pin only after authenticated pairing
// or verification. This also completes TOFU migration after a saved-key verify.
func (cs *CredentialStore) SaveClientPairing(deviceID string, client *AirPlayClient) error {
	if client.PairKeys == nil || !client.PairKeys.receiverVerified || client.PairingProtocol() == PairingProtocolUnknown {
		return fmt.Errorf("cannot save an unverified receiver identity")
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	keys := client.PairKeys
	return cs.savePairingLocked(deviceID, client.PairingID, keys.Ed25519Public, keys.Ed25519Private, client.PairingProtocol(), keys)
}

// ForgetReceiverIdentity allows an explicitly approved replacement receiver to
// be paired again, preserving the controller identity and other device state.
func (cs *CredentialStore) ForgetReceiverIdentity(deviceID string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	creds, err := cs.backend.Lookup(deviceID)
	if err != nil || creds == nil {
		return err
	}
	copy := *creds
	copy.ReceiverID, copy.ReceiverPublic = "", nil
	return cs.backend.Save(deviceID, &copy)
}

func (cs *CredentialStore) savePairingLocked(deviceID string, pairingID string, pub ed25519.PublicKey, priv ed25519.PrivateKey, protocol PairingProtocol, keys *PairKeys) error {
	creds, err := cs.backend.Lookup(deviceID)
	if err != nil {
		return err
	}
	if creds == nil {
		creds = &SavedCredentials{}
	}
	copy := *creds
	creds = &copy
	creds.PairingID = pairingID
	creds.Ed25519Public = append([]byte(nil), pub...)
	creds.Ed25519Seed = append([]byte(nil), priv.Seed()...)
	if protocol != PairingProtocolUnknown {
		creds.PairingProtocol = protocol
	}
	if keys != nil {
		creds.ReceiverID = keys.ReceiverID
		creds.ReceiverPublic = append([]byte(nil), keys.ReceiverPublic...)
		creds.Transient = keys.Transient
	}
	return cs.backend.Save(deviceID, creds)
}

// SaveRestoreToken stores a Wayland screencast restore token for a device.
func (cs *CredentialStore) SaveRestoreToken(deviceID, restoreToken string) error {
	if restoreToken == "" {
		return nil
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	creds, err := cs.backend.Lookup(deviceID)
	if err != nil {
		return err
	}
	if creds == nil {
		creds = &SavedCredentials{}
	}
	copy := *creds
	creds = &copy
	creds.RestoreToken = restoreToken
	return cs.backend.Save(deviceID, creds)
}

// fileBackend stores credentials as a JSON file on disk.
type fileBackend struct {
	path    string
	devices map[string]*SavedCredentials
}

// Only the file backend needs an envelope; keyring backends already protect
// their contents. Keep the runtime credentials decrypted for existing callers.
type fileCredentials struct {
	SavedCredentials
	ProtectedSecrets []byte `json:"protected_secrets,omitempty"`
}

type credentialSecrets struct {
	Seed         []byte `json:"seed,omitempty"`
	RestoreToken string `json:"restore_token,omitempty"`
}

func newFileBackend(path string) (*fileBackend, error) {
	fb := &fileBackend{
		path:    path,
		devices: make(map[string]*SavedCredentials),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fb, nil
		}
		return nil, fmt.Errorf("read credential store: %w", err)
	}
	var stored map[string]*fileCredentials
	err = json.Unmarshal(data, &stored)
	if err != nil || stored == nil {
		return fb.recoverCorruptFile()
	}
	for _, entry := range stored {
		if entry == nil {
			return fb.recoverCorruptFile()
		}
	}
	migrate := false
	for id, entry := range stored {
		if len(entry.ProtectedSecrets) > 0 {
			plaintext, err := unprotectCredentialSecrets(entry.ProtectedSecrets)
			if err != nil {
				if protectCredentials {
					return fb.recoverCorruptFile()
				}
				return nil, fmt.Errorf("decrypt credential store: %w", err)
			}
			var secrets credentialSecrets
			if err := json.Unmarshal(plaintext, &secrets); err != nil {
				return fb.recoverCorruptFile()
			}
			entry.Ed25519Seed, entry.RestoreToken = secrets.Seed, secrets.RestoreToken
		} else if protectCredentials && (len(entry.Ed25519Seed) > 0 || entry.RestoreToken != "") {
			migrate = true
		}
		fb.devices[id] = &entry.SavedCredentials
	}
	if migrate {
		if err := fb.persist(); err != nil {
			return nil, fmt.Errorf("protect legacy credential store: %w", err)
		}
	}
	return fb, nil
}

func (fb *fileBackend) recoverCorruptFile() (*fileBackend, error) {
	if err := os.Rename(fb.path, fb.path+".bad"); err != nil {
		return nil, fmt.Errorf("move corrupt credential store aside: %w", err)
	}
	fb.devices = make(map[string]*SavedCredentials)
	return fb, nil
}

func (fb *fileBackend) Lookup(deviceID string) (*SavedCredentials, error) {
	return fb.devices[deviceID], nil
}

func (fb *fileBackend) Save(deviceID string, creds *SavedCredentials) error {
	old, existed := fb.devices[deviceID]
	fb.devices[deviceID] = creds
	if err := fb.persist(); err != nil {
		if existed {
			fb.devices[deviceID] = old
		} else {
			delete(fb.devices, deviceID)
		}
		return err
	}
	return nil
}

func (fb *fileBackend) persist() error {
	dir := filepath.Dir(fb.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}
	stored := make(map[string]*fileCredentials, len(fb.devices))
	for id, creds := range fb.devices {
		entry := &fileCredentials{SavedCredentials: *creds}
		if protectCredentials && (len(creds.Ed25519Seed) > 0 || creds.RestoreToken != "") {
			secrets, err := json.Marshal(credentialSecrets{Seed: creds.Ed25519Seed, RestoreToken: creds.RestoreToken})
			if err != nil {
				return err
			}
			entry.ProtectedSecrets, err = protectCredentialSecrets(secrets)
			if err != nil {
				return fmt.Errorf("protect credential store: %w", err)
			}
			entry.Ed25519Seed, entry.RestoreToken = nil, ""
		}
		stored[id] = entry
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credential store: %w", err)
	}
	file, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create temporary credential store: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write credential store: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync credential store: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close credential store: %w", err)
	}
	if err := os.Rename(file.Name(), fb.path); err != nil {
		return fmt.Errorf("replace credential store: %w", err)
	}
	return nil
}
