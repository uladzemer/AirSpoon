package airplay

// Сопряжение по PIN в «старом» протоколе AirPlay (POST /pair-pin-start,
// POST /pair-setup-pin), которое использует UxPlay и, значит, приёмник на
// Android TV. Это не HAP/TLV8: тела запросов — бинарные plist, SRP-6a на группе
// 2048 бит с SHA-1, а ключ контроллера передаётся под AES-128-GCM.
//
// Реализовано по исходникам приёмника: UxPlay lib/raop_handlers.h
// (raop_handler_pairsetup_pin), lib/pairing.c (srp_new_user, srp_validate_proof,
// srp_confirm_pair_setup) и lib/srp.c (вариант APPLE_VARIANT, rfc5054_compat=1).
// После успешного обмена выполняется обычный raw pair-verify (rawPairVerify):
// канал остаётся открытым, ключ Ed25519 приёмника берётся из последнего шага.
//
// Лицензия GPL-3.0-or-later (см. LICENSE и NOTICE).

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"math/big"

	"howett.net/plist"
)

const (
	legacySRPSaltSize       = 16 // SRP_SALT_SIZE в UxPlay
	legacySRPPrivateSize    = 32 // SRP_PRIVATE_KEY_SIZE
	legacySRPSessionKeySize = 40 // SRP_SESSION_KEY_SIZE: два SHA-1 по 20 байт
	legacySRPNSize          = 256
	// UxPlay требует, чтобы поле proof во втором шаге было ровно 64 байта
	// (sizeof(proof) в raop_handlers.h), а сверяет только первые 20 (SHA-1).
	legacyPINProofFieldSize = 64
	legacyPINServerProofLen = sha1.Size
	legacyPINUserMaxLen     = 24 // SRP_USERNAME_SIZE
	legacyPINGCMNonceSize   = 16
)

// ErrLegacyPINUnsupported означает, что приёмник не реализует /pair-setup-pin
// (HTTP-статус «нет такого пути»): вызывающий может перейти к HAP-сопряжению.
var ErrLegacyPINUnsupported = errors.New("receiver does not implement /pair-setup-pin")

// ErrLegacyPINRejected означает неверный PIN или отказ приёмника на шаге
// доказательства (UxPlay отвечает 470 «Client Authentication Failure»).
var ErrLegacyPINRejected = errors.New("receiver rejected the PIN")

var (
	legacySRPN = mustLegacySRPModulus()
	legacySRPG = big.NewInt(2)
)

// mustLegacySRPModulus — группа 2048 бит из RFC 5054 (srp.c, раздел «2048»).
func mustLegacySRPModulus() *big.Int {
	const hex = "AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050A37329CBB4" +
		"A099ED8193E0757767A13DD52312AB4B03310DCD7F48A9DA04FD50E8083969EDB767B0CF60" +
		"95179A163AB3661A05FBD5FAAAE82918A9962F0B93B855F97993EC975EEAA80D740ADBF4FF" +
		"747359D041D5C33EA71D281E446B14773BCA97B43A23FB801676BD207A436C6481F1D2B907" +
		"8717461A5B9D32E688F87748544523B524B0D57D5EA77A2775D2ECFA032CFBDBF52FB37861" +
		"60279004E57AE6AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8E9DB" +
		"FBB694B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F9E4AFF73"
	n, ok := new(big.Int).SetString(hex, 16)
	if !ok || n.BitLen() != legacySRPNSize*8 {
		panic("legacy SRP modulus is malformed")
	}
	return n
}

