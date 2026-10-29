package io.github.jqssun.airplay.update

import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.provider.Settings
import io.github.jqssun.airplay.appSettingsIntent
import java.io.File

object UpdateInstaller {

    /**
     * Проверка, разрешена ли установка из неизвестных источников (на Android O+).
     */
    fun canRequestPackageInstalls(context: Context): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            context.packageManager.canRequestPackageInstalls()
        } else {
            true
        }

    /**
     * Открытие системного экрана разрешения установки неизвестных приложений с запасными вариантами для ТВ.
     */
    fun openInstallPermissionSettings(context: Context): Boolean {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val intent = Intent(
                Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                Uri.parse("package:${context.packageName}")
            ).apply {
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            try {
                context.startActivity(intent)
                return true
            } catch (_: Exception) {
                // Запасной вариант 1: экран сведений о приложении
                try {
                    context.startActivity(context.appSettingsIntent().apply {
                        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    })
                    return true
                } catch (_: Exception) {
                    // Запасной вариант 2: общий экран настроек
                    try {
                        context.startActivity(Intent(Settings.ACTION_SETTINGS).apply {
                            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                        })
                        return true
                    } catch (_: Exception) {
                        return false
                    }
                }
            }
        }
        return false
    }

    /**
     * Установка APK через PackageInstaller (MODE_FULL_INSTALL).
     * Статус установки отправляется в InstallStatusReceiver через PendingIntent.
     */
    fun installPackage(context: Context, apkFile: File, canCommit: () -> Boolean): Boolean {
        val packageInstaller = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
            setSize(apkFile.length())
        }

        val sessionId = packageInstaller.createSession(params)
        val session = packageInstaller.openSession(sessionId)

        try {
            return writeAndCommit(context, session, sessionId, apkFile, canCommit)
        } catch (e: Exception) {
            runCatching { session.abandon() }
            throw e
        }
    }

    private fun writeAndCommit(context: Context, session: PackageInstaller.Session, sessionId: Int, apkFile: File,
                               canCommit: () -> Boolean): Boolean {
        session.use { s ->
            s.openWrite("airspoon_update", 0, apkFile.length()).use { out ->
                apkFile.inputStream().use { input ->
                    input.copyTo(out)
                }
                s.fsync(out)
            }

            val intent = Intent(context, InstallStatusReceiver::class.java).apply {
                action = InstallStatusReceiver.ACTION_INSTALL_STATUS
            }
            val flags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
            } else {
                PendingIntent.FLAG_UPDATE_CURRENT
            }
            val pendingIntent = PendingIntent.getBroadcast(
                context,
                sessionId,
                intent,
                flags
            )

            if (!canCommit()) {
                s.abandon()
                return false
            }
            s.commit(pendingIntent.intentSender)
            return true
        }
    }
}
