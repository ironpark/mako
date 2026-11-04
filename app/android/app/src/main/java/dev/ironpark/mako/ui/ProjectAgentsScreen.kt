package dev.ironpark.mako.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.mako
import dev.ironpark.mako.v2.AgentMessage
import dev.ironpark.mako.v2.ProjectAgent
import dev.ironpark.mako.v2.ProjectAgents
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

/** How often the project's agents are read again while the screen shows. */
private const val AGENTS_POLL = 2_000L

/**
 * ProjectAgentsScreen is the agents of the project the session self works
 * in, on node: each under the agent that spawned it, with what it is
 * doing, and the messages they sent each other, the latest first, Mako's
 * notes of an agent stuck or gone among them. An agent opens its session.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun ProjectAgentsScreen(mesh: Mesh, node: String, path: String, self: String, onBack: () -> Unit, onOpen: (SessionInfo) -> Unit) {
    val shown by LocalContext.current.mako.foreground.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val supported = mesh.readsProjectAgents(node)
    var data by remember(node, path) { mutableStateOf<ProjectAgents?>(null) }
    var error by remember(node, path) { mutableStateOf("") }
    LaunchedEffect(node, path, shown, supported) {
        while (shown && supported) {
            try {
                data = mesh.daemon.projectAgents(node, path)
                error = ""
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: e.toString()
            }
            delay(AGENTS_POLL)
        }
    }
    var expanded by remember { mutableLongStateOf(0L) }

    Scaffold(topBar = {
        TopAppBar(
            title = {
                Column {
                    Text(stringResource(R.string.project_agents), maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(dotted(mesh.nodeName(node), baseName(path)), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
        )
    }) { padding ->
        val snapshot = data
        Box(Modifier.fillMaxSize().padding(padding)) {
            when {
                !supported -> Message(stringResource(R.string.project_agents), stringResource(R.string.project_agents_unsupported))
                snapshot == null && error.isNotEmpty() -> Message(stringResource(R.string.project_agents), error)
                snapshot == null -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                else -> {
                    val byID = snapshot.agentsList.associateBy { it.session.id }
                    val label = { id: String -> byID[id]?.session?.label() ?: id }
                    val counts = projectAgentCounts(snapshot)
                    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(bottom = 16.dp)) {
                        if (error.isNotEmpty()) item {
                            Text(error, Modifier.padding(16.dp, 8.dp), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                        }
                        item { SectionHead(stringResource(R.string.agents), stringResource(R.string.project_agents_counts, counts.working, counts.waiting, counts.pending)) }
                        val rows = projectAgentRows(snapshot.agentsList)
                        if (rows.isEmpty()) item { Hint(stringResource(R.string.no_agents)) }
                        items(rows, key = { "agent:" + it.agent.session.id }) { row ->
                            // The mesh's copy is newer than the snapshot's.
                            val live = sessions.firstOrNull { it.id == row.agent.session.id && it.nodeId == row.agent.session.nodeId }
                            AgentRow(mesh, row, live ?: row.agent.session, nodes, self, onOpen = live?.let { { onOpen(it) } })
                        }
                        item { SectionHead(stringResource(R.string.recent_messages), "") }
                        val messages = snapshot.messagesList.asReversed()
                        if (messages.isEmpty()) item { Hint(stringResource(R.string.no_messages)) }
                        items(messages, key = { "message:" + it.id }) { m ->
                            MessageRow(m, label, self, open = expanded == m.id, onToggle = { expanded = if (expanded == m.id) 0L else m.id })
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun SectionHead(title: String, meta: String) {
    Row(Modifier.fillMaxWidth().padding(16.dp, 16.dp, 16.dp, 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(title, Modifier.weight(1f), style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        if (meta.isNotEmpty()) Text(meta, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun Hint(text: String) {
    Text(text, Modifier.padding(16.dp, 8.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
}

@Composable
private fun AgentRow(mesh: Mesh, row: ProjectAgentRow, s: SessionInfo, nodes: List<dev.ironpark.mako.v2.Node>, self: String, onOpen: (() -> Unit)?) {
    val pending = row.agent.pendingMessages
    Row(
        Modifier.fillMaxWidth().then(if (onOpen != null) Modifier.clickable(onClick = onOpen) else Modifier)
            .padding(start = 16.dp + 20.dp * row.depth, end = 16.dp, top = 8.dp, bottom = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        AgentAvatar(s.agent, agentColor(nodes, s.nodeId, s.agent), s.statusColor(), size = 32.dp)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(s.label(), maxLines = 1, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodyLarge)
            Text(
                dotted(
                    s.statusText(), mesh.nodeName(s.nodeId),
                    if (pending > 0) stringResource(R.string.pending_messages, pending) else null,
                ),
                maxLines = 1, overflow = TextOverflow.Ellipsis,
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        if (s.id == self) Pill(stringResource(R.string.this_session), MaterialTheme.colorScheme.primary)
    }
}

@Composable
private fun MessageRow(m: AgentMessage, label: (String) -> String, self: String, open: Boolean, onToggle: () -> Unit) {
    val who = when {
        m.note -> stringResource(R.string.mako_note_about, label(m.fromSessionId))
        else -> label(m.fromSessionId) + " → " + label(m.toSessionId)
    }
    val state = when (m.state) {
        "acknowledged", "delivered" -> null
        "removed" -> stringResource(R.string.message_removed)
        else -> stringResource(R.string.message_pending)
    }
    val container = when {
        m.note -> MaterialTheme.colorScheme.tertiaryContainer
        self.isNotEmpty() && m.fromSessionId == self -> MaterialTheme.colorScheme.secondaryContainer
        else -> MaterialTheme.colorScheme.surfaceContainerHigh
    }
    Surface(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp).clickable(onClick = onToggle),
        color = container, shape = MaterialTheme.shapes.medium,
    ) {
        Column(Modifier.padding(12.dp, 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (m.note) {
                    Icon(Icons.Outlined.Info, null, Modifier.size(14.dp))
                    Spacer(Modifier.width(4.dp))
                }
                Text(who, Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.labelMedium)
                if (state != null) {
                    Text(state, style = MaterialTheme.typography.labelSmall, color = StatusColors.Waiting)
                    Spacer(Modifier.width(6.dp))
                }
                Text(ago(m.createdAt.millis()), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Spacer(Modifier.height(2.dp))
            Text(m.text, maxLines = if (open) Int.MAX_VALUE else 4, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodyMedium)
        }
    }
}

internal data class ProjectAgentCounts(val working: Int, val waiting: Int, val pending: Int)

/** How many of the project's agents work, wait on the user, and how many messages wait for them. */
internal fun projectAgentCounts(snapshot: ProjectAgents): ProjectAgentCounts {
    var working = 0
    var waiting = 0
    var pending = 0
    for (a in snapshot.agentsList) {
        val s = a.session
        if (s.state == "running") {
            if (s.status == "working" || s.status == "compacting" || (s.status.isEmpty() && s.busy)) working++
            if (s.needsYou()) waiting++
        }
        pending += a.pendingMessages
    }
    return ProjectAgentCounts(working, waiting, pending)
}

internal data class ProjectAgentRow(val agent: ProjectAgent, val depth: Int)

/**
 * agents in order, each spawned one under its parent, a level in. One whose
 * parent is not among them heads a tree of its own, and a cycle, which
 * should not be, still shows each agent once.
 */
internal fun projectAgentRows(agents: List<ProjectAgent>): List<ProjectAgentRow> {
    val ids = agents.mapTo(HashSet()) { it.session.id }
    val children = agents.groupBy { it.parentSessionId }
    val seen = HashSet<String>()
    val out = ArrayList<ProjectAgentRow>()
    fun visit(a: ProjectAgent, depth: Int) {
        val id = a.session.id
        if (id.isEmpty() || !seen.add(id)) return
        out += ProjectAgentRow(a, depth)
        for (child in children[id].orEmpty()) visit(child, depth + 1)
    }
    for (a in agents) if (a.parentSessionId.isEmpty() || a.parentSessionId !in ids) visit(a, 0)
    for (a in agents) visit(a, 0)
    return out
}