func legacySRPHash(parts ...[]byte) []byte {
	h := sha1.New()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

// legacySRPPad дополняет число нулями слева до длины модуля (H_nn_rfc5054).
func legacySRPPad(n *big.Int) ([]byte, error) {
	if n.Sign() < 0 || n.BitLen() > legacySRPNSize*8 {
		return nil, errors.New("SRP value does not fit the group size")
	}
	return n.FillBytes(make([]byte, legacySRPNSize)), nil
}

// legacySRPHashPadded — H(pad(a) || pad(b)) как целое число.
func legacySRPHashPadded(a, b *big.Int) (*big.Int, error) {
	pa, err := legacySRPPad(a)
	if err != nil {
		return nil, err
	}
	pb, err := legacySRPPad(b)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(legacySRPHash(pa, pb)), nil
}

// legacySRPMultiplier — k = H(pad(N) || pad(g)).
func legacySRPMultiplier() *big.Int {
	k, err := legacySRPHashPadded(legacySRPN, legacySRPG)
	if err != nil {
		panic(err) // N и g всегда помещаются в группу
	}
	return k
}

// legacySRPX — x = H(salt || H(user ":" pin)); соль участвует как число без
// ведущих нулей (BN_bn2bin в srp.c), поэтому её нулевые старшие байты отбрасываются.
func legacySRPX(salt []byte, user, pin string) *big.Int {
	inner := legacySRPHash([]byte(user), []byte(":"), []byte(pin))
	saltNumber := new(big.Int).SetBytes(salt)
	return new(big.Int).SetBytes(legacySRPHash(saltNumber.Bytes(), inner))
}

// legacySRPSessionKey — K = SHA1(S || 00000000) || SHA1(S || 00000001) (hash_session_key).
func legacySRPSessionKey(secret *big.Int) []byte {
	s := secret.Bytes()
	key := legacySRPHash(s, []byte{0, 0, 0, 0})
	return append(key, legacySRPHash(s, []byte{0, 0, 0, 1})...)
}

// legacySRPClientProof — M = H(H(N) xor H(g) || H(user) || salt || A || B || K).
func legacySRPClientProof(user string, salt []byte, a, b *big.Int, key []byte) []byte {
	hashN := legacySRPHash(legacySRPN.Bytes())
	hashG := legacySRPHash(legacySRPG.Bytes())
	xor := make([]byte, len(hashN))
	for i := range xor {
		xor[i] = hashN[i] ^ hashG[i]
	}
	saltNumber := new(big.Int).SetBytes(salt)
	return legacySRPHash(xor, legacySRPHash([]byte(user)), saltNumber.Bytes(), a.Bytes(), b.Bytes(), key)
}

// legacySRPServerProof — H_AMK = H(A || M || K).
func legacySRPServerProof(a *big.Int, clientProof, key []byte) []byte {
	return legacySRPHash(a.Bytes(), clientProof, key)
}

// legacyPINAESKeys — ключ и IV GCM шага 3 (srp_confirm_pair_setup):
// SHA-512("Pair-Setup-AES-Key" || K)[:16] и SHA-512("Pair-Setup-AES-IV" || K)[:16].
func legacyPINAESKeys(sessionKey []byte) (key, iv []byte) {
	derive := func(label string) []byte {
		h := sha512.New()
		_, _ = h.Write([]byte(label))
		_, _ = h.Write(sessionKey)
		return h.Sum(nil)[:16]
	}
	return derive("Pair-Setup-AES-Key"), derive("Pair-Setup-AES-IV")
}

// legacyPINGCM возвращает AES-128-GCM с 16-байтовым nonce, как EVP_CTRL_GCM_SET_IVLEN=16.
func legacyPINGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, legacyPINGCMNonceSize)
}

// legacySRPClient — клиентская сторона SRP (srp_user_process_challenge).
type legacySRPClient struct {
	user string
	pin  string
	a    *big.Int
	A    *big.Int

	key []byte
	m   []byte
}

func newLegacySRPClient(user, pin string, random io.Reader) (*legacySRPClient, error) {
	if len(user) == 0 || len(user) > legacyPINUserMaxLen {
		return nil, fmt.Errorf("legacy PIN user must be 1..%d bytes, got %d", legacyPINUserMaxLen, len(user))
	}
	private := make([]byte, legacySRPPrivateSize)
	if _, err := io.ReadFull(random, private); err != nil {
		return nil, fmt.Errorf("generate SRP private key: %w", err)
	}
	a := new(big.Int).SetBytes(private)
	if a.Sign() == 0 {
		a.SetInt64(1)
	}
	return &legacySRPClient{
		user: user,
		pin:  pin,
		a:    a,
		A:    new(big.Int).Exp(legacySRPG, a, legacySRPN),
	}, nil
}

