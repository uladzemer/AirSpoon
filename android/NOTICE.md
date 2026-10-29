# NOTICE — происхождение и лицензии

AirSpoon для Android TV (каталог `android/`) — форк проекта **android-airplay-server**.
Приложение не связано с Apple Inc. и не одобрено ей. Слово «AirPlay» используется только
в значении «совместимо с AirPlay».

## Исходный проект

| | |
|---|---|
| Репозиторий | https://github.com/jqssun/android-airplay-server |
| Автор | jqssun и участники проекта |
| Коммит основы | `a46cca0a6a1274e9e33c0e750f0f1c1eb1d881ea` (ветка `main`, версия приложения 0.0.36) |
| Лицензия | GNU GPL v3 — файл [`LICENSE`](LICENSE) |

Исходный README сохранён без изменений в [`UPSTREAM_README.md`](UPSTREAM_README.md).

## Сторонний код (бывшие git-субмодули, включены в дерево как обычные файлы)

Каталог `app/src/main/cpp/third_party/`:

| Каталог | Репозиторий | Коммит | Лицензия |
|---|---|---|---|
| `UxPlay` | https://github.com/FDH2/UxPlay | `df67c212a433cf6dda3676dd40c097900d24e645` | GPL v3 (`UxPlay/LICENSE`); внутри — llhttp (MIT), playfair и др. со своими лицензиями |
| `libplist` | https://github.com/libimobiledevice/libplist | `f41b1ea67045e0c09339974d83e389972d84f166` | LGPL v2.1 (`libplist/COPYING.LESSER`) |
| `openssl-cmake` | https://github.com/viaduck/openssl-cmake | `4edd36a8dab5f85a8f92650b5bdf6e0cab13aab8` (ветка `v3`) | MIT (`openssl-cmake/LICENSE`); сам OpenSSL 3.x (Apache License 2.0) скачивается при сборке |
| `ffmpeg` | https://github.com/FFmpeg/FFmpeg | `38b88335f99e76ed89ff3c93f877fdefce736c13` | LGPL v2.1 и новее (`ffmpeg/LICENSE.md`); собирается без `--enable-gpl`, только декодеры ALAC и AAC |

Текст Apache-2.0 для OpenSSL: [`licenses/OpenSSL-Apache-2.0.txt`](licenses/OpenSSL-Apache-2.0.txt),
официальный источник: https://www.apache.org/licenses/LICENSE-2.0.txt.
Архив исходников OpenSSL также содержит `LICENSE.txt`, распаковываемый CMake при сборке;
в APK он автоматически не включается. Лицензии обеих платформ публикуются в `AirSpoon-LICENSES.zip`.

### FairPlay (`UxPlay/lib/playfair/`)

Рукопожатие FairPlay SAP (без него iPhone и iPad не начинают показ экрана) делает библиотека
**playfair**: https://github.com/EstebanKubata/playfair, автор EstebanKubata, 2015, GNU GPL v3
(`UxPlay/lib/playfair/LICENSE.md`). В UxPlay она пришла по цепочке ShairPlay (Juho Vähä-Herttua,
LGPL v2.1+) → RPiPlay → UxPlay. Таблицы и константы (`omg_hax.h`, `omg_hax.c`) получены авторами
playfair обратной разработкой; как именно — не задокументировано, и UxPlay прямо пишет:
«The legal status of that library is unclear». AirSpoon эти файлы не менял.

Служебные каталоги `.git` у субмодулей и файл `.gitmodules` в копию не вошли. Файлы CI исходного
проекта (`.github/workflows/apk.yml`) не копировались.

## Изменения относительно исходного проекта

- Подписанный Ed25519-манифест обновлений (аудит 3.3, 2026-10-29): проверка версии, имени,
  размера и SHA-256 APK до PackageInstaller; проверка подписи через JNI и уже включённый OpenSSL.

- Гигиена сборки и выпуска (2026-10-29): проверки хешей Android tools/Gradle, OpenSSL 3.4.8,
  официальный URL архива в openssl-cmake вместо зеркала viaduck, версия libplist 2.7.0 по
  включённому NEWS, проверка диапазона versionCode и архив лицензий релиза.

