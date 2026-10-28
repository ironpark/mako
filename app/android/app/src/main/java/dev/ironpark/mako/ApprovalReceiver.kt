package dev.ironpark.mako

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.widget.Toast
import androidx.core.app.NotificationManagerCompat
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** ApprovalReceiver answers an approval from its notification. */
class ApprovalReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val node = intent.getStringExtra(NODE) ?: return
        val id = intent.getStringExtra(SESSION) ?: return
        val decision = intent.getStringExtra(DECISION) ?: return
        val approval = intent.getStringExtra(APPROVAL) ?: return
        val app = context.mako
        val pending = goAsync()
        app.scope.launch {
            try {
                app.mesh.answer(node, id, approval, decision)
                NotificationManagerCompat.from(context).cancel(id.hashCode())
            } catch (e: Exception) {
                withContext(Dispatchers.Main) {
                    Toast.makeText(context, e.message, Toast.LENGTH_LONG).show()
                }
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        private const val NODE = "node"
        private const val SESSION = "session"
        private const val APPROVAL = "approval"
        private const val DECISION = "decision"

        fun intent(context: Context, s: SessionInfo, decision: String): PendingIntent =
            PendingIntent.getBroadcast(
                context, (s.id + decision).hashCode(),
                Intent(context, ApprovalReceiver::class.java)
                    .putExtra(NODE, s.nodeId)
                    .putExtra(SESSION, s.id)
                    .putExtra(APPROVAL, s.approval.id)
                    .putExtra(DECISION, decision),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
    }
}
