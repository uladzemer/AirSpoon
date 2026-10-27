# Исследование исходных проектов (2026-10-26)

# ЧАСТЬ А. Коротко, для владельца

1. **doubletake** — программа для Linux. Она передаёт экран и звук компьютера на приёмник AirPlay (Apple TV и совместимые). Это передатчик, а не приёмник.
2. Сама программа состоит из двух половин. Первая — «разговор» с приёмником: поиск в сети, сопряжение, шифрование, отправка кадров и звука. Эта часть написана целиком на Go и подходит для Windows. Вторая — захват экрана и звука. Она завязана на Linux, её для Windows пишем заново.
3. Есть **open-airplay** — переделка doubletake под Windows. Звук там уже снимается средствами Windows, а картинку пока снимает отдельная программа ffmpeg.exe. Из open-airplay берём готовые решения для Windows, кроме окна и значка в трее.
4. **android-airplay-server** — приёмник для Android TV, внутри него работает известный UxPlay. Режим для приставок, автозапуск после включения и 32-битная сборка в нём уже есть. Нужно сделать свой вариант: русский язык, имя «Телевизор», крупный экран ожидания, своя иконка.
5. **Звук: да, должен передаваться.** Передатчик отправляет звук в формате ALAC, приёмник его понимает. Нужно доделать:
   - звук с Windows-компьютера (у open-airplay это уже написано);
   - отправку в «старом» режиме, который понимает UxPlay. Новые режимы, рассчитанные на Apple TV, нужно выключить.
6. **Главные риски:**
   - Ни один из проектов не проверялся в связке «Windows → UxPlay на приставке». Работает ли это, станет ясно только на нашем железе.
   - Ваша приставка 32-битная и, возможно, слабая. 1080p при 60 кадрах она может не потянуть, начинаем с 30 кадров.
   - Сборку приложения под 32-битные приставки авторы проекта никогда не проверяли.
   - В коде есть позаимствованные «ключи» Apple (FairPlay), и их происхождение никто не проверял. Для себя это допустимо, для широкой раздачи это юридический вопрос.
   - Лицензия GPL-3: если будем раздавать программы, придётся открыть исходники.
7. **Первая цель:** на компьютере запускается один файл, на приставке появляется картинка рабочего стола. Звук и трей — следующими шагами.

# ЧАСТЬ Б. Технический план

## Б1. Что берём и под какой лицензией

- **Лицензии.**
  - doubletake: LGPL-3.0-or-later (по README; версии до v0.3.2 были под GPL-3.0).
  - open-airplay: GPL-3.0.
  - android-airplay-server: GPL-3.0. UxPlay внутри него — GPL-3.0, но в части файлов `lib/` стоят заголовки LGPL-2.1+, а `crypto.c` под GPL-3.
  - LGPLv3 §2 разрешает передавать код на условиях GPL-3. Поэтому `windows/` и `android/` целиком выходим под **GPL-3.0-or-later**, и лицензии совместимы.
  - Обязательно: сохраняем заголовки и `LICENSE` исходных проектов, пишем NOTICE с происхождением файлов (doubletake / open-airplay, хэш коммита). При распространении бинарников даём исходники.
- **FairPlay (`fpsap_tables.go`, `fpsap*.go`).** Происхождение не проверено: в README open-airplay стоит пункт про doubletake #59. Для личного использования это допустимо. Перед публичным релизом нужен отдельный разбор.
- **За основу берём `internal/airplay` из open-airplay, а не из doubletake.** В open-airplay уже есть:
  - `boot_clock_windows.go` (`QueryInterruptTimePrecise`, запасной путь `GetTickCount64`) — это закрывает проблему часов: в doubletake вне Linux время отсчитывается от старта процесса;
  - `capture_process_other.go` без `Pdeathsig`;
  - `stream_connection_id.go` (атомарный счётчик: на Windows id аудио- и видеопотока совпадали);
  - `audio_capture_windows.go`, `display_windows.go`;
  - удалённые hex-дампы ключей из логов.
- Пометка **«неясно»**: утверждение, что open-airplay отличается от doubletake *только* перечисленными правками, не подтвердилось. Отличаются также audio.go, client.go, mirror.go, latency.go, fairplay.go, pairing.go, replay.go, credentials*. По diff протокольная логика SETUP, FairPlay и pairing не менялась. Перед переносом нужно сверить по истории коммитов, что берём, а не полагаться на это утверждение.
- **Опровергнуто, не используем:** «одиночный .exe из doubletake собирается без правок». Не собирается: `syscall.Flock` в `internal/daemon/daemon.go:155` и `Pdeathsig` в `capture.go:782` стоят без build-тегов. Нужно выделить только ядро передатчика.

