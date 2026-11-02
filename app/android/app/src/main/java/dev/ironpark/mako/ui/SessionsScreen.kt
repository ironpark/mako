package dev.ironpark.mako.ui

import androidx.compose.foundation.ExperimentalFoundationApi
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
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.Devices
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.foundation.background
import androidx.compose.material3.TextButton
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Inbox
import androidx.compose.material.icons.outlined.StopCircle
import androidx.compose.material.icons.outlined.Lan
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.TopAppBarState
import androidx.compose.material3.rememberTopAppBarState
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** What the list shows of the sessions. */
internal enum class Filter { All, NeedsYou, Working }

@OptIn(ExperimentalMaterial3Api::class)
internal class SessionsScreenState(
    val list: LazyListState,
    val bar: TopAppBarState,
    val filter: MutableState<Filter>,
)

/** A separate snapshot gives the swipe preview the same filter and scroll without sharing a layout. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun rememberSessionsScreenState(source: SessionsScreenState? = null): SessionsScreenState {
    val list = rememberLazyListState(source?.list?.firstVisibleItemIndex ?: 0, source?.list?.firstVisibleItemScrollOffset ?: 0)
    val bar = rememberTopAppBarState(
        initialHeightOffsetLimit = source?.bar?.heightOffsetLimit ?: -Float.MAX_VALUE,
        initialHeightOffset = source?.bar?.heightOffset ?: 0f,
        initialContentOffset = source?.bar?.contentOffset ?: 0f,
    )
    val filter = rememberSaveable { mutableStateOf(source?.filter?.value ?: Filter.All) }
    return remember(list, bar, filter) { SessionsScreenState(list, bar, filter) }
}

/**
 * SessionsScreen is the mesh's sessions: those waiting for you first, as
 * cards to answer from here, then each computer's, the latest first.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun SessionsScreen(
    mesh: Mesh, onOpen: (SessionInfo) -> Unit, onMesh: () -> Unit, onNew: () -> Unit, onSettings: () -> Unit,
    onScan: () -> Unit = onMesh,
    screenState: SessionsScreenState = rememberSessionsScreenState(),
) {
    val state by mesh.state.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val loaded by mesh.loaded.collectAsState()
    val linked = nodes.count { it.trustsUs }
    var filter by screenState.filter
    var refreshing by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val bar = TopAppBarDefaults.enterAlwaysScrollBehavior(screenState.bar)

    Scaffold(
        modifier = Modifier.nestedScroll(bar.nestedScrollConnection),
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Text(stringResource(R.string.app_name), fontWeight = FontWeight.SemiBold)
                        if (linked > 0) {
                            Text(
                                pluralStringResource(R.plurals.linked_summary, sessions.size, linked, sessions.size),
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                },
                actions = {
                    IconButton(onClick = onSettings) { Icon(Icons.Outlined.Settings, stringResource(R.string.settings)) }
                    IconButton(onClick = onMesh) {
                        BadgedBox(badge = { if (linked == 0) Badge() }) {
                            Icon(Icons.Outlined.Devices, stringResource(R.string.computers))
                        }
                    }
                },
                scrollBehavior = bar,
            )
        },
        floatingActionButton = {
            if (linked > 0) {
                ExtendedFloatingActionButton(
                    onClick = onNew,
                    icon = { Icon(Icons.Filled.Add, null) },
                    text = { Text(stringResource(R.string.new_session)) },
                )
            }
        },
    ) { pad ->
        Box(Modifier.fillMaxSize().padding(pad)) {
            when (val s = state) {
                Mesh.State.Starting -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                is Mesh.State.Failed -> Message(
                    stringResource(R.string.node_failed), s.message,
                    stringResource(R.string.retry), mesh::start, icon = Icons.Outlined.CloudOff,
                )
                Mesh.State.Running -> when {
                    !loaded && sessions.isEmpty() -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                    linked == 0 && sessions.isEmpty() -> Message(
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
                        SessionList(mesh, nodes, sessions, filter, onFilter = { filter = it }, onOpen, screenState.list,
                            onNew = onNew.takeIf { linked > 0 })
                    }
                }
            }
        }
    }
}

@Composable
private fun SessionList(
    mesh: Mesh,
    nodes: List<Node>,
    sessions: List<SessionInfo>,
    filter: Filter,
    onFilter: (Filter) -> Unit,
    onOpen: (SessionInfo) -> Unit,
    listState: LazyListState,
    onNew: (() -> Unit)? = null,
) {
    // Times shown as "ago" move on with the clock.
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) {
        while (true) {
            delay(30_000)
            now = System.currentTimeMillis()
        }
    }
    val waiting = remember(sessions) { sessions.filter { it.needsYou() }.sortedByDescending { it.lastActive() } }
    val shown = remember(sessions, waiting, filter) {
        when (filter) {
            Filter.All -> sessions
            Filter.NeedsYou -> waiting
            Filter.Working -> sessions.filter { it.working() }
        }
    }
    val byNode = remember(shown, filter, nodes) {
        shown
            .filter { filter != Filter.All || !it.needsYou() }
            .sortedByDescending { it.lastActive() }
            .groupBy { it.nodeId }
            .toList()
            .sortedBy { mesh.nodeName(it.first).lowercase() }
    }

    LazyColumn(state = listState, contentPadding = PaddingValues(bottom = 96.dp), modifier = Modifier.fillMaxSize()) {
        item(key = "filters") {
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                for (f in Filter.entries) {
                    val label = when (f) {
                        Filter.All -> stringResource(R.string.filter_all, sessions.size)
                        Filter.NeedsYou -> stringResource(R.string.filter_needs_you, waiting.size)
                        Filter.Working -> stringResource(R.string.filter_working, sessions.count { it.working() })
                    }
                    FilterChip(filter == f, { onFilter(f) }, { Text(label) })
                }
            }
        }
        if (filter == Filter.All && waiting.isNotEmpty()) {
            item(key = "waiting") { Heading(stringResource(R.string.needs_you), waiting.size) }
            items(waiting, key = { "w:" + it.id }) { s -> WaitingCard(mesh, nodes, s, now, onOpen = { onOpen(s) }) }
        }
        if (shown.isEmpty()) {
            item(key = "empty") {
                val all = filter == Filter.All
                EmptyState(
                    stringResource(if (all) R.string.no_sessions else R.string.nothing_here),
                    if (all) stringResource(R.string.no_sessions_body) else "",
                    action = if (all && onNew != null) stringResource(R.string.new_session) else null,
                    onAction = { onNew?.invoke() },
                    icon = Icons.Outlined.Inbox,
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 32.dp, vertical = 64.dp),
                )
            }
        }
        for ((node, list) in byNode) {
            item(key = "node:$node") {
                val n = nodes.firstOrNull { it.id == node }
                Row(
                    Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 20.dp, bottom = 6.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Icon(osIcon(n?.os.orEmpty()), null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary)
                    Spacer(Modifier.width(8.dp))
                    Text(
                        mesh.nodeName(node), style = MaterialTheme.typography.titleSmall,
                        color = MaterialTheme.colorScheme.primary, modifier = Modifier.weight(1f),
                        maxLines = 1, overflow = TextOverflow.Ellipsis,
                    )
                    Text("${list.size}", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            items(list, key = { it.id }) { s -> SessionRow(mesh, nodes, s, now, onClick = { onOpen(s) }) }
        }
    }
}

@Composable
private fun Heading(text: String, count: Int) {
    Row(
        Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, top = 16.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Dot(StatusColors.Approval, size = 8.dp)
        Spacer(Modifier.width(8.dp))
        Text(text, style = MaterialTheme.typography.titleSmall, color = StatusColors.Approval, modifier = Modifier.weight(1f))
        Text("$count", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/**
 * SessionRow is a session in the list. Swiped right it is renamed, swiped
 * left stopped, after asking; a long press offers both as well.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun SessionRow(mesh: Mesh, nodes: List<Node>, s: SessionInfo, now: Long, onClick: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }
    var stopping by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    val act = actor()
    val scope = rememberCoroutineScope()
    val stoppable = s.state == "running" || s.state == "starting"
    val swipe = rememberSwipeToDismissBoxState()
    SwipeToDismissBox(
        swipe,
        enableDismissFromEndToStart = stoppable,
        backgroundContent = { SwipeBackground(swipe.dismissDirection) },
        onDismiss = { value ->
            haptic.performHapticFeedback(HapticFeedbackType.LongPress)
            if (value == SwipeToDismissBoxValue.StartToEnd) renaming = true
            if (value == SwipeToDismissBoxValue.EndToStart) stopping = true
            scope.launch { swipe.reset() }
        },
    ) { Box {
        Row(
            Modifier.fillMaxWidth()
                .background(MaterialTheme.colorScheme.surface)
                .combinedClickable(onClick = onClick, onLongClick = {
                    haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                    menu = true
                })
                .padding(horizontal = 16.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            val dimmed = s.ended()
            AgentAvatar(
                s.agent, agentColor(nodes, s.nodeId, s.agent).let { if (dimmed) it.copy(alpha = 0.5f) else it },
                s.statusColor().takeIf { !dimmed },
            )
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(
                    s.label(), style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    color = if (dimmed) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.onSurface,
                )
                val where = dotted(s.agent, baseName(s.project), s.branch)
                Text(
                    where, style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
            Spacer(Modifier.width(8.dp))
            Column(horizontalAlignment = Alignment.End) {
                Text(
                    s.statusText(), style = MaterialTheme.typography.labelMedium, color = s.statusColor(),
                    fontWeight = if (s.working()) FontWeight.SemiBold else FontWeight.Normal,
                )
                now // the time below follows the clock
                Text(ago(s.lastActive()), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            DropdownMenuItem(text = { Text(stringResource(R.string.rename)) },
                leadingIcon = { Icon(Icons.Outlined.Edit, null) }, onClick = {
                    menu = false
                    renaming = true
                })
            if (stoppable) {
                DropdownMenuItem(text = { Text(stringResource(R.string.stop_session)) },
                    leadingIcon = { Icon(Icons.Outlined.StopCircle, null) }, onClick = {
                        menu = false
                        stopping = true
                    })
            }
        }
    } }
    if (renaming) RenameDialog(mesh, s, onDismiss = { renaming = false })
    if (stopping) StopDialog(s, onDismiss = { stopping = false }) { act { mesh.closeSession(s) } }
}

/** What a row shows under it as it is swiped: renaming one way, stopping the other. */
@Composable
private fun SwipeBackground(direction: SwipeToDismissBoxValue) {
    val stop = direction == SwipeToDismissBoxValue.EndToStart
    if (direction == SwipeToDismissBoxValue.Settled) return
    val colors = MaterialTheme.colorScheme
    Row(
        Modifier.fillMaxSize().background(if (stop) colors.errorContainer else colors.secondaryContainer).padding(horizontal = 24.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = if (stop) Arrangement.End else Arrangement.Start,
    ) {
        val tint = if (stop) colors.onErrorContainer else colors.onSecondaryContainer
        Icon(if (stop) Icons.Outlined.StopCircle else Icons.Outlined.Edit, null, tint = tint)
        Spacer(Modifier.width(8.dp))
        Text(stringResource(if (stop) R.string.stop else R.string.rename), color = tint, style = MaterialTheme.typography.labelLarge)
    }
}

/**
 * WaitingCard is a session waiting for you: the approval its agent asks
 * for, answered here, or that it waits for your word.
 */
@Composable
private fun WaitingCard(mesh: Mesh, nodes: List<Node>, s: SessionInfo, now: Long, onOpen: () -> Unit) {
    val act = actor()
    Card(
        onClick = onOpen,
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerHigh),
    ) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                AgentAvatar(s.agent, agentColor(nodes, s.nodeId, s.agent), null, size = 32.dp)
                Spacer(Modifier.width(10.dp))
                Column(Modifier.weight(1f)) {
                    Text(s.label(), style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    now
                    Text(
                        dotted(mesh.nodeName(s.nodeId), baseName(s.project), ago(s.lastActive())),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1, overflow = TextOverflow.Ellipsis,
                    )
                }
                Pill(s.statusText(), s.statusColor())
            }
            if (s.hasApproval()) {
                val a = s.approval
                Text(
                    stringResource(R.string.approval_title, a.tool),
                    style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.padding(top = 12.dp),
                )
                if (a.detail.isNotEmpty()) {
                    Text(
                        a.detail, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                        maxLines = 3, overflow = TextOverflow.Ellipsis, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(top = 4.dp),
                    )
                }
                Row(
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
                ) {
                    OutlinedButton(onClick = { act { mesh.answer(s, "deny") } }) { Text(stringResource(R.string.deny)) }
                    Button(onClick = { act { mesh.answer(s, "allow") } }) { Text(stringResource(R.string.allow)) }
                }
            } else {
                Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.End) {
                    FilledTonalButton(onClick = onOpen) { Text(stringResource(R.string.reply)) }
                }
            }
        }
    }
}

