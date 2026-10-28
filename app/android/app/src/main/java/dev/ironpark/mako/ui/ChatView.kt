package dev.ironpark.mako.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.outlined.Build
import androidx.compose.material.icons.outlined.Checklist
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Language
import androidx.compose.material.icons.outlined.Search
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SmallFloatingActionButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Chat
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.ReadChat
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.Usage
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * ChatView is a session's conversation, as its agent's log has it: read
 * again whenever the session prints, and every few seconds while its agent
 * works; scrolling to the top reads further back. Above it, how much of
 * the context window and of the account's limits it has spent.
 */
@Composable
fun ChatView(mesh: Mesh, session: SessionInfo, modifier: Modifier = Modifier) {
    var chat by remember(session.id) { mutableStateOf<Chat?>(null) } // the latest messages
    var older by remember(session.id) { mutableStateOf<List<ChatMessage>>(emptyList()) }
    var start by remember(session.id) { mutableLongStateOf(0L) } // of what has been read, the earliest
    var error by remember(session.id) { mutableStateOf<String?>(null) }
    var paging by remember(session.id) { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    /** Reads the messages before the offset before, or with 0 the latest. */
    suspend fun fetch(before: Long = 0): Chat? = try {
        mesh.daemon.chat(
            ReadChat.newBuilder().setNodeId(session.nodeId).setSessionId(session.id).setBefore(before).build()
        ).also { error = null }
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        error = e.message
        null
    }

    suspend fun read() {
        val c = fetch() ?: return
        chat = c
        if (older.isEmpty()) start = c.start
    }

    // Read again as the agent's turn changes, and every few seconds while it works.
    LaunchedEffect(session.id, session.status, session.approval.id) {
        read()
        while (session.status == "working") {
            delay(3000)
            read()
        }
    }

    val latest = chat?.messagesList.orEmpty()
    val messages = remember(older, latest) { older + latest }
    val list = rememberLazyListState()
    val atEnd by remember { derivedStateOf { !list.canScrollForward } }
    var follow by remember(session.id) { mutableStateOf(true) }
    LaunchedEffect(atEnd) { follow = atEnd }
    LaunchedEffect(messages.size, messages.lastOrNull()?.pending) {
        // Follows the end as it grows, unless the reader scrolled away from it.
        if (follow && messages.isNotEmpty()) list.scrollToItem(list.layoutInfo.totalItemsCount.coerceAtLeast(1) - 1)
    }

    Column(modifier) {
        chat?.takeIf { it.hasUsage() }?.let { UsageStrip(it.usage) }
        Box(Modifier.weight(1f).fillMaxWidth()) {
            val c = chat
            when {
                c == null && error == null -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                c != null && !c.supported -> Message(stringResource(R.string.chat_unsupported), stringResource(R.string.chat_unsupported_body))
                error != null && messages.isEmpty() -> Message(stringResource(R.string.chat_failed), error.orEmpty())
                messages.isEmpty() -> Message(stringResource(R.string.chat_empty), stringResource(R.string.chat_empty_body))
                else -> SelectionContainer {
                    LazyColumn(
                        state = list,
                        contentPadding = PaddingValues(horizontal = 14.dp, vertical = 12.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp),
                    ) {
                        if (start > 0) {
                            item(key = "older") {
                                TextButton(
                                    enabled = !paging,
                                    onClick = {
                                        paging = true
                                        scope.launch {
                                            fetch(start)?.let {
                                                older = it.messagesList + older
                                                start = it.start
                                            }
                                            paging = false
                                        }
                                    },
                                    modifier = Modifier.fillMaxWidth(),
                                ) { Text(stringResource(R.string.load_earlier)) }
                            }
                        }
                        items(messages) { m -> MessageItem(m) }
                        if (session.working()) {
                            item(key = "working") { Working(c?.agentsList.orEmpty().count { it.running }) }
                        }
                    }
                }
            }
            // The overload of no scope: this one sits in a Box, in a Column.
            androidx.compose.animation.AnimatedVisibility(
                visible = !atEnd && messages.isNotEmpty(),
                enter = fadeIn(), exit = fadeOut(),
                modifier = Modifier.align(Alignment.BottomEnd).padding(16.dp),
            ) {
                SmallFloatingActionButton(onClick = {
                    scope.launch { list.animateScrollToItem(list.layoutInfo.totalItemsCount.coerceAtLeast(1) - 1) }
                }) { Icon(Icons.Filled.KeyboardArrowDown, stringResource(R.string.to_latest)) }
            }
        }
    }
}

/** UsageStrip is how full the context window is, and the account's limits. */
@Composable
private fun UsageStrip(u: Usage) {
    val parts = buildList {
        if (u.window > 0) add(stringResource(R.string.usage_context) to u.context.toFloat() / u.window)
        for (l in u.limitsList) add(limitName(l.minutes) to (l.used / 100).toFloat())
    }
    if (parts.isEmpty()) return
    Row(
        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        for ((name, used) in parts) {
            val color = when {
                used >= 0.9f -> StatusColors.Approval
                used >= 0.7f -> StatusColors.Waiting
                else -> MaterialTheme.colorScheme.primary
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(name, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Spacer(Modifier.width(6.dp))
                LinearProgressIndicator(
                    progress = { used.coerceIn(0f, 1f) },
                    modifier = Modifier.width(48.dp),
                    color = color,
                    strokeCap = StrokeCap.Round,
                    gapSize = 0.dp,
                    drawStopIndicator = {},
                )
                Spacer(Modifier.width(6.dp))
                Text("${(used * 100).toInt()}%", style = MaterialTheme.typography.labelSmall, color = color)
            }
        }
    }
}

@Composable
private fun limitName(minutes: Long): String = when {
    minutes == 5L * 60 -> stringResource(R.string.usage_5h)
    minutes == 7L * 24 * 60 -> stringResource(R.string.usage_week)
    minutes % (24 * 60) == 0L -> "${minutes / (24 * 60)}d"
    else -> "${minutes / 60}h"
}

/** Working says the agent is at work, and how many of its subagents. */
@Composable
private fun Working(subagents: Int) {
    Row(Modifier.padding(start = 4.dp, top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp, color = StatusColors.Working)
        Spacer(Modifier.width(10.dp))
        Text(
            if (subagents > 0) stringResource(R.string.working_with, subagents) else stringResource(R.string.status_working),
            style = MaterialTheme.typography.bodySmall, color = StatusColors.Working,
        )
    }
}

@Composable
private fun MessageItem(m: ChatMessage) {
    when {
        m.tool.isNotEmpty() || m.role == "tool" -> ToolItem(m)
        m.role == "user" -> Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            Surface(
                color = MaterialTheme.colorScheme.primaryContainer,
                shape = RoundedCornerShape(20.dp, 20.dp, 6.dp, 20.dp),
                modifier = Modifier.widthIn(max = 320.dp),
            ) {
                Text(
                    m.text, modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
                    color = MaterialTheme.colorScheme.onPrimaryContainer, style = MaterialTheme.typography.bodyMedium,
                )
            }
        }
        else -> Markdown(m.text)
    }
}

@Composable
private fun ToolItem(m: ChatMessage) {
    var open by remember { mutableStateOf(false) }
    val tint = if (m.error) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant
    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(
            Modifier.animateContentSize().clickable(enabled = m.result.isNotEmpty()) { open = !open }
                .padding(horizontal = 12.dp, vertical = 8.dp)
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (m.pending) CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
                else Icon(toolIcon(m.kind), null, Modifier.size(16.dp), tint = tint)
                Spacer(Modifier.width(10.dp))
                Text(m.tool.ifEmpty { m.kind }, style = MaterialTheme.typography.labelLarge, color = tint)
                Spacer(Modifier.width(8.dp))
                Text(
                    m.detail.ifEmpty { m.text }, style = MaterialTheme.typography.bodySmall,
                    fontFamily = FontFamily.Monospace, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
                )
                if (m.hasExitCode() && m.exitCode != 0) {
                    Text(" ${m.exitCode}", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.error)
                }
            }
            if (open) {
                Surface(
                    color = MaterialTheme.colorScheme.surfaceContainerHighest,
                    shape = RoundedCornerShape(8.dp),
                    modifier = Modifier.fillMaxWidth().padding(top = 8.dp).heightIn(max = 320.dp),
                ) {
                    Text(
                        m.result, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                        modifier = Modifier.padding(8.dp).verticalScroll(rememberScrollState()),
                    )
                }
            }
        }
    }
}

