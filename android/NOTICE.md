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
| `openssl-cmake` | https://github.com/viaduck/openssl-cmake | `4edd36a8dab5f85a8f92650b5bdf6e0cab13aab8` (ветка `v3`) | MIT (`openssl-cmake/LICENSE`); сам OpenSSL 3.5.9 LTS (Apache License 2.0) скачивается при сборке |
| `ffmpeg` | https://github.com/FFmpeg/FFmpeg | `38b88335f99e76ed89ff3c93f877fdefce736c13` | LGPL v2.1 и новее (`ffmpeg/LICENSE.md`); собирается без `--enable-gpl`, только декодеры ALAC и AAC |

Текст Apache-2.0 для OpenSSL: [`licenses/OpenSSL-Apache-2.0.txt`](licenses/OpenSSL-Apache-2.0.txt),
официальный источник: https://www.apache.org/licenses/LICENSE-2.0.txt.
Архив исходников OpenSSL также содержит `LICENSE.txt`, распаковываемый CMake при сборке.
Тексты лицензий из репозитория включены в APK как `assets/licenses/` и доступны в настройках
«О программе и лицензии» вместе с версией приложения и ссылкой на исходный код
https://github.com/uladzemer/AirSpoon. Лицензии обеих платформ также публикуются в `AirSpoon-LICENSES.zip`.

Экран перечисляет AirSpoon / android-airplay-server (GPL-3.0), UxPlay (GPL-3.0+),
ShairPlay и libplist / libcnary (LGPL-2.1+), playfair (GPL-3.0), llhttp, csrp, jsmn,
time64 и openssl-cmake (MIT), FFmpeg с декодерами ALAC/AAC (LGPL-2.1+),
OpenSSL, Oboe, AndroidX / Compose / Material / Media3, Dagger / Hilt,
Kotlin / kotlinx.coroutines (Apache-2.0). Уведомления MIT для csrp, jsmn и time64
скопированы из заголовков включённых исходников; остальные тексты — из файлов лицензий.

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

- Экран лицензий (2026-10-29): локальные тексты, управление пультом, версия и адрес исходников.
  OpenSSL переведён с неподдерживаемой 3.4.8 на 3.5.9 LTS;
  источник версии: https://openssl-library.org/source/;
  SHA-256: `603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a`,
  официальный файл: https://github.com/openssl/openssl/releases/download/openssl-3.5.9/openssl-3.5.9.tar.gz.sha256.

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
- Доработки восстановления и PIN (2026-10-29): разрыв RAOP-сеанса через очередь httpd после
  сбоя видеодекодера и трёх секунд без ключевого кадра (не чаще раза в 10 секунд);
  неблокирующая PIN-блокировка 1–10 секунд вместо задержки httpd, с сохранением ротации PIN
  после пяти ошибок и сброса счётчиков при успехе (справочные патчи `0011-…`, `0013-…`).
- Каталог `fastlane/` (метаданные Google Play / F-Droid исходного проекта) удалён.
- Устранение гонки восстановления видео (2026-10-29): поколение владельца mirror-потока
  читается под run_mutex и сохраняется при сбое кодека; httpd удаляет только RAOP-соединение
  с этим поколением, сохраняя новый сеанс (справочный патч `0013-…`).
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