/**
 * Message is a screen with nothing to list: why, and what to do about it,
 * under an icon that says what kind of nothing it is.
 */
@Composable
fun Message(
    title: String, body: String, action: String? = null, onAction: () -> Unit = {},
    icon: ImageVector? = null, secondary: String? = null, onSecondary: () -> Unit = {},
) {
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) { EmptyState(title, body, Modifier, action, onAction, icon, secondary, onSecondary) }
}

/** EmptyState is Message's content, for a place that scrolls already. */
@Composable
fun EmptyState(
    title: String, body: String, modifier: Modifier = Modifier, action: String? = null, onAction: () -> Unit = {},
    icon: ImageVector? = null, secondary: String? = null, onSecondary: () -> Unit = {},
) {
    Column(modifier, horizontalAlignment = Alignment.CenterHorizontally) {
        if (icon != null) {
            Surface(shape = CircleShape, color = MaterialTheme.colorScheme.secondaryContainer, modifier = Modifier.size(72.dp)) {
                Box(contentAlignment = Alignment.Center) {
                    Icon(icon, null, Modifier.size(34.dp), tint = MaterialTheme.colorScheme.onSecondaryContainer)
                }
            }
            Spacer(Modifier.height(20.dp))
        }
        Text(title, style = MaterialTheme.typography.titleLarge, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Text(
            body, style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        if (action != null) {
            Spacer(Modifier.height(24.dp))
            Button(onClick = onAction) { Text(action) }
        }
        if (secondary != null) {
            Spacer(Modifier.height(4.dp))
            TextButton(onClick = onSecondary) { Text(secondary) }
        }
    }
}

/** Section is a card of a screen's settings, under its heading. */
@Composable
fun Section(title: String, modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    Column(modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Text(
            title, style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary,
            modifier = Modifier.padding(start = 4.dp, bottom = 8.dp),
        )
        Card(Modifier.fillMaxWidth()) { content() }
    }
}
