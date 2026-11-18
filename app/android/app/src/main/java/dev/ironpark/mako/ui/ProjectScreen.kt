package dev.ironpark.mako.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.spring
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.scaleOut
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.border
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.windowInsetsBottomHeight
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material.icons.outlined.DeleteOutline
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Inbox
import androidx.compose.material.icons.outlined.PushPin
import androidx.compose.material.icons.outlined.StopCircle
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.core.content.edit
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.ReadChat
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.workspace.view
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * ProjectScreen is a project as a workspace of tabs, as a phone's browser
 * has them: one tab, a session, fills the screen, with a bar at the bottom
 * to go back, to see every tab, to open a new one and for the session's
 * actions. A swipe along the bar's tab goes to the one beside it. The
 * tabs are the project's sessions on the computers, not the phone's: a
 * tab is never closed here but by stopping its session, which says so and
 * asks. key names the project, tab the session to open on (else the
 * project's choice: what waits for you, else the tab looked at last);
 * picked brings the session a new tab started, to show it.
 */
@Composable
internal fun ProjectScreen(
    mesh: Mesh,
    key: String,
    tab: String?,
    space: String,
    picked: StateFlow<String>,
    onPicked: () -> Unit,
    onBack: () -> Unit,
    onNew: (ProjectItem) -> Unit,
    onBlock: (String, Panel) -> Unit,
    onFiles: (String, String) -> Unit,
    onAgents: (SessionInfo, String) -> Unit,
    onFile: (SessionInfo, ChatLink.File) -> Unit,
    onShown: (String, String) -> Unit,
    home: @Composable () -> Unit,
) {
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val loaded by mesh.loaded.collectAsState()
    val workspace by mesh.workspace.collectAsState()
    val view = remember(workspace.state) { workspace.state.view() }
    val items = remember(view, sessions, nodes) { projectIndex(view, sessions, mesh::nodeName) }
    var projectKey by rememberSaveable { mutableStateOf(key) }
    var current by rememberSaveable { mutableStateOf(tab.orEmpty()) }
    var overview by rememberSaveable { mutableStateOf(false) }
    // The tabs left on their terminal, by session, for each to open as it was left.
    var onTerminal by rememberSaveable { mutableStateOf(listOf<String>()) }
    // The project as it is now; one whose key changed, as when the workspace
    // comes after the sessions, found again by the tab shown.
    val project = items.firstOrNull { it.key == projectKey } ?: items.projectOf(current)
    LaunchedEffect(project?.key) { if (project != null && project.key != projectKey) projectKey = project.key }
    val pinned = remember(view) { view.pinnedSessions.toSet() }
    val tabs = remember(project, pinned) { tabOrder(project?.sessions.orEmpty(), pinned) }
    val context = LocalContext.current
    val last = remember(context) {
        val prefs = context.getSharedPreferences("home", android.content.Context.MODE_PRIVATE)
        LastTabs({ prefs.getString("lastTabs", "").orEmpty() }, { v -> prefs.edit { putString("lastTabs", v) } })
    }
    // Which side a tab came from, for it to come in from there.
    var enterFrom by remember { mutableIntStateOf(0) }
    var order by remember { mutableStateOf(emptyList<String>()) }
    val ids = tabs.map { it.id }
    LaunchedEffect(ids) {
        if (ids.isEmpty()) return@LaunchedEffect
        if (current !in ids) {
            enterFrom = 0
            current = (if (current.isNotEmpty() && current in order) tabAfter(order, current, tabs) else openingTab(tabs, last[projectKey])?.id).orEmpty()
        }
        order = ids
    }
    val pick by picked.collectAsState()
    LaunchedEffect(pick, ids) {
        if (pick.isNotEmpty() && pick in ids) {
            current = pick
            overview = false
            onPicked()
        }
    }
    LaunchedEffect(current, project?.key) {
        if (current.isNotEmpty() && project != null) {
            last[project.key] = current
            onShown(project.key, current)
        }
    }
    val haptic = LocalHapticFeedback.current
    fun go(step: Int) {
        val next = neighbourTab(tabs, current, step) ?: return
        haptic.performHapticFeedback(HapticFeedbackType.SegmentTick)
        enterFrom = step
        current = next
    }
    val shown = tabs.firstOrNull { it.id == current }
    val panelsNode = remember(view, nodes) { mesh.panelsNode() }
    val sources = rememberPanelSources(mesh, panelsNode, space)
    val panels = remember(sources, tabs) { projectPanels(sources.pages, sources.agents, tabs.map { it.id }.toSet()) }
    val act = actor()
    val elsewhere = needYouElsewhere(sessions, project)
    // No tab to show because the computers they are on cannot be reached, rather than none being there.
    val offline = tabs.isEmpty() && nodes.any { it.linkDown() } &&
        (project == null || project.nodes.any { n -> nodes.any { it.id == n && it.linkDown() } })
    val noTabs = loaded && tabs.isEmpty() && !offline

    BackHandler(overview && shown != null) { overview = false }
    Box(Modifier.fillMaxSize()) {
        if (shown != null) key(shown.id) {
            SessionScreen(
                mesh, shown.nodeId, shown.id, onBack = onBack, home = home,
                onFiles = { root -> onFiles(shown.nodeId, root) },
                onAgents = { path -> onAgents(shown, path) },
                onFile = { link -> onFile(shown, link) },
                project = project, enterFrom = enterFrom,
                pinned = shown.id in pinned,
                terminalFirst = shown.id in onTerminal,
                onView = { t -> onTerminal = if (t) (onTerminal - shown.id + shown.id).takeLast(32) else onTerminal - shown.id },
                onPin = if (workspace.editable && view.held) ({ pin -> act { mesh.pinSession(shown, pin) } }) else null,
                footer = { menu ->
                    TabBar(
                        tabs, shown, nodes, elsewhere,
                        onBack = onBack, onOverview = { overview = true },
                        onNew = { project?.let(onNew) }, onStep = ::go, menu = menu,
                    )
                },
            )
        } else if (offline) {
            val down = nodes.filter { it.linkDown() }
            Message(
                stringResource(R.string.unreachable, down.joinToString(", ") { it.name }), stringResource(R.string.offline_body),
                stringResource(R.string.reconnect), { act { down.forEach { mesh.relink(it.id) } } },
                icon = Icons.Outlined.CloudOff, secondary = stringResource(R.string.back), onSecondary = onBack,
            )
        } else if (!noTabs) Loading(stringResource(R.string.reaching_computers))
        AnimatedVisibility(
            (overview || noTabs) && !offline,
            enter = fadeIn(spring(stiffness = 600f)) + scaleIn(spring(stiffness = 600f), initialScale = 1.06f),
            exit = fadeOut(spring(stiffness = 600f)) + scaleOut(spring(stiffness = 600f), targetScale = 1.06f),
        ) {
            TabOverview(
                mesh, project, tabs, current, nodes, panels, panelsNode, pinned, workspace.editable && view.held,
                onClose = { if (shown != null) overview = false else onBack() },
                onPick = { id ->
                    enterFrom = 0
                    current = id
                    overview = false
                },
                onNew = { project?.let(onNew) },
                onPanel = { p -> panelsNode?.let { onBlock(it, p) } },
            )
        }
    }
}

/**
 * TabBar is the workspace's bar at the bottom, in reach of a thumb: back
 * to the projects, marked when a session elsewhere waits for you; the tab
 * shown, with how many there are, which opens them all and goes to the
 * one beside it when swiped; a new tab; and the session's actions.
 */
@Composable
private fun TabBar(
    tabs: List<SessionInfo>, shown: SessionInfo, nodes: List<Node>, elsewhere: Int,
    onBack: () -> Unit, onOverview: () -> Unit, onNew: () -> Unit, onStep: (Int) -> Unit, menu: SessionMenu,
) {
    var open by remember { mutableStateOf(false) }
    Surface(color = MaterialTheme.colorScheme.surfaceContainer) {
        Column {
            HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
            Row(
                Modifier.fillMaxWidth().navigationBarsPadding().height(60.dp).padding(horizontal = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                IconButton(onClick = onBack) {
                    BadgedBox(badge = { if (elsewhere > 0) Badge(containerColor = StatusColors.Approval) }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.projects))
                    }
                }
                // As wide as the bar allows on a phone, not wider than reads on a tablet or turned.
                Box(Modifier.weight(1f)) { TabPill(tabs, shown, nodes, Modifier.widthIn(max = 520.dp).fillMaxWidth(), onOverview, onStep) }
                IconButton(onClick = onNew) { Icon(Icons.Filled.Add, stringResource(R.string.new_tab)) }
                Box {
                    IconButton(onClick = { open = true }) { Icon(Icons.Filled.MoreVert, stringResource(R.string.session_actions)) }
                    DropdownMenu(expanded = open, onDismissRequest = { open = false }) { menu.items(this) { open = false } }
                }
            }
        }
    }
}

