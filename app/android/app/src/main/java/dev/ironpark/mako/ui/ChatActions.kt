package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.BackgroundTask
import dev.ironpark.mako.v2.Chat
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.Command
import dev.ironpark.mako.v2.Question
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.SubAgent
import java.util.Locale

/** Running agents come first; a stopped session cannot have active children. */
internal fun partitionSubagents(session: SessionInfo, agents: List<SubAgent>): Pair<List<SubAgent>, List<SubAgent>> =
    agents.partition { session.state == "running" && it.running }

/**
 * Applies an incremental [ReadChat] patch reply onto [previous], matching the
 * daemon's patch protocol (`internal/daemon/chat.go`):
 * - If [reply.unchanged] is set, returns [previous] (or [reply] when null).
 * - If [reply.base] is 0, [reply] is already a full snapshot.
 * - If [previous] matches [reply.base], combines
 *   `previous.messagesList.drop(drop).take(keep) + reply.messagesList` with bounds
 *   clamping while keeping all other fields from [reply] and clearing patch metadata.
 * - If [previous] is null or its digest mismatches [reply.base], falls back to [reply]
 *   with patch metadata cleared.
 */
internal fun applyChatPatch(previous: Chat?, reply: Chat): Chat {
    if (reply.unchanged) return previous ?: reply
    if (reply.base == 0L) return reply
    if (previous == null || previous.digest != reply.base) {
        return reply.toBuilder()
            .clearBase()
            .clearDrop()
            .clearKeep()
            .build()
    }
    val old = previous.messagesList
    val drop = reply.drop.coerceIn(0, old.size)
    val keep = reply.keep.coerceIn(0, (old.size - drop).coerceAtLeast(0))
    val merged = ArrayList<ChatMessage>(keep + reply.messagesCount)
    for (i in drop until drop + keep) merged += old[i]
    merged.addAll(reply.messagesList)
    return reply.toBuilder()
        .clearMessages()
        .addAllMessages(merged)
        .clearBase()
        .clearDrop()
        .clearKeep()
        .build()
}

/** Merge results split over pagination boundaries into their tool calls. */
internal fun joinMessages(older: List<ChatMessage>, newer: List<ChatMessage>): List<ChatMessage> {
    val out = older.toMutableList()
    val calls = older.withIndex().filter { it.value.role == "tool" && !it.value.output && it.value.id.isNotEmpty() }
        .associate { it.value.id to it.index }
    for (message in newer) {
        val index = calls[message.id]
        if (index != null && message.role == "tool" && message.output) {
            out[index] = out[index].toBuilder().setResult(message.text).setError(message.error).setPending(false).build()
        } else out += message
    }
    return out
}

internal fun questionRequest(session: SessionInfo, messages: List<ChatMessage>): String {
    if (session.state != "running" || session.hasApproval()) return ""
    if (session.status == "waiting") return "waiting"
    if (session.status == "idle" || session.status == "done") return ""
    return pendingQuestionCall(messages)?.let { "question:" + it.id.ifEmpty { it.text } }.orEmpty()
}

/** The question call the agent waits on, the last of the trailing calls; null when it asks none. */
internal fun pendingQuestionCall(messages: List<ChatMessage>): ChatMessage? =
    messages.asReversed().takeWhile { it.role == "tool" }
        .firstOrNull { it.kind == "question" && it.pending && !it.output }

/**
 * The questions of the call the session's agent waits on that the app can
 * answer in a form, typed into the agent's terminal by its node; none
 * when its node cannot, or the call asks none it reads.
 */
internal fun keyedQuestions(session: SessionInfo, messages: List<ChatMessage>): List<Question> =
    if (session.caps and CAP_ANSWER_KEYS == 0 || questionRequest(session, messages).isEmpty()) emptyList()
    else pendingQuestionCall(messages)?.questionsList.orEmpty()

internal fun matchingCommands(text: String, commands: List<Command>): List<Command> {
    if (!text.startsWith('/') || text.any { it.isWhitespace() }) return emptyList()
    val prefix = text.drop(1)
    return commands.filter { it.name.startsWith(prefix, ignoreCase = true) }.distinctBy { it.name }.sortedBy { it.name }
}

/** How many tool calls in a row fold into one line, as on the desktop. */
internal const val FOLD_RUN = 3

internal fun ChatMessage.isTool(): Boolean = tool.isNotEmpty() || role == "tool"

/**
 * ChatTurn is one row of the feed: a message, or a run of tool calls in a
 * row. A long run folds to a line, all but the last, which is the agent's
 * work in hand.
 */
internal data class ChatTurn(val key: String, val messages: List<ChatMessage>, val folded: Boolean = false) {
    val tools: Boolean get() = messages.first().isTool()
}

