package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SuggestionChip
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.material.icons.outlined.DesktopAccessDisabled
import androidx.compose.material3.Surface
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.OpenSession
import dev.ironpark.mako.v2.SessionInfo

/**
 * NewSessionScreen starts an agent on one of the computers that let this
 * device in: which computer, which agent and profile, and in which folder,
 * offering those its sessions have worked in.
 */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
internal fun NewSessionScreen(mesh: Mesh, project: ProjectItem? = null, onBack: () -> Unit, onStarted: (SessionInfo) -> Unit) {
    val nodes by mesh.nodes.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val usable = nodes.filter { it.trustsUs && it.agentsCount > 0 }
    // In a project, on the computer of its first folder that can run one.
    val home = project?.folders?.map { it.node }?.firstOrNull { n -> usable.any { it.id == n } }
    var nodeId by rememberSaveable { mutableStateOf(home ?: usable.firstOrNull()?.id.orEmpty()) }
    val node = usable.firstOrNull { it.id == nodeId } ?: usable.firstOrNull()
    // The agent last started on the computer, else its first.
    val usual = sessions.filter { it.nodeId == node?.id && it.agent != "shell" }.maxByOrNull { it.startedAt.seconds }?.agent
        ?.takeIf { a -> node?.agentsList?.any { it.name == a } == true }
    var agent by rememberSaveable(node?.id) { mutableStateOf(usual ?: node?.agentsList?.firstOrNull()?.name.orEmpty()) }
    var profile by rememberSaveable(node?.id, agent) { mutableStateOf("") }
    // A project's folders on the computer, its first chosen.
    val own = project?.folders.orEmpty().filter { it.node == node?.id }.map { it.path }
    var folder by rememberSaveable(node?.id) { mutableStateOf(own.firstOrNull().orEmpty()) }
    var starting by remember { mutableStateOf(false) }
    val act = actor()

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        if (project != null) stringResource(R.string.new_session_in, project.label) else stringResource(R.string.new_session),
                        maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                },
            )
        },
        bottomBar = {
            if (node != null) Surface(color = MaterialTheme.colorScheme.surface, tonalElevation = 3.dp) {
                Box(Modifier.fillMaxWidth().navigationBarsPadding().imePadding().padding(16.dp)) {
                    Button(
                        enabled = agent.isNotEmpty() && !starting,
                        modifier = Modifier.fillMaxWidth().height(52.dp),
                        onClick = {
                            starting = true
                            act {
                                try {
                                    val s = mesh.daemon.startSession(
                                        OpenSession.newBuilder()
                                            .setNodeId(node.id)
                                            .setAgent(agent)
                                            .setProfile(profile)
                                            .setProject(folder.trim())
                                            .setCols(COLS)
                                            .setRows(ROWS)
                                            .build()
                                    )
                                    mesh.refreshSessions()
                                    onStarted(s)
                                } finally {
                                    starting = false
                                }
                            }
                        },
                    ) {
                        if (starting) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                        else Text(stringResource(R.string.start_agent, agent))
                    }
                }
            }
        },
    ) { pad ->
        if (node == null) {
            Box(Modifier.padding(pad)) {
                Message(stringResource(R.string.no_computers_to_run), stringResource(R.string.no_computers_to_run_body),
                    icon = Icons.Outlined.DesktopAccessDisabled)
            }
            return@Scaffold
        }
        Column(
            Modifier.fillMaxSize().padding(pad).consumeWindowInsets(pad).imePadding().verticalScroll(rememberScrollState()).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Label(stringResource(R.string.computer))
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                for (n in usable) {
                    FilterChip(selected = n.id == node.id, onClick = { nodeId = n.id }, label = { Text(n.name) })
                }
            }

            Label(stringResource(R.string.agent))
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                for (a in node.agentsList) {
                    FilterChip(
                        selected = a.name == agent,
                        onClick = { agent = a.name },
                        label = { Text(a.name) },
                        leadingIcon = { AgentAvatar(a.name, agentColor(nodes, node.id, a.name), null, size = 24.dp) },
                    )
                }
            }
            val profiles = node.agentsList.firstOrNull { it.name == agent }?.profilesList.orEmpty()
            if (profiles.isNotEmpty()) {
                Label(stringResource(R.string.profile))
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilterChip(selected = profile.isEmpty(), onClick = { profile = "" }, label = { Text(stringResource(R.string.profile_default)) })
                    for (p in profiles) FilterChip(selected = p == profile, onClick = { profile = p }, label = { Text(p) })
                }
            }

            Label(stringResource(R.string.folder))
            OutlinedTextField(
                value = folder,
                onValueChange = { folder = it },
                placeholder = { Text(stringResource(R.string.folder_hint)) },
                singleLine = true,
                textStyle = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                modifier = Modifier.fillMaxWidth(),
            )
            val recent = (own + sessions.filter { it.nodeId == node.id && it.project.isNotEmpty() }
                .sortedByDescending { it.startedAt.seconds }
                .map { it.worktree.repo.ifEmpty { it.project } })
                .distinct()
                .take(8)
            if (recent.isNotEmpty()) {
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    for (p in recent) FilterChip(
                        selected = folder == p, onClick = { folder = p }, label = { Text(baseName(p)) },
                        leadingIcon = { Icon(Icons.Outlined.Folder, null, Modifier.size(18.dp)) },
                    )
                }
            }

        }
    }
}

/** A session started here opens at a desktop window's size, where it is likely seen next. */
private const val COLS = 120
private const val ROWS = 36

@Composable
private fun Label(text: String) {
    Text(
        text, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(top = 12.dp),
    )
}
