package dev.mygo

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import org.json.JSONObject

class ColaunchMobileBackgroundPlugin : MyGoPlugin {
    override fun call(method: String, args: JSONObject, reply: PluginReply) {
        if (method != "set") {
            reply.failure(PluginFailure("NoMethod", method))
            return
        }
        val active = args.opt("active") as? Boolean
        if (active == null) {
            reply.failure(PluginFailure("InvalidArgument", "active must be a boolean"))
            return
        }
        val text = if (active) BackgroundNotification(args.optString("title"), args.optString("body")) else null
        ColaunchBackgroundService.set(PluginHost.context, text, reply)
    }
}

internal data class BackgroundNotification(val title: String, val body: String)

class ColaunchBackgroundService : Service() {
    private class PendingStart(val initialText: BackgroundNotification, reply: PluginReply) {
        var desiredText: BackgroundNotification? = initialText
        val replies = mutableListOf(reply)
    }

    companion object {
        private const val CHANNEL_ID = "colaunch.ongoing_work"
        private const val NOTIFICATION_ID = 0x434F4C41

        // Plugin calls and Service callbacks both run on Android's main thread.
        private var starting: PendingStart? = null
        private var running: ColaunchBackgroundService? = null

        internal fun set(context: Context, text: BackgroundNotification?, reply: PluginReply) {
            starting?.let {
                it.desiredText = text
                it.replies.add(reply)
                return
            }
            try {
                val service = running
                if (text == null) {
                    service?.stop()
                } else if (service != null) {
                    service.updateNotification(text)
                    service.requestNotificationPermission()
                } else {
                    starting = PendingStart(text, reply)
                    check(context.startForegroundService(Intent(context, ColaunchBackgroundService::class.java)) != null) {
                        "Android did not start the foreground service"
                    }
                    return // onStartCommand replies after attempting foreground promotion.
                }
                reply.success(null)
            } catch (e: RuntimeException) {
                starting = null
                reply.failure(serviceError(e))
            }
        }

        private fun serviceError(e: RuntimeException) = PluginFailure("ServiceError", e.toString())
    }

    private var currentText: BackgroundNotification? = null

    private val notifications: NotificationManager
        get() = getSystemService(NotificationManager::class.java)

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val request = starting
        starting = null
        if (request == null) {
            stopSelf(startId)
            return START_NOT_STICKY
        }

        val failure = try {
            // Even if a stop arrived while starting, satisfy the promotion deadline
            // before stopping. Use the newest notification text if still active.
            val text = request.desiredText
            val notification = notification(text ?: request.initialText)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
            } else {
                startForeground(NOTIFICATION_ID, notification)
            }
            if (text == null) {
                stop()
            } else {
                running = this
                currentText = text
                requestNotificationPermission()
            }
            null
        } catch (e: RuntimeException) {
            stopSelf(startId)
            serviceError(e)
        }
        for (reply in request.replies) {
            if (failure == null) reply.success(null) else reply.failure(failure)
        }
        return START_NOT_STICKY
    }

    // Android 15+ calls this when the app's dataSync time budget runs out.
    override fun onTimeout(startId: Int, fgsType: Int) {
        stop()
    }

    override fun onDestroy() {
        if (running === this) running = null
        super.onDestroy()
    }

    private fun stop() {
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
        if (running === this) running = null
    }

    private fun updateNotification(text: BackgroundNotification) {
        currentText = text
        notifications.notify(NOTIFICATION_ID, notification(text))
    }

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) return
        if (PluginHost.activity?.hasWindowFocus() != true) return
        val prefs = getSharedPreferences(CHANNEL_ID, MODE_PRIVATE)
        if (prefs.getBoolean("askedForNotifications", false)) return
        // The service can run if the user declines. Ask only once and only
        // after promotion so the dialog does not delay starting the service.
        runCatching {
            PluginHost.requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS)) { granted ->
                if (granted && running === this) currentText?.let(::updateNotification)
            }
            prefs.edit().putBoolean("askedForNotifications", true).apply()
        }
    }

    private fun notification(text: BackgroundNotification): Notification {
        val appName = applicationInfo.loadLabel(packageManager)
        notifications.createNotificationChannel(NotificationChannel(CHANNEL_ID, appName, NotificationManager.IMPORTANCE_LOW))
        val launcher = Intent(Intent.ACTION_MAIN)
            .addCategory(Intent.CATEGORY_LAUNCHER)
            .setClass(this, MyGoActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val openApp = PendingIntent.getActivity(this, 0, launcher, PendingIntent.FLAG_IMMUTABLE)
        val builder = Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle(if (text.title.isBlank()) appName else text.title)
            .setContentText(text.body)
            .setContentIntent(openApp)
            .setCategory(Notification.CATEGORY_SERVICE)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            builder.setForegroundServiceBehavior(Notification.FOREGROUND_SERVICE_IMMEDIATE)
        }
        return builder.build()
    }
}
