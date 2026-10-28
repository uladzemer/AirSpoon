package io.github.jqssun.airplay.update

import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.BufferedReader
import java.net.HttpURLConnection
import java.net.URL

object UpdateChecker {
    private const val TAG = "UpdateChecker"
    const val LATEST_RELEASE_URL = "https://api.github.com/repos/uladzemer/AirSpoon/releases/latest"
    private const val TIMEOUT_MS = 15_000

    /**
     * Запрос к GitHub API для получения информации о последнем релизе.
     * При любых ошибках сети пишем в Logcat и возвращаем null (пользователю ничего не показываем).
     */
    suspend fun fetchLatestRelease(versionName: String): ReleaseInfo? = withContext(Dispatchers.IO) {
        var conn: HttpURLConnection? = null
        try {
            val url = URL(LATEST_RELEASE_URL)
            conn = (url.openConnection() as HttpURLConnection).apply {
                requestMethod = "GET"
                connectTimeout = TIMEOUT_MS
                readTimeout = TIMEOUT_MS
                setRequestProperty("Accept", "application/vnd.github+json")
                setRequestProperty("User-Agent", "AirSpoon-Receiver/$versionName")
                instanceFollowRedirects = true
            }

            val code = conn.responseCode
            if (code != HttpURLConnection.HTTP_OK) {
                Log.w(TAG, "GitHub releases request returned HTTP $code")
                return@withContext null
            }

            val body = conn.inputStream.bufferedReader().use(BufferedReader::readText)
            parseRelease(body)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to check for updates: ${e.message}")
            null
        } finally {
            conn?.disconnect()
        }
    }
}
