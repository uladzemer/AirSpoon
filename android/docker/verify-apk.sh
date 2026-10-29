#!/usr/bin/env bash
# Проверка APK: манифест (aapt2 badging), нативные библиотеки и экспорт JNI-символов (llvm-nm из NDK).
# Использование: bash verify-apk.sh путь/к.apk [ABI через запятую] (проверка внутри Docker-образа)
set -euo pipefail
export MSYS_NO_PATHCONV=1
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$HERE")"
IMAGE="${IMAGE:-airspoon-android-build}"
VOLUME="${VOLUME:-airspoon-gradle}"
APK="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
ABIS="${2:-${ABIS:-armeabi-v7a}}"
if command -v cygpath >/dev/null 2>&1; then MOUNT="$(cygpath -m "$(dirname "$APK")")"; else MOUNT="$(dirname "$APK")"; fi

docker run --rm -v "$MOUNT":/apk:ro -v "$VOLUME":/root/.gradle:ro "$IMAGE" bash -c '
  set -euo pipefail
  APK="/apk/$1"
  IFS=, read -ra ABIS <<< "$2"
  # aapt2 лежит в кэше Gradle (скачивается вместе с AGP)
  AAPT2=$(find /root/.gradle/caches -type f -name aapt2 -path "*linux*" | sed -n "1p")
  NM=$(find /opt/android-sdk/ndk -name llvm-nm -type f | sed -n "1p")
  mkdir -p /tmp/x && cd /tmp/x && unzip -q -o "$APK" "lib/*" 
  echo "== badging =="; "$AAPT2" dump badging "$APK" | grep -E "^(package|application:|application-label:|application-label-ru:|native-code|sdkVersion|minSdkVersion|targetSdkVersion|uses-permission|launchable-activity|leanback-launchable-activity)|^ *uses-feature"
  echo "== lib/ =="; unzip -l "$APK" | grep "lib/"
  echo "== JNI-символы =="
  for abi in "${ABIS[@]}"; do
    so="lib/$abi/libairplay_native.so"
    test -s "$so"
    echo "$so"
    "$NM" -D --defined-only "$so" | grep -c " Java_io_github_jqssun_airplay_"
    "$NM" -D --defined-only "$so" | grep " Java_io_github_jqssun_airplay_" | sed -n "1,40p"
    echo "== ELF =="; "$(dirname "$NM")/llvm-readelf" -h "$so" | grep -E "Class|Machine"
  done
' bash "$(basename "$APK")" "$ABIS"
