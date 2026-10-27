# AirSpoon Receiver (Android TV)

Приёмник экрана для телевизионных приставок на Android. Телефон, компьютер или планшет показывают
своё изображение и звук на телевизоре — без проводов и без ввода кодов. Совместимо с AirPlay.

Приложение не связано с Apple Inc. и не одобрено ей.

Это форк проекта [android-airplay-server](https://github.com/jqssun/android-airplay-server)
(автор jqssun, лицензия GPL-3.0), который построен на [UxPlay](https://github.com/FDH2/UxPlay).
Подробности о происхождении, коммитах и лицензиях — в [NOTICE.md](NOTICE.md).
Исходный README сохранён в [UPSTREAM_README.md](UPSTREAM_README.md).

## Что умеет

- Показывает на телевизоре экран и звук с iPhone, iPad, Mac и с Windows (программа AirSpoon Sender для Windows).
- Крупный экран ожидания: «Выберите на телефоне или компьютере:», имя телевизора огромными буквами,
  короткая инструкция и IP-адрес. Видно с дивана, управляется пультом.
- Имя приёмника по умолчанию — «Телевизор». Менять: вкладка «Настройки» → «Имя телевизора»
  (кириллица поддерживается, не длиннее 20 букв).
- Запускается сам после включения приставки и сам открывается при подключении устройства.
- Русский интерфейс (английский — запасной).

## Сборка

Сборка делается в Docker, Android SDK на компьютер ставить не нужно. Нужен Docker и интернет
(Gradle и OpenSSL скачиваются при сборке; первая сборка долгая — собираются OpenSSL и FFmpeg).

Из каталога `android/` (в Git Bash или WSL):

```bash
# Отладочный APK только для armeabi-v7a (целевая приставка).
# Скрипт сам соберёт образ Docker при первом запуске, соберёт APK и проверит его
# (манифест, нативные библиотеки, JNI-символы).
bash docker/build.sh

# Результат: out/airspoon-debug-armeabi-v7a.apk
```

Сборка для всех процессоров (для релиза):

```bash
ABIS=armeabi-v7a,arm64-v8a,x86_64 bash docker/build.sh
```

Юнит-тесты (выбор IP-адреса для экрана ожидания) вместе со сборкой: `bash docker/build.sh testDebugUnitTest assembleDebug`.

Повторная проверка готового APK: `bash docker/verify-apk.sh out/airspoon-debug-armeabi-v7a.apk`.

Не запускайте `docker run ... gradlew` вручную без ограничений памяти: на Docker с 2 ГБ
сборка падает с «Gradle build daemon disappeared». В `docker/build.sh` уже заданы нужные ключи
(`--max-workers=2`, `-Pkotlin.compiler.execution.strategy=in-process`, `-Xmx1100m`).

Заметки:

- По умолчанию собирается только `armeabi-v7a` (свойство `airspoon.abis` в `gradle.properties`).
- Исходники сторонних библиотек (UxPlay, libplist, openssl-cmake, FFmpeg) лежат в
  `app/src/main/cpp/third_party/` как обычные файлы; `git submodule` не нужен. Патчи UxPlay уже применены.
- Отладочный APK всегда подписан одним и тем же ключом `docker/debug.keystore` (пароль `android`, это не секрет), поэтому новую сборку можно ставить поверх старой: `adb install -r` сохраняет настройки и выданные права. APK, собранные до появления этого ключа, подписаны другим случайным ключом: при первом переходе приложение нужно один раз удалить (`adb uninstall app.airspoon.tv`).
- Воспроизводимость: образ Docker закреплён по digest, архив OpenSSL проверяется по SHA-256. Версии Gradle-зависимостей берутся из `gradle/libs.versions.toml`.
- Подпись релиза: ключ задаётся в `local.properties` (`storeFile`, `storePassword`, `keyAlias`,
  `keyPassword`). Без него `assembleRelease` даёт неподписанный APK; для установки используйте `assembleDebug`.
- Памяти Docker нужно не меньше 2 ГБ (проверено на 1,9 ГБ); если памяти больше, кучу Gradle можно поднять переменной `GRADLE_XMX` (например, `GRADLE_XMX=2g`). Сборка с нуля около 4 минут.
- Если Windows и Docker Desktop: запускайте команды в Git Bash или WSL; при проблемах с окончаниями
  строк убедитесь, что `gradlew` имеет окончания LF (в этом дереве они LF).

## Установка и права

```bash
adb install -r out/airspoon-debug-armeabi-v7a.apk
```

Чтобы экран ожидания открывался сам после включения приставки и при подключении телефона, нужно
**один раз вручную** выдать разрешение «Поверх других окон» (Android 10+ не даёт открывать
экраны из фона без него):

- Настройки приставки → Приложения → AirSpoon Receiver → «Поверх других окон» → разрешить, или
- `adb shell appops set app.airspoon.tv SYSTEM_ALERT_WINDOW allow`.

Если в прошивке нет экрана этого разрешения, приложение при включении переключателя в «Настройках»
покажет подсказку: разрешение выдаётся с компьютера при установке командой `adb` выше.

Без этого разрешения вместо экрана показывается полноэкранное уведомление (на части приставок это
лишь карточка, на которую нужно нажать пультом). Сам приём запускается после загрузки и без разрешения.

### Проверка автозапуска после перезагрузки

Автозапуск работает только при двух условиях, и проверить их можно лишь на самой приставке:

1. Приложение **хотя бы один раз открыли** после установки. После `adb install` оно находится в
   «остановленном» состоянии, и Android не доставляет ему сообщение о загрузке. Откройте его с пульта
   или командой `adb shell monkey -p app.airspoon.tv -c android.intent.category.LEANBACK_LAUNCHER 1`.
2. Выдано разрешение «Поверх других окон» (см. выше). Иначе после загрузки поднимется только
   приём, а вместо экрана придёт уведомление, которое на части ТВ-прошивок не видно вовсе.

Тест: установить, открыть один раз, выдать разрешение, перезагрузить приставку
(`adb reboot`), через ~30 с проверить `adb shell dumpsys activity top | grep app.airspoon.tv`.
Если на прошивке X96 есть менеджер автозапуска, приложению нужно разрешить автозапуск и там.

## Настройка через ADB

```bash
adb shell am broadcast -n app.airspoon.tv/io.github.jqssun.airplay.SettingsReceiver \
  -a app.airspoon.tv.SET_SETTING --es key server_name --es value "Гостиная"
adb shell am broadcast -n app.airspoon.tv/io.github.jqssun.airplay.SettingsReceiver \
  -a app.airspoon.tv.LIST_SETTINGS
```

Полезные ключи: `server_name` (имя), `boot_auto_start` (запуск при загрузке), `show_on_boot`
(экран после загрузки), `launch_on_connect` (открывать при подключении).

## Авторство

- Исходный проект: jqssun, [android-airplay-server](https://github.com/jqssun/android-airplay-server), GPL-3.0.
- [UxPlay](https://github.com/FDH2/UxPlay) — реализация протокола (GPL-3.0).
- [FFmpeg](https://ffmpeg.org) — декодер звука (LGPL-2.1+).
- [OpenSSL](https://www.openssl.org) и [openssl-cmake](https://github.com/viaduck/openssl-cmake).
- [libplist](https://github.com/libimobiledevice/libplist) (LGPL-2.1).
- [Next Player](https://github.com/anilbeesetti/nextplayer) — видеоплеер, на котором основан экран воспроизведения.

Лицензия всего проекта — GNU GPL v3, см. [LICENSE](LICENSE).
