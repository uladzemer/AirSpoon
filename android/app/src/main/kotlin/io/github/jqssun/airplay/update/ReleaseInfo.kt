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
    val apkAsset: ReleaseAsset,
    val assets: List<ReleaseAsset> = emptyList()
)

const val TARGET_APK_ASSET = "airspoon-armeabi-v7a.apk"

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
        val releaseAssets = mutableListOf<ReleaseAsset>()
        if (assets != null) {
            for (i in 0 until assets.length()) {
                val assetObj = assets.optJSONObject(i) ?: continue
                val name = assetObj.optString("name", "")
                releaseAssets += ReleaseAsset(
                    name, assetObj.optLong("size", -1L),
                    assetObj.optString("browser_download_url", ""),
                    if (assetObj.isNull("digest")) null else assetObj.optString("digest")
                )
            }
        }
        val matchedAsset = releaseAssets.singleOrNull { it.name == TARGET_APK_ASSET && it.downloadUrl.isNotEmpty() }
        matchedAsset?.let { asset ->
            val versionName = if (tagName.startsWith("v", ignoreCase = true)) tagName.substring(1) else tagName
            ReleaseInfo(
                tagName = tagName,
                versionName = versionName,
                htmlUrl = htmlUrl,
                apkAsset = asset,
                assets = releaseAssets
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
