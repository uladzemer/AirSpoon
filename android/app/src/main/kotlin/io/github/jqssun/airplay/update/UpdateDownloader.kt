package io.github.jqssun.airplay.update

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
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

    /** Обязательная проверка подписанного манифеста перед передачей APK установщику. */
    suspend fun downloadUpdate(context: Context, release: ReleaseInfo, onProgress: (Int) -> Unit): File {
        var completedFile: File? = null
        try {
            return withContext(Dispatchers.IO) {
                val metadata = mutableListOf<File>()
                var apk: File? = null
                var complete = false
                try {
                    if (release.apkAsset.name != TARGET_APK_ASSET ||
                        release.tagName.removePrefix("v") != release.versionName) {
                        throw SecurityException("Недопустимая версия или имя файла обновления")
                    }
                    suspend fun readMetadata(name: String, limit: Long): ByteArray {
                        val asset = release.assets.singleOrNull { it.name == name }
                            ?: throw SecurityException("В релизе отсутствует единственный файл $name")
                        if (asset.size <= 0 || asset.size > limit) {
                            throw SecurityException("Недопустимый размер файла $name")
                        }
                        val file = download(context, asset) {}
                        metadata += file
                        return file.readBytes()
                    }
                    val data = readMetadata(UPDATE_MANIFEST_NAME, MAX_UPDATE_MANIFEST_SIZE.toLong())
                    val signature = readMetadata(UPDATE_MANIFEST_SIGNATURE_NAME, 256)
                    val manifest = UpdateSignature.verify(data, signature, release.versionName)
                    val signedAsset = manifest.match(release.apkAsset)
                    val downloaded = download(context, release.apkAsset, onProgress)
                    apk = downloaded
                    signedAsset.verifyFile(downloaded)
                    coroutineContext.ensureActive()
                    complete = true
                    completedFile = downloaded
                    downloaded
                } finally {
                    metadata.forEach { it.delete() }
                    if (!complete) apk?.delete()
                }
            }
        } catch (e: Throwable) {
            withContext(NonCancellable + Dispatchers.IO) { completedFile?.delete() }
            throw e
        }
    }

    /**
     * Скачивание файла обновления в cacheDir/updates/.
     * Поддерживает только HTTPS, проверяет размер (не больше asset.size) и SHA-256 (если задан digest).
     */
    private suspend fun download(
        context: Context,
        asset: ReleaseAsset,
        onProgress: (Int) -> Unit
    ): File {
        var completedFile: File? = null
        try {
            return withContext(Dispatchers.IO) {
                val updatesDir = File(context.cacheDir, "updates").apply { mkdirs() }
                val tempFile = File.createTempFile("airspoon-update-", ".apk.part", updatesDir)
                val targetFile = File(updatesDir, tempFile.name.removeSuffix(".part"))

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

                    if (redirects >= MAX_REDIRECTS) throw IllegalStateException("Слишком много перенаправлений при скачивании обновления")

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
                        val expectedDigest = digestStr.substringAfter(':').trim().lowercase()
                        val actualDigest = messageDigest.digest().joinToString("") { "%02x".format(it) }
                        if (!actualDigest.equals(expectedDigest, ignoreCase = true)) {
                            throw IllegalStateException("Контрольная сумма SHA-256 не совпадает")
                        }
                    }

                    coroutineContext.ensureActive()
                    if (!tempFile.renameTo(targetFile)) {
                        // Если rename не сработал (например, между разделами), копируем
                        tempFile.copyTo(targetFile, overwrite = true)
                        tempFile.delete()
                    }

                    onProgress(100)
                    coroutineContext.ensureActive()
                    completedFile = targetFile
                    targetFile
                } catch (e: Throwable) {
                    if (tempFile.exists()) tempFile.delete()
                    targetFile.delete()
                    throw e
                }
            }
        } catch (e: Throwable) {
            withContext(NonCancellable + Dispatchers.IO) { completedFile?.delete() }
            throw e
        }
    }
}
