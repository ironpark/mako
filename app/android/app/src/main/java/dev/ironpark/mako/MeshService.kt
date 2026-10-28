package dev.ironpark.mako

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import dev.ironpark.mako.ui.label
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch

/**
 * MeshService keeps the node running while the app is away, and says when
 * an agent on the mesh waits for an answer, or has finished. It holds the
 * Wi-Fi's multicast open for the node's mDNS, which finds the computers on
 * the network.
 */
class MeshService : Service() {
    private var job: Job? = null
    private var multicast: WifiManager.MulticastLock? = null

    /** What was last seen of each session: its approval and its status. */
    private val seen = mutableMapOf<String, Pair<String, String>>()

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        channels(this)
        ServiceCompat.startForeground(
            this, ONGOING, ongoing(0),
            if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0,
        )
        multicast = (getSystemService(WIFI_SERVICE) as WifiManager).createMulticastLock("mako").apply {
            setReferenceCounted(false)
            acquire()
        }
        val app = mako
        app.mesh.start()
        job = app.scope.launch {
            launch {
                app.mesh.nodes.map { nodes -> nodes.count { it.trustsUs } }.distinctUntilChanged().collect {
                    NotificationManagerCompat.from(this@MeshService).notify(ONGOING, ongoing(it))
                }
            }
            combine(app.mesh.sessions, app.foreground, ::Pair).collect { (sessions, fg) -> notice(sessions, fg) }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            mako.mesh.stop()
            stopSelf()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        job?.cancel()
        multicast?.release()
        super.onDestroy()
    }

    /** Notifies of the approvals asked for, and the turns ended, since last. */
    private fun notice(sessions: List<SessionInfo>, foreground: Boolean) {
        val nm = NotificationManagerCompat.from(this)
        for (s in sessions) {
            val approval = if (s.hasApproval()) s.approval.id else ""
            val before = seen.put(s.id, approval to s.status) ?: continue // known from now on
            if (approval != before.first) {
                if (approval.isEmpty()) nm.cancel(s.id.hashCode())
                else if (!foreground) notify(nm, s.id.hashCode(), approvalNotice(s))
            } else if (!foreground && s.status != before.second && (s.status == "waiting" || s.status == "done")) {
                notify(nm, s.id.hashCode(), turnNotice(s))
            }
        }
        seen.keys.retainAll(sessions.map { it.id }.toSet())
    }

    private fun notify(nm: NotificationManagerCompat, id: Int, n: Notification) {
        if (Build.VERSION.SDK_INT < 33 ||
            ContextCompat.checkSelfPermission(this, android.Manifest.permission.POST_NOTIFICATIONS) ==
            android.content.pm.PackageManager.PERMISSION_GRANTED
        ) nm.notify(id, n)
    }

    private fun approvalNotice(s: SessionInfo): Notification {
        val who = sessionLabel(s)
        return NotificationCompat.Builder(this, AGENTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(getString(R.string.notice_approval, who, s.approval.tool))
            .setContentText(s.approval.detail)
            .setStyle(NotificationCompat.BigTextStyle().bigText(s.approval.detail))
            .setContentIntent(MainActivity.openSession(this, s))
            .setAutoCancel(true)
            .setCategory(NotificationCompat.CATEGORY_MESSAGE)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .addAction(0, getString(R.string.allow), ApprovalReceiver.intent(this, s, "allow"))
            .addAction(0, getString(R.string.deny), ApprovalReceiver.intent(this, s, "deny"))
            .build()
    }

    private fun turnNotice(s: SessionInfo): Notification =
        NotificationCompat.Builder(this, AGENTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(
                getString(if (s.status == "waiting") R.string.notice_waiting else R.string.notice_done, sessionLabel(s))
            )
            .setContentText(s.title.ifEmpty { s.project })
            .setContentIntent(MainActivity.openSession(this, s))
            .setAutoCancel(true)
            .build()

    private fun sessionLabel(s: SessionInfo): String =
        "${s.label()} · ${mako.mesh.nodeName(s.nodeId)}"

    private fun ongoing(linked: Int): Notification {
        val stop = PendingIntent.getService(
            this, 0, Intent(this, MeshService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, LINK)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(resources.getQuantityString(R.plurals.linked_computers, linked, linked))
            .setContentIntent(MainActivity.open(this))
            .setOngoing(true)
            .setSilent(true)
            .addAction(0, getString(R.string.disconnect), stop)
            .build()
    }

    companion object {
        private const val ONGOING = 1
        private const val LINK = "link"
        private const val AGENTS = "agents"
        private const val ACTION_STOP = "dev.ironpark.mako.STOP"

        fun start(context: Context) {
            ContextCompat.startForegroundService(context, Intent(context, MeshService::class.java))
        }

        private fun channels(context: Context) {
            val nm = context.getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(
                NotificationChannel(LINK, context.getString(R.string.channel_link), NotificationManager.IMPORTANCE_MIN)
            )
            nm.createNotificationChannel(
                NotificationChannel(AGENTS, context.getString(R.string.channel_agents), NotificationManager.IMPORTANCE_HIGH)
            )
        }
    }
}
