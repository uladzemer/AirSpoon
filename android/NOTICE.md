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

- `applicationId` = `app.airspoon.tv`. Gradle-`namespace` и Kotlin-пакет `io.github.jqssun.airplay`
  оставлены прежними: от них зависят имена JNI-функций в `native_bridge.cpp` и правила ProGuard.
- Строки действий (`app.airspoon.tv.*`) вместо `io.github.jqssun.airplay.*`, чтобы не пересекаться
  с установленным оригиналом.
- Название «AirSpoon», русский перевод, имя приёмника по умолчанию «Телевизор».
- Крупный экран ожидания для ТВ, тёмная тема на ТВ, показ экрана после загрузки приставки.
- Новые значки и баннер.
- Сборка по умолчанию только `armeabi-v7a` (свойство `airspoon.abis`).
- Патчи UxPlay из `app/src/main/cpp/patches/UxPlay/` (0001–0009 от исходного проекта, 0010 — наш)
  применены к исходникам в `third_party/UxPlay` заранее; Gradle-задача `applyUxplayPatches` удалена.
- Переводы строк CRLF → LF (исходная копия была получена с автозаменой окончаний строк).
- Каталог `fastlane/` (метаданные Google Play / F-Droid исходного проекта) удалён.
- Звук при показе экрана (2026-10-27): повтор ожидания входного буфера декодера, плавные переходы
  и маскировка одиночного потерянного кадра, нижний порог запаса после провалов Wi-Fi
  (`timeline_buffer.h`, `audio_decoder.h`, `audio_engine.cpp`); диагностика провалов приёма
  и повторных запросов в UxPlay `raop_buffer.c`/`raop_rtp.c` (справочный патч `0010-…`).
- Отладочный ключ подписи `docker/debug.keystore` (общий, не секретный), имя темы `Theme.AirSpoon`, файлы скачанных видео `AirSpoon_*.mp4`, ограничение имени приёмника 20 знаками.
- Проверка и установка обновлений из GitHub Releases (пакет `io.github.jqssun.airplay.update`, настройка «Проверять обновления», установка через PackageInstaller).
