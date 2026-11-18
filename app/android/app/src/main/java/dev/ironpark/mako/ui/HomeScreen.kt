package dev.ironpark.mako.ui

import androidx.activity.BackEventCompat
import androidx.activity.compose.LocalActivity
import androidx.activity.compose.PredictiveBackHandler
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material.icons.outlined.Dashboard
import androidx.compose.material.icons.outlined.Devices
import androidx.compose.material.icons.outlined.Inbox
import androidx.compose.material.icons.outlined.Lan
import androidx.compose.material.icons.outlined.PushPin
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.FilterChipDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.TopAppBarState
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.material3.rememberTopAppBarState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.core.content.edit
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.workspace.LOOK_COLORS
import dev.ironpark.mako.workspace.Look
import dev.ironpark.mako.workspace.WorkspaceView
import dev.ironpark.mako.workspace.view
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** What home shows of the projects: all, those with a session waiting for you, or one at work. */
internal enum class Filter { All, NeedsYou, Working }

@OptIn(ExperimentalMaterial3Api::class)
internal class HomeState(
    val list: LazyListState,
    val bar: TopAppBarState,
    val filter: MutableState<Filter>,
    /** The space of the workspace shown, by ID; "" for every project's. */
    val space: MutableState<String>,
)

/** Home's state; source gives a preview of home, behind a session, the same filter and scroll without sharing a layout. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun rememberHomeState(source: HomeState? = null): HomeState {
    val list = rememberLazyListState(source?.list?.firstVisibleItemIndex ?: 0, source?.list?.firstVisibleItemScrollOffset ?: 0)
    val bar = rememberTopAppBarState(
        initialHeightOffsetLimit = source?.bar?.heightOffsetLimit ?: -Float.MAX_VALUE,
        initialHeightOffset = source?.bar?.heightOffset ?: 0f,
        initialContentOffset = source?.bar?.contentOffset ?: 0f,
    )
    val filter = rememberSaveable { mutableStateOf(source?.filter?.value ?: Filter.All) }
    // The space shown is kept, as the desktop's window keeps it, by its ID.
    val prefs = LocalContext.current.getSharedPreferences("home", android.content.Context.MODE_PRIVATE)
    val space = remember { mutableStateOf(source?.space?.value ?: prefs.getString("space", null).orEmpty()) }
    LaunchedEffect(space.value) { prefs.edit { putString("space", space.value) } }
    return remember(list, bar, filter, space) { HomeState(list, bar, filter, space) }
}

/**
 * HomeScreen is the projects on the computers: first what waits for you,
 * answerable here, then what is pinned, then every project of the space
 * shown, each saying how its sessions stand. A project opens as a
 * workspace of tabs, its sessions (onProject, with the tab to open on, or
 * null for the project's own choice).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun HomeScreen(
    mesh: Mesh,
    onProject: (ProjectItem, String?) -> Unit,
    onMesh: () -> Unit,
    onNew: (ProjectItem?) -> Unit,
    onSettings: () -> Unit,
    onScan: () -> Unit = onMesh,
    onPanels: ((String) -> Unit)? = null,
    last: SessionInfo? = null, onLast: () -> Unit = {},
    state: HomeState = rememberHomeState(),
) {
    val running by mesh.state.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val loaded by mesh.loaded.collectAsState()
    val workspace by mesh.workspace.collectAsState()
    val peers by mesh.peers.collectAsState()
    val linked = nodes.filter { it.trustsUs }
    var refreshing by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val bar = TopAppBarDefaults.enterAlwaysScrollBehavior(state.bar)
    val view = remember(workspace.state) { workspace.state.view() }
    val spaceAt = if (view.held) view.spaceIndex(state.space.value) else 0

    // How far a swipe from the right edge has pulled the last session in.
    var pull by remember { mutableFloatStateOf(0f) }
    val haptic = LocalHapticFeedback.current
    val openLast by rememberUpdatedState(onLast)
    val activity = LocalActivity.current
    fun pulled(p: Float) {
        if (last == null) return
        if (p >= 1f && pull < 1f) haptic.performHapticFeedback(HapticFeedbackType.GestureThresholdActivate)
        pull = p
    }
    fun released(open: Boolean) {
        pull = 0f
        if (open && last != null) openLast()
    }
    // With gesture navigation the right edge's swipe is the system's back:
    // from that edge it brings the last session back, from the other it leaves.
    PredictiveBackHandler(enabled = last != null) { events ->
        var edge = BackEventCompat.EDGE_NONE
        try {
            events.collect { e ->
                edge = e.swipeEdge
                if (edge == BackEventCompat.EDGE_RIGHT) pulled(e.progress * 1.5f)
            }
            if (edge == BackEventCompat.EDGE_RIGHT) released(true) else activity?.moveTaskToBack(true)
        } finally {
            pull = 0f
        }
    }

    Scaffold(
        modifier = Modifier.nestedScroll(bar.nestedScrollConnection).edgeSwipes(::pulled, ::released),
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        if (view.held && view.spaces.isNotEmpty()) SpaceSwitcher(view, spaceAt) { state.space.value = view.spaceId(it) }
                        else Text(stringResource(R.string.app_name), style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.SemiBold)
                        if (linked.isNotEmpty()) Text(
                            dotted(
                                if (linked.size == 1) linked[0].name else pluralStringResource(R.plurals.computers_count, linked.size, linked.size),
                                pluralStringResource(R.plurals.sessions_count, sessions.size, sessions.size),
                            ),
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                },
                actions = {
                    IconButton(onClick = onMesh) {
                        BadgedBox(badge = { if (linked.isEmpty() || linked.any { it.linkDown() }) Badge() }) {
                            Icon(Icons.Outlined.Devices, stringResource(R.string.computers))
                        }
                    }
                    IconButton(onClick = onSettings) { Icon(Icons.Outlined.Settings, stringResource(R.string.settings)) }
                },
                scrollBehavior = bar,
                colors = TopAppBarDefaults.topAppBarColors(scrolledContainerColor = MaterialTheme.colorScheme.surface),
            )
        },
        floatingActionButton = {
            // With nothing listed, the empty list's own button starts one.
            if (linked.isNotEmpty() && (sessions.isNotEmpty() || view.projects.isNotEmpty())) {
                // Out at the top of the list, an icon alone once it scrolls.
                val top by remember { derivedStateOf { state.list.firstVisibleItemIndex == 0 } }
                ExtendedFloatingActionButton(
                    onClick = { onNew(null) }, expanded = top,
                    icon = { Icon(Icons.Filled.Add, null) },
                    text = { Text(stringResource(R.string.new_session)) },
                    containerColor = MaterialTheme.colorScheme.primary,
                    contentColor = MaterialTheme.colorScheme.onPrimary,
                )
            }
        },
    ) { pad ->
        Box(Modifier.fillMaxSize().padding(pad)) {
            when (val s = running) {
                Mesh.State.Starting -> Loading(stringResource(R.string.starting_node))
                is Mesh.State.Failed -> Message(
                    stringResource(R.string.node_failed), s.message,
                    stringResource(R.string.retry), mesh::start, icon = Icons.Outlined.CloudOff,
                )
                Mesh.State.Running -> when {
                    !loaded && sessions.isEmpty() -> PlaceholderRows(5, stringResource(R.string.reaching_computers))
                    // Computers known but not reached now: say so, not that there are none.
                    linked.isEmpty() && sessions.isEmpty() && (nodes.any { it.unreachable() } || peers.isNotEmpty()) -> {
                        val down = nodes.filter { it.unreachable() }
                        if (down.isEmpty()) Message(
                            stringResource(R.string.unreachable_any), stringResource(R.string.offline_body),
                            stringResource(R.string.computers), onMesh, icon = Icons.Outlined.CloudOff,
                        ) else Message(
                            stringResource(R.string.unreachable, down.joinToString(", ") { it.name }),
                            stringResource(R.string.offline_body),
                            stringResource(R.string.reconnect), { scope.launch { down.forEach { runCatching { mesh.relink(it.id) } } } },
                            icon = Icons.Outlined.CloudOff,
                            secondary = stringResource(R.string.computers), onSecondary = onMesh,
                        )
                    }
                    linked.isEmpty() && sessions.isEmpty() -> Message(
                        stringResource(R.string.connect_title), stringResource(R.string.connect_body),
                        stringResource(R.string.scan_qr), onScan, icon = Icons.Outlined.Lan,
                        secondary = stringResource(R.string.computers), onSecondary = onMesh,
                    )
                    else -> PullToRefreshBox(
                        isRefreshing = refreshing,
                        onRefresh = {
                            refreshing = true
                            scope.launch {
                                mesh.refreshAll()
                                refreshing = false
                            }
                        },
                        modifier = Modifier.fillMaxSize(),
                    ) {
                        ProjectList(mesh, nodes, sessions, view, spaceAt, state, onProject, onNew.takeIf { linked.isNotEmpty() },
                            onPanels = onPanels, workspaceEditable = workspace.editable)
                    }
                }
            }
            if (last != null) LastSessionPeek(last, nodes, pull, Modifier.align(Alignment.CenterEnd))
        }
    }
}

@Composable
private fun ProjectList(
    mesh: Mesh,
    nodes: List<Node>,
    sessions: List<SessionInfo>,
    view: WorkspaceView,
    spaceAt: Int,
    state: HomeState,
    onProject: (ProjectItem, String?) -> Unit,
    onNew: ((ProjectItem?) -> Unit)?,
    onPanels: ((String) -> Unit)?,
    workspaceEditable: Boolean,
) {
    // Times shown as "ago" move on with the clock.
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) {
        while (true) {
            delay(30_000)
            now = System.currentTimeMillis()
        }
    }
    var filter by state.filter
    val items = remember(view, sessions, nodes) { projectIndex(view, sessions, mesh::nodeName) }
    val inSpace = remember(items, spaceAt) { items.filter { spaceAt == 0 || it.space == 0 || it.space == spaceAt } }
    val tray = remember(sessions) { needingYou(sessions) }
    val working = inSpace.sumOf { p -> p.sessions.count { it.working() } }
    val shown = remember(items, spaceAt, filter, view) { homeProjects(items, spaceAt, filter, view.pinnedProjects) }
    val pinnedSessions = remember(view, sessions) {
        view.pinnedSessions.mapNotNull { id -> sessions.firstOrNull { it.id == id } }
    }
    val manyNodes = nodes.count { it.trustsUs } > 1 || sessions.map { it.nodeId }.distinct().size > 1
    val down = nodes.filter { it.unreachable() }
    val prefs = LocalContext.current.getSharedPreferences("home", android.content.Context.MODE_PRIVATE)
    var trayOpen by remember { mutableStateOf(!prefs.getBoolean("trayFolded", false)) }
    var trayAll by rememberSaveable { mutableStateOf(false) }
    val act = actor()
    val panelsNode = remember(view, nodes) { mesh.panelsNode() }

    LazyColumn(state = state.list, contentPadding = PaddingValues(bottom = 112.dp), modifier = Modifier.fillMaxSize()) {
        if (down.isNotEmpty()) item(key = "down") {
            LinkBanner(down, Modifier.animateItem()) { node -> act { mesh.relink(node) } }
        }
        if (sessions.isNotEmpty()) item(key = "filters") {
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                for (f in Filter.entries) {
                    val (label, count) = when (f) {
                        Filter.All -> stringResource(R.string.filter_all) to 0
                        Filter.NeedsYou -> stringResource(R.string.filter_needs_you) to tray.size
                        Filter.Working -> stringResource(R.string.filter_working) to working
                    }
                    FilterChip(
                        filter == f, { filter = f }, { Text(label) },
                        trailingIcon = if (count == 0) null else ({
                            Text("$count", style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.SemiBold,
                                color = if (f == Filter.NeedsYou) StatusColors.Approval else StatusColors.Working)
                        }),
                        colors = FilterChipDefaults.filterChipColors(
                            selectedContainerColor = MaterialTheme.colorScheme.secondaryContainer,
                        ),
                    )
                }
            }
        }
        // What waits for you, from every space, answerable here.
        if (filter != Filter.Working && tray.isNotEmpty()) {
            item(key = "tray") {
                FoldHeading(
                    stringResource(R.string.needs_you), tray.size, trayOpen, Modifier.animateItem(), color = StatusColors.Approval,
                ) {
                    trayOpen = !trayOpen
                    prefs.edit { putBoolean("trayFolded", !trayOpen) }
                }
            }
            if (trayOpen) {
                val cap = if (trayAll || filter == Filter.NeedsYou) tray.size else TRAY_CAP
                val list = tray.take(cap)
                itemsIndexed(list, key = { _, s -> "w:" + s.id }) { i, s ->
                    val p = items.projectOf(s.id)
                    Column(Modifier.animateItem().padding(horizontal = 16.dp).clip(groupShape(i, list.size))
                        .background(MaterialTheme.colorScheme.surfaceContainerLow)) {
                        if (i > 0) HorizontalDivider(Modifier.padding(start = 62.dp), color = MaterialTheme.colorScheme.outlineVariant)
                        AttentionRow(mesh, nodes, s, p, now, onOpen = { if (p != null) onProject(p, s.id) })
                    }
                }
                if (tray.size > cap) item(key = "tray-more") {
                    TextButton(onClick = { trayAll = true }, modifier = Modifier.padding(start = 8.dp).animateItem()) {
                        Text(stringResource(R.string.show_all_n, tray.size))
                    }
                }
            }
        }
        if (filter == Filter.All && pinnedSessions.isNotEmpty()) {
            item(key = "pinned-sessions") { Label(stringResource(R.string.pinned_sessions), Modifier.animateItem()) }
            itemsIndexed(pinnedSessions, key = { _, s -> "ps:" + s.id }) { i, s ->
                val p = items.projectOf(s.id)
                Column(Modifier.animateItem().padding(horizontal = 16.dp).clip(groupShape(i, pinnedSessions.size))
                    .background(MaterialTheme.colorScheme.surfaceContainerLow)) {
                    if (i > 0) HorizontalDivider(Modifier.padding(start = 68.dp), color = MaterialTheme.colorScheme.outlineVariant)
                    PinnedSessionRow(mesh, nodes, s, p, now,
                        onClick = { if (p != null) onProject(p, s.id) },
                        onUnpin = { act { mesh.pinSession(s, false) } }.takeIf { workspaceEditable })
                }
            }
        }
        val (pinned, rest) = shown.partition { it.pinned }
        val pinnedLabel = if (filter == Filter.All && pinnedSessions.isNotEmpty()) R.string.pinned_projects else R.string.pinned
        for ((label, list) in listOf(pinnedLabel to pinned, R.string.projects to rest)) {
            if (list.isEmpty()) continue
            item(key = "h:$label") {
                Label(stringResource(label), Modifier.animateItem(),
                    count = if (label == R.string.projects && list.size > 1) list.size else null)
            }
            itemsIndexed(list, key = { _, p -> "p:" + p.key }) { i, p ->
                Column(Modifier.animateItem().padding(horizontal = 16.dp).clip(groupShape(i, list.size))
                    .background(MaterialTheme.colorScheme.surfaceContainerLow)) {
                    if (i > 0) HorizontalDivider(Modifier.padding(start = 70.dp), color = MaterialTheme.colorScheme.outlineVariant)
                    ProjectRow(
                        mesh, p, now, manyNodes,
                        onClick = { onProject(p, null) },
                        onPin = if (workspaceEditable && p.scope != null) ({
                            act {
                                if (p.pinned) p.pins.forEach { mesh.pinScope(it, false) } else mesh.pinScope(p.scope, true)
                            }
                        }) else null,
                        onNew = onNew?.let { { it(p) } },
                    )
                }
            }
        }
        if (shown.isEmpty()) item(key = "empty") {
            val all = filter == Filter.All
            EmptyState(
                stringResource(if (all) R.string.no_sessions else R.string.nothing_here),
                if (all) stringResource(R.string.no_sessions_body) else "",
                action = if (all && onNew != null) stringResource(R.string.new_session) else null,
                onAction = { onNew?.invoke(null) },
                icon = Icons.Outlined.Inbox,
                modifier = Modifier.fillMaxWidth().padding(horizontal = 32.dp, vertical = 56.dp),
            )
        }
        // The computer's panels, for those beside no project in view.
        if (onPanels != null && panelsNode != null && mesh.showsBlocks(panelsNode) && filter == Filter.All) item(key = "panels") {
            Column(Modifier.animateItem().padding(top = 8.dp)) {
                Surface(
                    onClick = { onPanels(panelsNode) },
                    shape = RoundedCornerShape(16.dp),
                    color = MaterialTheme.colorScheme.surfaceContainerLow,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp).fillMaxWidth(),
                ) {
                    Row(Modifier.padding(horizontal = 16.dp, vertical = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                        Icon(Icons.Outlined.Dashboard, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                        Spacer(Modifier.width(16.dp))
                        Column(Modifier.weight(1f)) {
                            Text(stringResource(R.string.panels_of, mesh.nodeName(panelsNode)), style = MaterialTheme.typography.bodyLarge)
                            Text(stringResource(R.string.panels_note), style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.outline)
                    }
                }
            }
        }
    }
}

/**
 * Whether n is a computer this device cannot reach now: one that could run
 * sessions, not another phone the computers know, whose link is down.
 */
