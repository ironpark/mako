package dev.ironpark.mako.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsDraggedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
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
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.outlined.Chat
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.outlined.Build
import androidx.compose.material.icons.outlined.Checklist
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Language
import androidx.compose.material.icons.outlined.Layers
import androidx.compose.material.icons.outlined.QuestionAnswer
import androidx.compose.material.icons.outlined.Refresh
import androidx.compose.material.icons.outlined.Search
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SmallFloatingActionButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.net.envelope
import dev.ironpark.mako.v2.AgentModel
import dev.ironpark.mako.v2.BackgroundTask
import dev.ironpark.mako.v2.Chat
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.ListAgentModels
import dev.ironpark.mako.v2.LiveBlock
import dev.ironpark.mako.v2.Question
import dev.ironpark.mako.v2.ReadChat
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.SubAgent
import dev.ironpark.mako.v2.Usage
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/**
 * ChatView is a session's conversation, as its agent's log has it: streamed
 * live over WatchChat where supported and patched incrementally with ReadChat;
 * scrolling to the top reads further back. Above it, how much of the context
 * window and of the account's limits it has spent.
 */
@Composable
fun ChatView(
    mesh: Mesh, session: SessionInfo, modifier: Modifier = Modifier,
    agent: String = "", onQuestion: (String, List<Question>) -> Unit = { _, _ -> }, onAnswer: () -> Unit = {},
    onFile: (ChatLink.File) -> Unit = {},
    pending: List<PendingMessage> = emptyList(),
    onMessages: (List<ChatMessage>) -> Unit = {},
    extraWorkingSubagents: Int = 0,
) {
    var selectedAgent by rememberSaveable(session.id, agent) { mutableStateOf("") }
    var agentsExpanded by rememberSaveable(session.id, agent) { mutableStateOf(false) }
    var tasksExpanded by rememberSaveable(session.id, agent) { mutableStateOf(false) }
    val questionChanged by rememberUpdatedState(onQuestion)
    val messagesChanged by rememberUpdatedState(onMessages)
    var chat by remember(session.id, agent) { mutableStateOf<Chat?>(null) } // the latest messages
    var older by remember(session.id, agent) { mutableStateOf<List<ChatMessage>>(emptyList()) }
    var start by remember(session.id, agent) { mutableLongStateOf(0L) } // of what has been read, the earliest
    var error by remember(session.id, agent) { mutableStateOf<String?>(null) }
    var paging by remember(session.id, agent) { mutableStateOf(false) }
    var liveBlocks by remember(session.id, agent) { mutableStateOf<List<LiveBlock>>(emptyList()) }
    var watchConnected by remember(session.id, agent) { mutableStateOf(false) }
    val readMutex = remember(session.id, agent) { Mutex() }
    val scope = rememberCoroutineScope()
    val lifecycle = LocalLifecycleOwner.current

    // Observe node revisions so WatchChat and model catalog capabilities react as nodes connect.
    val nodes by mesh.nodes.collectAsState()
    val canWatch = remember(nodes, session.nodeId) { mesh.watchesChat(session.nodeId) }
    val supportsCatalog = remember(nodes, session.nodeId) { mesh.supportsOpenOverrides(session.nodeId) }

    var modelCatalog by remember(session.nodeId, session.agent) { mutableStateOf<List<AgentModel>>(emptyList()) }
    LaunchedEffect(session.nodeId, session.agent, session.hasControls(), supportsCatalog) {
        if (!session.hasControls() || !supportsCatalog || session.agent.isEmpty() || modelCatalog.isNotEmpty()) return@LaunchedEffect
        try {
            modelCatalog = mesh.daemon.call(
                envelope {
                    listAgentModels = ListAgentModels.newBuilder()
                        .setNodeId(session.nodeId)
                        .setAgent(session.agent)
                        .build()
                }
            ).agentModels.modelsList
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            // Optional catalog: fall back to AgentControls lists when unavailable.
        }
    }

    /**
     * Reads the messages before the offset [before], or with 0 the latest;
     * [seen] is the digest of the chat on show, which comes back unchanged
     * when the conversation is, or as a compact patch when [patch] is set.
     */
    suspend fun fetch(before: Long = 0, seen: Long = 0, patch: Boolean = false): Chat? = try {
        val req = ReadChat.newBuilder()
            .setNodeId(session.nodeId)
            .setSessionId(session.id)
            .setAgent(agent)
            .setBefore(before)
            .setSeen(seen)
            .apply { if (patch && before == 0L && seen != 0L) setPatch(true) }
            .build()
        mesh.daemon.chat(req).also { error = null }
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        error = e.message
        null
    }

    suspend fun read() = readMutex.withLock {
        val previous = chat
        val seen = previous?.digest ?: 0L
        val reply = fetch(before = 0L, seen = seen, patch = seen != 0L) ?: return@withLock
        if (reply.unchanged) return@withLock
        val merged = if (reply.base != 0L && (previous == null || previous.digest != reply.base)) {
            // Base digest drifted: request a full latest snapshot.
            fetch(before = 0L, seen = 0L, patch = false) ?: return@withLock
        } else {
            applyChatPatch(previous, reply)
        }
        // Do not silently skip records when the latest window moves forward.
        if (older.isNotEmpty() && previous != null && merged.start > previous.start) {
            older = emptyList()
        }
        chat = merged
        if (older.isEmpty()) start = merged.start
    }

    // Read immediately on lifecycle resume and state/status/approval changes.
    LaunchedEffect(lifecycle, session.id, agent, session.state, session.status, session.approval.id) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            read()
        }
    }

    // Stream live reply blocks and log growth notifications while running on capable nodes.
    LaunchedEffect(lifecycle, session.nodeId, session.id, agent, session.state, canWatch) {
        if (session.state != "running" || !canWatch) {
            watchConnected = false
            liveBlocks = emptyList()
            return@LaunchedEffect
        }
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            var backoffMs = 1_000L
            try {
                while (isActive) {
                    var waitingLoggedReads = 0
                    var heldLiveBlocks: List<LiveBlock> = emptyList()
                    try {
                        coroutineScope {
                            mesh.daemon.watchChat(session.nodeId, session.id, agent).collect { live ->
                                watchConnected = true
                                backoffMs = 1_000L
                                if (live.logged) {
                                    waitingLoggedReads++
                                    heldLiveBlocks = live.blocksList
                                    launch {
                                        try {
                                            read()
                                        } finally {
                                            waitingLoggedReads = (waitingLoggedReads - 1).coerceAtLeast(0)
                                            if (waitingLoggedReads == 0) {
                                                liveBlocks = heldLiveBlocks
                                            }
                                        }
                                    }
                                } else if (waitingLoggedReads > 0) {
                                    heldLiveBlocks = live.blocksList
                                } else {
                                    liveBlocks = live.blocksList
                                }
                            }
                        }
                    } catch (e: CancellationException) {
                        throw e
                    } catch (_: Exception) {
                        // Transient stream failure; reconnect with backoff while safety poll covers updates.
                    } finally {
                        watchConnected = false
                        liveBlocks = emptyList()
                    }
                    delay(backoffMs)
                    backoffMs = (backoffMs * 2L).coerceAtMost(10_000L)
                }
            } finally {
                watchConnected = false
                liveBlocks = emptyList()
            }
        }
    }

    // Light safety poll while WatchChat is connected (15s), or 3s fallback when disconnected/unsupported.
    LaunchedEffect(lifecycle, session.id, agent, session.state, watchConnected) {
        if (session.state != "running") return@LaunchedEffect
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            val intervalMs = if (watchConnected) 15_000L else 3_000L
            while (true) {
                delay(intervalMs)
                read()
            }
        }
    }

    val latest = chat?.messagesList.orEmpty()
    val messages = remember(older, latest) { joinMessages(older, latest) }
    val turns = remember(messages) { chatTurns(messages) }
    LaunchedEffect(messages) { messagesChanged(messages) }

    val activeLiveBlocks = if (session.state == "running") liveBlocks else emptyList()
    val streamBlocks = remember(activeLiveBlocks) {
        activeLiveBlocks.filter { (it.kind == "text" && it.text.isNotBlank()) || (it.kind == "tool" && it.tool.isNotBlank()) }
    }
    val showThinking = activeLiveBlocks.lastOrNull()?.kind == "thinking"
    val hasFeedContent = messages.isNotEmpty() || pending.isNotEmpty() || streamBlocks.isNotEmpty() || showThinking

    val links = rememberLinkOpener(mesh, session, onFile)
    val openImage: (String) -> Unit = { path -> onFile(ChatLink.File(path)) }
    val (activeAgents, endedAgents) = remember(session.state, chat?.agentsList) {
        partitionSubagents(session, chat?.agentsList.orEmpty())
    }
    val request = if (agent.isEmpty()) questionRequest(session, latest) else ""
    val keyed = remember(session.caps, request, latest) { if (agent.isEmpty()) keyedQuestions(session, latest) else emptyList() }
    LaunchedEffect(request, keyed) { if (agent.isEmpty()) questionChanged(request, keyed) }
    val list = rememberLazyListState()
    val atEnd by remember { derivedStateOf { !list.canScrollForward } }
    val dragging by list.interactionSource.collectIsDraggedAsState()
    var follow by remember(session.id, agent) { mutableStateOf(true) }
    var lastPendingCount by remember(session.id, agent) { mutableIntStateOf(0) }
    LaunchedEffect(pending.size) {
        if (pending.size > lastPendingCount) follow = true
        lastPendingCount = pending.size
    }
    LaunchedEffect(atEnd, dragging) {
        if (atEnd) follow = true else if (dragging) follow = false
    }
    LaunchedEffect(list, follow) {
        if (!follow) return@LaunchedEffect
        // Markdown parses off-thread: follow layout growth as well as new messages.
        snapshotFlow { list.layoutInfo to list.isScrollInProgress }.collect { (layout, scrolling) ->
            if (!scrolling && layout.totalItemsCount > 0) {
                val last = layout.visibleItemsInfo.lastOrNull()
                if (last?.index != layout.totalItemsCount - 1) list.scrollToItem(layout.totalItemsCount - 1)
                else {
                    val overflow = last.offset + last.size + layout.afterContentPadding - layout.viewportEndOffset
                    if (overflow > 0) list.scrollBy(overflow.toFloat())
                }
            }
        }
    }

    /**
     * Reads the messages before those on show, keeping in place the message
     * at the top, which the earlier ones then come in above.
     */
    val shownTurns by rememberUpdatedState(turns)
    fun loadOlder() {
        if (paging || start <= 0) return
        paging = true
        scope.launch {
            val anchor = list.layoutInfo.visibleItemsInfo.firstOrNull { (it.key as? String)?.startsWith("m:") == true }
            val before = shownTurns
            try {
                val read = fetch(before = start, seen = 0L, patch = false) ?: return@launch
                val joined = joinMessages(read.messagesList, older)
                val grew = joined.size > older.size
                older = joined
                start = read.start
                if (anchor == null || !grew) return@launch
                // Once the list has the earlier messages, the anchor's key is further down.
                val now = snapshotFlow { shownTurns }.first { it !== before }
                val i = now.indexOfFirst { "m:" + it.key == anchor.key }
                if (i < 0) return@launch
                list.scrollToItem(i + if (start > 0) 1 else 0)
                if (anchor.offset != 0) list.scrollBy(-anchor.offset.toFloat())
            } finally {
                paging = false
            }
        }
    }
    // At the top of what has been read, the earlier messages come by themselves.
    LaunchedEffect(list, start) {
        snapshotFlow { !follow && start > 0 && list.firstVisibleItemIndex == 0 && list.layoutInfo.totalItemsCount > 1 }
            .collect { top -> if (top) loadOlder() }
    }

    val sortedTasks = remember(session.tasksList) { sortBackgroundTasks(session.tasksList) }
    var lastTasks by remember(session.id, agent) { mutableStateOf<List<BackgroundTask>>(emptyList()) }
    if (sortedTasks.isNotEmpty()) lastTasks = sortedTasks

    Column(modifier) {
        chat?.takeIf { it.hasUsage() }?.let { UsageStrip(it.usage) }
        if (session.hasControls()) AgentControlsRow(session.controls, chat?.usage, catalog = modelCatalog) { mode, model, effort ->
            scope.launch { runCatching { mesh.configure(session, mode, model, effort) }.onFailure { error = it.message } }
        }
        AnimatedVisibility(
            visible = error != null,
            enter = MakoMotion.cardEnter,
            exit = MakoMotion.cardExit,
        ) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(error.orEmpty(), Modifier.weight(1f), color = MaterialTheme.colorScheme.error,
                    style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                IconButton(onClick = { scope.launch { read() } }) {
                    Icon(Icons.Outlined.Refresh, stringResource(R.string.retry))
                }
            }
        }
        AnimatedVisibility(
            visible = request.isNotEmpty(),
            enter = MakoMotion.cardEnter,
            exit = MakoMotion.cardExit,
        ) {
            val asked = remember(request, latest) {
                latest.lastOrNull { request == "question:" + it.id.ifEmpty { it.text } }?.let { it.detail.ifEmpty { it.text } }.orEmpty()
            }
            QuestionBanner(asked, onAnswer)
        }
        Box(Modifier.weight(1f).fillMaxWidth()) {
            val c = chat
            when {
                c == null && error == null && !hasFeedContent -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                c != null && !c.supported -> Message(stringResource(R.string.chat_unsupported), stringResource(R.string.chat_unsupported_body),
                    icon = Icons.Outlined.Terminal)
                error != null && !hasFeedContent -> Message(stringResource(R.string.chat_failed), error.orEmpty(),
                    stringResource(R.string.retry), { scope.launch { read() } }, icon = Icons.Outlined.CloudOff)
                !hasFeedContent -> Message(stringResource(R.string.chat_empty), stringResource(R.string.chat_empty_body),
                    icon = Icons.AutoMirrored.Outlined.Chat)
                else -> SelectionContainer {
                    LazyColumn(
                        state = list,
                        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 8.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        if (start > 0) {
                            item(key = "older") {
                                Box(Modifier.fillMaxWidth().heightIn(min = 48.dp), contentAlignment = Alignment.Center) {
                                    if (paging) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                                    else TextButton(onClick = ::loadOlder) { Text(stringResource(R.string.load_earlier)) }
                                }
                            }
                        }
                        items(turns, key = { "m:" + it.key }, contentType = { if (it.tools) "tools" else it.messages.first().role }) { t ->
                            val itemMod = Modifier.animateItem(
                                fadeInSpec = MakoMotion.fastTween(),
                                placementSpec = MakoMotion.SnappyIntOffsetSpring,
                            )
                            if (t.tools) ToolRun(t, itemMod)
                            else MessageItem(t.messages.first(), mesh, session, links, openImage, onAnchor = { follow = false }, modifier = itemMod)
                        }
                        if (agent.isEmpty() && pending.isNotEmpty()) {
                            items(pending, key = { "pending:${it.id}" }, contentType = { "pending_user" }) { p ->
                                PendingUserBubble(
                                    p,
                                    Modifier.animateItem(
                                        fadeInSpec = MakoMotion.fastTween(),
                                        placementSpec = MakoMotion.SnappyIntOffsetSpring,
                                    ),
                                )
                            }
                        }
                        if (streamBlocks.isNotEmpty()) {
                            items(streamBlocks, key = { "live:${it.index}:${it.kind}" }, contentType = { "live_${it.kind}" }) { block ->
                                val itemMod = Modifier.animateItem(
                                    fadeInSpec = MakoMotion.fastTween(),
                                    placementSpec = MakoMotion.SnappyIntOffsetSpring,
                                )
                                when (block.kind) {
                                    "text" -> Box(itemMod) {
                                        MarkdownMessage(block.text, onLink = links, onAnchor = { follow = false })
                                    }
                                    "tool" -> LiveToolCard(block, itemMod)
                                }
                            }
                        }
                        if (showThinking) {
                            item(key = "live:thinking", contentType = "live_thinking") {
                                LiveThinkingPill(
                                    Modifier.animateItem(
                                        fadeInSpec = MakoMotion.fastTween(),
                                        placementSpec = MakoMotion.SnappyIntOffsetSpring,
                                    ),
                                )
                            }
                        }
                        if (agent.isEmpty() && (activeAgents.isNotEmpty() || endedAgents.isNotEmpty())) {
                            item(key = "agents") {
                                SubagentSummary(
                                    activeAgents.size,
                                    endedAgents.size,
                                    agentsExpanded,
                                    Modifier.animateItem(
                                        fadeInSpec = MakoMotion.fastTween(),
                                        placementSpec = MakoMotion.SnappyIntOffsetSpring,
                                    ),
                                ) {
                                    agentsExpanded = !agentsExpanded
                                }
                            }
                            if (agentsExpanded) {
                                items(activeAgents + endedAgents, key = { "agent:" + it.id }) { sub ->
                                    SubagentRow(
                                        sub,
                                        session.state == "running",
                                        Modifier.animateItem(
                                            fadeInSpec = MakoMotion.fastTween(),
                                            placementSpec = MakoMotion.SnappyIntOffsetSpring,
                                        ),
                                        onClick = { selectedAgent = sub.id },
                                    )
                                }
                            }
                        }
                        if (agent.isEmpty() && session.working() && streamBlocks.isEmpty() && !showThinking) {
                            item(key = "working") {
                                Working(
                                    activeAgents.size + extraWorkingSubagents,
                                    Modifier.animateItem(
                                        fadeInSpec = MakoMotion.fastTween(),
                                        placementSpec = MakoMotion.SnappyIntOffsetSpring,
                                    ),
                                )
                            }
                        }
                    }
                }
            }
            // The overload of no scope: this one sits in a Box, in a Column.
            androidx.compose.animation.AnimatedVisibility(
                visible = !atEnd && hasFeedContent,
                enter = MakoMotion.scaleFadeEnter,
                exit = MakoMotion.scaleFadeExit,
                modifier = Modifier.align(Alignment.BottomEnd).padding(16.dp),
            ) {
                val fabInteraction = remember { MutableInteractionSource() }
                SmallFloatingActionButton(
                    interactionSource = fabInteraction,
                    modifier = Modifier.pressScale(fabInteraction, pressedScale = 0.92f),
                    onClick = {
                        follow = true
                        scope.launch { list.animateScrollToItem(list.layoutInfo.totalItemsCount.coerceAtLeast(1) - 1) }
                    },
                ) { Icon(Icons.Filled.KeyboardArrowDown, stringResource(R.string.to_latest)) }
            }
        }
        AnimatedVisibility(
            visible = agent.isEmpty() && session.state == "running" && session.status == "compacting",
            enter = MakoMotion.cardEnter,
            exit = MakoMotion.cardExit,
        ) {
            CompactingBanner()
        }
        AnimatedVisibility(
            visible = agent.isEmpty() && sortedTasks.isNotEmpty(),
            enter = MakoMotion.cardEnter,
            exit = MakoMotion.cardExit,
        ) {
            val shown = if (sortedTasks.isNotEmpty()) sortedTasks else lastTasks
            if (shown.isNotEmpty()) {
                BackgroundTasksCard(
                    tasks = shown,
                    expanded = tasksExpanded,
                    onToggle = { tasksExpanded = !tasksExpanded },
                )
            }
        }
    }
    if (selectedAgent.isNotEmpty()) {
        val sub = chat?.agentsList?.firstOrNull { it.id == selectedAgent }
        Dialog(onDismissRequest = { selectedAgent = "" }, properties = DialogProperties(usePlatformDefaultWidth = false)) {
            Surface(Modifier.fillMaxSize()) {
                Column(Modifier.safeDrawingPadding()) {
                    Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
                        IconButton(onClick = { selectedAgent = "" }) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back))
                        }
                        Column(Modifier.weight(1f)) {
                            Text(sub?.description?.ifEmpty { sub.kind } ?: stringResource(R.string.subagent),
                                style = MaterialTheme.typography.titleMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
                            Text(stringResource(R.string.subagent_read_only), style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                    key(selectedAgent) {
                        ChatView(mesh, session, Modifier.weight(1f), agent = selectedAgent, onFile = onFile)
                    }
                }
            }
        }
    }
}

