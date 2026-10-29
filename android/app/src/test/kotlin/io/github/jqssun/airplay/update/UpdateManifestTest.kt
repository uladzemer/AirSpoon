package io.github.jqssun.airplay.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.File
import java.security.MessageDigest

class UpdateManifestTest {
    private val payload = "trusted apk".toByteArray()
    private val hash = MessageDigest.getInstance("SHA-256").digest(payload).joinToString("") { "%02x".format(it) }
    private val entry = """{"name":"$TARGET_APK_ASSET","size":${payload.size},"sha256":"$hash"}"""
    private val json = """{"version":"1.2.3","assets":[$entry]}"""
    private val key = ByteArray(32)
    private val signature = ByteArray(64)
    private val accept = ManifestSignatureVerifier { _, _, _ -> true }

    private fun verify(text: String = json, version: String = "1.2.3") =
        verifyUpdateManifest(text.toByteArray(), signature, key, version, accept)

    private fun rejects(block: () -> Unit) {
        try {
            block()
            fail("Недоверенное обновление принято")
        } catch (_: SecurityException) {
            // Ожидаемый отказ; JNI не требуется в JVM-тестах.
        }
    }

    @Test
    fun `проверяются точные байты до разбора JSON`() {
        val bytes = json.toByteArray()
        val manifest = verifyUpdateManifest(bytes, signature, key, "1.2.3") { pub, message, sig ->
            assertTrue(pub.contentEquals(key))
            assertTrue(message.contentEquals(bytes))
            assertTrue(sig.contentEquals(signature))
            true
        }
        assertEquals("1.2.3", manifest.version)
        rejects { verifyUpdateManifest(bytes, signature, key, "1.2.3") { _, _, _ -> false } }
        var called = false
        rejects {
            verifyUpdateManifest(bytes, ByteArray(63), key, "1.2.3") { _, _, _ -> called = true; true }
        }
        rejects { verifyUpdateManifest(bytes, signature, ByteArray(31), "1.2.3", accept) }
        rejects { verifyUpdateManifest(ByteArray(MAX_UPDATE_MANIFEST_SIZE + 1), signature, key, "1.2.3", accept) }
        assertFalse(called)
        rejects { verifyUpdateManifest("not JSON".toByteArray(), signature, key, "1.2.3") { _, _, _ -> false } }
    }

    @Test
    fun `неверная версия и некорректные записи запрещены`() {
        rejects { verify(version = "1.2.4") }
        rejects { verify(json.replace("1.2.3", "v1.2.3"), "v1.2.3") }
        rejects { verify("{") }
        rejects { verify("""{"version":"1.2.3","assets":[]}""") }
        rejects { verify("""{"version":"1.2.3","assets":[$entry,$entry]}""") }
        for (invalid in listOf(
            entry.replace("\"size\":${payload.size}", "\"size\":0"),
            entry.replace("\"size\":${payload.size}", "\"size\":\"${payload.size}\""),
            entry.replace("\"size\":${payload.size}", "\"size\":1.5"),
            entry.replace(hash, "bad"),
            entry.replace(TARGET_APK_ASSET, "../file.apk")
        )) {
            rejects { verify("""{"version":"1.2.3","assets":[$invalid]}""") }
        }
    }

    @Test
    fun `имя размер и хеш APK должны совпасть`() {
        val manifest = verify()
        val asset = ReleaseAsset(TARGET_APK_ASSET, payload.size.toLong(), "https://example.com/apk")
        val signed = manifest.match(asset)
        rejects { manifest.match(asset.copy(name = "other.apk")) }
        rejects { manifest.match(asset.copy(size = asset.size + 1)) }
        val file = File.createTempFile("manifest-test-", ".apk")
        try {
            file.writeBytes(payload)
            signed.verifyFile(file)
            file.writeBytes(payload.copyOf().also { it[0] = (it[0].toInt() xor 1).toByte() })
            rejects { signed.verifyFile(file) }
            file.writeBytes(payload + byteArrayOf(0))
            rejects { signed.verifyFile(file) }
        } finally {
            file.delete()
        }
    }

    @Test
    fun `манифест и подпись берутся по точному имени из того же релиза`() {
        val release = parseRelease("""{
            "tag_name":"v1.2.3", "assets":[
                {"name":"$TARGET_APK_ASSET","size":11,"browser_download_url":"https://example.com/apk"},
                {"name":"$UPDATE_MANIFEST_NAME","size":200,"browser_download_url":"https://example.com/manifest"},
                {"name":"$UPDATE_MANIFEST_SIGNATURE_NAME","size":88,"browser_download_url":"https://example.com/sig"}
            ]}
        """)!!
        assertEquals(3, release.assets.size)
        assertEquals("https://example.com/manifest", release.assets.single { it.name == UPDATE_MANIFEST_NAME }.downloadUrl)
        assertEquals("https://example.com/sig", release.assets.single { it.name == UPDATE_MANIFEST_SIGNATURE_NAME }.downloadUrl)
    }
}
