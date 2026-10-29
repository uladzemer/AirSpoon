# Образ для сборки AirSpoon (Android) без установки Android SDK на хост.
# Проверено: сборка assembleDebug (armeabi-v7a) проходит. Версии взяты из app/build.gradle.kts и
# gradle/libs.versions.toml (JDK 21, NDK 27.0.12077973, CMake 3.22.1, compileSdk 37).
# Образ закреплён по digest (21-jdk-jammy на 2026-10-26), чтобы JDK не менялся между сборками.
FROM eclipse-temurin:21-jdk-jammy@sha256:742daacb9316be4b6fc60258f6e4ac2a11882f6c597373c92290a57bfad0d2e0

ENV ANDROID_HOME=/opt/android-sdk
ENV ANDROID_SDK_ROOT=/opt/android-sdk
ENV PATH=$PATH:/opt/android-sdk/cmdline-tools/latest/bin

RUN apt-get update \
 && apt-get install -y --no-install-recommends unzip curl git make ninja-build ca-certificates perl python3 patch build-essential \
 && rm -rf /var/lib/apt/lists/*

# Android command-line tools 12.0 (11076708), SHA-256 официального ZIP.
# https://dl.google.com/android/repository/repository2-3.xml публикует для него SHA-1
# d313adb7aedccf6cf0cfca51ec180f0059f5f8f8; архив сверен с ним перед вычислением SHA-256.
RUN mkdir -p /opt/android-sdk/cmdline-tools \
 && curl -fsSL -o /tmp/cmdline-tools.zip https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip \
 && echo '2d2d50857e4eb553af5a6dc3ad507a17adf43d115264b1afc116f95c92e5e258  /tmp/cmdline-tools.zip' | sha256sum -c - \
 && unzip -q /tmp/cmdline-tools.zip -d /opt/android-sdk/cmdline-tools \
 && mv /opt/android-sdk/cmdline-tools/cmdline-tools /opt/android-sdk/cmdline-tools/latest \
 && rm /tmp/cmdline-tools.zip

RUN yes | sdkmanager --licenses >/dev/null \
 && sdkmanager "platform-tools" "platforms;android-37.0" "ndk;27.0.12077973" "cmake;3.22.1"

WORKDIR /work
