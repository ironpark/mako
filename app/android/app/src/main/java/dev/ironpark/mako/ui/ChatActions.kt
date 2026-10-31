package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.Command
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.SubAgent

/** Running agents come first; a stopped session cannot have active children. */
internal fun partitionSubagents(session: SessionInfo, agents: List<SubAgent>): Pair<List<SubAgent>, List<SubAgent>> =
    agents.partition { session.state == "running" && it.running }

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
    return messages.asReversed().takeWhile { it.role == "tool" }
        .firstOrNull { it.kind == "question" && it.pending && !it.output }
        ?.let { "question:" + it.id.ifEmpty { it.text } }.orEmpty()
}

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
