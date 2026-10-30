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
