package io.github.jqssun.airplay.download

import android.content.ContentValues
import android.content.Context
import android.media.MediaScannerConnection
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import android.util.Log
import android.widget.Toast
import androidx.media3.common.MediaItem
import androidx.media3.transformer.Composition
import androidx.media3.transformer.ExportException
import androidx.media3.transformer.ExportResult
import androidx.media3.transformer.ProgressHolder
import androidx.media3.transformer.Transformer
import io.github.jqssun.airplay.R
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlin.concurrent.thread
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow

// transformer requires a looper thread
class VideoDownloader(private val context: Context) {

    private val mainHandler = Handler(Looper.getMainLooper())
    private var transformer: Transformer? = null
    private var activeFile: File? = null
    private var generation = 0L

    // null = idle, -1 = unknown, else 0..100
    private val _progress = MutableStateFlow<Int?>(null)
    val progress = _progress.asStateFlow()

    private val _progressTick = object : Runnable {
        override fun run() {
            val t = transformer ?: return
            val holder = ProgressHolder()
            if (t.getProgress(holder) == Transformer.PROGRESS_STATE_AVAILABLE) {
                _progress.value = holder.progress
            }
            mainHandler.postDelayed(this, PROGRESS_INTERVAL_MS)
        }
    }

    fun start(location: String) = mainHandler.post {
        if (transformer != null) return@post
        val jobGeneration = ++generation
        _progress.value = -1
        var file: File? = null
        try {
            val output = File.createTempFile("video_download_", ".mp4", context.cacheDir)
            file = output
            check(output.delete())
            activeFile = output
            val t = Transformer.Builder(context)
                .addListener(object : Transformer.Listener {
                    override fun onCompleted(composition: Composition, result: ExportResult) {
                        if (jobGeneration != generation) {
                            output.delete()
                            return
                        }
                        _reset()
                        thread {
                            val path = _publish(output)
                            output.delete()
                            _toast(
                                jobGeneration,
                                if (path != null) context.getString(R.string.download_saved, path)
                                else context.getString(R.string.download_failed)
                            )
                        }
                    }
                    override fun onError(composition: Composition, result: ExportResult, exception: ExportException) {
                        output.delete()
                        if (jobGeneration != generation) return
                        Log.w(TAG, "export failed", exception)
                        _reset()
                        _toast(jobGeneration, context.getString(R.string.download_failed))
                    }
                })
                .build()
            transformer = t
            t.start(MediaItem.fromUri(location), output.absolutePath)
            mainHandler.postDelayed(_progressTick, PROGRESS_INTERVAL_MS)
        } catch (e: Exception) {
            Log.w(TAG, "export start failed", e)
            transformer?.cancel()
            _reset()
            file?.delete()
            _toast(jobGeneration, context.getString(R.string.download_failed))
        }
    }

    fun cancel() = mainHandler.post {
        ++generation
        val file = activeFile
        transformer?.cancel()
        _reset()
        file?.delete()
    }

    private fun _reset() {
        mainHandler.removeCallbacks(_progressTick)
        transformer = null
        activeFile = null
        _progress.value = null
    }

    private fun _publish(file: File): String? {
        val stamp = SimpleDateFormat("yyyyMMdd_HHmmss_SSS", Locale.US).format(Date())
        val name = "AirSpoon_$stamp.mp4"
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                val resolver = context.contentResolver
                val values = ContentValues().apply {
                    put(MediaStore.Video.Media.DISPLAY_NAME, name)
                    put(MediaStore.Video.Media.MIME_TYPE, "video/mp4")
                    put(MediaStore.Video.Media.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
                    put(MediaStore.Video.Media.IS_PENDING, 1)
                }
                val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values) ?: return null
                resolver.openOutputStream(uri)!!.use { out -> file.inputStream().use { it.copyTo(out) } }
                values.clear()
                values.put(MediaStore.Video.Media.IS_PENDING, 0)
                resolver.update(uri, values, null, null)
                val path = resolver.query(uri, arrayOf(MediaStore.MediaColumns.DATA), null, null, null)?.use {
                    if (it.moveToFirst()) it.getString(0) else null
                }
                return path ?: "${Environment.DIRECTORY_DOWNLOADS}/$name"
            } else {
                // pre-q public storage needs a runtime permission
                val dir = context.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: return null
                val dest = File(dir, name)
                file.copyTo(dest, overwrite = true)
                MediaScannerConnection.scanFile(context, arrayOf(dest.absolutePath), arrayOf("video/mp4"), null)
                return dest.absolutePath
            }
        } catch (e: Exception) {
            Log.w(TAG, "publish failed", e)
            return null
        }
    }

    private fun _toast(jobGeneration: Long, msg: String) = mainHandler.post {
        if (jobGeneration == generation) Toast.makeText(context, msg, Toast.LENGTH_LONG).show()
    }

    companion object {
        private const val TAG = "VideoDownloader"
        private const val PROGRESS_INTERVAL_MS = 500L
    }
}
