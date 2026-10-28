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
fun NewSessionScreen(mesh: Mesh, onBack: () -> Unit, onStarted: (SessionInfo) -> Unit) {
    val nodes by mesh.nodes.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val usable = nodes.filter { it.trustsUs && it.agentsCount > 0 }
    var nodeId by rememberSaveable { mutableStateOf(usable.firstOrNull()?.id.orEmpty()) }
    val node = usable.firstOrNull { it.id == nodeId } ?: usable.firstOrNull()
    var agent by rememberSaveable(node?.id) { mutableStateOf(node?.agentsList?.firstOrNull()?.name.orEmpty()) }
    var profile by rememberSaveable(node?.id, agent) { mutableStateOf("") }
    var project by rememberSaveable(node?.id) { mutableStateOf("") }
    var starting by remember { mutableStateOf(false) }
    val act = actor()

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(stringResource(R.string.new_session)) },
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                },
            )
        },
    ) { pad ->
        if (node == null) {
            Message(stringResource(R.string.no_computers_to_run), stringResource(R.string.no_computers_to_run_body))
            return@Scaffold
        }
        Column(
            Modifier.fillMaxSize().padding(pad).imePadding().verticalScroll(rememberScrollState()).padding(16.dp),
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
                        leadingIcon = a.tint()?.let { c -> { Dot(c) } },
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
                value = project,
                onValueChange = { project = it },
                placeholder = { Text(stringResource(R.string.folder_hint)) },
                singleLine = true,
                textStyle = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                modifier = Modifier.fillMaxWidth(),
            )
            val recent = sessions.filter { it.nodeId == node.id && it.project.isNotEmpty() }
                .sortedByDescending { it.startedAt.seconds }
                .map { it.worktree.repo.ifEmpty { it.project } }
                .distinct()
                .take(8)
            if (recent.isNotEmpty()) {
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    for (p in recent) SuggestionChip(onClick = { project = p }, label = { Text(baseName(p)) })
                }
            }

            Spacer(Modifier.height(16.dp))
            Button(
                enabled = agent.isNotEmpty() && !starting,
                modifier = Modifier.fillMaxWidth(),
                onClick = {
                    starting = true
                    act {
                        try {
                            val s = mesh.daemon.startSession(
                                OpenSession.newBuilder()
                                    .setNodeId(node.id)
                                    .setAgent(agent)
                                    .setProfile(profile)
                                    .setProject(project.trim())
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
                else Text(stringResource(R.string.start))
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
