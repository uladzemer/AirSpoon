package airplay

// Серверная сторона /pair-setup-pin тестового приёмника с профилем UxPlay.
// Написана как построчный перевод raop_handler_pairsetup_pin и
// srp_new_user / srp_validate_proof / srp_confirm_pair_setup из UxPlay
// (lib/raop_handlers.h, lib/pairing.c, lib/srp.c), независимо от клиентской
// части в pairing_legacy_pin.go, чтобы тест клиента не проверял сам себя.
//
// Лицензия GPL-3.0-or-later (см. LICENSE и NOTICE).

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"

	"howett.net/plist"
)

type receiverLegacyPINStage uint8

const (
	receiverLegacyPINIdle receiverLegacyPINStage = iota
	receiverLegacyPINChallenged
	receiverLegacyPINProven
)

// receiverLegacyPINState — srp_t и имя пользователя одного TCP-соединения.
type receiverLegacyPINState struct {
	stage receiverLegacyPINStage
	user  string
	salt  []byte
	v     *big.Int
	b     *big.Int
	B     *big.Int
	key   []byte
}

// receiverLegacyPINResult — ответ на один шаг. status 470 соответствует
// «Client Authentication Failure» UxPlay; пустое тело при 200 — «return» без ответа.
type receiverLegacyPINResult struct {
	status int
	body   []byte
}

func receiverLegacyPINFail() receiverLegacyPINResult { return receiverLegacyPINResult{status: 470} }

// handle разбирает один запрос /pair-setup-pin. serverKey — публичный Ed25519
// приёмника; accept вызывается с ключом клиента после успешного шага 3.
func (s *receiverLegacyPINState) handle(body []byte, pin string, random io.Reader,
	serverKey ed25519.PublicKey, accept func(ed25519.PublicKey)) receiverLegacyPINResult {
	var request map[string]any
	if len(body) == 0 {
		return receiverLegacyPINFail()
	}
	if _, err := decodeNetworkPlist(body, &request); err != nil {
		return receiverLegacyPINFail()
	}
	method, hasMethod := request["method"].(string)
	user, hasUser := request["user"].(string)
	clientPK, hasPK := request["pk"].([]byte)
	clientProof, hasProof := request["proof"].([]byte)
	epk, hasEPK := request["epk"].([]byte)
	authTag, hasTag := request["authTag"].([]byte)

	switch {
	case hasMethod && hasUser:
		return s.begin(method, user, pin, random)
	case hasPK && hasProof:
		return s.prove(clientPK, clientProof)
	case hasEPK && hasTag:
		return s.confirm(epk, authTag, serverKey, accept)
	}
	return receiverLegacyPINFail()
}

func (s *receiverLegacyPINState) begin(method, user, pin string, random io.Reader) receiverLegacyPINResult {
	// strncmp(method, "pin", strlen(method)): UxPlay сверяет только префикс; при
	// несовпадении просто выходит без ответа.
	if method == "" || len(method) > len("pin") || "pin"[:len(method)] != method {
		return receiverLegacyPINResult{status: 200}
	}
	if len(user) == 0 || len(user) > legacyPINUserMaxLen {
		return receiverLegacyPINFail()
	}
	*s = receiverLegacyPINState{user: user}

	// UxPlay берёт соль как число из 128 случайных бит и отвечает отказом, если
	// число короче 16 байт или проверочное значение короче 256 байт (len_s/len_v
	// != SRP_*_SIZE); здесь соль выбирается заново, пока обе длины сходятся.
	for attempt := 0; attempt < 64; attempt++ {
		salt := make([]byte, legacySRPSaltSize)
		if _, err := io.ReadFull(random, salt); err != nil {
			return receiverLegacyPINFail()
		}
		if salt[0] == 0 {
			continue
		}
		v := new(big.Int).Exp(legacySRPG, legacySRPX(salt, user, pin), legacySRPN)
		if len(v.Bytes()) != legacySRPNSize {
			continue
		}
		privateBytes := make([]byte, legacySRPPrivateSize)
		if _, err := io.ReadFull(random, privateBytes); err != nil {
			return receiverLegacyPINFail()
		}
		b := new(big.Int).SetBytes(privateBytes)
		// B = (k*v + g^b) mod N (rfc5054_compat=1).
		kv := new(big.Int).Mul(legacySRPMultiplier(), v)
		kv.Mod(kv, legacySRPN)
		serverPublic := new(big.Int).Exp(legacySRPG, b, legacySRPN)
		serverPublic.Add(serverPublic, kv)
		serverPublic.Mod(serverPublic, legacySRPN)

		s.stage = receiverLegacyPINChallenged
		s.salt, s.v, s.b, s.B = salt, v, b, serverPublic
		response, err := plist.Marshal(map[string]any{"pk": serverPublic.Bytes(), "salt": salt}, plist.BinaryFormat)
		if err != nil {
			return receiverLegacyPINFail()
		}
		return receiverLegacyPINResult{status: 200, body: response}
	}
	return receiverLegacyPINFail()
}

