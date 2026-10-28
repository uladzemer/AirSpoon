#!/usr/bin/env bash
# Сборка отладочного APK AirSpoon внутри Docker (Android SDK на компьютер ставить не нужно).
# Запускать из Git Bash или WSL:  bash android/docker/build.sh [gradle-задачи...]
# По умолчанию: assembleDebug только для armeabi-v7a. Все ABI: ABIS=armeabi-v7a,arm64-v8a,x86_64 bash build.sh
# Результат: android/out/airspoon-debug-armeabi-v7a.apk (+ вывод проверки APK).
set -euo pipefail
export MSYS_NO_PATHCONV=1   # Git Bash на Windows не должен переписывать пути /work, /root/.gradle

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$HERE")"                 # каталог android/
IMAGE="${IMAGE:-airspoon-android-build}"
VOLUME="${VOLUME:-airspoon-gradle}"       # docker volume с кэшем Gradle (~1,5 ГБ)
ABIS="${ABIS:-armeabi-v7a}"
TASKS=("${@:-assembleDebug}")

# Для docker run на Windows нужен путь вида D:/..., на Linux/WSL — обычный.
if command -v cygpath >/dev/null 2>&1; then MOUNT="$(cygpath -m "$ROOT")"; else MOUNT="$ROOT"; fi

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo ">> Собираю образ $IMAGE"
  docker build -t "$IMAGE" "$HERE"
fi

START=$(date +%s)
VERSION_ARG=()
if [ -n "${AIRSPOON_VERSION:-}" ]; then
  VERSION_ARG=("-Pairspoon.version=$AIRSPOON_VERSION")
fi
# Ключ подписи AirSpoon (см. app/build.gradle.kts): AIRSPOON_KEYSTORE + AIRSPOON_KEYSTORE_PASSWORD,
# иначе ~/.airspoon/release.jks и release.password, если есть; нет ни того ни другого — отладочный ключ.
KEY_ARGS=()
if [ -z "${AIRSPOON_KEYSTORE:-}" ] && [ -f "$HOME/.airspoon/release.jks" ] && [ -f "$HOME/.airspoon/release.password" ]; then
  AIRSPOON_KEYSTORE="$HOME/.airspoon/release.jks"
  AIRSPOON_KEYSTORE_PASSWORD="$(cat "$HOME/.airspoon/release.password")"
fi
if [ -n "${AIRSPOON_KEYSTORE:-}" ]; then
  [ -f "$AIRSPOON_KEYSTORE" ] || { echo "Нет файла ключа $AIRSPOON_KEYSTORE" >&2; exit 1; }
  [ -n "${AIRSPOON_KEYSTORE_PASSWORD:-}" ] || { echo "Не задан AIRSPOON_KEYSTORE_PASSWORD" >&2; exit 1; }
  if command -v cygpath >/dev/null 2>&1; then KEY_HOST="$(cygpath -m "$AIRSPOON_KEYSTORE")"; else KEY_HOST="$AIRSPOON_KEYSTORE"; fi
  # Пароль уходит в контейнер переменной окружения (-e без значения), не в командную строку.
  export AIRSPOON_KEYSTORE_PASSWORD
  KEY_ARGS=(-v "$KEY_HOST":/keystore/release.jks:ro -e AIRSPOON_KEYSTORE=/keystore/release.jks -e AIRSPOON_KEYSTORE_PASSWORD)
  echo ">> Подпись: ключ AirSpoon"
else
  echo ">> Подпись: общий отладочный ключ"
fi
# Куча Gradle ограничена: у Docker Desktop по умолчанию ~2 ГБ ОЗУ. Если памяти больше — можно поднять GRADLE_XMX.
docker run --rm -v "$MOUNT":/work -v "$VOLUME":/root/.gradle ${KEY_ARGS[@]+"${KEY_ARGS[@]}"} "$IMAGE" \
  sh ./gradlew "${TASKS[@]}" ${VERSION_ARG[@]+"${VERSION_ARG[@]}"} --no-daemon --max-workers=2 -Pairspoon.abis="$ABIS" \
  -Pkotlin.compiler.execution.strategy=in-process \
  -Dorg.gradle.jvmargs="-Xmx${GRADLE_XMX:-1100m} -Dfile.encoding=UTF-8"
echo ">> Gradle: $(( $(date +%s) - START )) с"

mkdir -p "$ROOT/out"
OUT="$ROOT/out/airspoon-debug-${ABIS//,/+}.apk"
cp "$ROOT/app/build/outputs/apk/debug/app-debug.apk" "$OUT"
echo ">> APK: $OUT ($(wc -c < "$OUT") байт)"

bash "$HERE/verify-apk.sh" "$OUT"