/** messages as the feed's turns, each run of tool calls one of them. */
internal fun chatTurns(messages: List<ChatMessage>): List<ChatTurn> {
    var lastRun = -1
    for (i in messages.indices) if (messages[i].isTool() && (i == 0 || !messages[i - 1].isTool())) lastRun = i
    val seen = HashMap<String, Int>()
    val turns = ArrayList<ChatTurn>()
    var i = 0
    while (i < messages.size) {
        val start = i
        var key = messageKey(messages[i])
        val run = if (messages[i].isTool()) {
            key = "run:$key"
            while (i < messages.size && messages[i].isTool()) i++
            messages.subList(start, i).toList()
        } else listOf(messages[i++])
        // The same words said twice, with no ID to tell them apart, are two turns.
        val n = seen[key] ?: 0
        seen[key] = n + 1
        if (n > 0) key += "#$n"
        turns += ChatTurn(key, run, folded = run.first().isTool() && start != lastRun)
    }
    return turns
}

/** A message's own ID, or what it says, which keeps its row across reads. */
internal fun messageKey(m: ChatMessage): String =
    m.id.ifEmpty { listOf(m.role, m.tool, m.text).joinToString("\u0000").hashCode().toUInt().toString(16) }

/** How long an optimistic sent message waits for the log before expiring. */
internal const val PENDING_PROMPT_MS = 2 * 60_000L

/** Slash commands often change settings without being logged, so they expire sooner. */
internal const val PENDING_COMMAND_MS = 10_000L

/**
 * A user message sent from the composer and not yet written to the agent's log.
 * Mirrors desktop `internal/gui/sending.go`: anchored to the latest user message
 * present at send time ([afterKey]'s [occurrence]-th occurrence) and [ahead]
 * earlier pending messages.
 */
data class PendingMessage(
    val id: Long,
    val text: String,
    val afterKey: String,
    val occurrence: Int,
    val ahead: Int,
    val sentAtMs: Long,
)

internal fun pendingTimeoutMs(text: String): Long =
    if (text.trim().startsWith('/')) PENDING_COMMAND_MS else PENDING_PROMPT_MS

/** Key of the latest user message in [messages] and its 1-based occurrence count. */
internal fun latestUser(messages: List<ChatMessage>): Pair<String, Int> {
    var key = ""
    for (i in messages.lastIndex downTo 0) {
        if (messages[i].role == "user") {
            key = messageKey(messages[i])
            break
        }
    }
    if (key.isEmpty()) return "" to 0
    var count = 0
    for (msg in messages) {
        if (msg.role == "user" && messageKey(msg) == key) count++
    }
    return key to count
}

/**
 * Counts user messages appearing after [pending]'s anchor and reports whether
 * any of them matches [pending]'s trimmed text.
 */
internal fun usersAfter(messages: List<ChatMessage>, pending: PendingMessage): Pair<Int, Boolean> {
    var start = 0
    if (pending.afterKey.isNotEmpty()) {
        start = -1
        var seen = 0
        for (i in messages.indices) {
            val msg = messages[i]
            if (msg.role == "user" && messageKey(msg) == pending.afterKey) {
                seen++
                if (seen == pending.occurrence) {
                    start = i + 1
                    break
                }
            }
        }
        if (start < 0) return 0 to false
    }
    var count = 0
    var said = false
    val want = pending.text.trim()
    for (i in start until messages.size) {
        val msg = messages[i]
        if (msg.role == "user") {
            count++
            if (msg.text.trim() == want) said = true
        }
    }
    return count to said
}

/** Drops pending messages now present in [messages] or past their expiry window. */
internal fun settlePending(
    pending: List<PendingMessage>,
    messages: List<ChatMessage>,
    nowMs: Long,
): List<PendingMessage> {
    if (pending.isEmpty()) return pending
    val kept = ArrayList<PendingMessage>(pending.size)
    for (p in pending) {
        val (n, said) = usersAfter(messages, p)
        val wait = pendingTimeoutMs(p.text)
        if (said || n > p.ahead || (nowMs - p.sentAtMs) > wait) continue
        kept += p
    }
    return if (kept.size == pending.size) pending else kept
}

/** Records a newly sent [text] in [pending], anchored to the latest user message in [messages]. */
internal fun notePending(
    pending: List<PendingMessage>,
    messages: List<ChatMessage>,
    text: String,
    nowMs: Long,
    id: Long = nowMs,
): List<PendingMessage> {
    if (text.isBlank()) return pending
    val settled = settlePending(pending, messages, nowMs)
    val (afterKey, occurrence) = latestUser(messages)
    return settled + PendingMessage(
        id = id,
        text = text,
        afterKey = afterKey,
        occurrence = occurrence,
        ahead = settled.size,
        sentAtMs = nowMs,
    )
}