/**
 * QuestionBanner is the agent's question, waiting for an answer it reads
 * from its terminal: what it asks, and a way there.
 */
@Composable
private fun QuestionBanner(question: String, onAnswer: () -> Unit) {
    Surface(
        onClick = onAnswer,
        color = MaterialTheme.colorScheme.tertiaryContainer,
        shape = RoundedCornerShape(16.dp),
        border = MakoColors.cardBorderStroke,
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp),
    ) {
        Row(Modifier.padding(start = 14.dp, end = 8.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Outlined.QuestionAnswer, null, Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onTertiaryContainer)
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.agent_asks), style = MaterialTheme.typography.labelLarge,
                    color = MaterialTheme.colorScheme.onTertiaryContainer)
                Text(question.lines().firstOrNull { it.isNotBlank() }?.trim() ?: stringResource(R.string.agent_asks_body),
                    style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis,
                    color = MaterialTheme.colorScheme.onTertiaryContainer.copy(alpha = 0.8f))
            }
            Spacer(Modifier.width(8.dp))
            FilledTonalButton(onClick = onAnswer) { Text(stringResource(R.string.answer)) }
        }
    }
}

private data class UsageMetric(val name: String, val used: Float, val resetText: String = "")

/**
 * UsageStrip shows context window fill, account rate limits (with reset time),
 * and estimated cost. Tapping toggles between Used % and Remaining %.
 */
