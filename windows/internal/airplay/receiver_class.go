package airplay

import "fmt"

// Автовыбор профиля совместимости по ответу приёмника на GET /info.
//
// Приёмники AirPlay отвечают по-разному, и клиенту нужно одно из трёх поведений:
//
//   - ReceiverClassUxPlay — UxPlay и AirSpoon Receiver (на его основе): legacy
//     raw-сопряжение (pair-setup/pair-verify без шифрования канала, PIN через
//     /pair-setup-pin), время только NTP, звук AES-CBC без RFC 2198, ключ
//     FairPlay смешивается с секретом pair-verify. Это поведение проверено на
//     настоящем приёмнике владельца и включается EnableUxPlayCompat.
//   - ReceiverClassAirPlay2 — Apple TV (4K, HD), HomePod и телевизоры с настоящим
//     AirPlay 2: HAP/транзиентное сопряжение с шифрованием канала (PIN на экране
//     телевизора — 4 цифры), PTP при поддержке. Ядро (из doubletake, проверено
//     авторами на Apple TV 3/4K, Roku, Samsung) само выбирает всё по features,
//     профиль UxPlay здесь вреден: он запрещает канал HAP.
//   - ReceiverClassLegacyOther — приёмники с legacy-сопряжением, но современным
//     стеком (Roku, LG, AirServer, Samsung): их проверенный путь — тот же
//     универсальный, что и у AirPlay 2, без ограничений UxPlay.
//
// Различаем по двум признакам, а не по «отпечатку» модели: UxPlay намеренно
// выдаёт себя за Apple TV 3 (model AppleTV3,2), так что по модели его не узнать.
//
//  1. Способ сопряжения (PrefersLegacyPairing, features 26/27/38/43/46/48/51): у
//     Apple TV с AirPlay 2 включены биты CoreUtils (38/43/46/48) без «чужих» 26/51.
//  2. Версия стека sourceVersion: у UxPlay это «220.68» (копия Apple TV 3), у
//     современных реализаций (Roku 377.x, LG 377.x, AirServer 375.x) — 350 и выше.
//     Порог 300 лежит между поколениями AirPlay 1 (200-ые) и AirPlay 2 (350+).

// ReceiverClass — тип приёмника для выбора профиля.
type ReceiverClass int

const (
	ReceiverClassUxPlay ReceiverClass = iota
	ReceiverClassAirPlay2
	ReceiverClassLegacyOther
)

func (c ReceiverClass) String() string {
	switch c {
	case ReceiverClassUxPlay:
		return "UxPlay / AirSpoon Receiver (legacy raw-сопряжение)"
	case ReceiverClassAirPlay2:
		return "Apple TV / AirPlay 2 (HAP, транзиентное сопряжение)"
	case ReceiverClassLegacyOther:
		return "телевизор или приставка с legacy-сопряжением и современным стеком (Roku, LG, Samsung, AirServer)"
	}
	return "неизвестный"
}

// UsesUxPlayProfile сообщает, нужно ли ограничить клиента возможностями UxPlay.
func (c ReceiverClass) UsesUxPlayProfile() bool { return c == ReceiverClassUxPlay }

// minimumModernSourceMajor — с этой основной версии sourceVersion считается
// реализацией эпохи AirPlay 2 (у UxPlay и Apple TV 3 — 220).
const minimumModernSourceMajor = 300

// ClassifyReceiver определяет тип приёмника по ответу /info; reason — пояснение
// для журнала (какие признаки сработали).
func ClassifyReceiver(info *ReceiverInfo) (class ReceiverClass, reason string) {
	if info == nil {
		return ReceiverClassUxPlay, "ответ /info отсутствует: действует проверенный профиль UxPlay"
	}
	if !info.PrefersLegacyPairing() {
		return ReceiverClassAirPlay2, fmt.Sprintf("есть признаки сопряжения AirPlay 2 (features 0x%x), сторонних признаков 26/51 нет", info.Features)
	}
	major, _, _, ok := parseSourceVersion(info.SourceVersion)
	if ok && major >= minimumModernSourceMajor {
		return ReceiverClassLegacyOther, fmt.Sprintf("legacy-сопряжение, но версия стека %s (от %d): современная реализация", info.SourceVersion, minimumModernSourceMajor)
	}
	if ok {
		return ReceiverClassUxPlay, fmt.Sprintf("legacy-сопряжение и версия стека %s (поколение AirPlay 1, как у UxPlay)", info.SourceVersion)
	}
	return ReceiverClassUxPlay, "legacy-сопряжение, версия стека не указана: действует проверенный профиль UxPlay"
}
