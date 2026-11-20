package dev.ironpark.mako.ui

import android.content.ClipData
import android.content.ClipboardManager
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.tween
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.isImeVisible
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Bedtime
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.FitScreen
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.Groups
import androidx.compose.material.icons.outlined.PushPin
import androidx.compose.material.icons.outlined.QuestionAnswer
import androidx.compose.material.icons.outlined.StopCircle
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.term.TermSession
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.Question
import dev.ironpark.mako.v2.ScreenDialog
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * SessionScreen is one session: its conversation, where its agent's driver
 * reads one, and its terminal, attached while it shows, with the approval
 * its agent waits for above both. The conversation's composer types into
 * the session without the terminal, on a node that can.
 */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
internal fun SessionScreen(
    mesh: Mesh, node: String, id: String, onBack: () -> Unit, home: @Composable () -> Unit, onFiles: (String) -> Unit,
    onAgents: (String) -> Unit = {},
    onFile: (ChatLink.File) -> Unit = {},
    project: ProjectItem? = null,
    enterFrom: Int = 0,
    onPin: ((Boolean) -> Unit)? = null,
    pinned: Boolean = false,
    terminalFirst: Boolean = false,
    onView: (terminal: Boolean) -> Unit = {},
    footer: @Composable (SessionMenu) -> Unit = {},
) {
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val session = sessions.firstOrNull { it.id == id }
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val lifecycle = LocalLifecycleOwner.current
    val term = remember(node, id) {
        TermSession(mesh.daemon, id, node, scope, onCopy = { text ->
            context.getSystemService(ClipboardManager::class.java)?.setPrimaryClip(ClipData.newPlainText("mako", text))
        })
    }
    val chat = session == null || session.caps and CAP_CHAT != 0
    // An agent whose conversation is not read shows its terminal alone.
    val pager = rememberPagerState(initialPage = if (terminalFirst && chat) 2 else 1) { if (chat) 3 else 2 }
    val terminalPage = if (chat) 2 else 1
    // Each tab keeps the view it was left on, as a browser's tab keeps its place.
    LaunchedEffect(pager.settledPage) { if (pager.settledPage > 0) onView(pager.settledPage == terminalPage && chat) }
    val focus = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(pager.settledPage) {
        focus.clearFocus(force = true)
        keyboard?.hide()
    }
    // An older node takes typing only on an attach, which the composer then
    // needs as well.
    val canSend = remember(nodes, node) { mesh.canSend(node) }
    val canSee = remember(nodes, node) { mesh.seesSession(node) }
    val canSleep = remember(nodes, node) { mesh.sleepsSession(node) }

    // Mark session attention as seen across the mesh while this screen is visible.
    LaunchedEffect(lifecycle, session?.id, session?.attentionSeq, session?.seenSeq, canSee) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            val current = session
            if (current != null && canSee && current.attentionSeq > current.seenSeq) {
                mesh.seeSession(current)
            }
        }
    }

    // Optimistic pending user messages for this session until logged or expired.
    var pendingMessages by remember(node, id) { mutableStateOf<List<PendingMessage>>(emptyList()) }
    var latestMessages by remember(node, id) { mutableStateOf<List<ChatMessage>>(emptyList()) }
    var pendingSeq by remember(node, id) { mutableLongStateOf(0L) }
    LaunchedEffect(pendingMessages) {
        val waitMs = nextPendingExpiryDelayMs(pendingMessages, System.currentTimeMillis()) ?: return@LaunchedEffect
        delay(waitMs)
        pendingMessages = settlePending(pendingMessages, latestMessages, System.currentTimeMillis())
    }

    // What was typed and not sent outlasts this screen and the app.
    val drafts = rememberDrafts()
    val draft = drafts[node, id]
    var question by rememberSaveable(node, id) { mutableStateOf(false) }
    var request by remember(node, id) { mutableStateOf("") }
    // Questions answered in a form: those of the approval, or those the
    // agent asks in its terminal's dialog, which its node types answers to.
    var keyed by remember(node, id) { mutableStateOf(emptyList<Question>()) }
    var form by rememberSaveable(node, id) { mutableStateOf(false) }
    var planning by rememberSaveable(node, id) { mutableStateOf(false) }
    val asked = if (session?.asks() == true) session.approval.questionsList else keyed
    // A session asleep, or restored, starts again on its conversation once
    // attached: opening it wakes it, so what is typed in chat reaches it.
    val asleep = session?.state == "stopped"
    val attached = pager.currentPage == terminalPage || pager.targetPage == terminalPage || question || !canSend || asleep
    LaunchedEffect(request, pager.settledPage, pager.isScrollInProgress) {
        if (request.isNotEmpty() && chat && pager.settledPage == 1 && !pager.isScrollInProgress) {
            if (keyed.isNotEmpty()) form = true else question = true
        }
    }
    LaunchedEffect(session?.state, session?.status) {
        if (session?.ended() == true || session?.status == "done" || session?.status == "idle") {
            question = false
            form = false
        }
    }
    LaunchedEffect(asked.isEmpty()) { if (asked.isEmpty()) form = false }
    val swipeHint = stringResource(R.string.swipe_tabs)
    DisposableEffect(term, attached) {
        if (attached) term.start()
        onDispose { term.stop() }
    }
    LaunchedEffect(session?.cols, session?.rows) {
        if (session != null) term.follow(session.cols, session.rows)
    }
    var renaming by remember { mutableStateOf(false) }
    var stopping by remember { mutableStateOf(false) }
    var fits by remember { mutableIntStateOf(0) } // asked to fit the session to the pane, so many times
    val approvalId = session?.approval?.id.orEmpty()
    var approvalExpanded by remember(approvalId) { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    LaunchedEffect(approvalId) {
        if (approvalId.isNotEmpty()) haptic.performHapticFeedback(HapticFeedbackType.LongPress)
    }
    val act = actor()

    // Preserve last dialog/approval snapshot so cardExit collapses smoothly instead of snapping empty.
    var lastDialog by remember(node, id) { mutableStateOf<ScreenDialog?>(null) }
    if (session != null && session.hasDialog()) lastDialog = session.dialog
    var lastApprovalSession by remember(node, id) { mutableStateOf<SessionInfo?>(null) }
    if (session != null && session.hasApproval()) lastApprovalSession = session

    val onTerminalPage = pager.currentPage == terminalPage
    // The session's actions, which the bar at the bottom offers from its menu.
    val menu = SessionMenu { close ->
        DropdownMenuItem(text = { Text(stringResource(R.string.project_files)) },
            leadingIcon = { Icon(Icons.Outlined.Folder, null) }, enabled = session != null,
            onClick = { close(); onFiles(session?.project.orEmpty().ifBlank { "~" }) })
        DropdownMenuItem(text = { Text(stringResource(R.string.project_agents)) },
            leadingIcon = { Icon(Icons.Outlined.Groups, null) },
            enabled = session != null && session.project.isNotEmpty() && mesh.readsProjectAgents(node),
            onClick = { close(); onAgents(session?.project.orEmpty()) })
        DropdownMenuItem(text = { Text(stringResource(R.string.answer_agent)) },
            leadingIcon = { Icon(Icons.Outlined.QuestionAnswer, null) },
            enabled = session?.state == "running", onClick = { close(); question = true })
        DropdownMenuItem(text = { Text(stringResource(R.string.fit_screen)) },
            leadingIcon = { Icon(Icons.Outlined.FitScreen, null) }, onClick = {
                close()
                scope.launch { pager.animateScrollToPage(terminalPage) }
                fits++
            })
        HorizontalDivider()
        if (onPin != null) DropdownMenuItem(text = { Text(stringResource(if (pinned) R.string.unpin else R.string.pin_to_top)) },
            leadingIcon = { Icon(Icons.Outlined.PushPin, null) }, onClick = { close(); onPin(!pinned) })
        DropdownMenuItem(text = { Text(stringResource(R.string.rename)) },
            leadingIcon = { Icon(Icons.Outlined.Edit, null) }, enabled = session != null, onClick = {
                close()
                renaming = true
            })
        DropdownMenuItem(
            text = { Text(stringResource(R.string.sleep_session_menu)) },
            leadingIcon = { Icon(Icons.Outlined.Bedtime, null) },
            enabled = session != null && session.state == "running" && canSleep,
            onClick = {
                close()
                val target = session ?: return@DropdownMenuItem
                act { mesh.sleepSession(target) }
            },
        )
        DropdownMenuItem(text = { Text(stringResource(R.string.stop_session_menu), color = MaterialTheme.colorScheme.error) },
            leadingIcon = { Icon(Icons.Outlined.StopCircle, null, tint = MaterialTheme.colorScheme.error) },
            enabled = session != null && !session.ended(), onClick = {
                close()
                stopping = true
            })
    }
    val keyboardUp = WindowInsets.isImeVisible
    // A tab opened from the one beside it comes in from that side.
    val arrive = remember(node, id) { Animatable(if (enterFrom == 0) 1f else 0f) }
    LaunchedEffect(arrive) { arrive.animateTo(1f, tween(220)) }
    // The insets beneath are the panes' own: the keyboard's, or the
    // navigation bar's under the bar at the bottom.
    SessionPager(pager, onHome = onBack, modifier = Modifier.semantics { contentDescription = swipeHint }, footer = { footer(menu) },
        footerShown = !keyboardUp, header = {
        // One header over chat and terminal, which stays as they change.
        Surface(color = MaterialTheme.colorScheme.surface) {
            Column {
                SessionHeader(
                    mesh, node, session, project,
                    switch = {
                        if (chat) SessionViewSwitch(terminal = onTerminalPage, onTerminal = { terminal ->
                            scope.launch { pager.animateScrollToPage(if (terminal) terminalPage else 1) }
                        })
                    },
                )
                val showDialog = session != null && !session.hasApproval() && session.hasDialog()
                AnimatedVisibility(
                    visible = showDialog,
                    enter = MakoMotion.cardEnter,
                    exit = MakoMotion.cardExit,
                ) {
                    val activeDialog = if (showDialog) session.dialog else lastDialog
                    if (activeDialog != null) {
                        DialogCard(
                            activeDialog,
                            onTerminal = { if (showDialog) question = true },
                        ) { option ->
                            if (showDialog) act { mesh.answerDialog(session, option) }
                        }
                    }
                }
                val showApproval = session != null && session.hasApproval()
                AnimatedVisibility(
                    visible = showApproval,
                    enter = MakoMotion.cardEnter,
                    exit = MakoMotion.cardExit,
                ) {
                    val active = if (showApproval) session else lastApprovalSession
                    if (active != null && active.hasApproval()) {
                        val decide = { decision: String ->
                            if (showApproval) {
                                act { mesh.answer(session, decision); if (decision == "terminal") question = true }
                            }
                        }
                        when {
                            active.asks() -> QuestionCard(
                                active.approval.questionsList,
                                onAnswer = { if (showApproval) form = true },
                                onDecision = decide,
                            )
                            active.plans() -> PlanCard(
                                active.approval,
                                onReview = { if (showApproval) planning = true },
                                onTerminal = { decide("terminal") },
                            )
                            else -> ApprovalCard(
                                active,
                                approvalExpanded,
                                onExpand = { approvalExpanded = !approvalExpanded },
                            ) { decision, option, message ->
                                if (showApproval) {
                                    act { mesh.answer(session, decision, option = option, message = message); if (decision == "terminal") question = true }
                                }
                            }
                        }
                    }
                }
            }
        }
    }) { page ->
        if (page == 0) {
            home()
            return@SessionPager
        }
        val pageTab = if (chat) page - 1 else 1
        Surface(Modifier.fillMaxSize().graphicsLayer {
            alpha = 0.4f + 0.6f * arrive.value
            translationX = (1f - arrive.value) * enterFrom * 32.dp.toPx()
        }, color = MaterialTheme.colorScheme.background) {
            // The bar at the bottom stands over the navigation bar, until the keyboard takes its place.
            val insets = if (keyboardUp) Modifier else Modifier.consumeWindowInsets(WindowInsets.navigationBars)
            Column(Modifier.fillMaxSize().consumeWindowInsets(WindowInsets.statusBars).then(insets).imePadding()) {
                Box(Modifier.weight(1f)) {
                    if (pageTab == 0 && session != null) ChatView(
                        mesh, session, Modifier.fillMaxSize(),
                        onQuestion = { r, qs -> request = r; keyed = qs },
                        onAnswer = { if (keyed.isNotEmpty()) form = true else question = true },
                        onFile = onFile,
                        pending = pendingMessages,
                        onMessages = { msgs ->
                            latestMessages = msgs
                            pendingMessages = settlePending(pendingMessages, msgs, System.currentTimeMillis())
                        },
                    )
                    else if (pageTab == 1) TerminalTab(term, fits, Modifier.fillMaxSize())
                }
                if (pageTab == 0 && session != null) {
                    Composer(
                        mesh = mesh, session = session, text = draft, onText = { drafts[node, id] = it },
                        onSend = { text ->
                            val now = System.currentTimeMillis()
                            val msgId = ++pendingSeq
                            pendingMessages = notePending(pendingMessages, latestMessages, text, nowMs = now, id = msgId)
                            try {
                                if (canSend) mesh.send(session, text, keys = "\r") else term.submit(text)
                            } catch (e: Throwable) {
                                pendingMessages = pendingMessages.filterNot { it.id == msgId }
                                throw e
                            }
                        },
                        onInterrupt = { if (canSend) act { mesh.send(session, keys = "\u001b") } else term.send("\u001b") },
                        onOpen = { path -> onFile(ChatLink.File(path)) },
                    )
                }
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

    if (planning && session != null && session.plans()) {
        ModalBottomSheet(
            onDismissRequest = { planning = false },
            sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
        ) {
            Column(Modifier.fillMaxHeight(0.9f).imePadding().padding(horizontal = 16.dp)) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.plan_ready), Modifier.weight(1f), style = MaterialTheme.typography.titleMedium)
                    TextButton(onClick = {
                        planning = false
                        act { mesh.answer(session, "terminal"); question = true }
                    }) { Text(stringResource(R.string.in_terminal)) }
                }
                PlanReview(
                    session.approval, modifier = Modifier.weight(1f).padding(bottom = 16.dp),
                    onApprove = { option -> planning = false; act { mesh.answer(session, "allow", option = option) } },
                    onKeepPlanning = { why ->
                        planning = false
                        act { mesh.answer(session, "deny", message = why) }
                    },
                )
            }
        }
    }

    if (form && session != null && asked.isNotEmpty()) {
        val approval = session.asks()
        ModalBottomSheet(
            onDismissRequest = { form = false },
            sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
        ) {
            Column(Modifier.fillMaxHeight(0.85f).imePadding()) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.agent_asks), Modifier.weight(1f), style = MaterialTheme.typography.titleMedium)
                    TextButton(onClick = {
                        form = false
                        if (approval) act { mesh.answer(session, "terminal") }
                        question = true
                    }) { Text(stringResource(R.string.in_terminal)) }
                }
                QuestionForm(
                    asked,
                    onSubmit = { answers ->
                        form = false
                        act {
                            if (approval) mesh.answer(session, "allow", answers)
                            else mesh.answerQuestions(session, asked, answers)
                        }
                    },
                    modifier = Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(16.dp),
                )
            }
        }
    }

    if (renaming && session != null) RenameDialog(mesh, session, onDismiss = { renaming = false })
    if (stopping && session != null) StopDialog(session, onDismiss = { stopping = false }) { act { mesh.closeSession(session) } }
}

