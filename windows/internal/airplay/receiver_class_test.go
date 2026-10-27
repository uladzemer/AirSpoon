package airplay

import "testing"

// infoFromProfile строит ответ /info так, как его отдаёт тестовый приёмник данного профиля.
func infoFromProfile(t *testing.T, p ReceiverProfile) *ReceiverInfo {
	t.Helper()
	spec, err := receiverProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	return &ReceiverInfo{Model: spec.model, Manufacturer: spec.manufacturer, SourceVersion: spec.sourceVersion, Features: spec.features}
}

func TestClassifyReceiverByProfile(t *testing.T) {
	cases := []struct {
		profile ReceiverProfile
		want    ReceiverClass
	}{
		{ReceiverProfileUxPlay, ReceiverClassUxPlay},
		{ReceiverProfileUxPlayAndroid, ReceiverClassUxPlay},
		{ReceiverProfileModern, ReceiverClassAirPlay2},  // Apple TV 4K
		{ReceiverProfileAppleTV3, ReceiverClassUxPlay},  // Apple TV 3: тот же стек 220.68, проверенный путь UxPlay с повтором при отказе SETUP
		{ReceiverProfileRoku, ReceiverClassLegacyOther}, // Roku: legacy-сопряжение, стек 377.x
		{ReceiverProfileLG, ReceiverClassLegacyOther},   // LG: HAP поверх legacy-признаков, стек 377.x
		// AirServer выдаёт признаки AirPlay 2 (как Apple TV 4K), а сопряжение у него
		// raw: универсальный путь ядра сам откатывается на raw при отказе HAP.
		{ReceiverProfileAirServer, ReceiverClassAirPlay2},
	}
	for _, c := range cases {
		info := infoFromProfile(t, c.profile)
		got, reason := ClassifyReceiver(info)
		if got != c.want {
			t.Errorf("профиль %s: класс %v, ожидали %v (признак: %s; PrefersLegacyPairing=%v srcvers=%s features=0x%x)",
				c.profile, got, c.want, reason, info.PrefersLegacyPairing(), info.SourceVersion, info.Features)
		}
		if reason == "" {
			t.Errorf("профиль %s: нет пояснения", c.profile)
		}
	}
}

// Настоящий UxPlay на Android (проверен владельцем): model AppleTV3,2, srcvers
// 220.68, features 0x4005a7ffef7 — должен получить профиль UxPlay.
func TestClassifyRealAndroidUxPlay(t *testing.T) {
	info := &ReceiverInfo{Name: "Android AirPlay", Model: "AppleTV3,2", SourceVersion: "220.68", Features: 0x4005a7ffef7, StatusFlags: 0x44}
	class, reason := ClassifyReceiver(info)
	if class != ReceiverClassUxPlay || !class.UsesUxPlayProfile() {
		t.Fatalf("реальный UxPlay: %v (%s)", class, reason)
	}
}

func TestClassifyReceiverEdgeCases(t *testing.T) {
	if c, _ := ClassifyReceiver(nil); c != ReceiverClassUxPlay {
		t.Error("без /info — проверенный профиль UxPlay")
	}
	// Версия стека не указана: профиль UxPlay (прежнее поведение по умолчанию).
	if c, _ := ClassifyReceiver(&ReceiverInfo{Features: 0x5a7ffef7}); c != ReceiverClassUxPlay {
		t.Errorf("без sourceVersion: %v", c)
	}
	if ReceiverClassAirPlay2.UsesUxPlayProfile() || ReceiverClassLegacyOther.UsesUxPlayProfile() {
		t.Error("профиль UxPlay нужен только классу UxPlay")
	}
}