/** Milliseconds until the earliest pending message expires, or null when [pending] is empty. */
internal fun nextPendingExpiryDelayMs(pending: List<PendingMessage>, nowMs: Long): Long? {
    if (pending.isEmpty()) return null
    var minDelay = Long.MAX_VALUE
    for (p in pending) {
        val expiresAt = p.sentAtMs + pendingTimeoutMs(p.text) + 1L
        val remaining = (expiresAt - nowMs).coerceAtLeast(0L)
        if (remaining < minDelay) minDelay = remaining
    }
    return minDelay
}

private val PARTIAL_JSON_STRING_FIELD = Regex("\"(\\w+)\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)")
private val TOOL_PREVIEW_KEYS = listOf(
    "description", "command", "cmd", "file_path", "path", "target_file",
    "filename", "pattern", "query", "url", "glob", "prompt",
)

/**
 * Extracts a concise single-line preview from a streaming tool call's partial JSON arguments.
 */
internal fun liveToolArgsPreview(rawJson: String): String {
    if (rawJson.isBlank()) return ""
    val fields = LinkedHashMap<String, String>()
    for (match in PARTIAL_JSON_STRING_FIELD.findAll(rawJson)) {
        val key = match.groupValues[1]
        val value = unescapePartialJsonString(match.groupValues[2]).trim()
        if (value.isNotEmpty() && key !in fields) {
            fields[key] = value
        }
    }
    for (preferred in TOOL_PREVIEW_KEYS) {
        val value = fields[preferred]
        if (!value.isNullOrEmpty()) return singleLinePreview(value)
    }
    val first = fields.values.firstOrNull()
    if (!first.isNullOrEmpty()) return singleLinePreview(first)
    return singleLinePreview(rawJson.trim())
}

private fun unescapePartialJsonString(raw: String): String {
    val clean = raw.removeSuffix("\\")
    val out = StringBuilder(clean.length)
    var i = 0
    while (i < clean.length) {
        val c = clean[i]
        if (c == '\\' && i + 1 < clean.length) {
            when (val next = clean[i + 1]) {
                'n' -> out.append(' ')
                'r' -> {}
                't' -> out.append(' ')
                '"' -> out.append('"')
                '\\' -> out.append('\\')
                '/' -> out.append('/')
                else -> out.append(next)
            }
            i += 2
        } else {
            out.append(c)
            i++
        }
    }
    return out.toString()
}

private fun singleLinePreview(text: String): String =
    text.replace('\r', ' ').replace('\n', ' ').replace(Regex("\\s+"), " ").trim()

/** Names a rate limit window cleanly; non-positive windows fall back to [quotaLabel]. */
internal fun limitLabel(
    minutes: Long,
    quotaLabel: String = "Quota",
    fiveHoursLabel: String = "5h",
    weekLabel: String = "Week",
): String = when {
    minutes <= 0L -> quotaLabel
    minutes == 5L * 60L -> fiveHoursLabel
    minutes == 7L * 24L * 60L -> weekLabel
    minutes % (24L * 60L) == 0L -> "${minutes / (24L * 60L)}d"
    minutes % 60L == 0L -> "${minutes / 60L}h"
    else -> "${minutes}m"
}

/** Formats a Unix-second reset timestamp as a compact relative duration (e.g. `2h 14m`). */
internal fun formatReset(resetsUnixSeconds: Long, nowMs: Long = System.currentTimeMillis()): String {
    if (resetsUnixSeconds <= 0L) return ""
    val diffSec = resetsUnixSeconds - (nowMs / 1000L)
    if (diffSec <= 0L) return ""
    if (diffSec < 60L) return "<1m"
    val days = diffSec / 86_400L
    val hours = (diffSec % 86_400L) / 3600L
    val mins = (diffSec % 3600L) / 60L
    return when {
        days > 0L && hours > 0L -> "${days}d ${hours}h"
        days > 0L -> "${days}d"
        hours > 0L && mins > 0L -> "${hours}h ${mins}m"
        hours > 0L -> "${hours}h"
        else -> "${mins}m"
    }
}

/** Formats an estimated USD spend for compact display (e.g. `$0.12`), or empty when non-positive. */
internal fun formatCost(costUsd: Double): String {
    if (!costUsd.isFinite() || costUsd <= 0.0) return ""
    if (costUsd < 0.01) return "<$0.01"
    return String.format(Locale.US, "$%.2f", costUsd)
}

/** Orders background tasks with running tasks first, newest first within each group. */
internal fun sortBackgroundTasks(tasks: List<BackgroundTask>): List<BackgroundTask> {
    if (tasks.size <= 1) return tasks
    val reversed = tasks.asReversed()
    val (running, finished) = reversed.partition { it.running }
    return running + finished
}