/**
 * TabPill is the tab shown, as a browser's address bar names its page:
 * how many tabs there are, marked when another waits for you, then the
 * session's agent, status and name. Tapped it shows every tab; swiped, it
 * follows the finger and goes to the tab on that side.
 */
@Composable
private fun TabPill(
    tabs: List<SessionInfo>, shown: SessionInfo, nodes: List<Node>, modifier: Modifier,
    onOverview: () -> Unit, onStep: (Int) -> Unit,
) {
    val drag = remember { Animatable(0f) }
    val scope = rememberCoroutineScope()
    val threshold = with(LocalDensity.current) { 56.dp.toPx() }
    val othersWait = tabs.any { it.id != shown.id && it.needsYou() }
    val at = tabs.indexOfFirst { it.id == shown.id }
    val label = stringResource(R.string.tabs_of, at + 1, tabs.size, shown.label())
    val previous = stringResource(R.string.previous_tab)
    val next = stringResource(R.string.next_tab)
    Surface(
        onClick = onOverview,
        shape = RoundedCornerShape(14.dp),
        color = MaterialTheme.colorScheme.surfaceContainerHighest,
        modifier = modifier.padding(horizontal = 4.dp).heightIn(min = 44.dp)
            .semantics {
                contentDescription = label
                customActions = listOfNotNull(
                    CustomAccessibilityAction(previous) { onStep(-1); true }.takeIf { at > 0 },
                    CustomAccessibilityAction(next) { onStep(1); true }.takeIf { at < tabs.size - 1 },
                )
            }
            .pointerInput(tabs.size, at) {
                detectHorizontalDragGestures(
                    onDragEnd = {
                        val d = drag.value
                        scope.launch { drag.animateTo(0f, spring(stiffness = 800f)) }
                        if (d < -threshold) onStep(1) else if (d > threshold) onStep(-1)
                    },
                    onDragCancel = { scope.launch { drag.animateTo(0f) } },
                ) { change, amount ->
                    change.consume()
                    // Resists towards a side with no tab.
                    val blocked = (amount < 0 && at >= tabs.size - 1) || (amount > 0 && at <= 0)
                    scope.launch { drag.snapTo(drag.value + amount * if (blocked) 0.25f else 0.6f) }
                }
            },
    ) {
        Row(Modifier.padding(start = 8.dp, end = 12.dp), verticalAlignment = Alignment.CenterVertically) {
            TabCount(tabs.size, othersWait)
            Row(
                Modifier.weight(1f).padding(start = 10.dp).offset { IntOffset(drag.value.roundToInt(), 0) }
                    .graphicsLayer { alpha = 1f - (abs(drag.value) / (threshold * 3)).coerceIn(0f, 0.6f) },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                AgentAvatar(shown.agent, agentColor(nodes, shown.nodeId, shown.agent), shown.statusColor().takeIf { !shown.ended() }, size = 22.dp)
                Spacer(Modifier.width(8.dp))
                Text(shown.label(), style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Medium,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/** TabCount is how many tabs there are, in a square as a browser counts them, with a dot when one waits for you. */
@Composable
private fun TabCount(n: Int, waits: Boolean) {
    Box(Modifier.size(28.dp)) {
        Box(
            Modifier.size(22.dp).align(Alignment.Center).border(1.5.dp, MaterialTheme.colorScheme.onSurface, RoundedCornerShape(6.dp)),
            contentAlignment = Alignment.Center,
        ) {
            Text(if (n > 99) ":)" else "$n", style = MaterialTheme.typography.labelSmall, fontWeight = FontWeight.Bold, textAlign = TextAlign.Center)
        }
        if (waits) Dot(StatusColors.Approval, Modifier.align(Alignment.TopEnd).border(1.5.dp, MaterialTheme.colorScheme.surfaceContainerHighest, CircleShape), size = 9.dp)
    }
}

/**
 * TabOverview is every tab of the project, as cards a browser's tab
 * switcher shows, the one shown outlined; those whose program has ended
 * after them; and the panels beside the project. A card's menu renames
 * the session, pins it, or stops it, which ends its agent on its computer,
 * after asking.
 */
@Composable
private fun TabOverview(
    mesh: Mesh, project: ProjectItem?, tabs: List<SessionInfo>, current: String, nodes: List<Node>,
    panels: List<Panel>, panelsNode: String?, pinned: Set<String>, canPin: Boolean,
    onClose: () -> Unit, onPick: (String) -> Unit, onNew: () -> Unit, onPanel: (Panel) -> Unit,
) {
    val (ended, live) = tabs.partition { it.state == "exited" }
    var showEnded by rememberSaveable { mutableStateOf(false) }
    val sum = remember(project) { project?.summary() }
    val grid = rememberLazyGridState(initialFirstVisibleItemIndex = maxOf(0, live.indexOfFirst { it.id == current }))
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize()) {
            Row(
                Modifier.fillMaxWidth().statusBarsPadding().heightIn(min = 64.dp).padding(start = 4.dp, end = 12.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                IconButton(onClick = onClose) { Icon(Icons.Filled.Close, stringResource(R.string.close)) }
                if (project != null) ProjectTile(project.look, size = 32.dp)
                Spacer(Modifier.width(10.dp))
                Text(project?.label.orEmpty(), style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f))
                FilledTonalButton(onClick = onNew, contentPadding = PaddingValues(start = 12.dp, end = 16.dp)) {
                    Icon(Icons.Filled.Add, null, Modifier.size(18.dp))
                    Spacer(Modifier.width(6.dp))
                    Text(stringResource(R.string.new_tab))
                }
            }
            if (sum != null && project != null) StatusLine(sum, project.nodes.map(mesh::nodeName).takeIf { it.size > 1 }.orEmpty(),
                Modifier.padding(start = 20.dp, end = 20.dp, bottom = 12.dp))
            LazyVerticalGrid(
                GridCells.Adaptive(160.dp), state = grid,
                contentPadding = PaddingValues(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 24.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp), verticalArrangement = Arrangement.spacedBy(10.dp),
                modifier = Modifier.fillMaxSize(),
            ) {
                if (tabs.isEmpty()) item(span = { GridItemSpan(maxLineSpan) }, key = "empty") {
                    EmptyState(
                        stringResource(R.string.no_tabs, project?.label.orEmpty()), stringResource(R.string.no_tabs_body),
                        action = stringResource(R.string.new_session_here), onAction = onNew, icon = Icons.Outlined.Inbox,
                        modifier = Modifier.padding(horizontal = 24.dp, vertical = 48.dp),
                    )
                }
                items(live, key = { it.id }) { s ->
                    TabCard(mesh, s, project, nodes, s.id == current, s.id in pinned, canPin, Modifier.animateItem()) { onPick(s.id) }
                }
                if (ended.isNotEmpty()) {
                    item(span = { GridItemSpan(maxLineSpan) }, key = "ended") {
                        FoldHeading(stringResource(R.string.group_ended), ended.size, showEnded, Modifier.offset(x = (-8).dp)) { showEnded = !showEnded }
                    }
                    if (showEnded) items(ended, key = { it.id }) { s ->
                        TabCard(mesh, s, project, nodes, s.id == current, s.id in pinned, canPin, Modifier.animateItem()) { onPick(s.id) }
                    }
                }
                if (panels.isNotEmpty() && panelsNode != null) {
                    item(span = { GridItemSpan(maxLineSpan) }, key = "panels-h") {
                        SectionLabel(stringResource(R.string.panels), Modifier.padding(start = 8.dp, top = 16.dp, bottom = 0.dp))
                    }
                    item(span = { GridItemSpan(maxLineSpan) }, key = "panels") {
                        Surface(shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceContainerLow) {
                            Column {
                                panels.forEachIndexed { i, p ->
                                    if (i > 0) HorizontalDivider(Modifier.padding(start = 56.dp), color = MaterialTheme.colorScheme.outlineVariant)
                                    // An agent's panel is named by its session rather than its ID.
                                    val owner = tabs.firstOrNull { p.block.startsWith("agent:" + it.id + "/") }
                                    PanelRow(if (owner != null) p.copy(detail = dotted(owner.agent, owner.label())) else p) { onPanel(p) }
                                }
                            }
                        }
                    }
                }
                item(span = { GridItemSpan(maxLineSpan) }, key = "inset") { Spacer(Modifier.windowInsetsBottomHeight(WindowInsets.navigationBars)) }
            }
        }
    }
}

/**
 * TabCard is a tab in the overview: its agent and status, its name, and
 * what it shows now, the question it waits on or the last it said; the
 * one shown outlined.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun TabCard(
    mesh: Mesh, s: SessionInfo, project: ProjectItem?, nodes: List<Node>, current: Boolean, pinned: Boolean, canPin: Boolean,
    modifier: Modifier = Modifier, onClick: () -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }
    var stopping by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    val act = actor()
    val preview = rememberPreview(mesh, s)
    val shape = RoundedCornerShape(16.dp)
    val outline = when {
        current -> MaterialTheme.colorScheme.onSurface.copy(alpha = 0.75f)
        s.needsYou() -> s.statusColor().copy(alpha = 0.5f)
        else -> Color.Transparent
    }
    Box(modifier) {
        Surface(
            shape = shape, color = MaterialTheme.colorScheme.surfaceContainerLow,
            modifier = Modifier.fillMaxWidth().height(172.dp).border(if (current) 2.dp else 1.dp, outline, shape).clip(shape)
                .semantics { selected = current; role = Role.Button }
                .combinedClickable(onClick = onClick, onLongClick = {
                    haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                    menu = true
                }),
        ) {
            Column(Modifier.padding(start = 12.dp, top = 10.dp, bottom = 12.dp, end = 4.dp).alpha(if (s.ended()) 0.7f else 1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    AgentAvatar(s.agent, agentColor(nodes, s.nodeId, s.agent), null, size = 22.dp)
                    Spacer(Modifier.width(8.dp))
                    Text(s.statusText(), style = MaterialTheme.typography.labelMedium, color = s.statusColor(),
                        fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                    if (pinned) Icon(Icons.Outlined.PushPin, stringResource(R.string.pinned), Modifier.size(14.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    IconButton(onClick = { menu = true }, modifier = Modifier.size(36.dp)) {
                        Icon(Icons.Filled.MoreVert, stringResource(R.string.session_actions), Modifier.size(18.dp))
                    }
                }
                Text(s.label(), style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 4.dp, end = 8.dp))
                Text(
                    preview.text, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    fontFamily = if (preview.code) FontFamily.Monospace else null,
                    maxLines = 3, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 6.dp, end = 8.dp).weight(1f),
                )
                Text(dotted(project?.tags?.get(s.id), s.agent, ago(s.lastActive())), style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            DropdownMenuItem(text = { Text(stringResource(R.string.rename)) }, leadingIcon = { Icon(Icons.Outlined.Edit, null) },
                onClick = { menu = false; renaming = true })
            if (canPin) DropdownMenuItem(text = { Text(stringResource(if (pinned) R.string.unpin else R.string.pin_to_top)) },
                leadingIcon = { Icon(Icons.Outlined.PushPin, null) }, onClick = { menu = false; act { mesh.pinSession(s, !pinned) } })
            HorizontalDivider()
            if (s.state == "exited") DropdownMenuItem(text = { Text(stringResource(R.string.remove_from_list)) },
                leadingIcon = { Icon(Icons.Outlined.DeleteOutline, null) }, onClick = { menu = false; act { mesh.closeSession(s) } })
            else DropdownMenuItem(
                text = { Text(stringResource(R.string.stop_session_menu), color = MaterialTheme.colorScheme.error) },
                leadingIcon = { Icon(Icons.Outlined.StopCircle, null, tint = MaterialTheme.colorScheme.error) },
                onClick = { menu = false; stopping = true },
            )
        }
    }
    if (renaming) RenameDialog(mesh, s, onDismiss = { renaming = false })
    if (stopping) StopDialog(s, onDismiss = { stopping = false }) { act { mesh.closeSession(s) } }
}

/** What a tab card shows of its session: words, and whether they are a command's. */
private data class Preview(val text: String, val code: Boolean = false)

/** The last thing each session said, by its ID, with when, so that the overview asks again only after it says more. */
private val said = java.util.concurrent.ConcurrentHashMap<String, Pair<Long, String>>()

/**
 * What a tab's card shows: what it waits for you on, an approval's command
 * or a question, else the dialog in its terminal, else the last its agent
 * wrote, read once from its conversation and again only after it writes.
 */
@Composable
private fun rememberPreview(mesh: Mesh, s: SessionInfo): Preview {
    val asks = when {
        s.asks() -> Preview(s.approval.getQuestions(0).question)
        s.plans() -> Preview(s.approval.detail.lineSequence().firstOrNull { it.isNotBlank() }.orEmpty().trimStart('#', ' '))
        s.hasApproval() -> Preview(s.approval.detail.ifEmpty { s.approval.tool }, code = true)
        s.hasDialog() -> Preview(s.dialog.text)
        else -> null
    }
    if (asks != null) return asks
    val stamp = maxOf(s.lastMessage.millis(), s.statusAt.millis())
    var text by remember(s.id) { mutableStateOf(said[s.id]?.second.orEmpty()) }
    LaunchedEffect(s.id, stamp) {
        if (s.caps and CAP_CHAT == 0) return@LaunchedEffect
        val known = said[s.id]
        if (known != null && known.first >= stamp) return@LaunchedEffect
        try {
            val chat = mesh.daemon.chat(ReadChat.newBuilder().setNodeId(s.nodeId).setSessionId(s.id).build())
            val last = chat.messagesList.lastOrNull { it.role != "tool" && it.tool.isEmpty() && it.text.isNotBlank() }
            val words = last?.text.orEmpty().replace(Regex("[*_`#>]"), "").replace(Regex("\\s+"), " ").trim().take(240)
            said[s.id] = stamp to words
            text = words
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
        }
    }
    return Preview(text.ifEmpty { s.notice.body.ifEmpty { s.title } })
}
