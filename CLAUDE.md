# AirSpoon — проектные правила

Показ экрана по AirPlay: **AirSpoon Sender** для Windows (`windows/`, Go) → **AirSpoon Receiver** на Android TV-приставке (`android/`, форк android-airplay-server: Kotlin + нативное ядро UxPlay). Лицензия GPL-3.0, приватный репозиторий `uladzemer/AirSpoon`. План и состояние — `plans/airspoon.md`, передача между сессиями — `plans/handoff-*.md`.

## Сборка и проверка
- **Windows:** `windows/build.ps1` (PowerShell) → `windows/bin/AirSpoonSender.exe`. Внутри: значки, ресурсы `.syso` (go-winres, генерируются — в git не кладём), `go vet`, `go test ./...`, сборка. Go: `C:\Program Files\Go\bin` (в git-bash добавить в PATH).
- **Android:** `bash android/docker/build.sh testDebugUnitTest assembleDebug` (нужен запущенный Docker Desktop) → `android/out/airspoon-debug-armeabi-v7a.apk`. SDK на хост не ставится, всё в образе `airspoon-android-build`.
- «Готово» = тесты зелёные + живая проверка на приставке (показ с iPad/Windows, счётчики `audio stats` в logcat).

## Стенд
- Приставка P7 (Allwinner H616, Android 12, armeabi-v7a), adb `192.168.30.126:5555` (DHCP, адрес меняется). adb — из пакета scrcpy: `%LOCALAPPDATA%\Microsoft\WinGet\Packages\Genymobile.scrcpy*`.
- Установка: `adb install -r android/out/airspoon-debug-armeabi-v7a.apk`, пакет `app.airspoon.tv`, активность `io.github.jqssun.airplay.MainActivity`.
- Ядерный лог Wi-Fi на этой прошивке не идёт в logcat — смотреть `dmesg`.

## Зоны высокого риска
- `android/app/src/main/cpp/timeline_buffer.h`, `audio_engine.cpp`, `audio_output.h` — звук в реальном времени: в колбэке Oboe (`read()`) никаких блокировок, аллокаций и логов; события — через lock-free кольца. Нет автотестов — любое изменение проверять живым показом.
- `android/app/src/main/cpp/third_party/UxPlay/` — патчи применены прямо в исходники; `cpp/patches/UxPlay/` — только справка (при правке UxPlay добавить туда патч). Сетевой ввод от любого отправителя в LAN.
- Сопряжение/FairPlay (`playfair`, `raop_handlers.h`), `windows/internal/airplay/` — протокол; правки сверять с doubletake/UxPlay.
- GPL: при изменениях исходного проекта дописывать `android/NOTICE.md`, лицензии сторонних частей сохранять.

## Конвенции
- Интерфейс и тексты для пользователя — по-русски; код и имена — по-английски.
- Коммит и пуш — только по команде владельца.