// processChallenge принимает соль и B приёмника и возвращает M (20 байт).
func (c *legacySRPClient) processChallenge(salt, serverPublic []byte) ([]byte, error) {
	b := new(big.Int).SetBytes(serverPublic)
	if new(big.Int).Mod(b, legacySRPN).Sign() == 0 {
		return nil, errors.New("SRP: server public value is zero modulo N")
	}
	u, err := legacySRPHashPadded(c.A, b)
	if err != nil {
		return nil, err
	}
	if u.Sign() == 0 {
		return nil, errors.New("SRP: scrambling parameter is zero")
	}
	x := legacySRPX(salt, c.user, c.pin)
	k := legacySRPMultiplier()
	// S = (B - k*g^x) ^ (a + u*x) mod N; Mod у big.Int неотрицателен, как BN_nnmod в OpenSSL.
	base := new(big.Int).Exp(legacySRPG, x, legacySRPN)
	base.Mul(base, k)
	base.Sub(b, base)
	base.Mod(base, legacySRPN)
	exponent := new(big.Int).Mul(u, x)
	exponent.Add(exponent, c.a)
	secret := new(big.Int).Exp(base, exponent, legacySRPN)

	c.key = legacySRPSessionKey(secret)
	c.m = legacySRPClientProof(c.user, salt, c.A, b, c.key)
	return c.m, nil
}

// verifyServerProof сверяет H_AMK приёмника с ожидаемым.
func (c *legacySRPClient) verifyServerProof(proof []byte) bool {
	if len(c.m) == 0 || len(proof) < legacyPINServerProofLen {
		return false
	}
	want := legacySRPServerProof(c.A, c.m, c.key)
	return subtle.ConstantTimeCompare(want, proof[:legacyPINServerProofLen]) == 1
}

func legacyPINPlist(fields map[string]any) ([]byte, error) {
	return plist.Marshal(fields, plist.BinaryFormat)
}

func legacyPINData(response []byte, fields ...string) (map[string][]byte, error) {
	if len(response) == 0 {
		return nil, errors.New("empty response")
	}
	var decoded map[string]any
	if _, err := decodeNetworkPlist(response, &decoded); err != nil {
		return nil, fmt.Errorf("decode plist: %w", err)
	}
	result := make(map[string][]byte, len(fields))
	for _, field := range fields {
		value, ok := decoded[field].([]byte)
		if !ok || len(value) == 0 {
			return nil, fmt.Errorf("response has no %q data", field)
		}
		result[field] = value
	}
	return result, nil
}

// legacyPINRequest отправляет один шаг /pair-setup-pin. Статусы «пути нет»
// превращаются в ErrLegacyPINUnsupported только на первом шаге (probe), 470 — в
// ErrLegacyPINRejected.
func (c *AirPlayClient) legacyPINRequest(fields map[string]any, probe bool) ([]byte, error) {
	body, err := legacyPINPlist(fields)
	if err != nil {
		return nil, err
	}
	response, err := c.httpRequest("POST", "/pair-setup-pin", "application/x-apple-binary-plist", body, c.pairHeaders())
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) {
			switch {
			case statusErr.StatusCode == 470:
				return nil, fmt.Errorf("%w (HTTP 470)", ErrLegacyPINRejected)
			case probe && isUnsupportedHAPPairSetup(err):
				return nil, fmt.Errorf("%w (HTTP %d)", ErrLegacyPINUnsupported, statusErr.StatusCode)
			}
		}
		return nil, err
	}
	return response, nil
}

