package dev.ironpark.mako.ui

import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.outlined.Shield
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.term.TermSession
import dev.ironpark.mako.v2.SessionInfo

/**
 * SessionScreen is one session: its conversation, where its agent's driver
 * reads one, and its terminal, attached while it shows, with the approval
 * its agent waits for above both. The conversation's composer types into
 * the session without the terminal, on a node that can.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionScreen(mesh: Mesh, node: String, id: String, onBack: () -> Unit) {
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val session = sessions.firstOrNull { it.id == id }
    val scope = rememberCoroutineScope()
    val term = remember(node, id) { TermSession(mesh.daemon, id, node, scope) }
    val chat = session == null || session.caps and CAP_CHAT != 0
    // An agent whose conversation is not read shows its terminal alone.
    var picked by rememberSaveable(node, id) { mutableIntStateOf(0) }
    val tab = if (chat) picked else 1
    // An older node takes typing only on an attach, which the composer then
    // needs as well.
    val canSend = mesh.canSend(node)
    var draft by rememberSaveable(node, id) { mutableStateOf("") }
    var question by rememberSaveable(node, id) { mutableStateOf(false) }
    var request by remember(node, id) { mutableStateOf("") }
    val attached = tab == 1 || question || !canSend
    LaunchedEffect(request) { if (request.isNotEmpty() && tab == 0) question = true }
    LaunchedEffect(session?.state, session?.status) {
        if (session?.ended() == true || session?.status == "done" || session?.status == "idle") question = false
    }
    val density = LocalDensity.current
    val swipeThreshold = with(density) { 48.dp.toPx() }
    val swipeHint = stringResource(R.string.swipe_tabs)
    DisposableEffect(term, attached) {
        if (attached) term.start()
        onDispose { term.stop() }
    }
    LaunchedEffect(session?.cols, session?.rows) {
        if (session != null) term.follow(session.cols, session.rows)
    }
    var renaming by remember { mutableStateOf(false) }
    var fits by remember { mutableIntStateOf(0) } // asked to fit the session to the pane, so many times
    val act = actor()
    val tabState = rememberSaveableStateHolder()

    // The insets beneath are the panes' own: the keyboard's, or the
    // navigation bar's under the bar at the bottom.
    Scaffold(
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                },
                title = {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (session != null) {
                            AgentAvatar(session.agent, agentColor(nodes, node, session.agent), session.statusColor(), size = 34.dp)
                            Spacer(Modifier.width(12.dp))
                        }
                        Column {
                            Text(
                                session?.label().orEmpty(), maxLines = 1, overflow = TextOverflow.Ellipsis,
                                style = MaterialTheme.typography.titleMedium,
                            )
                            Text(
                                dotted(mesh.nodeName(node), baseName(session?.project.orEmpty()), session?.branch),
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                maxLines = 1, overflow = TextOverflow.Ellipsis,
                            )
                        }
                    }
                },
                actions = {
                    var menu by remember { mutableStateOf(false) }
                    IconButton(onClick = { menu = true }) { Icon(Icons.Filled.MoreVert, stringResource(R.string.session_actions)) }
                    DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(text = { Text(stringResource(R.string.answer_agent)) },
                            enabled = session?.state == "running", onClick = { menu = false; question = true })
                        DropdownMenuItem(text = { Text(stringResource(R.string.rename)) }, onClick = {
                            menu = false
                            renaming = true
                        })
                        DropdownMenuItem(text = { Text(stringResource(R.string.fit_screen)) }, onClick = {
                            menu = false
                            picked = 1
                            fits++
                        })
                        DropdownMenuItem(text = { Text(stringResource(R.string.stop_session)) }, onClick = {
                            menu = false
                            if (session != null) act { mesh.closeSession(session) }
                        })
                    }
                },
            )
        },
    ) { pad ->
        Column(Modifier.fillMaxSize().padding(pad).consumeWindowInsets(pad).imePadding()) {
            if (chat) {
                PrimaryTabRow(selectedTabIndex = tab, modifier = Modifier
                    .semantics { contentDescription = swipeHint }
                    .pointerInput(swipeThreshold) {
                        var distance = 0f
                        detectHorizontalDragGestures(
                            onDragStart = { distance = 0f },
                            onDragCancel = { distance = 0f },
                            onDragEnd = {
                                if (distance < -swipeThreshold) picked = 1
                                if (distance > swipeThreshold) picked = 0
                            },
                        ) { change, amount -> change.consume(); distance += amount }
                    }) {
                    Tab(selected = tab == 0, onClick = { picked = 0 }, text = { Text(stringResource(R.string.tab_chat)) })
                    Tab(selected = tab == 1, onClick = { picked = 1 }, text = { Text(stringResource(R.string.tab_terminal)) })
                }
            }
            AnimatedVisibility(session != null && session.hasApproval()) {
                if (session != null && session.hasApproval()) {
                    ApprovalCard(session, onAnswer = { decision -> act { mesh.answer(session, decision); if (decision == "terminal") question = true } })
                }
            }
            Box(Modifier.weight(1f)) {
                tabState.SaveableStateProvider(tab) {
                    if (tab == 0 && session != null) ChatView(mesh, session, Modifier.fillMaxSize(), onQuestion = { request = it }, onAnswer = { question = true })
                    else TerminalTab(term, fits, Modifier.fillMaxSize())
                }
            }
            if (tab == 0 && session != null) {
                Composer(
                    mesh = mesh, session = session, text = draft, onText = { draft = it },
                    onSend = { text -> if (canSend) mesh.send(session, text, keys = "\r") else term.submit(text) },
                    onInterrupt = { if (canSend) act { mesh.send(session, keys = "\u001b") } else term.send("\u001b") },
                )
            }
        }
    }

    if (question && session != null) {
        ModalBottomSheet(
            onDismissRequest = { question = false },
            sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
            sheetGesturesEnabled = false,
        ) {
            Column(Modifier.fillMaxHeight(0.85f).imePadding()) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.answer_agent), Modifier.weight(1f), style = MaterialTheme.typography.titleMedium)
                    TextButton(onClick = { question = false }) { Text(stringResource(R.string.close)) }
                }
                Text(stringResource(R.string.answer_hint), Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                TerminalTab(term, 0, Modifier.weight(1f))
            }
        }
    }

    if (renaming && session != null) RenameDialog(mesh, session, onDismiss = { renaming = false })
}

/**
 * ApprovalCard is the tool call an agent waits for you to allow: allowed,
 * denied, or left to the agent's own question in its terminal.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ApprovalCard(s: SessionInfo, onAnswer: (String) -> Unit) {
    val a = s.approval
    var expanded by remember(a.id) { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    LaunchedEffect(a.id) { haptic.performHapticFeedback(HapticFeedbackType.LongPress) }
    Card(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerHigh),
        shape = RoundedCornerShape(20.dp),
    ) {
        Column(Modifier.padding(16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Outlined.Shield, null, tint = StatusColors.Approval, modifier = Modifier.size(20.dp))
                Spacer(Modifier.width(8.dp))
                Text(
                    stringResource(R.string.approval_title, a.tool),
                    style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.weight(1f),
                )
            }
            if (a.detail.isNotEmpty()) {
                Surface(
                    color = MaterialTheme.colorScheme.surfaceContainerHighest,
                    shape = RoundedCornerShape(10.dp),
                    modifier = Modifier.fillMaxWidth().padding(top = 10.dp),
                ) {
                    Text(
                        a.detail, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                        maxLines = if (expanded) Int.MAX_VALUE else 4, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.padding(10.dp),
                    )
                }
            }
            if (expanded && a.input.isNotEmpty()) {
                Surface(
                    color = MaterialTheme.colorScheme.surfaceContainerHighest,
                    shape = RoundedCornerShape(10.dp),
                    modifier = Modifier.fillMaxWidth().padding(top = 8.dp).heightIn(max = 220.dp),
                ) {
                    Text(
                        a.input, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                        modifier = Modifier.padding(10.dp).verticalScroll(rememberScrollState()),
                    )
                }
            }
            FlowRow(
                Modifier.fillMaxWidth().padding(top = 12.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                TextButton(onClick = { expanded = !expanded }) {
                    Text(stringResource(if (expanded) R.string.less else R.string.details))
                }
                TextButton(onClick = { onAnswer("terminal") }) { Text(stringResource(R.string.in_terminal)) }
                OutlinedButton(onClick = { onAnswer("deny") }) { Text(stringResource(R.string.deny)) }
                Button(onClick = { onAnswer("allow") }) { Text(stringResource(R.string.allow)) }
            }
        }
    }
}
