package io.github.jqssun.airplay.update

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import kotlin.coroutines.coroutineContext

object UpdateDownloader {
    private const val BUFFER_SIZE = 32 * 1024
    private const val TIMEOUT_MS = 30_000
    private const val MAX_REDIRECTS = 5

    /**
     * Скачивание файла обновления в cacheDir/updates/.
     * Поддерживает только HTTPS, проверяет размер (не больше asset.size) и SHA-256 (если задан digest).
     */
    suspend fun download(
        context: Context,
        asset: ReleaseAsset,
        onProgress: (Int) -> Unit
    ): File = withContext(Dispatchers.IO) {
        val updatesDir = File(context.cacheDir, "updates").apply { mkdirs() }
        val targetFile = File(updatesDir, asset.name)
        val tempFile = File(updatesDir, "${asset.name}.part")
        if (tempFile.exists()) tempFile.delete()

        try {
            var currentUrl = asset.downloadUrl
            var conn: HttpURLConnection? = null
            var redirects = 0

            while (redirects < MAX_REDIRECTS) {
                val parsedUrl = URL(currentUrl)
                if (!parsedUrl.protocol.equals("https", ignoreCase = true)) {
                    throw SecurityException("Только HTTPS разрешён для загрузки обновления: $currentUrl")
                }

                conn = (parsedUrl.openConnection() as HttpURLConnection).apply {
                    connectTimeout = TIMEOUT_MS
                    readTimeout = TIMEOUT_MS
                    instanceFollowRedirects = false
                    setRequestProperty("User-Agent", "AirSpoon-Receiver")
                }

                val status = conn.responseCode
                if (status in listOf(
                        HttpURLConnection.HTTP_MOVED_PERM,
                        HttpURLConnection.HTTP_MOVED_TEMP,
                        HttpURLConnection.HTTP_SEE_OTHER,
                        307,
                        308
                    )
                ) {
                    val location = conn.getHeaderField("Location")
                        ?: throw IllegalStateException("Редирект без заголовка Location")
                    conn.disconnect()
                    currentUrl = if (location.startsWith("http://") || location.startsWith("https://")) {
                        location
                    } else {
                        URL(parsedUrl, location).toString()
                    }
                    redirects++
                    continue
                }

                if (status != HttpURLConnection.HTTP_OK) {
                    throw IllegalStateException("Сбой скачивания, HTTP $status")
                }
                break
            }

            val connection = conn ?: throw IllegalStateException("Не удалось установить соединение")
            val totalBytes = if (asset.size > 0) asset.size else connection.contentLengthLong

            val messageDigest = if (asset.digest?.startsWith("sha256:", ignoreCase = true) == true) {
                MessageDigest.getInstance("SHA-256")
            } else null

            var bytesRead = 0L
            var lastPercent = -1

            connection.inputStream.use { input ->
                FileOutputStream(tempFile).use { output ->
                    val buffer = ByteArray(BUFFER_SIZE)
                    var read: Int
                    while (input.read(buffer).also { read = it } != -1) {
                        coroutineContext.ensureActive()

                        bytesRead += read
                        if (asset.size > 0 && bytesRead > asset.size) {
                            throw IllegalStateException("Размер скачанного файла превышает ожидаемый: $bytesRead > ${asset.size}")
                        }

                        output.write(buffer, 0, read)
                        messageDigest?.update(buffer, 0, read)

                        if (totalBytes > 0) {
                            val percent = ((bytesRead * 100) / totalBytes).toInt().coerceIn(0, 100)
                            if (percent != lastPercent) {
                                lastPercent = percent
                                onProgress(percent)
                            }
                        }
                    }
                    output.flush()
                }
            }

            if (asset.size > 0 && tempFile.length() != asset.size) {
                throw IllegalStateException("Размер файла не совпадает: ${tempFile.length()} != ${asset.size}")
            }

            val digestStr = asset.digest
            if (messageDigest != null && digestStr != null) {
                val expectedDigest = digestStr.substringAfter("sha256:").trim().lowercase()
                val actualDigest = messageDigest.digest().joinToString("") { "%02x".format(it) }
                if (!actualDigest.equals(expectedDigest, ignoreCase = true)) {
                    throw IllegalStateException("Контрольная сумма SHA-256 не совпадает")
                }
            }

            if (targetFile.exists()) targetFile.delete()
            if (!tempFile.renameTo(targetFile)) {
                // Если rename не сработал (например, между разделами), копируем
                tempFile.copyTo(targetFile, overwrite = true)
                tempFile.delete()
            }

            onProgress(100)
            targetFile
        } catch (e: Throwable) {
            if (tempFile.exists()) tempFile.delete()
            throw e
        }
    }
}
