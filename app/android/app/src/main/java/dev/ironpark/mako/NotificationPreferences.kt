package dev.ironpark.mako

import android.content.Context
import android.content.SharedPreferences
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow

internal enum class NoticeKind { Approval, Waiting, Done }

internal data class NotificationOptions(
    val enabled: Boolean = true,
    val approvals: Boolean = true,
    val waiting: Boolean = true,
    val done: Boolean = true,
    val foreground: Boolean = false,
) {
    fun allows(kind: NoticeKind, inForeground: Boolean): Boolean = enabled && (!inForeground || foreground) && when (kind) {
        NoticeKind.Approval -> approvals
        NoticeKind.Waiting -> waiting
        NoticeKind.Done -> done
    }
}

internal class NotificationPreferences(private val preferences: SharedPreferences) {
    constructor(context: Context) : this(context.getSharedPreferences("notifications", Context.MODE_PRIVATE))

    private val current = MutableStateFlow(NotificationOptions(
        preferences.getBoolean("enabled", true), preferences.getBoolean("approvals", true),
        preferences.getBoolean("waiting", true), preferences.getBoolean("done", true),
        preferences.getBoolean("foreground", false),
    ))
    val options = current.asStateFlow()

    fun update(value: NotificationOptions) {
        preferences.edit().putBoolean("enabled", value.enabled).putBoolean("approvals", value.approvals)
            .putBoolean("waiting", value.waiting).putBoolean("done", value.done)
            .putBoolean("foreground", value.foreground).apply()
        current.value = value
    }
}

internal fun SessionInfo.noticeKind(): NoticeKind? = when {
    hasApproval() && approval.id.isNotEmpty() -> NoticeKind.Approval
    status == "waiting" -> NoticeKind.Waiting
    status == "done" -> NoticeKind.Done
    else -> null
}

internal data class NoticeChange(val session: SessionInfo, val kind: NoticeKind?)

/** Observe every transition, even while disabled, so enabling alerts never replays old work. */
internal class NoticeTracker {
    private var seen = emptyMap<Pair<String, String>, SessionInfo>()

    fun update(sessions: List<SessionInfo>): List<NoticeChange> {
        val next = sessions.associateBy { it.nodeId to it.id }
        val changes = mutableListOf<NoticeChange>()
        for ((key, session) in next) {
            val old = seen[key] ?: continue
            if (old.approval.id != session.approval.id || old.noticeKind() != session.noticeKind()) {
                changes += NoticeChange(session, session.noticeKind())
            }
        }
        for ((key, session) in seen) if (key !in next) changes += NoticeChange(session, null)
        seen = next
        return changes
    }
}
