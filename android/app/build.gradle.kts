plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.ksp)
    alias(libs.plugins.hilt)
}

import java.util.Properties

val localProps = Properties().apply {
    rootProject.file("local.properties").takeIf { it.exists() }?.inputStream()?.use(::load)
}

// ABI для сборки. По умолчанию только armeabi-v7a (целевая Android TV-приставка).
// Релиз со всеми ABI: ./gradlew assembleRelease -Pairspoon.abis=armeabi-v7a,arm64-v8a,x86_64
val allAbis = (findProperty("airspoon.abis") as String? ?: "armeabi-v7a")
    .split(",").map { it.trim() }.filter { it.isNotEmpty() }

android {
    namespace = "io.github.jqssun.airplay"
    compileSdk = 37
    ndkVersion = "27.0.12077973"

    signingConfigs {
        // Отладочный ключ лежит в репозитории (docker/debug.keystore, пароль android, не секрет):
        // подпись одинакова при каждой сборке, и `adb install -r` обновляет приложение без удаления
        // (иначе пропадают настройки и выданные права). Для магазина используется ключ release ниже.
        getByName("debug") {
            storeFile = file("../docker/debug.keystore")
            storePassword = "android"
            keyAlias = "androiddebugkey"
            keyPassword = "android"
        }
        if (localProps.containsKey("storeFile")) {
            create("release") {
                storeFile = file(localProps.getProperty("storeFile"))
                storePassword = localProps.getProperty("storePassword")
                keyAlias = localProps.getProperty("keyAlias")
                keyPassword = localProps.getProperty("keyPassword")
            }
        }
    }

    defaultConfig {
        // applicationId отличается от namespace: Kotlin-пакет и JNI-символы остаются
        // io.github.jqssun.airplay (см. NOTICE.md), меняется только идентификатор приложения.
        applicationId = "app.airspoon.tv"
        minSdk = 23
        targetSdk = 37
        versionCode = 1
        versionName = "0.1.0" // основа: upstream 0.0.36

        externalNativeBuild {
            cmake {
                arguments += "-DANDROID_STL=c++_shared"
                arguments += "-DANDROID_SUPPORT_FLEXIBLE_PAGE_SIZES=ON"
            }
        }
    }

    externalNativeBuild {
        cmake {
            path = file("src/main/cpp/CMakeLists.txt")
        }
    }

    buildTypes {
        debug {
            ndk { abiFilters += allAbis }
        }
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfigs.findByName("release")?.let { signingConfig = it }
            ndk { abiFilters += allAbis }
        }
        // debuggable build with HWASan (arm64) + UBSan in native code
        create("sanitize") {
            initWith(getByName("debug"))
            matchingFallbacks += "debug"
            ndk {
                abiFilters.clear()
                abiFilters += "arm64-v8a"
            }
            externalNativeBuild {
                cmake { arguments += "-DSANITIZE=ON" }
            }
        }
    }

    dependenciesInfo {
        includeInApk = false
        includeInBundle = false
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        compose = true
        prefab = true
    }
}

// Патчи UxPlay (src/main/cpp/patches/UxPlay/*.patch) УЖЕ применены к исходникам в
// src/main/cpp/third_party/UxPlay. В форке у UxPlay нет собственного git-репозитория,
// а «git apply» внутри родительского репозитория молча пропускает файлы, поэтому
// исходная задача applyUxplayPatches удалена. Каталог patches/ оставлен как справка.

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime)
    implementation(libs.androidx.lifecycle.service)
    implementation(libs.androidx.lifecycle.viewmodel)
    implementation(libs.androidx.datastore.prefs)
    implementation(libs.androidx.media)
    implementation(libs.media3.exoplayer)
    implementation(libs.media3.exoplayer.hls)
    implementation(libs.media3.ui.compose.material3)
    implementation(libs.media3.transformer)
    implementation(libs.kotlinx.coroutines)
    implementation(libs.oboe)

    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.ui.graphics)
    implementation(libs.compose.ui.tooling.preview)
    implementation(libs.compose.material3)
    implementation(libs.compose.material.icons)
    debugImplementation(libs.compose.ui.tooling)

    implementation(libs.hilt.android)
    ksp(libs.hilt.compiler)
    implementation(libs.hilt.navigation.compose)

    testImplementation(libs.junit)
}
