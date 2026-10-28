package io.github.jqssun.airplay.update

import android.content.Context
import android.content.SharedPreferences
import android.util.Log
import io.github.jqssun.airplay.BuildConfig
import io.github.jqssun.airplay.Prefs
import io.github.jqssun.airplay.R
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File

/**
 * Состояния диалога обновления в Compose.
 */
sealed class UpdateDialogState {
    data class Offer(val release: ReleaseInfo) : UpdateDialogState()
    data class Downloading(val release: ReleaseInfo, val progress: Int) : UpdateDialogState()
    data class PermissionRequired(val release: ReleaseInfo, val apkFile: File) : UpdateDialogState()
    data class Error(val message: String) : UpdateDialogState()
}

/**
 * Менеджер проверки и установки обновлений.
 */
object UpdateManager {
    private const val TAG = "UpdateManager"
    private const val CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000L // 24 часа

    private val scope = CoroutineScope(Dispatchers.Main + SupervisorJob())
    private var checkJob: Job? = null
    private var downloadJob: Job? = null
    private var pendingRelease: ReleaseInfo? = null
    private var pendingApk: File? = null
    private var appContext: Context? = null
    private var sessionActive: () -> Boolean = { false }

    private val _dialogState = MutableStateFlow<UpdateDialogState?>(null)
    val dialogState: StateFlow<UpdateDialogState?> = _dialogState.asStateFlow()

    /**
     * Проверка обновлений на GitHub Releases.
     * Запускается только если включён BuildConfig.UPDATE_CHECK и настройка check_updates,
     * не чаще раза в 24 часа (время хранится в SharedPreferences).
     * Если идёт показ экрана/видео, предложение откладывается до возврата на экран ожидания.
     */
    fun checkForUpdates(
        context: Context,
        isSessionActive: () -> Boolean,
        force: Boolean = false
    ) {
        if (!BuildConfig.UPDATE_CHECK) {
            Log.d(TAG, "Update check disabled in build")
            return
        }

        appContext = context.applicationContext
        val prefs = context.getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)
        if (!prefs.getBoolean(Prefs.CHECK_UPDATES.key, Prefs.CHECK_UPDATES.def)) {
            Log.d(TAG, "Update check disabled in settings")
            return
        }

        val now = System.currentTimeMillis()
        val lastCheck = prefs.getLong(Prefs.KEY_LAST_UPDATE_CHECK_MS, 0L)
        if (!force && (now - lastCheck < CHECK_INTERVAL_MS)) {
            Log.d(TAG, "Skipping update check: last checked ${(now - lastCheck) / 1000}s ago")
            return
        }
        if (checkJob?.isActive == true || downloadJob?.isActive == true || _dialogState.value != null) return
        sessionActive = isSessionActive

        checkJob = scope.launch {
            Log.i(TAG, "Checking for updates (current version: ${BuildConfig.VERSION_NAME})...")
            val release = UpdateChecker.fetchLatestRelease(BuildConfig.VERSION_NAME)
            val newer = release != null && isUpdateAvailable(BuildConfig.VERSION_NAME, release)
            // Время ставим, когда ответ получен и обновления нет; найденное обновление
            // живёт только в памяти, поэтому сутки отсчитываются от «Позже» (onUserDismiss).
            if (release != null && !newer) markChecked(prefs)
            if (release != null && newer) {
                Log.i(TAG, "Update available: ${release.tagName}")
                if (isSessionActive()) {
                    Log.i(TAG, "Session active, holding update offer until session ends")
                    pendingRelease = release
                } else {
                    _dialogState.value = UpdateDialogState.Offer(release)
                }
            } else {
                Log.d(TAG, "No update available or unrecognized release")
            }
        }
    }

    /**
     * Вызывается при переходе на экран ожидания: если обновление было найдено во время сеанса,
     * показываем диалог сейчас.
     */
    fun onReturnToWaitingScreen() {
        pendingApk?.let { apk ->
            pendingApk = null
            appContext?.let { install(it, apk) }
            return
        }
        val release = pendingRelease ?: return
        if (_dialogState.value == null) {
            Log.i(TAG, "Presenting deferred update offer for ${release.tagName}")
            pendingRelease = null
            _dialogState.value = UpdateDialogState.Offer(release)
        }
    }

    /**
     * Пользователь нажал «Позже» — скрыть диалог до следующей проверки.
     */
    fun onUserDismiss() {
        pendingRelease = null
        pendingApk = null
        _dialogState.value = null
        appContext?.let { markChecked(it.getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)) }
    }

    private fun markChecked(prefs: SharedPreferences) =
        prefs.edit().putLong(Prefs.KEY_LAST_UPDATE_CHECK_MS, System.currentTimeMillis()).apply()

    /**
     * Запуск скачивания обновления.
     */
    fun startDownload(context: Context, release: ReleaseInfo) {
        appContext = context.applicationContext
        downloadJob?.cancel()
        _dialogState.value = UpdateDialogState.Downloading(release, 0)

        downloadJob = scope.launch {
            try {
                val apkFile = UpdateDownloader.download(context, release.apkAsset) { progress ->
                    _dialogState.value = UpdateDialogState.Downloading(release, progress)
                }
                onDownloadCompleted(context, release, apkFile)
            } catch (e: CancellationException) {
                Log.d(TAG, "Download cancelled")
                _dialogState.value = null
            } catch (e: Throwable) {
                Log.e(TAG, "Download error", e)
                _dialogState.value = UpdateDialogState.Error(e.message ?: context.getString(R.string.update_download_failed))
            }
        }
    }

    /**
     * Отмена текущего скачивания пользователем.
     */
    fun cancelDownload() {
        downloadJob?.cancel()
        downloadJob = null
        _dialogState.value = null
    }

    private fun onDownloadCompleted(context: Context, release: ReleaseInfo, apkFile: File) {
        if (!UpdateInstaller.canRequestPackageInstalls(context)) {
            _dialogState.value = UpdateDialogState.PermissionRequired(release, apkFile)
        } else {
            install(context, apkFile)
        }
    }

    /**
     * Пользователь выдал разрешение и продолжает установку.
     */
    fun onInstallWithPermission(context: Context, apkFile: File) = install(context, apkFile)

    // Запись APK в сессию — десятки МБ, поэтому не в главном потоке; сбой — в диалог, а не авария.
    // Если тем временем начался показ, ставим после него: установка закрывает приложение.
    private fun install(context: Context, apkFile: File) {
        _dialogState.value = null
        if (sessionActive()) {
            Log.i(TAG, "Session active, holding install until session ends")
            pendingApk = apkFile
            return
        }
        scope.launch {
            try {
                withContext(Dispatchers.IO) { UpdateInstaller.installPackage(context.applicationContext, apkFile) }
            } catch (e: Exception) {
                Log.e(TAG, "Install session error", e)
                _dialogState.value = UpdateDialogState.Error(e.message ?: context.getString(R.string.update_install_failed))
            }
        }
    }

    /**
     * Ошибка сессии PackageInstaller.
     */
    fun onInstallError(message: String) {
        _dialogState.value = UpdateDialogState.Error(message)
    }

    /**
     * Успешная установка PackageInstaller.
     */
    fun onInstallSuccess() {
        _dialogState.value = null
    }
}