func (s *receiverLegacyPINState) prove(clientPK, clientProof []byte) receiverLegacyPINResult {
	if s.stage != receiverLegacyPINChallenged {
		return receiverLegacyPINFail()
	}
	// После любой неудачи srp-состояние освобождается (free(session->srp)).
	fail := func() receiverLegacyPINResult {
		*s = receiverLegacyPINState{}
		return receiverLegacyPINFail()
	}
	if len(clientProof) != legacyPINProofFieldSize {
		return fail()
	}
	a := new(big.Int).SetBytes(clientPK)
	if new(big.Int).Mod(a, legacySRPN).Sign() == 0 {
		return fail()
	}
	u, err := legacySRPHashPadded(a, s.B)
	if err != nil {
		return fail()
	}
	// S = (A * v^u)^b mod N.
	vu := new(big.Int).Exp(s.v, u, legacySRPN)
	vu.Mul(vu, a)
	secret := new(big.Int).Exp(vu, s.b, legacySRPN)
	key := legacySRPSessionKey(secret)
	wantM := legacySRPClientProof(s.user, s.salt, a, s.B, key)
	if subtle.ConstantTimeCompare(wantM, clientProof[:len(wantM)]) != 1 {
		return fail()
	}
	s.key = key
	s.stage = receiverLegacyPINProven
	response, err := plist.Marshal(map[string]any{"proof": legacySRPServerProof(a, wantM, key)}, plist.BinaryFormat)
	if err != nil {
		return fail()
	}
	return receiverLegacyPINResult{status: 200, body: response}
}

func (s *receiverLegacyPINState) confirm(epk, authTag []byte, serverKey ed25519.PublicKey,
	accept func(ed25519.PublicKey)) receiverLegacyPINResult {
	if s.stage != receiverLegacyPINProven {
		return receiverLegacyPINFail()
	}
	key := s.key
	*s = receiverLegacyPINState{} // «SRP6a data is no longer needed»
	if len(epk) != ed25519.PublicKeySize || len(authTag) != 16 {
		return receiverLegacyPINFail()
	}
	aesKey, aesIV := legacyPINAESKeys(key)
	aead, err := legacyPINGCM(aesKey)
	if err != nil {
		return receiverLegacyPINFail()
	}
	aesIV[15]++
	clientKey, err := aead.Open(nil, aesIV, append(append([]byte(nil), epk...), authTag...), nil)
	if err != nil {
		return receiverLegacyPINFail()
	}
	accept(ed25519.PublicKey(clientKey))

	aesIV[15]++ // «previously undocumented additional nonce»
	sealed := aead.Seal(nil, aesIV, serverKey, nil)
	response, err := plist.Marshal(map[string]any{
		"epk":     sealed[:ed25519.PublicKeySize],
		"authTag": sealed[ed25519.PublicKeySize:],
	}, plist.BinaryFormat)
	if err != nil {
		return receiverLegacyPINFail()
	}
	return receiverLegacyPINResult{status: 200, body: response}
}

func legacyPINControllerID(clientKey ed25519.PublicKey) string {
	return "legacy-pin:" + hex.EncodeToString(clientKey)
}

// acceptLegacyPINController запоминает ключ клиента, прошедшего PIN, как
// контроллера для следующего raw pair-verify на этом соединении.
func (s *receiverPairingState) acceptLegacyPINController(clientKey ed25519.PublicKey) error {
	if len(clientKey) != ed25519.PublicKeySize {
		return fmt.Errorf("controller key has length %d, want %d", len(clientKey), ed25519.PublicKeySize)
	}
	s.resetVerification()
	s.setup = nil
	clear(s.transientControllers)
	s.rawControllerKey = append(ed25519.PublicKey(nil), clientKey...)
	s.rawPINAuthenticated = true
	// Как register_client/check_register в UxPlay: ключ, прошедший PIN, помнится
	// на приёмнике, и вернувшийся клиент проходит pair-verify без нового PIN.
	return s.controllers.remember(legacyPINControllerID(clientKey), clientKey)
}
