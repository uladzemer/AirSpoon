#!/usr/bin/env bash
# Проверка APK: манифест (aapt2 badging), нативные библиотеки и экспорт JNI-символов (llvm-nm из NDK).
# Использование: bash verify-apk.sh путь/к.apk   (выполняется внутри Docker-образа)
set -euo pipefail
export MSYS_NO_PATHCONV=1
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$HERE")"
IMAGE="${IMAGE:-airspoon-android-build}"
VOLUME="${VOLUME:-airspoon-gradle}"
APK="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
if command -v cygpath >/dev/null 2>&1; then MOUNT="$(cygpath -m "$(dirname "$APK")")"; else MOUNT="$(dirname "$APK")"; fi

docker run --rm -v "$MOUNT":/apk:ro -v "$VOLUME":/root/.gradle:ro "$IMAGE" bash -c '
  set -e
  APK=/apk/'"$(basename "$APK")"'
  # aapt2 лежит в кэше Gradle (скачивается вместе с AGP)
  AAPT2=$(find /root/.gradle/caches -type f -name aapt2 -path "*linux*" | head -1)
  NM=$(find /opt/android-sdk/ndk -name llvm-nm -type f | head -1)
  mkdir -p /tmp/x && cd /tmp/x && unzip -q -o "$APK" "lib/*" 
  echo "== badging =="; "$AAPT2" dump badging "$APK" | grep -E "^(package|application:|application-label:|application-label-ru:|native-code|sdkVersion|minSdkVersion|targetSdkVersion|uses-permission|launchable-activity|leanback-launchable-activity)|^ *uses-feature"
  echo "== lib/ =="; unzip -l "$APK" | grep "lib/"
  echo "== JNI-символы =="
  for so in lib/*/libairplay_native.so; do echo "$so"; "$NM" -D --defined-only "$so" | grep -c " Java_io_github_jqssun_airplay_" ; done
  "$NM" -D --defined-only lib/*/libairplay_native.so | grep " Java_io_github_jqssun_airplay_" | head -40
  echo "== ELF =="; "$(dirname "$NM")/llvm-readelf" -h lib/armeabi-v7a/libairplay_native.so | grep -E "Class|Machine"
'
