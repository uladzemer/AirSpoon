package io.github.jqssun.airplay.update

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import android.util.Log
import io.github.jqssun.airplay.R

/**
 * Получатель статуса сессии установки PackageInstaller.
 */
class InstallStatusReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_INSTALL_STATUS) return

        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)
        Log.i(TAG, "Install status received: status=$status, message=$message")

        when (status) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                val confirmIntent = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                    intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                } else {
                    @Suppress("DEPRECATION")
                    intent.getParcelableExtra(Intent.EXTRA_INTENT)
                }
                if (confirmIntent != null) {
                    confirmIntent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    context.startActivity(confirmIntent)
                } else {
                    UpdateManager.onInstallError(context.getString(R.string.update_install_failed))
                }
            }
            PackageInstaller.STATUS_SUCCESS -> {
                Log.i(TAG, "Update installed successfully")
                UpdateManager.onInstallSuccess()
            }
            // человек отказался на системном экране: это не сбой
            PackageInstaller.STATUS_FAILURE_ABORTED -> UpdateManager.onInstallSuccess()
            else -> {
                val err = context.getString(R.string.update_install_failed) + (message?.let { "\n$it" } ?: "")
                Log.e(TAG, "Install failed: $err")
                UpdateManager.onInstallError(err)
            }
        }
    }

    companion object {
        private const val TAG = "InstallStatusReceiver"
        const val ACTION_INSTALL_STATUS = "app.airspoon.tv.INSTALL_STATUS"
    }
}
