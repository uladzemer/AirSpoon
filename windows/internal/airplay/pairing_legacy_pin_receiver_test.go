package airplay

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

const legacyPINTestCode = "4827"

func waitReceiverVideoStats(server *ReceiverServer) ReceiverStats {
	deadline := time.Now().Add(time.Second)
	stats := server.Stats()
	for stats.VideoDecrypted == 0 && stats.VideoCryptoErrors == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		stats = server.Stats()
	}
	return stats
}

// Полный путь через PIN: pair-pin-start, три шага pair-setup-pin, raw
// pair-verify, FairPlay, SETUP и расшифровка видео на приёмнике. Профиль
// uxplay-android несёт features по умолчанию из исходников приёмника на Android
// (бит 27 включён): без режима UxPlay клиент выбрал бы ключ без смешивания.
func TestLegacyPINPairingEndToEnd(t *testing.T) {
	for _, profile := range []ReceiverProfile{ReceiverProfileUxPlay, ReceiverProfileUxPlayAndroid} {
		t.Run(string(profile), func(t *testing.T) {
			server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{
				Profile: profile, Auth: ReceiverAuthPIN, Code: legacyPINTestCode,
			})
			client.EnableUxPlayCompat()
			if err := client.StartPINDisplay(); err != nil {
				t.Fatalf("start PIN display: %v", err)
			}
			if err := client.Pair(ctx, legacyPINTestCode); err != nil {
				t.Fatalf("PIN pair: %v", err)
			}
			if got := client.PairingProtocol(); got != PairingProtocolRaw {
				t.Fatalf("протокол сопряжения = %v, ожидался raw", got)
			}
			if client.encrypted {
				t.Fatal("после PIN канал не должен шифроваться (raw)")
			}
			stats := server.Stats()
			if stats.PINStarts != 1 || stats.PairSetup != 3 || stats.PairVerify != 2 {
				t.Fatalf("статистика PIN-сопряжения = %+v (ожидалось 1 старт, 3 шага setup, 2 шага verify)", stats)
			}

			if err := client.FairPlaySetup(ctx); err != nil {
				t.Fatalf("FairPlay setup: %v", err)
			}
			session, err := client.SetupMirror(ctx, StreamConfig{NoAudio: true})
			if err != nil {
				t.Fatalf("setup mirror: %v", err)
			}
			frame := session.streamCipher(receiverTestAVCC([]byte{0x65, 0x80}))
			timestamp, timeline := session.frameTimeNow()
			if err := session.sendFrame(frame, true, timestamp, timeline); err != nil {
				t.Fatalf("send encrypted frame: %v", err)
			}
			stats = waitReceiverVideoStats(server)
			if stats.VideoDecrypted != 1 || stats.VideoCryptoErrors != 0 {
				t.Fatalf("видео: расшифровано=%d ошибок=%d, ожидался 1 кадр без ошибок", stats.VideoDecrypted, stats.VideoCryptoErrors)
			}
			if err := session.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("close mirror session: %v", err)
			}
		})
	}
}

// Этот тест фиксирует причину, по которой нужен режим UxPlay: по эвристике
// «бит 27 включён -> ключ без смешивания» (она верна для других raw-приёмников)
// клиент не смешал бы ключ, а UxPlay смешивает всегда, если pair-verify прошёл.
func TestLegacyPINWithoutUxPlayModeMismatchesKeyOnAndroidFeatures(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{
		Profile: ReceiverProfileUxPlayAndroid, Auth: ReceiverAuthPIN, Code: legacyPINTestCode,
	})
	if err := client.Pair(ctx, legacyPINTestCode); err != nil {
		t.Fatalf("PIN pair: %v", err)
	}
	if client.PairKeys.MixFairPlayKey {
		t.Fatal("без режима UxPlay при бите 27 ожидался ключ без смешивания (предпосылка теста)")
	}
	if err := client.FairPlaySetup(ctx); err != nil {
		t.Fatalf("FairPlay setup: %v", err)
	}
	// SETUP приёмника проверяет, что shk совпадает с ключом, смешанным по правилам
	// UxPlay, поэтому без режима UxPlay он отклоняется.
	if _, err := client.SetupMirror(ctx, StreamConfig{NoAudio: true}); err == nil {
		t.Fatal("SETUP с несмешанным ключом принят приёмником UxPlay")
	}
	_ = server
}

func TestLegacyPINWrongCodeIsRejectedNotMaskedByHAPFallback(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{
		Profile: ReceiverProfileUxPlay, Auth: ReceiverAuthPIN, Code: legacyPINTestCode,
	})
	client.EnableUxPlayCompat()
	err := client.Pair(ctx, "0000")
	if !errors.Is(err, ErrLegacyPINRejected) {
		t.Fatalf("неверный PIN: ошибка %v, ожидался ErrLegacyPINRejected", err)
	}
	if got := client.PairingProtocol(); got != PairingProtocolUnknown {
		t.Fatalf("после отказа протокол = %v, ожидался неизвестный", got)
	}
	stats := server.Stats()
	if stats.PairSetup != 2 || stats.PairVerify != 0 {
		t.Fatalf("статистика = %+v: ожидалось 2 шага setup (отказ на доказательстве) и ни одного verify", stats)
	}
}

// Приёмник «старого» поколения без /pair-setup-pin остаётся на HAP-пути.
func TestLegacyPINFallsBackToHAPWhenReceiverHasNoPairSetupPIN(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{
		Profile: ReceiverProfileAppleTV3, Auth: ReceiverAuthPIN, Code: legacyPINTestCode,
	})
	if !client.info.PrefersLegacyPairing() {
		t.Fatal("предпосылка: профиль AppleTV3 должен считаться legacy")
	}
	if err := client.StartPINDisplay(); err != nil {
		t.Fatalf("start PIN display: %v", err)
	}
	if err := client.Pair(ctx, legacyPINTestCode); err != nil {
		t.Fatalf("PIN pair (HAP fallback): %v", err)
	}
	if got := client.PairingProtocol(); got != PairingProtocolHAP {
		t.Fatalf("протокол = %v, ожидался HAP", got)
	}
	_ = server
}

func TestUxPlayModeRejectsEncryptedHAPControlChannel(t *testing.T) {
	_, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{Profile: ReceiverProfileModern, Auth: ReceiverAuthNone})
	client.EnableUxPlayCompat()
	if err := client.Pair(ctx, ""); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if !client.encrypted {
		t.Fatal("предпосылка: модернизированный приёмник должен зашифровать канал")
	}
	_, err := client.SetupMirror(ctx, StreamConfig{NoAudio: true})
	if err == nil || !strings.Contains(err.Error(), "UxPlay") {
		t.Fatalf("ожидалась ошибка режима UxPlay, получено %v", err)
	}
}