private fun toolIcon(kind: String): ImageVector = when (kind) {
    "shell", "command" -> Icons.Outlined.Terminal
    "edit", "multiedit", "write", "patch" -> Icons.Outlined.Edit
    "read" -> Icons.Outlined.Description
    "search" -> Icons.Outlined.Search
    "fetch", "websearch" -> Icons.Outlined.Language
    "todo", "plan" -> Icons.Outlined.Checklist
    "task" -> Icons.Outlined.SmartToy
    else -> Icons.Outlined.Build
}

/** A block of what an agent wrote: prose, or code fenced off. */
private sealed interface Block {
    data class Prose(val lines: List<String>) : Block
    data class Code(val text: String) : Block
}

private fun blocks(text: String): List<Block> {
    val out = mutableListOf<Block>()
    val prose = mutableListOf<String>()
    var code: StringBuilder? = null
    for (line in text.lines()) {
        if (line.trimStart().startsWith("```")) {
            if (code == null) {
                if (prose.isNotEmpty()) out += Block.Prose(prose.toList()).also { prose.clear() }
                code = StringBuilder()
            } else {
                out += Block.Code(code.toString().trimEnd('\n'))
                code = null
            }
            continue
        }
        if (code != null) code.append(line).append('\n') else prose += line
    }
    code?.let { out += Block.Code(it.toString().trimEnd('\n')) }
    if (prose.isNotEmpty()) out += Block.Prose(prose)
    return out
}

