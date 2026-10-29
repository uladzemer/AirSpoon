package airplay

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
)

// ErrReceiverIdentityChanged must not trigger automatic fresh pairing: that
// would silently replace the pin with a key advertised by an untrusted peer.
var ErrReceiverIdentityChanged = errors.New("ключ или идентификатор приёмника изменился; выполните повторное сопряжение (--pair)")

func (c *AirPlayClient) checkReceiverIdentity(identifier string, key []byte) error {
	if len(c.PairKeys.ReceiverPublic) != 0 && !bytes.Equal(c.PairKeys.ReceiverPublic, key) ||
		c.PairKeys.ReceiverID != "" && c.PairKeys.ReceiverID != identifier {
		return ErrReceiverIdentityChanged
	}
	return nil
}

func (c *AirPlayClient) receiverVerificationKey(identifier string) (ed25519.PublicKey, error) {
	key := c.PairKeys.ReceiverPublic
	if len(key) == 0 && c.info != nil {
		// Migration of credentials without a receiver pin: accept /info once,
		// but remember it only after the peer proves possession in pair-verify.
		key = ed25519.PublicKey(c.info.PK)
	}
	if len(key) == 0 {
		return nil, nil
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("receiver Ed25519 public key not available (call GetInfo or pair again)")
	}
	if err := c.checkReceiverIdentity(identifier, key); err != nil {
		return nil, err
	}
	return key, nil
}

func (c *AirPlayClient) rememberReceiverIdentity(identifier string, key []byte) {
	c.PairKeys.ReceiverID = identifier
	c.PairKeys.ReceiverPublic = append(ed25519.PublicKey(nil), key...)
	c.PairKeys.receiverVerified = true
}

// A transient controller identity must be regenerated, but the receiver pin
// must survive the new pair-setup and every protocol fallback.
func (c *AirPlayClient) installPairingKeys(pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	keys := &PairKeys{Ed25519Public: pub, Ed25519Private: priv}
	if c.PairKeys != nil {
		keys.ReceiverID = c.PairKeys.ReceiverID
		keys.ReceiverPublic = append(ed25519.PublicKey(nil), c.PairKeys.ReceiverPublic...)
	}
	c.PairKeys = keys
}

func (c *AirPlayClient) RestoreReceiverIdentity(saved *SavedCredentials) error {
	if saved == nil {
		return nil
	}
	if len(saved.ReceiverPublic) != 0 && len(saved.ReceiverPublic) != ed25519.PublicKeySize {
		return fmt.Errorf("saved credentials contain invalid receiver public key; pair again")
	}
	if c.PairKeys == nil {
		c.PairKeys = &PairKeys{}
	}
	c.PairKeys.ReceiverID = saved.ReceiverID
	c.PairKeys.ReceiverPublic = append(ed25519.PublicKey(nil), saved.ReceiverPublic...)
	return nil
}