**Переносим (протокол, чистый Go):**
- `client.go`, `digest.go`, `discovery.go`, `pairing.go`;
- `fairplay*.go`, `fpsap*.go`;
- `compatibility.go`, `video_codec.go`, `mirror.go` (без HEVC на первом этапе);
- `event_channel.go`, `plist_types.go`, `replay.go`, `latency.go`;
- `credentials*.go`: путь заменить на `%APPDATA%\AirLive`, keyring работает через go-keyring/wincred;
- `stream_connection_id.go`, `boot_clock_windows.go`;
- аудиоупаковка: `audio.go` (RTP, AES-CBC, sync), `audio_frame.go`, `alac_compressed.go`, `audio_resample.go`, `audio_assembler.go`, `audio_pump.go`, `audio_device_format.go`;
- образцы COM: `audio_capture_windows.go`, `display_windows.go`;
- `aac_eld_stub.go`. Файл `aac_eld_fdk.go` не берём: не нужен cgo.

**Не переносим:** `internal/daemon`, `capture.go` (Linux-часть с GStreamer, xrandr, pactl, godbus), `capture_broadcast.go`, `receiver_*.go`. Исключение: `cmd/doubletake-test-receiver -profile uxplay` держим отдельно как регрессионный стенд, без железа.

**Обязательная правка API.** `MirrorSession.StreamFrames(ctx, *ScreenCapture, delay)` (mirror.go:1367) принимает конкретный тип, а `videoAccessUnitReader` не экспортирован (capture_frame.go:30). Вводим экспортированный интерфейс:
```go
type VideoSource interface {
    ReadAccessUnit(ctx context.Context) (VideoAccessUnit, error) // AnnexB с SPS/PPS/IDR, PTS
    Close() error
}
```
Кодер отдаёт Annex-B, обёртку в AVCC и avcC-пакет делает существующий парсер (`avccWrap`, `buildAVCCConfig`).

## Б2. Структура `windows/`

```
windows/
  go.mod                      (go 1.25+, CGO_ENABLED=0)
  cmd/airlive/main.go         запуск, флаги, single-instance (named mutex)
  internal/airplay/           перенесённое ядро (см. выше) + VideoSource
  internal/win/com/           хелперы vtable/HRESULT/GUID поверх x/sys/windows
  internal/capture/dxgi/      Desktop Duplication -> ID3D11Texture2D
  internal/encode/mf/         Media Foundation H.264 MFT (HW, затем SW)
  internal/encode/ffmpeg/     временный путь веха 1 (из capture_windows.go open-airplay)
  internal/audio/wasapi/      loopback (из audio_capture_windows.go)
  internal/tray/              Shell_NotifyIcon
  internal/config/            %APPDATA%\AirLive\config.json
  assets/ (ico, manifest)     go:embed + go-winres для ресурсов .exe
  NOTICE, LICENSE (GPL-3)
```

## Б3. API Windows и библиотеки (cgo не нужен)

- **COM.** Вызовы делаем через `syscall.SyscallN` по слотам vtable, как в open-airplay. go-ole не нужен.
- **Захват экрана: DXGI Desktop Duplication.**
  - Цепочка: `D3D11CreateDevice` → `IDXGIOutput1::DuplicateOutput` → `AcquireNextFrame`/`ReleaseFrame`.
  - Обработать `DXGI_ERROR_ACCESS_LOST` (смена режима, UAC, блокировка) пересозданием.
  - Ограничения те же, что у ddagrab: нужен интерактивный рабочий стол, работает только адаптер по умолчанию.
  - Если кадр не изменился, переотправлять последний кадр с частотой fps, иначе приёмник может решить, что поток остановился.
- **Цвет BGRA → NV12.**
  - Основной вариант: `ID3D11VideoProcessor` на GPU (кадр остаётся в видеопамяти, масштабирование там же).
  - Запасной: перевод на CPU в Go.
