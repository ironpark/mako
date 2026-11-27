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
    hasAuthError() && state == "running" -> NoticeKind.Waiting
    status == "waiting" -> NoticeKind.Waiting
    status == "done" -> NoticeKind.Done
    else -> null
}

/**
 * The notice session calls for, against old, what it was last seen as,
 * and oldKind, the notice that called for. A turn ends as its agent goes
 * done, or, as an agent its screen is read of does, idle: idle counts as
 * done where the node counted a turn ended since old, and stays so until
 * it works again. A node older than turns counts none, and its idle
 * agents call for nothing.
 */
internal fun SessionInfo.noticeKind(old: SessionInfo?, oldKind: NoticeKind?): NoticeKind? = when {
    // Looked at in a window, or here, as its node says: nothing to tell.
    seenElsewhere() -> null
    else -> noticeKind() ?: when {
        state != "running" || status != "idle" -> null
        old != null && (turns > old.turns || askedSince(old)) -> NoticeKind.Done
        old?.status == "idle" && oldKind == NoticeKind.Done -> NoticeKind.Done
        else -> null
    }
}

/**
 * Whether a node that numbers its sessions' status (revision 36) says the
 * user has seen all this one asked for: a turn done or a question, though
 * not an approval, which waits on an answer however long it is looked at.
 * Keeps backward compatibility with older nodes where stateSeq == 0 and
 * attentionSeq == 0.
 */
internal fun SessionInfo.seenElsewhere(): Boolean =
    (stateSeq > 0L || attentionSeq > 0L) && attentionSeq <= seenSeq && noticeKind() != NoticeKind.Approval

/** Whether session asked for the user since old, as its node numbers it. */
private fun SessionInfo.askedSince(old: SessionInfo): Boolean =
    (stateSeq > 0L || attentionSeq > 0L) && attentionSeq > seenSeq &&
        (startedAt != old.startedAt || attentionSeq > old.attentionSeq)

internal data class NoticeChange(val session: SessionInfo, val kind: NoticeKind?)

/** Observe every transition, even while disabled, so enabling alerts never replays old work. */
internal class NoticeTracker {
    private var seen = emptyMap<Pair<String, String>, SessionInfo>()
    private var kinds = emptyMap<Pair<String, String>, NoticeKind?>()

    /** The notice each session last seen calls for. */
    fun kind(s: SessionInfo): NoticeKind? = kinds[s.nodeId to s.id]

    fun update(sessions: List<SessionInfo>): List<NoticeChange> {
        val next = sessions.associateBy { it.nodeId to it.id }
        val nextKinds = HashMap<Pair<String, String>, NoticeKind?>()
        val changes = mutableListOf<NoticeChange>()
        for ((key, session) in next) {
            val old = seen[key]
            val kind = session.noticeKind(old, kinds[key])
            nextKinds[key] = kind
            if (old == null) continue
            // Another turn ended or question asked while the last one's notice stood,
            // between two looks, is news too.
            val again = (kind == NoticeKind.Done && session.turns > old.turns) ||
                (kind != null && kind != NoticeKind.Approval && session.askedSince(old))
            if (old.approval.id != session.approval.id || kinds[key] != kind || again) {
                changes += NoticeChange(session, kind)
            }
        }
        for ((key, session) in seen) if (key !in next) changes += NoticeChange(session, null)
        seen = next
        kinds = nextKinds
        return changes
    }
}