/**
 * QuestionCard is the questions an agent waits for you to answer: in a
 * form, declined, or left to its own dialog in its terminal, from the
 * corner.
 */
@Composable
private fun QuestionCard(questions: List<Question>, onAnswer: () -> Unit, onDecision: (String) -> Unit) {
    WaitCard(Icons.Outlined.QuestionAnswer, stringResource(R.string.agent_asks), onTerminal = { onDecision("terminal") }) {
        Text(
            questions.first().question, style = MaterialTheme.typography.bodyMedium,
            maxLines = 3, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 4.dp),
        )
        if (questions.size > 1) Text(
            pluralStringResource(R.plurals.more_questions, questions.size - 1, questions.size - 1),
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Row(Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { onDecision("deny") }, modifier = Modifier.weight(1f).pressScale()) { Text(stringResource(R.string.decline)) }
            Button(onClick = onAnswer, modifier = Modifier.weight(1f).pressScale()) { Text(stringResource(R.string.answer)) }
        }
    }
}


/** SessionMenu is a session's actions, as the items of a menu that close calls to close. */
internal class SessionMenu(val items: @Composable ColumnScope.(close: () -> Unit) -> Unit)

/**
 * SessionHeader says where the session shown is: its project, by its mark
 * and name, as home listed it, and its folder of the project, branch and
 * computer; with the switch between its chat and its terminal.
 */
@Composable
private fun SessionHeader(mesh: Mesh, node: String, session: SessionInfo?, project: ProjectItem?, switch: @Composable () -> Unit) {
    // Collected, not read: an agent's colour may come after the header.
    val nodes by mesh.nodes.collectAsState()
    Row(
        Modifier.fillMaxWidth().statusBarsPadding().heightIn(min = 56.dp).padding(start = 12.dp, end = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (project != null) ProjectTile(project.look, Modifier.shared("tile:" + project.key), size = 32.dp)
        else if (session != null) AgentAvatar(session.agent, agentColor(nodes, node, session.agent), session.statusColor(), size = 32.dp)
        Spacer(Modifier.width(10.dp))
        Column(Modifier.weight(1f)) {
            Text(
                project?.label ?: session?.label().orEmpty(), maxLines = 1, overflow = TextOverflow.Ellipsis,
                style = MaterialTheme.typography.titleMedium,
                modifier = if (project != null) Modifier.sharedBounds("name:" + project.key) else Modifier,
            )
            Text(
                dotted(session?.let { project?.tags?.get(it.id) }, session?.branch, mesh.nodeName(node)),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
        switch()
    }
}
