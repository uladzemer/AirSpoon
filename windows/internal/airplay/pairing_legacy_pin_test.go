package airplay

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"
)

// Известные ответы посчитаны независимой реализацией на Python прямо по srp.c
// UxPlay (APPLE_VARIANT, rfc5054_compat=1): тот же вход -> те же A, B, K, M, H_AMK.
func TestLegacySRPKnownAnswer(t *testing.T) {
	const (
		user     = "02:11:22:33:44:55"
		pin      = "1234"
		saltHex  = "1112131415161718191a1b1c1d1e1f20"
		aHex     = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
		bHex     = "65666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f8081828384"
		wantA    = "630acdff5d334462d92a29e0b7fa6e20020f3333292f6d3a640f1c7a76ad9d317531c57979952e5736c88db118d060dc0539a812b9b0af3b4002380a9f28ae4a7c45a896542de05fbcf76a4e7e0739b9a55d5d6c7aba4f1e1b58729a79bc084d5ff513eaec33ce978f5bad87e579b5a95fc773198e22697b2eadab9eb94f84cdcf1fe94ff09f88d4ca46e968bba443ff71167571f19feb052869bd28d7dabf963b7fe399a1f70e7e08d00e1a3778ed1dddc3325dd09e05d31e774d1fd295c4abfbc613446232004d67cb03d6a034d2ce6ca0a544a0ff5b434b4b4267fa6c6d72acbbda2efc1ef1d1fe36d35382b089abe556862aec35b29d3d0cdf359a9cfed3"
		wantB    = "3c22051d49bdbe2e6f192c1707b25ced959457b4a37c47becc58fc2e2552a3d2705536c09afcb62c69fbe189acdbb82e05056f23cc2a97adee315eb6f304ddab3286ef699f11ffb37015ac19d8be0b42638b220cc7abc4c9364802d9fd48f0ae298953daa028c243539b86259ce5435aece4a60da70b7619ad24c3ee146257ab2348d40da008e1f70e201b129ef7021765e54f7ce635ba125d01320faa82aae58cf61364acbe19f0adcb7e5397926bb09aeb62ed14579884ac8db68e6d751b037227fb27821088214297f9142a58d6d098baa420450ad964117ad14e0a0fde13af8cf953865653380fbcbc72bee6487a272ce3d3f1f4ff1c4a205fe1031ad4c8"
		wantKey  = "e815250ec8482c723a00d4f66de0f51971299a4b99f8e5c56b21f422bb2a959653d46269a6a60d82"
		wantM    = "2d3eb1b2ab2fa14428b8c93312bc47c5557fca05"
		wantHAMK = "982b63ec2f216325e28fe6c15e17c4aa436823bd"
	)
	mustHex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	salt := mustHex(saltHex)

	// Сторона приёмника: v = g^x, B = (k*v + g^b) mod N.
	b := new(big.Int).SetBytes(mustHex(bHex))
	v := new(big.Int).Exp(legacySRPG, legacySRPX(salt, user, pin), legacySRPN)
	serverPublic := new(big.Int).Mul(legacySRPMultiplier(), v)
	serverPublic.Add(serverPublic, new(big.Int).Exp(legacySRPG, b, legacySRPN))
	serverPublic.Mod(serverPublic, legacySRPN)
	if got := hex.EncodeToString(serverPublic.Bytes()); got != wantB {
		t.Fatalf("B = %s\nwant %s", got, wantB)
	}

	// Сторона клиента с фиксированным закрытым ключом.
	client, err := newLegacySRPClient(user, pin, bytes.NewReader(mustHex(aHex)))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(client.A.Bytes()); got != wantA {
		t.Fatalf("A = %s\nwant %s", got, wantA)
	}
	m, err := client.processChallenge(salt, serverPublic.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(client.key); got != wantKey {
		t.Fatalf("K = %s\nwant %s", got, wantKey)
	}
	if got := hex.EncodeToString(m); got != wantM {
		t.Fatalf("M = %s\nwant %s", got, wantM)
	}
	hamk := legacySRPServerProof(client.A, m, client.key)
	if got := hex.EncodeToString(hamk); got != wantHAMK {
		t.Fatalf("H_AMK = %s\nwant %s", got, wantHAMK)
	}
	if !client.verifyServerProof(hamk) {
		t.Fatal("клиент не принял правильное доказательство приёмника")
	}
	bad := append([]byte(nil), hamk...)
	bad[0] ^= 1
	if client.verifyServerProof(bad) {
		t.Fatal("клиент принял искажённое доказательство приёмника")
	}
}

func TestLegacySRPClientRejectsDegenerateServerValue(t *testing.T) {
	client, err := newLegacySRPClient("02:00:00:00:00:01", "1234", bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	for _, zero := range [][]byte{{0}, legacySRPN.Bytes(), new(big.Int).Lsh(legacySRPN, 1).Bytes()} {
		if _, err := client.processChallenge(bytes.Repeat([]byte{1}, 16), zero); err == nil {
			t.Fatalf("значение B=%x принято", zero)
		}
	}
}

func TestLegacyPINRejectsLongUser(t *testing.T) {
	if _, err := newLegacySRPClient("a-user-name-that-is-longer-than-24", "1234", bytes.NewReader(nil)); err == nil {
		t.Fatal("слишком длинное имя пользователя принято")
	}
}