internal fun Node.unreachable(): Boolean = linkDown() && os != "android"

/** How many of the sessions waiting for you the tray shows before it offers the rest. */
private const val TRAY_CAP = 3

@Composable
private fun Label(text: String, modifier: Modifier = Modifier, count: Int? = null) {
    Row(modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 20.dp, bottom = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        SectionLabel(text, Modifier.weight(1f))
        if (count != null) Text("$count", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** A heading that folds what is under it away when tapped, and says whether it has. */
@Composable
internal fun FoldHeading(text: String, count: Int, open: Boolean, modifier: Modifier = Modifier, color: Color? = null, onToggle: () -> Unit) {
    val state = stringResource(if (open) R.string.expanded else R.string.collapsed)
    val turn by animateFloatAsState(if (open) 0f else -90f, label = "fold")
    Row(
        modifier.fillMaxWidth().semantics { stateDescription = state }.clickable(role = Role.Button, onClick = onToggle)
            .padding(start = 20.dp, end = 16.dp, top = 16.dp, bottom = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (color != null) {
            Dot(color, size = 8.dp)
            Spacer(Modifier.width(8.dp))
        }
        SectionLabel(text)
        Spacer(Modifier.width(6.dp))
        Text("$count", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.weight(1f))
        Icon(Icons.Filled.KeyboardArrowDown, null, Modifier.size(20.dp).rotate(turn), tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/**
 * ProjectRow is a project on home: its mark, its name, how its sessions
 * stand, and the one it would open on. A long press pins it, or starts a
 * session in it.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ProjectRow(
    mesh: Mesh, p: ProjectItem, now: Long, manyNodes: Boolean,
    onClick: () -> Unit, onPin: (() -> Unit)?, onNew: (() -> Unit)?,
) {
    val sum = remember(p) { p.summary() }
    var menu by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    val empty = sum.total == 0
    Box {
        Row(
            Modifier.fillMaxWidth()
                .combinedClickable(onClick = onClick, onLongClick = if (onPin != null || onNew != null) ({
                    haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                    menu = true
                }) else null)
                .padding(start = 12.dp, end = 16.dp, top = 10.dp, bottom = 12.dp)
                .alpha(if (empty) 0.6f else 1f),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            ProjectTile(p.look, Modifier.shared("tile:" + p.key))
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                        Text(
                            p.label, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.weight(1f, fill = false).sharedBounds("name:" + p.key),
                        )
                        if (p.pinned) Icon(Icons.Outlined.PushPin, stringResource(R.string.pinned), Modifier.padding(start = 6.dp).size(14.dp),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    now // the time below follows the clock
                    if (sum.latest > 0) Text(ago(sum.latest), style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 8.dp))
                }
                Spacer(Modifier.height(2.dp))
                StatusLine(sum, if (manyNodes) p.nodes.map(mesh::nodeName) else emptyList())
                val head = sum.headline
                if (head != null) Text(
                    head.label(), style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 1.dp),
                )
            }
        }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            if (onNew != null) DropdownMenuItem(text = { Text(stringResource(R.string.new_session_here)) },
                leadingIcon = { Icon(Icons.Filled.Add, null) }, onClick = { menu = false; onNew() })
            if (onPin != null) DropdownMenuItem(text = { Text(stringResource(if (p.pinned) R.string.unpin else R.string.pin_to_top)) },
                leadingIcon = { Icon(Icons.Outlined.PushPin, null) }, onClick = { menu = false; onPin() })
        }
    }
}

/**
 * StatusLine says how a project's sessions stand, what needs you first, in
 * the colours of their statuses, then how many there are and where.
 */
@Composable
internal fun StatusLine(sum: ProjectSummary, nodes: List<String>, modifier: Modifier = Modifier) {
    val muted = MaterialTheme.colorScheme.onSurfaceVariant
    val needs = if (sum.needsYou > 0) pluralStringResource(R.plurals.n_need_you, sum.needsYou, sum.needsYou) else null
    val working = if (sum.working > 0) pluralStringResource(R.plurals.n_working, sum.working, sum.working) else null
    val total = if (sum.total == 0) stringResource(R.string.no_sessions_short)
    else pluralStringResource(R.plurals.sessions_count, sum.total, sum.total)
    Text(
        buildAnnotatedString {
            fun sep() { if (length > 0) append("  ·  ") }
            if (needs != null) withStyle(SpanStyle(color = StatusColors.Approval, fontWeight = FontWeight.SemiBold)) { append(needs) }
            if (working != null) { sep(); withStyle(SpanStyle(color = StatusColors.Working, fontWeight = FontWeight.Medium)) { append(working) } }
            sep(); append(total)
            if (nodes.isNotEmpty()) { sep(); append(nodes.joinToString(", ")) }
        },
        style = MaterialTheme.typography.bodySmall, color = muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
        modifier = modifier,
    )
}

/** PinnedSessionRow is a session pinned to the top of home, opening its project at it. */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun PinnedSessionRow(
    mesh: Mesh, nodes: List<Node>, s: SessionInfo, p: ProjectItem?, now: Long, onClick: () -> Unit, onUnpin: (() -> Unit)?,
) {
    var menu by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    Box {
        Row(
            Modifier.fillMaxWidth().combinedClickable(onClick = onClick, onLongClick = onUnpin?.let { {
                haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                menu = true
            } }).padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            AgentAvatar(s.agent, agentColor(nodes, s.nodeId, s.agent), s.statusColor().takeIf { !s.ended() }, size = 36.dp)
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(s.label(), style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (p != null) {
                        LookGlyph(p.look, size = 12.dp)
                        Spacer(Modifier.width(4.dp))
                    }
                    Text(dotted(p?.label, s.agent.takeUnless { s.label().equals(it, true) }), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
            Spacer(Modifier.width(8.dp))
            now
            StatusText(s)
        }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            if (onUnpin != null) DropdownMenuItem(text = { Text(stringResource(R.string.unpin)) },
                leadingIcon = { Icon(Icons.Outlined.PushPin, null) }, onClick = { menu = false; onUnpin() })
        }
    }
}

/** A session's status as a word in its colour; an idle one says when it last did anything instead. */
@Composable
internal fun StatusText(s: SessionInfo) {
    if (s.hasApproval() || s.ended() || s.status in setOf("working", "waiting", "done")) Text(
        s.statusText(), style = MaterialTheme.typography.labelMedium, color = s.statusColor(),
        fontWeight = if (s.working() || s.needsYou()) FontWeight.SemiBold else FontWeight.Normal,
    ) else Text(ago(s.lastActive()), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
}

/**
 * AttentionRow is a session waiting for you, a row of the tray: its agent,
 * name and project, and what it waits on; an approval of a command is
 * answered on the row, anything else opens the session, where it is.
 */
@Composable
private fun AttentionRow(mesh: Mesh, nodes: List<Node>, s: SessionInfo, p: ProjectItem?, now: Long, onOpen: () -> Unit) {
    val act = actor()
    val tone = s.statusColor()
    val approves = s.hasApproval() && !s.asks() && !s.plans()
    Column(Modifier.fillMaxWidth().clickable(onClick = onOpen).padding(start = 14.dp, end = 14.dp, top = 12.dp, bottom = 12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            AgentAvatar(s.agent, agentColor(nodes, s.nodeId, s.agent), tone, size = 36.dp)
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(s.label(), style = MaterialTheme.typography.bodyLarge, fontWeight = FontWeight.Medium, maxLines = 1,
                        overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                    Text(s.waitText(), style = MaterialTheme.typography.labelMedium, color = tone, fontWeight = FontWeight.SemiBold,
                        modifier = Modifier.padding(start = 8.dp))
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (p != null) {
                        LookGlyph(p.look, size = 12.dp)
                        Spacer(Modifier.width(4.dp))
                    }
                    now
                    Text(dotted(p?.label ?: baseName(s.project), ago(s.waitingSince())),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
        val ask = when {
            s.asks() -> s.approval.getQuestions(0).question
            s.plans() -> s.approval.detail.lineSequence().firstOrNull { it.isNotBlank() }.orEmpty().trimStart('#', ' ')
            else -> ""
        }
        if (ask.isNotEmpty()) Text(ask, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(start = 48.dp, top = 6.dp))
        if (approves) {
            Surface(color = MaterialTheme.colorScheme.surfaceContainerHigh, shape = RoundedCornerShape(8.dp),
                modifier = Modifier.padding(start = 48.dp, top = 8.dp).fillMaxWidth()) {
                Text(
                    dotted(s.approval.tool, s.approval.detail.lineSequence().firstOrNull().orEmpty()),
                    style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace, maxLines = 2, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(horizontal = 10.dp, vertical = 7.dp),
                )
            }
            Row(Modifier.fillMaxWidth().padding(start = 48.dp, top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
                OutlinedButton(onClick = { act { mesh.answer(s, "deny") } }, contentPadding = SmallButton, modifier = Modifier.heightIn(min = 40.dp)) {
                    Text(stringResource(R.string.deny))
                }
                Button(onClick = { act { mesh.answer(s, "allow") } }, contentPadding = SmallButton, modifier = Modifier.heightIn(min = 40.dp)) {
                    Text(stringResource(R.string.allow))
                }
            }
        }
    }
}

private val SmallButton = PaddingValues(horizontal = 18.dp, vertical = 6.dp)

/** What a session waiting for you waits on, in a word or two. */
@Composable
internal fun SessionInfo.waitText(): String = when {
    asks() -> stringResource(R.string.status_asks)
    plans() -> stringResource(R.string.status_plan)
    else -> statusText()
}

/** LinkBanner says which computers cannot be reached now, what the list shows of them being what they last said. */
@Composable
private fun LinkBanner(down: List<Node>, modifier: Modifier = Modifier, onRetry: (String) -> Unit) {
    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerHigh, shape = RoundedCornerShape(16.dp),
        modifier = modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp),
    ) {
        Row(Modifier.padding(start = 16.dp, end = 8.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Outlined.CloudOff, null, tint = StatusColors.Waiting, modifier = Modifier.size(20.dp))
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.unreachable, down.joinToString(", ") { it.name }), style = MaterialTheme.typography.bodyMedium,
                    fontWeight = FontWeight.Medium)
                Text(stringResource(R.string.unreachable_body), style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            TextButton(onClick = { down.forEach { onRetry(it.id) } }, enabled = down.any { it.link != Mesh.LINK_LINKING }) {
                Text(stringResource(R.string.reconnect))
            }
        }
    }
}

/**
 * SpaceSwitcher is the title of home: the space shown, as the desktop's
 * sidebar heads its list, opening the others; every project's first,
 * unless the workspace leaves it out.
 */
@Composable
private fun SpaceSwitcher(view: WorkspaceView, current: Int, onChoose: (Int) -> Unit) {
    var open by remember { mutableStateOf(false) }
    @Composable
    fun name(i: Int): String {
        val sp = view.spaces.getOrNull(i - 1)
        return when {
            sp == null -> stringResource(R.string.all_spaces)
            sp.name.isNotEmpty() -> sp.name
            else -> stringResource(R.string.space_n, i)
        }
    }
    fun color(i: Int): Color? = view.spaces.getOrNull(i - 1)?.color
        ?.takeIf { c -> LOOK_COLORS.any { it.first == c } }?.let { Look("", it).tint() }
    val label = name(current)
    val chooseSpace = stringResource(R.string.choose_space)
    Box {
        Row(
            Modifier.clip(RoundedCornerShape(8.dp)).clickable(role = Role.DropdownList) { open = true }
                .clearAndSetSemantics { contentDescription = "$chooseSpace, $label" }
                .heightIn(min = 40.dp).padding(end = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            color(current)?.let {
                Dot(it, size = 10.dp)
                Spacer(Modifier.width(10.dp))
            }
            Text(label, style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Icon(Icons.Filled.ArrowDropDown, null)
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            for (i in view.firstSpace()..view.spaces.size) {
                DropdownMenuItem(
                    text = { Text(name(i)) },
                    leadingIcon = { Box(Modifier.size(24.dp), contentAlignment = Alignment.Center) { color(i)?.let { Dot(it, size = 10.dp) } } },
                    trailingIcon = { if (i == current) Icon(Icons.Filled.Check, null) },
                    onClick = { open = false; onChoose(i) },
                )
            }
        }
    }
}
