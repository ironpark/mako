package dev.ironpark.mako.ui

import android.net.Uri
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.outlined.AttachFile
import androidx.compose.ui.platform.LocalContext
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
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

/** A file on its way to the session's node, and how much of it has gone. */
private data class Upload(val id: Long, val name: String, val size: Long, val sent: Long = 0)

/**
 * Suggestions only insert text. Sending is explicit and a failed send
 * retains the draft. Files attached are sent to the session's node first,
 * then go into the draft by the paths its node keeps them at, as files
 * dropped on the desktop's composer do; images among them show over it.
 */
@Composable
internal fun Composer(
    mesh: Mesh, session: SessionInfo, text: String, onText: (String) -> Unit,
    onSend: suspend (String) -> Unit, onInterrupt: () -> Unit,
    onOpen: (String) -> Unit = {},
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
    val context = LocalContext.current
    var uploads by remember(session.id) { mutableStateOf<List<Upload>>(emptyList()) }
    val attached = remember(text, session.project) { attachments(text, session.project) }
    val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        if (uris.isEmpty()) return@rememberLauncherForActivityResult
        val pending = uris.mapIndexed { i, uri -> uri to Upload(System.nanoTime() + i, displayName(context, uri), fileSize(context, uri)) }
        uploads = uploads + pending.map { it.second }
        act {
            try {
                // One at a time, each into the draft as it arrives.
                for ((uri, up) in pending) {
                    val path = withContext(Dispatchers.IO) {
                        val input = context.contentResolver.openInputStream(uri) ?: error(context.getString(R.string.attach_unreadable, up.name))
                        input.use { mesh.daemon.stage(session.nodeId, up.name, it) { sent -> uploads = uploads.map { u -> if (u.id == up.id) u.copy(sent = sent) else u } } }
                    }
                    uploads = uploads.filterNot { it.id == up.id }
                    onText(withPath(currentText, path))
                }
            } finally {
                val ids = pending.map { it.second.id }.toSet()
                uploads = uploads.filterNot { it.id in ids }
            }
        }
    }
    Surface(color = MaterialTheme.colorScheme.surfaceContainer) {
        Column(Modifier.navigationBarsPadding()) {
            if (attached.isNotEmpty() || uploads.isNotEmpty()) Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(start = 12.dp, end = 12.dp, top = 8.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.Bottom,
            ) {
                for (a in attached) key(a.path) {
                    val remove: () -> Unit = { onText(withoutWord(currentText, a.word)) }
                    if (a.image) Box {
                        RemoteThumbnail(mesh.daemon, session.nodeId, a.path, named = false, onOpen = { onOpen(a.path) })
                        Surface(Modifier.align(Alignment.TopEnd).padding(3.dp).size(24.dp).clickable(onClick = remove),
                            shape = RoundedCornerShape(12.dp), color = MaterialTheme.colorScheme.surfaceContainerHighest.copy(alpha = 0.9f)) {
                            Icon(Icons.Filled.Close, stringResource(R.string.attach_remove), Modifier.padding(4.dp))
                        }
                    } else FileChip(baseName(a.path), onClick = { onOpen(a.path) }) {
                        IconButton(onClick = remove, modifier = Modifier.size(28.dp)) {
                            Icon(Icons.Filled.Close, stringResource(R.string.attach_remove), Modifier.size(16.dp))
                        }
                    }
                }
                for (u in uploads) key(u.id) {
                    FileChip(u.name) {
                        val progress = if (u.size > 0) (u.sent.toFloat() / u.size).coerceIn(0f, 1f) else null
                        Box(Modifier.size(28.dp), contentAlignment = Alignment.Center) {
                            if (progress != null) CircularProgressIndicator(progress = { progress }, Modifier.size(16.dp), strokeWidth = 2.dp)
                            else CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
                        }
                    }
                }
            }
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
            Row(Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (session.working()) FilledTonalIconButton(onClick = onInterrupt) {
                    Icon(Icons.Filled.Stop, stringResource(R.string.interrupt))
                }
                IconButton(
                    onClick = { pick.launch("*/*") },
                    enabled = session.state == "running" && mesh.canTransfer(session.nodeId),
                ) { Icon(Icons.Outlined.AttachFile, stringResource(R.string.attach)) }
                OutlinedTextField(
                    value = editing, onValueChange = { editing = it; onText(it.text) },
                    placeholder = { Text(stringResource(R.string.composer_hint)) },
                    modifier = Modifier.weight(1f).heightIn(max = 160.dp), shape = RoundedCornerShape(24.dp),
                    colors = OutlinedTextFieldDefaults.colors(unfocusedContainerColor = MaterialTheme.colorScheme.surface,
                        focusedContainerColor = MaterialTheme.colorScheme.surface),
                )
                FilledIconButton(enabled = text.isNotBlank() && !sending && uploads.isEmpty() && session.state == "running", onClick = {
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

/** The name uri's file goes by, as a staged file keeps it: with an extension for its type. */
private fun displayName(context: android.content.Context, uri: Uri): String {
    val name = runCatching {
        context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) c.getString(0) else null
        }
    }.getOrNull() ?: uri.lastPathSegment?.substringAfterLast('/') ?: "attachment"
    val clean = stagedName(name)
    if (clean.contains('.')) return clean
    val ext = context.contentResolver.getType(uri)?.let { MimeTypeMap.getSingleton().getExtensionFromMimeType(it) }
    return if (ext != null) "$clean.$ext" else clean
}

/** How long uri's file is, or 0 when its provider does not say. */
private fun fileSize(context: android.content.Context, uri: Uri): Long = runCatching {
    context.contentResolver.query(uri, arrayOf(OpenableColumns.SIZE), null, null, null)?.use { c ->
        if (c.moveToFirst() && !c.isNull(0)) c.getLong(0) else 0L
    }
}.getOrNull() ?: 0L
