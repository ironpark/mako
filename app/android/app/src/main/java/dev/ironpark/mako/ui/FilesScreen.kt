package dev.ironpark.mako.ui

import android.text.format.Formatter
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
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
import dev.ironpark.mako.net.ProjectFiles
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
    val scrolls = rememberSaveableStateHolder()
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
            selected.isNotEmpty() -> selected = ""
            searching -> { searching = false; query = ""; focus.clearFocus(); keyboard?.hide() }
            section == 0 && resolvedRoot.isNotEmpty() && relativeRemote(resolvedRoot, currentPath).isNotEmpty() -> path = remoteParent(currentPath)
            else -> onBack()
        }
    }
    BackHandler { back() }
    if (selected.isNotEmpty()) {
        key(selected, diffPath) {
            ProjectFileScreen(files, selectedRoot, selected, selectedLine, diffPath, diffFrom, deleted, onBack = { selected = "" })
        }
        return
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
                                    FileRow(entry.name, dotted(size, if (entry.link) stringResource(R.string.symbolic_link) else "",
                                        if (entry.ignored) stringResource(R.string.git_ignored) else ""),
                                        if (entry.dir) Icons.Outlined.Folder else if (imageFile(entry.name)) Icons.Outlined.Image else Icons.Outlined.Description,
                                        onClick = { val next = remoteJoin(dir.path, entry.name); if (entry.dir) path = next else open(next) })
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

@Composable
internal fun FileRow(title: String, detail: String, icon: ImageVector, trailing: String = "", onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick).heightIn(min = 52.dp).padding(horizontal = 16.dp, vertical = 8.dp),
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
