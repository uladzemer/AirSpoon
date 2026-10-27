package io.github.jqssun.airplay.service

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.app.NotificationCompat
import io.github.jqssun.airplay.MainActivity
import io.github.jqssun.airplay.R

/**
 * Открывает экран приложения из фона (после загрузки приставки или при подключении телефона).
 *
 * Начиная с Android 10 запускать Activity из фона нельзя. Разрешённые пути:
 *  1. разрешение «Поверх других окон» (SYSTEM_ALERT_WINDOW) — тогда Activity стартует напрямую;
 *  2. запасной путь — полноэкранное уведомление (USE_FULL_SCREEN_INTENT, обычное разрешение,
 *     выдаётся при установке). На части приставок оно показывается лишь как карточка, на которую
 *     нужно нажать пультом.
 *
 * Разрешение (1) выдаётся вручную: Настройки → Приложения → AirSpoon → «Поверх других окон»,
 * либо командой `adb shell appops set app.airspoon.tv SYSTEM_ALERT_WINDOW allow`.
 */
object ScreenLauncher {
    private const val TAG = "ScreenLauncher"
    private const val CHANNEL_ID = "airspoon_open_screen"
    private const val NOTIFICATION_ID = 2

    fun canStartFromBackground(context: Context): Boolean =
        Build.VERSION.SDK_INT < Build.VERSION_CODES.Q || Settings.canDrawOverlays(context)

    fun open(context: Context) {
        val intent = Intent(context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_REORDER_TO_FRONT)
        if (canStartFromBackground(context)) {
            try {
                context.startActivity(intent)
                return
            } catch (e: Exception) {
                Log.w(TAG, "Failed to launch activity, falling back to notification", e)
            }
        }
        showFullScreenNotification(context, intent)
    }

    private fun showFullScreenNotification(context: Context, intent: Intent) {
        val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(
                    CHANNEL_ID,
                    context.getString(R.string.notification_open_channel),
                    NotificationManager.IMPORTANCE_HIGH
                )
            )
        }
        val pi = PendingIntent.getActivity(context, 1, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val notification = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_media_play)
            .setContentTitle(context.getString(R.string.notification_open_title))
            .setContentText(context.getString(R.string.notification_open_text))
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_ALARM)
            .setContentIntent(pi)
            .setFullScreenIntent(pi, true)
            .setAutoCancel(true)
            .setTimeoutAfter(60_000L)
            .build()
        try {
            nm.notify(NOTIFICATION_ID, notification)
        } catch (e: SecurityException) {
            Log.w(TAG, "Notification not allowed", e)
        }
    }
}