/**
 * Markdown shows what an agent writes as a phone should: headings, lists
 * and emphasis in the text, and code in boxes of their own that scroll
 * across.
 */
@Composable
private fun Markdown(text: String) {
    val parts = remember(text) { blocks(text) }
    val code = MaterialTheme.colorScheme.surfaceContainerHigh
    Column(Modifier.fillMaxWidth().padding(horizontal = 2.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        for (b in parts) when (b) {
            is Block.Code -> Surface(color = code, shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxWidth()) {
                Text(
                    b.text, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                    softWrap = false,
                    modifier = Modifier.horizontalScroll(rememberScrollState()).padding(12.dp),
                )
            }
            is Block.Prose -> {
                val styled = remember(b) { prose(b.lines, code) }
                if (styled.isNotBlank()) Text(styled, style = MaterialTheme.typography.bodyMedium)
            }
        }
    }
}

private val BulletLine = Regex("^(\\s*)[-*+] (.*)")
private val HeadingLine = Regex("^#{1,6} (.*)")

private fun prose(lines: List<String>, codeBackground: Color): AnnotatedString = buildAnnotatedString {
    val trimmed = lines.dropWhile { it.isBlank() }.dropLastWhile { it.isBlank() }
    trimmed.forEachIndexed { i, raw ->
        if (i > 0) append('\n')
        val line = raw.trimEnd()
        val bullet = BulletLine.find(line)
        val heading = HeadingLine.find(line)
        when {
            heading != null -> withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { inline(heading.groupValues[1], codeBackground) }
            bullet != null -> {
                append(bullet.groupValues[1])
                append("•  ")
                inline(bullet.groupValues[2], codeBackground)
            }
            else -> inline(line, codeBackground)
        }
    }
}

private fun AnnotatedString.Builder.inline(line: String, codeBackground: Color) {
    var i = 0
    while (i < line.length) {
        val code = line.indexOf('`', i)
        val bold = line.indexOf("**", i)
        val next = listOf(code, bold).filter { it >= 0 }.minOrNull()
        if (next == null) {
            append(line.substring(i))
            return
        }
        append(line.substring(i, next))
        if (next == code) {
            val end = line.indexOf('`', code + 1)
            if (end < 0) {
                append(line.substring(code))
                return
            }
            withStyle(SpanStyle(fontFamily = FontFamily.Monospace, background = codeBackground)) {
                append(line.substring(code + 1, end))
            }
            i = end + 1
        } else {
            val end = line.indexOf("**", bold + 2)
            if (end < 0) {
                append(line.substring(bold))
                return
            }
            withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(line.substring(bold + 2, end)) }
            i = end + 2
        }
    }
}