@Composable
private fun UsageStrip(u: Usage) {
    var showRemaining by rememberSaveable { mutableStateOf(false) }
    val hasResets = remember(u.limitsList) { u.limitsList.any { it.resets > 0L } }
    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(hasResets) {
        if (!hasResets) return@LaunchedEffect
        while (true) {
            nowMs = System.currentTimeMillis()
            delay(60_000L)
        }
    }

    val quotaLabel = stringResource(R.string.usage_quota)
    val fiveHoursLabel = stringResource(R.string.usage_5h)
    val weekLabel = stringResource(R.string.usage_week)
    val contextLabel = stringResource(R.string.usage_context)

    val parts = buildList {
        if (u.window > 0L) add(UsageMetric(contextLabel, (u.context.toFloat() / u.window).coerceIn(0f, 1f)))
        for (l in u.limitsList) {
            val resetText = formatReset(l.resets, nowMs)
            val expired = l.resets > 0L && l.resets * 1000L <= nowMs
            val fraction = if (expired) 0f else (l.used / 100.0).toFloat().coerceIn(0f, 1f)
            add(UsageMetric(limitLabel(l.minutes, quotaLabel, fiveHoursLabel, weekLabel), fraction, resetText))
        }
    }
    val costText = remember(u.costUsd) { formatCost(u.costUsd) }
    if (parts.isEmpty() && costText.isEmpty()) return

    val toggleLabel = stringResource(R.string.usage_toggle_mode)
    Row(
        Modifier
            .fillMaxWidth()
            .clickable(
                enabled = parts.isNotEmpty(),
                role = Role.Button,
                onClickLabel = toggleLabel,
            ) { showRemaining = !showRemaining }
            .horizontalScroll(rememberScrollState())
            .padding(horizontal = 12.dp, vertical = 5.dp),
        horizontalArrangement = Arrangement.spacedBy(16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        for (metric in parts) {
            val color = when {
                metric.used >= 0.9f -> StatusColors.Approval
                metric.used >= 0.7f -> StatusColors.Waiting
                else -> MaterialTheme.colorScheme.primary
            }
            val targetProgress = if (showRemaining) (1f - metric.used).coerceIn(0f, 1f) else metric.used.coerceIn(0f, 1f)
            val animatedProgress by animateFloatAsState(
                targetValue = targetProgress,
                animationSpec = MakoMotion.GentleSpring,
                label = "usageBar",
            )
            val usedPct = (metric.used * 100f + 0.5f).toInt().coerceIn(0, 100)
            val leftPct = 100 - usedPct
            val pctText = if (showRemaining) stringResource(R.string.usage_left_pct, leftPct) else "$usedPct%"

            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(metric.name, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Spacer(Modifier.width(6.dp))
                LinearProgressIndicator(
                    progress = { animatedProgress },
                    modifier = Modifier.width(48.dp),
                    color = color,
                    strokeCap = StrokeCap.Round,
                    gapSize = 0.dp,
                    drawStopIndicator = {},
                )
                Spacer(Modifier.width(6.dp))
                Text(pctText, style = MaterialTheme.typography.labelSmall.tabular(), color = color)
                if (metric.resetText.isNotEmpty()) {
                    Text(
                        " · ${metric.resetText}",
                        style = MaterialTheme.typography.labelSmall.tabular(),
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
        if (costText.isNotEmpty()) {
            Surface(
                color = MaterialTheme.colorScheme.surfaceContainerLow,
                shape = RoundedCornerShape(50),
                border = MakoColors.cardBorderStroke,
            ) {
                Text(
                    costText,
                    style = MaterialTheme.typography.labelSmall.tabular(),
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp),
                )
            }
        }
    }
}

/** Working says the agent is at work, and how many of its subagents. */
@Composable
private fun Working(subagents: Int, modifier: Modifier = Modifier) {
    Row(modifier.padding(start = 6.dp, top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        PulsingStatusDot(StatusColors.Working, active = true, size = 8.dp)
        Spacer(Modifier.width(10.dp))
        Text(
            if (subagents > 0) stringResource(R.string.working_with, subagents) else stringResource(R.string.status_working),
            style = MaterialTheme.typography.bodySmall, color = StatusColors.Working,
        )
    }
}

/** Optimistic user bubble shown immediately while waiting for the agent log to include the sent prompt. */
@Composable
private fun PendingUserBubble(pending: PendingMessage, modifier: Modifier = Modifier) {
    val transition = rememberInfiniteTransition(label = "sendingPulse")
    val sendingAlpha by transition.animateFloat(
        initialValue = 0.45f,
        targetValue = 0.9f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 900),
            repeatMode = RepeatMode.Reverse,
        ),
        label = "sendingAlpha",
    )
    Column(
        modifier.fillMaxWidth(),
        horizontalAlignment = Alignment.End,
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Surface(
            color = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.62f),
            shape = RoundedCornerShape(20.dp, 20.dp, 6.dp, 20.dp),
            border = MakoColors.cardBorderStroke,
            modifier = Modifier.widthIn(max = 320.dp),
        ) {
            Text(
                pending.text,
                modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
                color = MaterialTheme.colorScheme.onPrimaryContainer.copy(alpha = 0.78f),
                style = MaterialTheme.typography.bodyMedium,
            )
        }
        Row(
            modifier = Modifier.padding(end = 6.dp).alpha(sendingAlpha),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(5.dp),
        ) {
            PulsingStatusDot(MaterialTheme.colorScheme.onSurfaceVariant, active = true, size = 5.dp)
            Text(
                stringResource(R.string.chat_sending),
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

/** Subtle breathing pill shown while the model streams a thinking block. */
@Composable
private fun LiveThinkingPill(modifier: Modifier = Modifier) {
    val transition = rememberInfiniteTransition(label = "thinkingBreath")
    val textAlpha by transition.animateFloat(
        initialValue = 0.55f,
        targetValue = 1.0f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 1100),
            repeatMode = RepeatMode.Reverse,
        ),
        label = "thinkingAlpha",
    )
    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        shape = RoundedCornerShape(50),
        border = MakoColors.cardBorderStroke,
        modifier = modifier,
    ) {
        Row(
            Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            PulsingStatusDot(StatusColors.Working, active = true, size = 7.dp)
            Text(
                stringResource(R.string.chat_thinking),
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.alpha(textAlpha),
            )
        }
    }
}

/** Live card for a tool call whose arguments are still streaming in before being logged. */
@Composable
private fun LiveToolCard(block: LiveBlock, modifier: Modifier = Modifier) {
    val preview = remember(block.text) { liveToolArgsPreview(block.text) }
    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        shape = RoundedCornerShape(12.dp),
        border = MakoColors.cardBorderStroke,
        modifier = modifier.fillMaxWidth(),
    ) {
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 9.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            PulsingStatusDot(StatusColors.Working, active = true, size = 8.dp)
            Spacer(Modifier.width(10.dp))
            Text(
                block.tool,
                style = MaterialTheme.typography.labelLarge,
                color = MaterialTheme.colorScheme.onSurface,
            )
            if (preview.isNotEmpty()) {
                Spacer(Modifier.width(8.dp))
                Text(
                    preview,
                    style = MaterialTheme.typography.bodySmall,
                    fontFamily = FontFamily.Monospace,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
            }
        }
    }
}

/** Compact banner shown while the session's agent is compacting its conversation context. */
@Composable
private fun CompactingBanner() {
    Surface(
        color = StatusColors.Compacting.copy(alpha = 0.10f),
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(MakoColors.hairlineWidth, StatusColors.Compacting.copy(alpha = 0.28f)),
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp),
    ) {
        Row(
            Modifier.padding(horizontal = 12.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            PulsingStatusDot(StatusColors.Compacting, active = true, size = 8.dp)
            Text(
                stringResource(R.string.chat_compacting),
                style = MaterialTheme.typography.labelMedium,
                color = StatusColors.Compacting,
            )
        }
    }
}

/** Collapsible card listing running and recently finished background shell commands. */
@Composable
private fun BackgroundTasksCard(
    tasks: List<BackgroundTask>,
    expanded: Boolean,
    onToggle: () -> Unit,
) {
    val runningCount = remember(tasks) { tasks.count { it.running } }
    val headline = if (runningCount > 0) {
        pluralStringResource(R.plurals.tasks_running_count, runningCount, runningCount)
    } else {
        pluralStringResource(R.plurals.tasks_total_count, tasks.size, tasks.size)
    }
    val topPreview = remember(tasks) {
        val first = tasks.first()
        first.description.ifBlank { first.command.lineSequence().firstOrNull()?.trim().orEmpty() }.ifBlank { first.id }
    }
    val chevronRotation by animateFloatAsState(
        targetValue = if (expanded) 180f else 0f,
        animationSpec = MakoMotion.SnappySpring,
        label = "tasksChevron",
    )

    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        shape = RoundedCornerShape(12.dp),
        border = MakoColors.cardBorderStroke,
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp),
    ) {
        Column(Modifier.animateContentSize(animationSpec = MakoMotion.GentleIntSizeSpring)) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .clickable(
                        role = Role.Button,
                        onClickLabel = stringResource(if (expanded) R.string.tasks_collapse else R.string.tasks_expand),
                        onClick = onToggle,
                    )
                    .heightIn(min = 42.dp)
                    .padding(horizontal = 12.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                if (runningCount > 0) {
                    PulsingStatusDot(StatusColors.Working, active = true, size = 8.dp)
                } else {
                    Icon(
                        Icons.Outlined.Terminal,
                        contentDescription = null,
                        modifier = Modifier.size(16.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                Text(
                    headline,
                    style = MaterialTheme.typography.labelLarge.tabular(),
                    color = if (runningCount > 0) StatusColors.Working else MaterialTheme.colorScheme.onSurface,
                )
                if (!expanded && topPreview.isNotEmpty()) {
                    Text(
                        topPreview,
                        style = MaterialTheme.typography.bodySmall,
                        fontFamily = FontFamily.Monospace,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f),
                    )
                } else {
                    Spacer(Modifier.weight(1f))
                }
                Icon(
                    Icons.Filled.KeyboardArrowDown,
                    contentDescription = null,
                    modifier = Modifier.size(18.dp).rotate(chevronRotation),
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            AnimatedVisibility(
                visible = expanded,
                enter = MakoMotion.cardEnter,
                exit = MakoMotion.cardExit,
            ) {
                Column {
                    HorizontalDivider(color = MakoColors.subtleDivider)
                    Column(
                        Modifier
                            .fillMaxWidth()
                            .heightIn(max = 200.dp)
                            .verticalScroll(rememberScrollState())
                            .padding(horizontal = 12.dp, vertical = 6.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        for (task in tasks) {
                            key(task.id.ifEmpty { task.command }) {
                                BackgroundTaskRow(task)
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun BackgroundTaskRow(task: BackgroundTask) {
    val failed = !task.running && (task.status == "failed" || (task.hasExitCode() && task.exitCode != 0))
    val dotColor = when {
        task.running -> StatusColors.Working
        failed -> StatusColors.Approval
        task.status == "completed" || task.status.isEmpty() -> StatusColors.Done
        else -> StatusColors.Idle
    }
    val commandLine = remember(task.command) { task.command.lineSequence().firstOrNull()?.trim().orEmpty() }
    val hasDesc = task.description.isNotBlank()
    val statusText = when {
        task.running -> stringResource(R.string.task_running)
        task.hasExitCode() && task.exitCode != 0 -> stringResource(R.string.task_exit_code, task.exitCode)
        task.status == "failed" -> stringResource(R.string.task_failed)
        task.status == "killed" -> stringResource(R.string.task_killed)
        task.status == "stopped" -> stringResource(R.string.task_stopped)
        else -> stringResource(R.string.task_finished)
    }

    Row(
        Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        PulsingStatusDot(dotColor, active = task.running, size = 7.dp)
        Column(Modifier.weight(1f)) {
            Text(
                if (hasDesc) task.description else commandLine.ifEmpty { task.id },
                style = MaterialTheme.typography.bodySmall,
                fontFamily = if (hasDesc) null else FontFamily.Monospace,
                fontWeight = if (hasDesc) FontWeight.Medium else FontWeight.Normal,
                color = MaterialTheme.colorScheme.onSurface,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (hasDesc && commandLine.isNotEmpty()) {
                Text(
                    commandLine,
                    style = MaterialTheme.typography.labelSmall,
                    fontFamily = FontFamily.Monospace,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        Text(
            statusText,
            style = MaterialTheme.typography.labelSmall.tabular(),
            color = if (failed) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun MessageItem(
    m: ChatMessage, mesh: Mesh, session: SessionInfo, onLink: (String) -> Unit, onImage: (String) -> Unit,
    onAnchor: () -> Unit = {},
    modifier: Modifier = Modifier,
) {
    when {
        m.role == "user" -> Column(modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(6.dp)) {
            // The images it holds, and those it names by their paths, over it.
            val named = remember(m.text, session.project) { imageRefs(m.text, session.project).filterNot { it in m.imagesList } }
            if (m.imagesCount + named.size > 0) FlowRow(
                Modifier.widthIn(max = 320.dp),
                horizontalArrangement = Arrangement.spacedBy(6.dp, Alignment.End),
                verticalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                for (p in m.imagesList) RemoteThumbnail(mesh.daemon, session.nodeId, p, named = false, onOpen = { onImage(p) })
                for (p in named) RemoteThumbnail(mesh.daemon, session.nodeId, p, named = true, onOpen = { onImage(p) })
            }
            if (m.text.isNotBlank()) Surface(
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
        else -> Box(modifier) { MarkdownMessage(m.text, onLink = onLink, onAnchor = onAnchor) }
    }
}

/**
 * ToolRun is a run of tool calls in a row, as one card whose rows fold. A
 * long run folds into one line that counts them and names their tools, as
 * on the desktop; the last run starts open, as the agent's work in hand.
 */
@Composable
private fun ToolRun(t: ChatTurn, modifier: Modifier = Modifier) {
    // Unless it is turned by hand, a run's fold follows whether it is the last.
    var turned by rememberSaveable(t.key) { mutableStateOf<Boolean?>(null) }
    val open = t.messages.size < FOLD_RUN || (turned ?: !t.folded)
    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        shape = RoundedCornerShape(12.dp),
        border = MakoColors.cardBorderStroke,
        modifier = modifier.fillMaxWidth(),
    ) {
        Column(Modifier.animateContentSize(animationSpec = MakoMotion.GentleIntSizeSpring).padding(vertical = 2.dp)) {
            if (t.messages.size >= FOLD_RUN) {
                val names = remember(t.messages) { t.messages.map { it.tool.ifEmpty { it.kind } }.filter { it.isNotEmpty() }.distinct() }
                val pending = t.messages.any { it.pending }
                val failed = t.messages.count { it.error }
                Row(
                    Modifier.fillMaxWidth().clickable(
                        role = Role.Button,
                        onClickLabel = stringResource(if (open) R.string.tool_run_collapse else R.string.tool_run_expand),
                    ) { turned = !open }.heightIn(min = 40.dp).padding(horizontal = 12.dp, vertical = 6.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    if (pending) PulsingStatusDot(StatusColors.Working, active = true, size = 8.dp)
                    else Icon(Icons.Outlined.Layers, null, Modifier.size(16.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.width(10.dp))
                    Text(stringResource(R.string.tool_calls, t.messages.size), style = MaterialTheme.typography.labelLarge.tabular(),
                        color = MaterialTheme.colorScheme.onSurfaceVariant)
                    if (failed > 0) Text(" · " + stringResource(R.string.tool_calls_failed, failed), style = MaterialTheme.typography.labelSmall.tabular(),
                        color = MaterialTheme.colorScheme.error)
                    Spacer(Modifier.width(8.dp))
                    Text(names.joinToString(", "), Modifier.weight(1f), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Icon(if (open) Icons.Filled.KeyboardArrowUp else Icons.Filled.KeyboardArrowDown, null,
                        Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            if (open) for (m in t.messages) key(messageKey(m)) { ToolItem(m) }
        }
    }
}

@Composable
private fun ToolItem(m: ChatMessage) {
    var open by remember(m.id, m.text) { mutableStateOf(m.error) }
    val tint = if (m.error) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant
    Column(
        Modifier.fillMaxWidth().clickable(enabled = m.result.isNotEmpty() || m.text.isNotEmpty()) { open = !open }
            .padding(horizontal = 12.dp, vertical = 6.dp)
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (m.pending) PulsingStatusDot(StatusColors.Working, active = true, size = 8.dp)
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
                Text(" ${m.exitCode}", style = MaterialTheme.typography.labelSmall.tabular(), color = MaterialTheme.colorScheme.error)
            }
        }
        if (open) {
            Surface(
                color = MaterialTheme.colorScheme.surfaceContainerHighest,
                shape = RoundedCornerShape(8.dp),
                modifier = Modifier.fillMaxWidth().padding(top = 8.dp).heightIn(max = 320.dp),
            ) {
                Text(
                    listOf(m.text, m.result).filter { it.isNotBlank() }.joinToString("\n\n"), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                    modifier = Modifier.padding(8.dp).verticalScroll(rememberScrollState()),
                )
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

@Composable
private fun SubagentSummary(active: Int, ended: Int, expanded: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    Surface(
        shape = RoundedCornerShape(10.dp),
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        border = MakoColors.cardBorderStroke,
        modifier = modifier,
    ) {
        Row(
            Modifier.fillMaxWidth().clickable(
                role = Role.Button,
                onClickLabel = stringResource(if (expanded) R.string.subagents_collapse else R.string.subagents_expand),
                onClick = onClick,
            ).heightIn(min = 48.dp).padding(horizontal = 10.dp, vertical = 6.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (active > 0) PulsingStatusDot(StatusColors.Working, active = true, size = 8.dp)
            else Icon(Icons.Outlined.SmartToy, null, Modifier.size(16.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(stringResource(R.string.subagents), Modifier.weight(1f), style = MaterialTheme.typography.labelLarge,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(stringResource(R.string.subagents_counts, active, ended), style = MaterialTheme.typography.labelSmall.tabular(),
                color = MaterialTheme.colorScheme.onSurfaceVariant)
            Icon(if (expanded) Icons.Filled.KeyboardArrowUp else Icons.Filled.KeyboardArrowDown,
                null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
private fun SubagentRow(sub: SubAgent, sessionRunning: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    val active = sub.running && sessionRunning
    val tint = if (active) StatusColors.Working else MaterialTheme.colorScheme.onSurfaceVariant
    Surface(
        shape = RoundedCornerShape(10.dp),
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        border = MakoColors.cardBorderStroke,
        modifier = modifier,
    ) {
        Row(
            Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick)
                .heightIn(min = 48.dp).padding(horizontal = 10.dp, vertical = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (active) PulsingStatusDot(tint, active = true, size = 8.dp)
            else Icon(Icons.Outlined.SmartToy, stringResource(R.string.subagent_ended), Modifier.size(16.dp), tint = tint)
            Column(Modifier.weight(1f)) {
                Text(sub.description.ifEmpty { sub.kind }.ifEmpty { stringResource(R.string.subagent) },
                    style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(dotted(sub.kind, stringResource(R.string.tool_calls, sub.toolCalls), sub.tool, sub.detail),
                    style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Icon(Icons.Filled.ChevronRight, null, Modifier.size(16.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}
