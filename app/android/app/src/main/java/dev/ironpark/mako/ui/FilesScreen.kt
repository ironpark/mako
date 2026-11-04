package dev.ironpark.mako.ui

import android.text.format.Formatter
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.net.FileClip
import dev.ironpark.mako.net.FileOps
import dev.ironpark.mako.net.ProjectFiles
import android.widget.Toast
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import dev.ironpark.mako.v2.GitFile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun FilesScreen(files: ProjectFiles, root: String, nodeName: String, onBack: () -> Unit) {
    var path by rememberSaveable(root) { mutableStateOf(root.ifBlank { "~" }) }
    var resolvedRoot by rememberSaveable(root) { mutableStateOf("") }
    var refresh by remember { mutableIntStateOf(0) }
    var section by rememberSaveable { mutableIntStateOf(0) }
    var hidden by rememberSaveable { mutableStateOf(false) }
    var searching by rememberSaveable { mutableStateOf(false) }
    var query by rememberSaveable { mutableStateOf("") }
    var contents by rememberSaveable { mutableStateOf(false) }
    var selected by rememberSaveable { mutableStateOf("") }
    var selectedRoot by rememberSaveable { mutableStateOf("") }
    var selectedLine by rememberSaveable { mutableLongStateOf(0L) }
    var diffPath by rememberSaveable { mutableStateOf("") }
    var diffFrom by rememberSaveable { mutableStateOf("") }
    var deleted by rememberSaveable { mutableStateOf(false) }
    // The files a file's links led from, to go back to, latest last, as path and line.
    var trail by rememberSaveable { mutableStateOf(listOf<String>()) }
    val scrolls = rememberSaveableStateHolder()
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val clip by FileOps.clip.collectAsState()
    var ask by remember { mutableStateOf<FileAsk?>(null) }
    // A file operation goes off the screen's thread; the folder is read
    // again after it, and a failure says why.
    fun run(work: suspend () -> Unit) {
        scope.launch {
            try { work() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { Toast.makeText(context, e.message ?: e.toString(), Toast.LENGTH_LONG).show() }
            refresh++
        }
    }
    val focus = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val directory = remoteLoad(files, path, refresh) { files.list(path) }
    val currentPath = (directory as? RemoteLoad.Ready)?.value?.path ?: path
    LaunchedEffect(currentPath, directory) {
        if (directory is RemoteLoad.Ready && resolvedRoot.isEmpty()) resolvedRoot = currentPath
    }

    fun open(fullPath: String, line: Long = 0, change: GitFile? = null, repoRoot: String = resolvedRoot) {
        focus.clearFocus()
        keyboard?.hide()
        selectedRoot = repoRoot
        selectedLine = line
        diffPath = change?.path.orEmpty()
        diffFrom = change?.from.orEmpty()
        deleted = change?.status == "deleted"
        selected = fullPath
    }
    fun back() {
        when {
            trail.isNotEmpty() -> {
                val (from, line) = trail.last().split('\n', limit = 2).let { it[0] to (it.getOrNull(1)?.toLongOrNull() ?: 0L) }
                trail = trail.dropLast(1)
                open(from, line, repoRoot = selectedRoot)
            }
            selected.isNotEmpty() -> selected = ""
            searching -> { searching = false; query = ""; focus.clearFocus(); keyboard?.hide() }
            section == 0 && resolvedRoot.isNotEmpty() && relativeRemote(resolvedRoot, currentPath).isNotEmpty() -> path = remoteParent(currentPath)
            else -> onBack()
        }
    }
    BackHandler { back() }
    if (selected.isNotEmpty()) {
        key(selected, diffPath) {
            ProjectFileScreen(files, selectedRoot, selected, selectedLine, diffPath, diffFrom, deleted, onBack = { back() },
                onFile = { link ->
                    if (link.folder) {
                        trail = emptyList()
                        selected = ""
                        section = 0
                        path = link.path
                    } else {
                        trail = trail + "$selected\n$selectedLine"
                        open(link.path, link.line, repoRoot = selectedRoot)
                    }
                })
        }
        return
    }

    ask?.let { a ->
        FileAskDialog(a, onDismiss = { ask = null }) { name ->
            ask = null
            run {
                when (a) {
                    is FileAsk.Create -> files.op(a.op, remoteJoin(a.dir, name))
                    is FileAsk.Rename -> files.op(FileOps.RENAME, a.path, remoteJoin(remoteParent(a.path), name))
                    is FileAsk.Delete -> {
                        files.op(FileOps.DELETE, a.path)
                        if (FileOps.clip.value?.let { it.node == files.node && it.path == a.path } == true) FileOps.clip.value = null
                    }
                }
            }
        }
    }
    Scaffold(topBar = {
        TopAppBar(
            title = {
                Column {
                    Text(stringResource(R.string.project_files), style = MaterialTheme.typography.titleMedium)
                    Text(dotted(nodeName, baseName(resolvedRoot.ifEmpty { root })), style = MaterialTheme.typography.bodySmall,
                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            },
            navigationIcon = { IconButton(onClick = { back() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
            actions = {
                IconButton(enabled = resolvedRoot.isNotEmpty(), onClick = { searching = !searching; if (searching) section = 0 }) {
                    Icon(if (searching) Icons.Filled.Close else Icons.Outlined.Search,
                        stringResource(if (searching) R.string.close_search else R.string.search_files))
                }
                IconButton(onClick = { refresh++ }) { Icon(Icons.Outlined.Refresh, stringResource(R.string.refresh)) }
                var menu by remember { mutableStateOf(false) }
                IconButton(onClick = { menu = true }) { Icon(Icons.Outlined.MoreVert, stringResource(R.string.file_options)) }
                DropdownMenu(menu, onDismissRequest = { menu = false }) {
                    if (section == 0 && !searching && directory is RemoteLoad.Ready) {
                        DropdownMenuItem(text = { Text(stringResource(R.string.new_file)) }, onClick = { menu = false; ask = FileAsk.Create(currentPath, FileOps.CREATE) })
                        DropdownMenuItem(text = { Text(stringResource(R.string.new_folder)) }, onClick = { menu = false; ask = FileAsk.Create(currentPath, FileOps.MKDIR) })
                        clip?.let { c ->
                            DropdownMenuItem(text = { Text(stringResource(R.string.paste_named, baseName(c.path))) }, onClick = {
                                menu = false
                                if (c.cut) FileOps.clip.value = null // a move is pasted once
                                run { files.paste(c, currentPath) }
                            })
                        }
                        HorizontalDivider()
                    }
                    DropdownMenuItem(text = { Text(stringResource(R.string.show_hidden_files)) },
                        trailingIcon = { Checkbox(hidden, onCheckedChange = null) },
                        onClick = { hidden = !hidden; menu = false })
                }
            },
        )
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).consumeWindowInsets(padding).imePadding()) {
            if (searching) {
                val requester = remember { FocusRequester() }
                OutlinedTextField(query, onValueChange = { query = it }, singleLine = true,
                    label = { Text(stringResource(if (contents) R.string.search_contents else R.string.search_names)) },
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp).focusRequester(requester))
                LaunchedEffect(Unit) { requester.requestFocus() }
                Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilterChip(!contents, onClick = { contents = false }, label = { Text(stringResource(R.string.search_names)) })
                    FilterChip(contents, onClick = { contents = true }, label = { Text(stringResource(R.string.search_contents)) })
                }
                FileSearch(files, resolvedRoot, query, contents, refresh, retry = { refresh++ }, onOpen = { name, line -> open(remoteJoin(resolvedRoot, name), line) })
            } else {
                Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilterChip(section == 0, onClick = { section = 0 }, label = { Text(stringResource(R.string.files)) })
                    FilterChip(section == 1, onClick = { section = 1 }, label = { Text(stringResource(R.string.git_changes)) })
                }
                if (section == 0) {
                    Breadcrumbs(resolvedRoot, currentPath, onOpen = { path = it })
                    RemoteContent(directory, retry = { refresh++ }) { dir ->
                        val entries = remember(dir, hidden) { dir.entriesList.filter { hidden || !it.name.startsWith('.') } }
                        if (entries.isEmpty()) Message(stringResource(R.string.folder_empty), stringResource(R.string.folder_empty_body))
                        else scrolls.SaveableStateProvider("dir:${dir.path}") {
                            LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(bottom = 16.dp)) {
                                items(entries, key = { it.name }) { entry ->
                                    val size = if (entry.dir) "" else Formatter.formatShortFileSize(LocalContext.current, entry.size)
                                    val full = remoteJoin(dir.path, entry.name)
                                    var actions by remember { mutableStateOf(false) }
                                    Box {
                                        FileRow(entry.name, dotted(size, if (entry.link) stringResource(R.string.symbolic_link) else "",
                                            if (entry.ignored) stringResource(R.string.git_ignored) else ""),
                                            if (entry.dir) Icons.Outlined.Folder else if (imageFile(entry.name)) Icons.Outlined.Image else Icons.Outlined.Description,
                                            onLongClick = { actions = true },
                                            onClick = { if (entry.dir) path = full else open(full) })
                                        DropdownMenu(actions, onDismissRequest = { actions = false }) {
                                            val copy = { cut: Boolean -> FileOps.clip.value = FileClip(files.node, full, entry.dir, cut); actions = false }
                                            DropdownMenuItem(text = { Text(stringResource(R.string.copy)) }, onClick = { copy(false) })
                                            DropdownMenuItem(text = { Text(stringResource(R.string.cut)) }, onClick = { copy(true) })
                                            DropdownMenuItem(text = { Text(stringResource(R.string.rename)) }, onClick = { actions = false; ask = FileAsk.Rename(full) })
                                            DropdownMenuItem(text = { Text(stringResource(R.string.delete)) }, onClick = { actions = false; ask = FileAsk.Delete(full) })
                                        }
                                    }
                                }
                            }
                        }
                    }
                } else if (resolvedRoot.isEmpty()) {
                    RemoteContent(directory, retry = { refresh++ }) { }
                } else {
                    val changes = remoteLoad(files, resolvedRoot, refresh) { files.changes(resolvedRoot) }
                    RemoteContent(changes, retry = { refresh++ }) { git ->
                        when {
                            git.notRepo -> Message(stringResource(R.string.not_git_repo), resolvedRoot)
                            git.filesCount == 0 -> Message(stringResource(R.string.no_git_changes), git.branch)
                            else -> Column {
                                Text(dotted(git.branch, git.upstream, if (git.ahead > 0 || git.behind > 0) "↑${git.ahead} ↓${git.behind}" else ""),
                                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp), style = MaterialTheme.typography.labelMedium)
                                scrolls.SaveableStateProvider("changes") {
                                    LazyColumn {
                                        items(git.filesList, key = { it.path }) { file ->
                                            FileRow(file.path, dotted(gitStatusText(file.status), file.from), Icons.Outlined.Difference,
                                                trailing = "+${file.added} −${file.removed}",
                                                onClick = { open(remoteJoin(git.root, file.path), change = file, repoRoot = git.root) })
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/** A file operation that asks the user first: a name, or whether to delete. */
private sealed interface FileAsk {
    data class Create(val dir: String, val op: String) : FileAsk
    data class Rename(val path: String) : FileAsk
    data class Delete(val path: String) : FileAsk
}

@Composable
private fun FileAskDialog(ask: FileAsk, onDismiss: () -> Unit, onDone: (String) -> Unit) {
    if (ask is FileAsk.Delete) {
        AlertDialog(onDismissRequest = onDismiss,
            title = { Text(stringResource(R.string.delete_named, baseName(ask.path))) },
            text = { Text(stringResource(R.string.delete_file_body)) },
            confirmButton = { TextButton(onClick = { onDone("") }) { Text(stringResource(R.string.delete), color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } })
        return
    }
    val title = when (ask) {
        is FileAsk.Create -> stringResource(if (ask.op == FileOps.MKDIR) R.string.new_folder else R.string.new_file)
        else -> stringResource(R.string.rename)
    }
    var name by remember { mutableStateOf(if (ask is FileAsk.Rename) baseName(ask.path) else "") }
    val valid = name.isNotBlank() && '/' !in name && '\\' !in name && name != "." && name != ".."
    AlertDialog(onDismissRequest = onDismiss,
        title = { Text(title) },
        text = { OutlinedTextField(name, onValueChange = { name = it }, singleLine = true, label = { Text(stringResource(R.string.file_name)) }) },
        confirmButton = { TextButton(enabled = valid, onClick = { onDone(name.trim()) }) { Text(stringResource(android.R.string.ok)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } })
}

@Composable
private fun Breadcrumbs(root: String, path: String, onOpen: (String) -> Unit) {
    if (root.isEmpty()) return
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        TextButton(onClick = { onOpen(root) }) { Text(baseName(root).ifEmpty { root }) }
        var parent = root
        for (part in relativeRemote(root, path).split('/').filter { it.isNotEmpty() }) {
            parent = remoteJoin(parent, part)
            val destination = parent
            Text("/", color = MaterialTheme.colorScheme.onSurfaceVariant)
            TextButton(onClick = { onOpen(destination) }) { Text(part) }
        }
    }
}

@Composable
private fun FileSearch(files: ProjectFiles, root: String, query: String, contents: Boolean, refresh: Int, retry: () -> Unit, onOpen: (String, Long) -> Unit) {
    if (contents && query.isBlank()) {
        Message(stringResource(R.string.search_contents_hint), stringResource(R.string.search_project_body))
        return
    }
    val result = remoteLoad(files, root, if (contents) query else "", contents, refresh) {
        if (contents) delay(300)
        files.search(root, if (contents) query.trim() else "", names = !contents)
    }
    RemoteContent(result, retry) { found ->
        val names = remember(found, query) { matchingFiles(found.filesList, query) }
        if (names.isEmpty() && found.matchesCount == 0) Message(stringResource(R.string.no_file_results), query)
        else LazyColumn(Modifier.fillMaxSize()) {
            if (found.truncated || names.size > 200) item { FileNotice(stringResource(R.string.search_results_limited)) }
            items(names.take(200), key = { "file:$it" }) { name ->
                FileRow(baseName(name), name, Icons.Outlined.Description, onClick = { onOpen(name, 0) })
            }
            items(found.matchesList) { match ->
                FileRow("${match.path}:${match.line}", match.text.trim(), Icons.Outlined.Search, onClick = { onOpen(match.path, match.line) })
            }
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun FileRow(title: String, detail: String, icon: ImageVector, trailing: String = "", onLongClick: (() -> Unit)? = null, onClick: () -> Unit) {
    val press = if (onLongClick == null) Modifier.clickable(role = Role.Button, onClick = onClick)
        else Modifier.combinedClickable(role = Role.Button, onLongClick = onLongClick, onClick = onClick)
    Row(Modifier.fillMaxWidth().then(press).heightIn(min = 52.dp).padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Icon(icon, null, Modifier.size(20.dp), tint = MaterialTheme.colorScheme.primary)
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (detail.isNotEmpty()) Text(detail, style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        if (trailing.isNotEmpty()) Text(trailing, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
internal fun FileNotice(text: String) {
    Surface(color = MaterialTheme.colorScheme.secondaryContainer) {
        Text(text, Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp), style = MaterialTheme.typography.bodySmall)
    }
}

@Composable
private fun gitStatusText(status: String): String = stringResource(when (status) {
    "added" -> R.string.git_added
    "deleted" -> R.string.git_deleted
    "renamed" -> R.string.git_renamed
    "conflicted" -> R.string.git_conflicted
    else -> R.string.git_modified
})
