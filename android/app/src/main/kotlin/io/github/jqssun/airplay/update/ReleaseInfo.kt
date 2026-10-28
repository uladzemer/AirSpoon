package io.github.jqssun.airplay.update

import org.json.JSONObject

/**
 * Семантическая версия (X.Y.Z с опциональным префиксом 'v').
 */
data class SemanticVersion(val major: Int, val minor: Int, val patch: Int) : Comparable<SemanticVersion> {
    override fun compareTo(other: SemanticVersion): Int {
        if (major != other.major) return major.compareTo(other.major)
        if (minor != other.minor) return minor.compareTo(other.minor)
        return patch.compareTo(other.patch)
    }

    companion object {
        private val REGEX = Regex("""^v?(\d+)\.(\d+)\.(\d+)$""")

        fun parse(versionStr: String?): SemanticVersion? {
            if (versionStr.isNullOrBlank()) return null
            val match = REGEX.matchEntire(versionStr.trim()) ?: return null
            val (x, y, z) = match.destructured
            val major = x.toIntOrNull() ?: return null
            val minor = y.toIntOrNull() ?: return null
            val patch = z.toIntOrNull() ?: return null
            return SemanticVersion(major, minor, patch)
        }
    }
}

/**
 * Ассет релиза GitHub.
 */
data class ReleaseAsset(
    val name: String,
    val size: Long,
    val downloadUrl: String,
    val digest: String? = null
)

/**
 * Информация о релизе с GitHub.
 */
data class ReleaseInfo(
    val tagName: String,
    val versionName: String,
    val htmlUrl: String,
    val apkAsset: ReleaseAsset
)

const val TARGET_APK_ASSET = "airspoon-debug-armeabi-v7a.apk"

/**
 * Разбор JSON ответа GitHub Releases (/releases/latest).
 * Ищет ассет TARGET_APK_ASSET. Нераспознанный формат или отсутствие ассета возвращает null.
 */
fun parseRelease(jsonStr: String): ReleaseInfo? = try {
    val json = JSONObject(jsonStr)
    val tagName = json.optString("tag_name", "").trim()
    if (tagName.isEmpty()) null
    else {
        val htmlUrl = json.optString("html_url", "")
        val assets = json.optJSONArray("assets")
        var matchedAsset: ReleaseAsset? = null
        if (assets != null) {
            for (i in 0 until assets.length()) {
                val assetObj = assets.optJSONObject(i) ?: continue
                val name = assetObj.optString("name", "")
                if (name == TARGET_APK_ASSET) {
                    val size = assetObj.optLong("size", -1L)
                    val downloadUrl = assetObj.optString("browser_download_url", "")
                    if (downloadUrl.isEmpty()) continue
                    val digest = if (assetObj.has("digest") && !assetObj.isNull("digest")) {
                        assetObj.optString("digest")
                    } else null
                    matchedAsset = ReleaseAsset(
                        name = name,
                        size = size,
                        downloadUrl = downloadUrl,
                        digest = digest
                    )
                    break
                }
            }
        }
        matchedAsset?.let { asset ->
            val versionName = if (tagName.startsWith("v", ignoreCase = true)) tagName.substring(1) else tagName
            ReleaseInfo(
                tagName = tagName,
                versionName = versionName,
                htmlUrl = htmlUrl,
                apkAsset = asset
            )
        }
    }
} catch (_: Exception) {
    null
}

/**
 * Проверка, является ли релиз более новым, чем текущая версия приложения.
 * Сравнение версий vX.Y.Z с текущей версией строго числом; нераспознанное -> не новее (false).
 */
fun isUpdateAvailable(currentVersion: String?, release: ReleaseInfo?): Boolean {
    if (release == null) return false
    val current = SemanticVersion.parse(currentVersion) ?: return false
    val remote = SemanticVersion.parse(release.tagName) ?: return false
    return remote > current
}
