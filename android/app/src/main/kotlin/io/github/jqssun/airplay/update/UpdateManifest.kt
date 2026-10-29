package io.github.jqssun.airplay.update

import org.json.JSONObject
import java.io.File
import java.security.MessageDigest

const val UPDATE_MANIFEST_NAME = "airspoon-update.json"
const val UPDATE_MANIFEST_SIGNATURE_NAME = "$UPDATE_MANIFEST_NAME.sig"
const val MAX_UPDATE_MANIFEST_SIZE = 64 * 1024

fun interface ManifestSignatureVerifier {
    fun verify(publicKey: ByteArray, message: ByteArray, signature: ByteArray): Boolean
}

data class ManifestAsset(val name: String, val size: Long, val sha256: String) {
    fun verifyFile(file: File) {
        if (file.length() != size) throw SecurityException("Размер обновления не совпадает с подписанным манифестом")
        val hash = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(32 * 1024)
            var count: Int
            while (input.read(buffer).also { count = it } != -1) hash.update(buffer, 0, count)
        }
        val actual = hash.digest().joinToString("") { "%02x".format(it) }
        if (actual != sha256) throw SecurityException("SHA-256 обновления не совпадает с подписанным манифестом")
    }
}

data class UpdateManifest(val version: String, val assets: List<ManifestAsset>) {
    fun match(asset: ReleaseAsset): ManifestAsset =
        assets.singleOrNull { it.name == asset.name && it.size == asset.size }
            ?: throw SecurityException("Имя или размер обновления не совпадает с подписанным манифестом")
}

// Сначала проверяется подпись точных байтов, только затем разбирается JSON.
fun verifyUpdateManifest(
    data: ByteArray,
    signature: ByteArray,
    publicKey: ByteArray,
    version: String,
    verifier: ManifestSignatureVerifier
): UpdateManifest {
    if (publicKey.size != 32 || signature.size != 64 || data.size > MAX_UPDATE_MANIFEST_SIZE ||
        !verifier.verify(publicKey, data, signature)) {
        throw SecurityException("Подпись манифеста обновления недействительна")
    }
    try {
        val json = JSONObject(data.toString(Charsets.UTF_8))
        val manifestVersion = json.getString("version")
        if (!Regex("""^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$""").matches(manifestVersion) ||
            manifestVersion != version) {
            throw SecurityException("Версия манифеста не совпадает с версией релиза")
        }
        val entries = json.getJSONArray("assets")
        val assets = (0 until entries.length()).map { i ->
            val entry = entries.getJSONObject(i)
            val size = entry.get("size")
            // JSONObject.getLong также принимает строки и дроби: их запрещаем.
            if (size !is Long && size !is Int) throw SecurityException("Недопустимый размер в манифесте обновления")
            ManifestAsset(entry.getString("name"), (size as Number).toLong(), entry.getString("sha256")).also {
                if (it.name.isEmpty() || '/' in it.name || '\\' in it.name || it.size <= 0 ||
                    !Regex("[0-9a-f]{64}").matches(it.sha256)) {
                    throw SecurityException("Недопустимый файл в манифесте обновления")
                }
            }
        }
        if (assets.isEmpty() || assets.map { it.name }.distinct().size != assets.size) {
            throw SecurityException("Пустой манифест обновления или повтор имени файла")
        }
        return UpdateManifest(manifestVersion, assets)
    } catch (e: SecurityException) {
        throw e
    } catch (e: Exception) {
        throw SecurityException("Не удалось разобрать манифест обновления", e)
    }
}