// performLegacyPINSetupAndVerify выполняет три шага /pair-setup-pin и затем
// raw pair-verify. Ключи Ed25519 клиента уже лежат в c.PairKeys.
func (c *AirPlayClient) performLegacyPINSetupAndVerify(ctx context.Context, pin string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.PairKeys == nil || len(c.PairKeys.Ed25519Public) != ed25519.PublicKeySize {
		return errors.New("legacy PIN pairing needs an Ed25519 identity")
	}
	user := uuidToMAC(c.sessionID)
	srp, err := newLegacySRPClient(user, pin, rand.Reader)
	if err != nil {
		return err
	}

	// Шаг 1: метод и имя пользователя -> соль и B.
	dbg("[PAIR-PIN] step 1: method=pin user=%s", user)
	resp, err := c.legacyPINRequest(map[string]any{"method": "pin", "user": user}, true)
	if err != nil {
		return err
	}
	step1, err := legacyPINData(resp, "pk", "salt")
	if err != nil {
		return fmt.Errorf("pair-setup-pin step 1: %w", err)
	}

	m, err := srp.processChallenge(step1["salt"], step1["pk"])
	if err != nil {
		return fmt.Errorf("pair-setup-pin: %w", err)
	}

	// Шаг 2: A и доказательство знания PIN.
	proof := make([]byte, legacyPINProofFieldSize)
	copy(proof, m)
	dbg("[PAIR-PIN] step 2: sending A (%d bytes) and proof", len(srp.A.Bytes()))
	resp, err = c.legacyPINRequest(map[string]any{"pk": srp.A.Bytes(), "proof": proof}, false)
	if err != nil {
		return err
	}
	step2, err := legacyPINData(resp, "proof")
	if err != nil {
		return fmt.Errorf("pair-setup-pin step 2: %w", err)
	}
	if !srp.verifyServerProof(step2["proof"]) {
		return fmt.Errorf("pair-setup-pin: receiver proof does not match (%w)", ErrLegacyPINRejected)
	}

	// Шаг 3: наш Ed25519 под AES-GCM; в ответ — Ed25519 приёмника.
	key, iv := legacyPINAESKeys(srp.key)
	aead, err := legacyPINGCM(key)
	if err != nil {
		return fmt.Errorf("pair-setup-pin cipher: %w", err)
	}
	iv[15]++
	sealed := aead.Seal(nil, iv, c.PairKeys.Ed25519Public, nil)
	epk, tag := sealed[:ed25519.PublicKeySize], sealed[ed25519.PublicKeySize:]
	dbg("[PAIR-PIN] step 3: sending encrypted Ed25519 identity")
	resp, err = c.legacyPINRequest(map[string]any{"epk": epk, "authTag": tag}, false)
	if err != nil {
		return err
	}
	step3, err := legacyPINData(resp, "epk", "authTag")
	if err != nil {
		return fmt.Errorf("pair-setup-pin step 3: %w", err)
	}
	if len(step3["epk"]) != ed25519.PublicKeySize || len(step3["authTag"]) != aead.Overhead() {
		return errors.New("pair-setup-pin step 3: unexpected epk/authTag length")
	}
	iv[15]++ // «ещё один nonce»: приёмник шифрует ответ со следующим значением IV
	serverKey, err := aead.Open(nil, iv, append(append([]byte(nil), step3["epk"]...), step3["authTag"]...), nil)
	if err != nil {
		return fmt.Errorf("pair-setup-pin: receiver identity failed authentication: %w", ErrLegacyPINRejected)
	}

	if c.info == nil {
		c.info = &ReceiverInfo{}
	}
	c.info.PK = serverKey
	dbg("[PAIR-PIN] pair-setup-pin complete, receiver Ed25519 %02x...; starting raw pair-verify", serverKey[:8])
	if err := c.rawPairVerify(ctx); err != nil {
		return fmt.Errorf("raw pair-verify: %w", err)
	}
	return nil
}

// PairWithLegacyPIN сопрягается только через /pair-setup-pin приёмника UxPlay,
// без отката на HAP. Нужен, когда приёмник не сообщает о PIN в /info (UxPlay
// всегда отдаёт statusFlags=0x44), а PIN известен пользователю.
func (c *AirPlayClient) PairWithLegacyPIN(ctx context.Context, pin string) error {
	c.pairingProtocol = pairingProtocolUnknown
	c.pairType = pairingTypeLegacy
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519: %w", err)
	}
	c.installPairingKeys(pub, priv)
	return c.performLegacyPINSetupAndVerify(ctx, pin)
}
