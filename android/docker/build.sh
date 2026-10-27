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
# Куча Gradle ограничена: у Docker Desktop по умолчанию ~2 ГБ ОЗУ. Если памяти больше — можно поднять GRADLE_XMX.
docker run --rm -v "$MOUNT":/work -v "$VOLUME":/root/.gradle "$IMAGE" \
  sh ./gradlew "${TASKS[@]}" --no-daemon --max-workers=2 -Pairspoon.abis="$ABIS" \
  -Pkotlin.compiler.execution.strategy=in-process \
  -Dorg.gradle.jvmargs="-Xmx${GRADLE_XMX:-1100m} -Dfile.encoding=UTF-8"
echo ">> Gradle: $(( $(date +%s) - START )) с"

mkdir -p "$ROOT/out"
OUT="$ROOT/out/airspoon-debug-${ABIS//,/+}.apk"
cp "$ROOT/app/build/outputs/apk/debug/app-debug.apk" "$OUT"
echo ">> APK: $OUT ($(wc -c < "$OUT") байт)"

bash "$HERE/verify-apk.sh" "$OUT"
