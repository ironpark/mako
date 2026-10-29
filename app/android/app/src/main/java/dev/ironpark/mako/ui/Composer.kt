package dev.ironpark.mako.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Command
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.CancellationException

/** Suggestions only insert text. Sending is explicit and a failed send retains the draft. */
@Composable
internal fun Composer(
    mesh: Mesh, session: SessionInfo, text: String, onText: (String) -> Unit,
    onSend: suspend (String) -> Unit, onInterrupt: () -> Unit,
) {
    var commands by remember(session.id) { mutableStateOf<List<Command>>(emptyList()) }
    var failed by remember(session.id) { mutableStateOf(false) }
    var loading by remember(session.id) { mutableStateOf(false) }
    var attempt by remember(session.id) { mutableIntStateOf(0) }
    var sending by remember(session.id) { mutableStateOf(false) }
    val currentText by rememberUpdatedState(text)
    var editing by remember(session.id) { mutableStateOf(TextFieldValue(text, TextRange(text.length))) }
    LaunchedEffect(text) {
        if (editing.text != text) editing = TextFieldValue(text, TextRange(text.length))
    }
    val act = actor()
    LaunchedEffect(session.nodeId, session.id, session.project, attempt) {
        loading = true
        try {
            commands = mesh.daemon.commands(session.nodeId, session.id)
            failed = false
        } catch (e: CancellationException) { throw e }
        catch (_: Exception) { failed = true }
        finally { loading = false }
    }
    val matches = remember(text, commands) { matchingCommands(text, commands) }
    Surface(color = MaterialTheme.colorScheme.surfaceContainer) {
        Column(Modifier.navigationBarsPadding()) {
            if (text.startsWith('/') && !text.any { it.isWhitespace() }) {
                if (loading) LinearProgressIndicator(Modifier.fillMaxWidth())
                if (failed) TextButton(onClick = { attempt++ }, enabled = !loading) { Text(stringResource(R.string.commands_retry)) }
                if (!loading && !failed && matches.isEmpty()) Text(stringResource(R.string.commands_empty),
                    Modifier.padding(12.dp), style = MaterialTheme.typography.bodySmall)
                LazyColumn(Modifier.fillMaxWidth().heightIn(max = 200.dp)) {
                    items(matches, key = { it.name }) { command ->
                        ListItem(
                            modifier = Modifier.clickable { onText("/${command.name} ") },
                            headlineContent = { Text("/${command.name}" + command.args.takeIf { it.isNotBlank() }?.let { " $it" }.orEmpty()) },
                            supportingContent = { Text(dotted(command.description, command.source), maxLines = 2, overflow = TextOverflow.Ellipsis) },
                        )
                    }
                }
            }
            Row(Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (session.working()) FilledTonalIconButton(onClick = onInterrupt) {
                    Icon(Icons.Filled.Stop, stringResource(R.string.interrupt))
                }
                OutlinedTextField(
                    value = editing, onValueChange = { editing = it; onText(it.text) },
                    placeholder = { Text(stringResource(R.string.composer_hint)) },
                    modifier = Modifier.weight(1f).heightIn(max = 160.dp), shape = RoundedCornerShape(24.dp),
                    colors = OutlinedTextFieldDefaults.colors(unfocusedContainerColor = MaterialTheme.colorScheme.surface,
                        focusedContainerColor = MaterialTheme.colorScheme.surface),
                )
                FilledIconButton(enabled = text.isNotBlank() && !sending && session.state == "running", onClick = {
                    val submitted = text
                    sending = true
                    act {
                        try {
                            onSend(submitted)
                            if (currentText == submitted) onText("")
                        } finally { sending = false }
                    }
                }) {
                    if (sending) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                    else Icon(Icons.AutoMirrored.Filled.Send, stringResource(R.string.send))
                }
            }
        }
    }
}
