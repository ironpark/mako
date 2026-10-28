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
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.Devices
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
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
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
private enum class Filter { All, NeedsYou, Working }

/**
 * SessionsScreen is the mesh's sessions: those waiting for you first, as
 * cards to answer from here, then each computer's, the latest first.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionsScreen(mesh: Mesh, onOpen: (SessionInfo) -> Unit, onMesh: () -> Unit, onNew: () -> Unit) {
    val state by mesh.state.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val loaded by mesh.loaded.collectAsState()
    val linked = nodes.count { it.trustsUs }
    var filter by rememberSaveable { mutableStateOf(Filter.All) }
    var refreshing by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val bar = TopAppBarDefaults.enterAlwaysScrollBehavior()

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
                    stringResource(R.string.retry), mesh::start,
                )
                Mesh.State.Running -> when {
                    !loaded && sessions.isEmpty() -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                    linked == 0 && sessions.isEmpty() -> Message(
                        stringResource(R.string.connect_title), stringResource(R.string.connect_body),
                        stringResource(R.string.computers), onMesh,
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
                        SessionList(mesh, nodes, sessions, filter, onFilter = { filter = it }, onOpen)
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

    LazyColumn(contentPadding = PaddingValues(bottom = 96.dp), modifier = Modifier.fillMaxSize()) {
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
            item(key = "waiting") { Heading(stringResource(R.string.needs_you)) }
            items(waiting, key = { "w:" + it.id }) { s -> WaitingCard(mesh, nodes, s, now, onOpen = { onOpen(s) }) }
        }
        if (shown.isEmpty()) {
            item(key = "empty") {
                Text(
                    stringResource(if (filter == Filter.All) R.string.no_sessions_body else R.string.nothing_here),
                    style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.fillMaxWidth().padding(48.dp),
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
private fun Heading(text: String) {
    Text(
        text, style = MaterialTheme.typography.titleSmall, color = StatusColors.Approval,
        modifier = Modifier.padding(start = 20.dp, end = 20.dp, top = 16.dp, bottom = 6.dp),
    )
}

/** SessionRow is a session in the list; a long press offers renaming and stopping it. */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun SessionRow(mesh: Mesh, nodes: List<Node>, s: SessionInfo, now: Long, onClick: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    val act = actor()
    Box {
        Row(
            Modifier.fillMaxWidth()
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
            DropdownMenuItem(text = { Text(stringResource(R.string.rename)) }, onClick = {
                menu = false
                renaming = true
            })
            if (s.state == "running" || s.state == "starting") {
                DropdownMenuItem(text = { Text(stringResource(R.string.stop_session)) }, onClick = {
                    menu = false
                    act { mesh.closeSession(s) }
                })
            }
        }
    }
    if (renaming) RenameDialog(mesh, s, onDismiss = { renaming = false })
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

/** Message is a screen with nothing to list: why, and what to do about it. */
@Composable
fun Message(title: String, body: String, action: String? = null, onAction: () -> Unit = {}) {
    Column(
        Modifier.fillMaxSize().padding(32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
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
