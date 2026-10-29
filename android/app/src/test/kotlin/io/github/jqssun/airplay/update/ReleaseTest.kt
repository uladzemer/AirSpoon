package io.github.jqssun.airplay.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ReleaseTest {

    @Test
    fun `разбор семантических версий`() {
        assertEquals(SemanticVersion(0, 1, 0), SemanticVersion.parse("0.1.0"))
        assertEquals(SemanticVersion(0, 1, 0), SemanticVersion.parse("v0.1.0"))
        assertEquals(SemanticVersion(1, 2, 3), SemanticVersion.parse("v1.2.3"))
        assertEquals(SemanticVersion(10, 20, 30), SemanticVersion.parse("  v10.20.30  "))

        assertNull(SemanticVersion.parse(null))
        assertNull(SemanticVersion.parse(""))
        assertNull(SemanticVersion.parse("   "))
        assertNull(SemanticVersion.parse("1.2"))
        assertNull(SemanticVersion.parse("1.2.3.4"))
        assertNull(SemanticVersion.parse("1.2.3-alpha"))
        assertNull(SemanticVersion.parse("vX.Y.Z"))
        assertNull(SemanticVersion.parse("random_string"))
    }

    @Test
    fun `сравнение версий`() {
        val v0_1_0 = SemanticVersion.parse("v0.1.0")!!
        val v0_1_1 = SemanticVersion.parse("v0.1.1")!!
        val v0_2_0 = SemanticVersion.parse("0.2.0")!!
        val v1_0_0 = SemanticVersion.parse("1.0.0")!!

        assertTrue(v0_1_1 > v0_1_0)
        assertTrue(v0_2_0 > v0_1_1)
        assertTrue(v1_0_0 > v0_2_0)
        assertFalse(v0_1_0 > v0_1_0)
        assertFalse(v0_1_0 > v0_1_1)
        assertEquals(0, v0_1_0.compareTo(SemanticVersion.parse("0.1.0")!!))
    }

    @Test
    fun `проверка доступности обновления`() {
        val release = ReleaseInfo(
            tagName = "v0.2.0",
            versionName = "0.2.0",
            htmlUrl = "https://github.com/uladzemer/AirSpoon/releases/tag/v0.2.0",
            apkAsset = ReleaseAsset("airspoon-armeabi-v7a.apk", 12345L, "https://example.com/apk")
        )

        // Более новая версия
        assertTrue(isUpdateAvailable("0.1.0", release))
        assertTrue(isUpdateAvailable("v0.1.99", release))

        // Такая же или старая
        assertFalse(isUpdateAvailable("0.2.0", release))
        assertFalse(isUpdateAvailable("v0.2.0", release))
        assertFalse(isUpdateAvailable("0.3.0", release))
        assertFalse(isUpdateAvailable("1.0.0", release))

        // Нераспознанное -> не новее
        assertFalse(isUpdateAvailable("custom-build", release))
        assertFalse(isUpdateAvailable("0.1.0", release.copy(tagName = "invalid-tag")))
        assertFalse(isUpdateAvailable("0.1.0", null))
        assertFalse(isUpdateAvailable(null, release))
    }

    @Test
    fun `разбор JSON ответа релиза с digest`() {
        val json = """
            {
              "tag_name": "v1.2.3",
              "html_url": "https://github.com/uladzemer/AirSpoon/releases/tag/v1.2.3",
              "assets": [
                {
                  "name": "AirSpoonSender.exe",
                  "size": 5000000,
                  "browser_download_url": "https://example.com/AirSpoonSender.exe"
                },
                {
                  "name": "airspoon-debug-armeabi-v7a.apk",
                  "size": 15000000,
                  "browser_download_url": "https://example.com/legacy.apk"
                },
                {
                  "name": "airspoon-armeabi-v7a.apk",
                  "size": 15000000,
                  "browser_download_url": "https://github.com/uladzemer/AirSpoon/releases/download/v1.2.3/airspoon-armeabi-v7a.apk",
                  "digest": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
                }
              ]
            }
        """.trimIndent()

        val parsed = parseRelease(json)
        assertNotNull(parsed)
        assertEquals("v1.2.3", parsed!!.tagName)
        assertEquals("1.2.3", parsed.versionName)
        assertEquals("https://github.com/uladzemer/AirSpoon/releases/tag/v1.2.3", parsed.htmlUrl)
        assertEquals("airspoon-armeabi-v7a.apk", parsed.apkAsset.name)
        assertEquals(15000000L, parsed.apkAsset.size)
        assertEquals(
            "https://github.com/uladzemer/AirSpoon/releases/download/v1.2.3/airspoon-armeabi-v7a.apk",
            parsed.apkAsset.downloadUrl
        )
        assertEquals(
            "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            parsed.apkAsset.digest
        )
    }

    @Test
    fun `разбор JSON без нужного APK ассета возвращает null`() {
        val json = """
            {
              "tag_name": "v1.2.3",
              "html_url": "https://github.com/uladzemer/AirSpoon/releases/tag/v1.2.3",
              "assets": [
                {
                  "name": "AirSpoonSender.exe",
                  "size": 5000000,
                  "browser_download_url": "https://example.com/AirSpoonSender.exe"
                }
              ]
            }
        """.trimIndent()

        assertNull(parseRelease(json))
    }

    @Test
    fun `разбор некорректного JSON возвращает null`() {
        assertNull(parseRelease(""))
        assertNull(parseRelease("not-json"))
        assertNull(parseRelease("{}"))
        assertNull(parseRelease("""{"tag_name": "v1.0.0"}"""))
    }
}