- `applicationId` = `app.airspoon.tv`. Gradle-`namespace` и Kotlin-пакет `io.github.jqssun.airplay`
  оставлены прежними: от них зависят имена JNI-функций в `native_bridge.cpp` и правила ProGuard.
- Строки действий (`app.airspoon.tv.*`) вместо `io.github.jqssun.airplay.*`, чтобы не пересекаться
  с установленным оригиналом.
- Название «AirSpoon», русский перевод, имя приёмника по умолчанию «Телевизор».
- Крупный экран ожидания для ТВ, тёмная тема на ТВ, показ экрана после загрузки приставки.
- Новые значки и баннер.
- Сборка по умолчанию только `armeabi-v7a` (свойство `airspoon.abis`).
- Патчи UxPlay из `app/src/main/cpp/patches/UxPlay/` (0001–0009 от исходного проекта, 0010–0011 — наши)
  применены к исходникам в `third_party/UxPlay` заранее; Gradle-задача `applyUxplayPatches` удалена.
- Защита HTTP/HLS/FairPlay от некорректного сетевого ввода (аудит 2026-10-29): границы буферов и размеров, проверки состояния и индексов, неблокирующий разбор префикса HTTP (справочный патч `0012-harden-http-hls-and-fairplay-input.patch`).
- Переводы строк CRLF → LF (исходная копия была получена с автозаменой окончаний строк).
- Защита UxPlay от некорректных SPS/PPS/VPS и plist, обхода PIN и гонок mirror-потока (2026-10-29, справочный патч `0011-…`).
- Второй раунд аудита (2026-10-29): авторизация управляющих запросов и отдельного HTTP-сеанса HLS,
  смена четырёхзначного PIN после пяти неверных SRP-доказательств с задержкой ответа и обновлением
  экрана, освобождение SRP-буферов; совместимость LF/CRLF condensed-плейлистов YouTube,
  повторные URI и повторная загрузка HLS, повторный запуск mirror-потока после ошибки кодека.
  Сохранение видеодекодера при пропуске кадров, атомарное отключение клиентов и диагностика LatencyProbe.
- Каталог `fastlane/` (метаданные Google Play / F-Droid исходного проекта) удалён.
- Звук при показе экрана (2026-10-27): повтор ожидания входного буфера декодера, плавные переходы
  и маскировка одиночного потерянного кадра, нижний порог запаса после провалов Wi-Fi
  (`timeline_buffer.h`, `audio_decoder.h`, `audio_engine.cpp`); диагностика провалов приёма
  и повторных запросов в UxPlay `raop_buffer.c`/`raop_rtp.c` (справочный патч `0010-…`).
- Отладочный ключ подписи `docker/debug.keystore` (общий, не секретный), имя темы `Theme.AirSpoon`, файлы скачанных видео `AirSpoon_*.mp4`, ограничение имени приёмника 20 знаками.
- Проверка и установка обновлений из GitHub Releases (пакет `io.github.jqssun.airplay.update`, настройка «Проверять обновления», установка через PackageInstaller).
- Стабильность 24/7 (2026-10-29): освобождение JNI-потоков, проверки выделения памяти,
  sticky-перезапуск с сохранением ручной остановки, лимиты DMAP и обложек, закрытие DACP,
  восстановление и синхронное отключение EGL-поверхности, 64-битные lock-free счётчики аудиокольца.
- Релизный APK с R8 и подписью AirSpoon (без ключа — отладочная подпись); сохранение Media3 для отражательных вызовов и совместимого имени APK для клиентов 0.1.x (2026-10-29).
- Стабильность 24/7, пакет E (2026-10-29): восстановление Oboe/MediaCodec, аудио FLUSH и LatencyProbe, корректные кодеки mDNS и повтор NSD, освобождение и восстановление видеодекодера, DACP без гонок, фоновый журнал с ротацией, допустимый PiP, изоляция загрузок, периодическая проверка и отсрочка установки обновлений при активном сеансе.
