package dev.motty.app.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import dev.motty.app.MainActivity
import dev.motty.app.R
import dev.motty.app.core.SessionInfo

/**
 * Keeps the process, and with it the open SSH sessions, alive while the app is
 * in the background. The notification also surfaces a connection that is
 * waiting for the user.
 */
class SessionKeepAliveService : Service() {
    override fun onCreate() {
        super.onCreate()
        createChannel()
        running = true
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val count = intent?.getIntExtra(EXTRA_SESSIONS, 0) ?: 0
        val waiting = intent?.getBooleanExtra(EXTRA_WAITING, false) ?: false
        val notification = buildNotification(count, waiting)
        val type = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
        } else {
            0
        }
        ServiceCompat.startForeground(this, NOTIFICATION_ID, notification, type)
        return START_NOT_STICKY
    }

    /**
     * Android 15 limits dataSync foreground services to six hours. The core
     * keeps running only while the process does, so stop cleanly and let the
     * user reconnect from the app.
     */
    override fun onTimeout(startId: Int) {
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf(startId)
    }

    override fun onDestroy() {
        running = false
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun buildNotification(count: Int, waiting: Boolean): Notification {
        val contentIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val text = if (waiting) {
            getString(R.string.notification_auth_required)
        } else {
            resources.getQuantityString(R.plurals.notification_sessions, count, count)
        }
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_motty)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setContentIntent(contentIntent)
            .setOngoing(true)
            .setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .build()
    }

    private fun createChannel() {
        val manager = getSystemService(NotificationManager::class.java) ?: return
        val existing = manager.getNotificationChannel(CHANNEL_ID)
        if (existing != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.notification_channel_sessions),
            NotificationManager.IMPORTANCE_LOW,
        )
        channel.description = getString(R.string.notification_channel_sessions)
        channel.setShowBadge(false)
        manager.createNotificationChannel(channel)
    }

    companion object {
        private const val CHANNEL_ID = "sessions"
        private const val NOTIFICATION_ID = 1
        private const val EXTRA_SESSIONS = "sessions"
        private const val EXTRA_WAITING = "waiting"

        @Volatile
        private var running = false

        /**
         * Starts or updates the foreground service to match the current
         * session table, and stops it when the last session is gone.
         */
        fun sync(context: Context, sessions: Map<Long, SessionInfo>, waiting: Boolean) {
            val count = sessions.size
            if (count == 0) {
                if (running) {
                    context.stopService(Intent(context, SessionKeepAliveService::class.java))
                    running = false
                }
                return
            }
            val intent = Intent(context, SessionKeepAliveService::class.java)
                .putExtra(EXTRA_SESSIONS, count)
                .putExtra(EXTRA_WAITING, waiting)
            if (running) {
                context.startService(intent)
            } else {
                androidx.core.content.ContextCompat.startForegroundService(context, intent)
            }
        }
    }
}
