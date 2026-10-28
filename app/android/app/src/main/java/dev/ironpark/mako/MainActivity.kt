package dev.ironpark.mako

import android.Manifest
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import dev.ironpark.mako.ui.MakoNav
import dev.ironpark.mako.ui.MakoTheme
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.flow.MutableStateFlow

class MainActivity : ComponentActivity() {
    /** The session a notification asked to open, for the navigation to take. */
    private val target = MutableStateFlow<SessionRef?>(null)

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        if (Build.VERSION.SDK_INT >= 33) askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        MeshService.start(this)
        take(intent)
        setContent {
            MakoTheme { MakoNav(mako.mesh, target) }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        take(intent)
    }

    private fun take(intent: Intent?) {
        val id = intent?.getStringExtra(SESSION) ?: return
        target.value = SessionRef(intent.getStringExtra(NODE).orEmpty(), id)
    }

    companion object {
        private const val SESSION = "session"
        private const val NODE = "node"

        fun open(context: Context): PendingIntent = PendingIntent.getActivity(
            context, 0, Intent(context, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )

        fun openSession(context: Context, s: SessionInfo): PendingIntent = PendingIntent.getActivity(
            context, s.id.hashCode(),
            Intent(context, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
                .putExtra(SESSION, s.id)
                .putExtra(NODE, s.nodeId),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }
}

/** SessionRef names a session, on its node. */
data class SessionRef(val node: String, val id: String)