- **Кодер: Media Foundation.**
  - Аппаратный: `MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER, MFT_ENUM_FLAG_HARDWARE|ASYNCMFT)`. Он асинхронный (события METransformNeedInput/HaveOutput), нужен `MF_TRANSFORM_ASYNC_UNLOCK` и `IMFDXGIDeviceManager` для D3D-текстур.
  - Программный запасной: Microsoft H.264 Encoder MFT, синхронный, вход NV12 из системной памяти.
  - Параметры через ICodecAPI: low latency, CBR, GOP = 2·fps, без B-кадров, профиль Main (или Baseline), уровень 4.1. Принудительный IDR (`CODECAPI_AVEncVideoForceKeyFrame`) — при старте и по запросу.
  - **Неясно:** кладёт ли MFT SPS/PPS в поток Annex-B у каждого IDR. Если нет, берём их из `MF_MT_MPEG_SEQUENCE_HEADER` и вставляем перед IDR сами.
- **Звук: WASAPI loopback**, код из open-airplay. Работает так:
  - `IAudioClient` в режиме SHARED с LOOPBACK, опрос раз в 5 мс;
  - метки времени по QPC;
  - ресемплинг в 44100 / стерео / S16;
  - ALAC кадрами по 352 сэмпла.
  - Константа выравнивания 25 мс подобрана под ddagrab + x264. Под MF её нужно перемерить.
- **Трей.** Свой минимальный `Shell_NotifyIconW` + скрытое окно + `TrackPopupMenu` через x/sys/windows, примерно 200–300 строк.
  - Альтернатива — `energye/systray` или `getlantern/systray`. **Неясно**, нет ли в них cgo на Windows. В репозиториях они не оценивались, перед выбором проверить go.mod и build-теги.
- **Один .exe.** `CGO_ENABLED=0 GOOS=windows go build -ldflags "-H windowsgui"`. Иконку, манифест (DPI-aware PerMonitorV2) и версию вшить через go-winres.
- **Логика под UxPlay** (поправки проверки учтены):
  - время — только NTP: sync 0xd4, 20 байт. Sync 0xd7 (PTP) UxPlay не разбирает;
  - аудио шифруется AES-128-CBC, хвост пакета открытый;
  - RFC 2198 (PT 0x61) и ChaCha для аудио выключены: UxPlay и android-проект их не поддерживают;
  - кодек ALAC;
  - discovery по `_airplay._tcp`; сопряжение — legacy raw pair-setup/pair-verify, FairPlay, видео AES-CTR (mixed key).
  - Важная деталь: 0x56 у UxPlay — это повторная отправка (resent), а не sync. Sync — это type 0x54.

## Б4. Веха 1 — «картинка на приставке»

Идея: сначала проверить протокол, потом менять кодер. Так проблемы одного не путаются с проблемами другого.

1. Создать `windows/`, перенести ядро и выкинуть daemon и Linux-захват. Проверка: `go build` и `go vet` под GOOS=windows проходят.
2. Ввести `VideoSource`, прогнать имеющиеся тесты, включая `fairplay_streamkey_test` и тесты test-receiver с профилем uxplay. Проверка: тесты зелёные.
3. Временный источник — `internal/encode/ffmpeg` (ddagrab + libx264 из open-airplay, `-profile:v main`, 1280x720@30). ffmpeg.exe лежит рядом, только на время разработки. Проверка на приставке: discovery находит «Телевизор», `GET /info` и сопряжение проходят, картинка есть. Снять лог `/info` приёмника: есть ли там `displays`/`maxFPS`.
4. Заменить источник на DXGI + MF (HW, затем SW). Проверка: та же картинка без ffmpeg.exe, один .exe, задержка на глаз как с ffmpeg.
5. Включить звук (WASAPI → ALAC, NTP, AES-CBC). Это уже веха 2. После неё — трей и автозапуск.

## Б5. План форка `android/`

- **Идентификатор.** Меняем только `applicationId` (`app/build.gradle.kts`, ~стр. 33). `namespace` и пакет Kotlin `io.github.jqssun.airplay` не трогаем. Иначе придётся переименовывать 23 символа `Java_io_github_jqssun_airplay_bridge_NativeBridge_*` в `native_bridge.cpp`, keep-правила в `proguard-rules.pro`, action-строки (`AirPlayService.kt:~1186`, `SettingsReceiver.kt`, манифест) и `MainViewModel.kt:238`. Такая ошибка видна только в рантайме release-сборки (`UnsatisfiedLinkError`). FileProvider сам перейдёт на `${applicationId}`.
- **Имя по умолчанию.** `Prefs.kt:75` (не 96, номер поправлен при проверке): `SERVER_NAME = "Телевизор"`. Новое значение применится на чистой установке, а она у нас и будет из-за нового applicationId.
- **Строки.**
  - Новый `res/values-ru/strings.xml`: около 162 `<string>` плюс string-array (`audio_adaptive_step_names` и др.).
  - `app_name` (`strings.xml:3`).
  - Хардкод в Kotlin полным аудитом не проверялся — **неясно**.
- **Экран ожидания.** `OverviewContent` в `MainScreen.kt` (~595–735): крупное имя вместо мелкого `titleMedium` (стр. 722) и текста `waiting_for_connection`. Крупные стили для TV — в `ui/theme/Theme.kt`. Нижний `NavigationBar` на TV спрятать или увеличить.
- **Автозапуск.** `BootReceiver` уже поднимает сервис (`BOOT_AUTO_START=true`), но экран не открывает. Варианты:
  - запуск Activity из BootReceiver/сервиса с помощью уже объявленного `SYSTEM_ALERT_WINDOW` (выдать через adb `appops`);
  - сделать приложение лаунчером/HOME. **Неясно**, какой вариант разрешит прошивка приставки.
  - `LAUNCH_ON_CONNECT` включить по умолчанию.
  - На Android 11 FGS из BOOT_COMPLETED разрешён.
- **armeabi-v7a.**
  - Сейчас `allAbis` содержит три ABI. Для приставки собирать только `armeabi-v7a` — быстрее и меньше APK.
  - OpenSSL 3.4.4 и ffmpeg (`--disable-asm`) собираются из исходников, под 32 бита это никогда не проверялось.
  - Собирать на Linux/WSL или в GitHub Actions: NDK 27.0.12077973, CMake 3.22.1, `submodules: recursive`.
  - Нужен свежий SDK: compileSdk 37 и `CINNAMON_BUN` в `Permissions.kt:11`.
- **Подпись.** Свой keystore и секреты `STORE`/`LOCAL`. Без них release получится unsigned. Для начала можно ставить `assembleDebug`.
- **Бренд.** Значки `ic_launcher*`, `banner.xml` (320x180), fastlane, README. Credits (UxPlay, FFmpeg, Next Player), LICENSE и «not affiliated with Apple» сохранить.
- **UxPlay.** Субмодуль закреплён на `df67c212…` и 9 патчах. Его не обновляем, иначе патчи придётся накатывать заново.
- **PIN.** Сейчас только случайный, по желанию. Фиксированный PIN — небольшая правка (`Prefs` + `native_bridge.cpp:~88`), по умолчанию оставляем выключенным.

## Б6. Проверки на реальных устройствах

1. Приставка: APK armeabi-v7a ставится, `libairplay_native.so` загружается, сервис стартует (logcat без `UnsatisfiedLinkError`).
2. После перезагрузки приставки сервис поднят, экран ожидания с «Телевизор» показан или открывается при подключении.
3. Windows видит приёмник по `_airplay._tcp`; снять TXT, `features` и `/info` (displays, maxFPS, pk, statusFlags).
4. Сопряжение по legacy-ветке: raw pair-setup, затем pair-verify, затем `/fp-setup`. Видео AES-CTR расшифровывается, картинка есть.
5. Нагрузка декодера: 720p30 → 1080p30 → 1080p60, профиль Main/High. Смотреть задержку, пропуски кадров и CPU приставки.
6. Звук ALAC: слышен; рассинхрон с видео по ролику «вспышка и бип»; ffmpeg-ALAC без asm на ARMv7 не грузит CPU.
7. Повторное подключение с сохранёнными ключами, обрыв Wi-Fi и восстановление, работа 30–60 минут.
8. Windows-сторона:
   - аппаратный MFT на конкретной видеокарте и программный запасной;
   - `ACCESS_LOST` при блокировке экрана или UAC;
   - смена устройства вывода звука;
   - несколько мониторов и разный DPI.
9. Брандмауэр Windows: исходящие TCP и входящие UDP на тройку портов N, N+1, N+2 (NTP-запросы приёмника, повторы аудио).
10. Регрессия без железа: `doubletake-test-receiver -profile uxplay` на каждом изменении ядра.