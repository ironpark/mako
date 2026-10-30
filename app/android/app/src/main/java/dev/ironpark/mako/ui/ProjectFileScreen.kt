package dev.ironpark.mako.ui

import android.content.ClipData
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.text.format.Formatter
import android.widget.Toast
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.outlined.Article
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.net.ProjectFiles
import dev.ironpark.mako.v2.File as RemoteFile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private data class FilePreview(val file: RemoteFile, val text: String, val bitmap: Bitmap?)
internal data class SourceLine(val text: String, val number: Long, val old: Long = 0, val op: String = "")

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun ProjectFileScreen(
    files: ProjectFiles, root: String, path: String, line: Long,
    diffPath: String, from: String, deleted: Boolean, onBack: () -> Unit,
) {
    var diff by rememberSaveable { mutableStateOf(diffPath.isNotEmpty()) }
    var preview by rememberSaveable { mutableStateOf(markdownFile(path) && line == 0L) }
    var refresh by remember { mutableIntStateOf(0) }
    val context = LocalContext.current
    val clipboard = LocalClipboard.current
    val scope = rememberCoroutineScope()
    Scaffold(topBar = {
        TopAppBar(
            title = { Column {
                Text(baseName(path), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(relativeRemote(root, path), style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            } },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
            actions = {
                if (diffPath.isNotEmpty() && !deleted) IconButton(onClick = { diff = !diff }) {
                    Icon(if (diff) Icons.Outlined.Description else Icons.Outlined.Difference,
                        stringResource(if (diff) R.string.view_file else R.string.view_diff))
                }
                if (!diff && markdownFile(path)) IconButton(onClick = { preview = !preview }) {
                    Icon(if (preview) Icons.Outlined.Code else Icons.AutoMirrored.Outlined.Article,
                        stringResource(if (preview) R.string.view_source else R.string.preview_markdown))
                }
                IconButton(onClick = { scope.launch {
                    clipboard.setClipEntry(ClipEntry(ClipData.newPlainText("mako", path)))
                    Toast.makeText(context, R.string.path_copied, Toast.LENGTH_SHORT).show()
                } }) { Icon(Icons.Outlined.ContentCopy, stringResource(R.string.copy_file_path)) }
                IconButton(onClick = { refresh++ }) { Icon(Icons.Outlined.Refresh, stringResource(R.string.refresh)) }
            },
        )
    }) { padding ->
        Box(Modifier.fillMaxSize().padding(padding)) {
            if (diff) {
                val result = remoteLoad(files, root, diffPath, from, refresh) { files.diff(root, diffPath, from) }
                RemoteContent(result, retry = { refresh++ }) { resultDiff ->
                    Column {
                        if (resultDiff.truncated) FileNotice(stringResource(R.string.diff_truncated))
                        when {
                            resultDiff.binary -> Message(stringResource(R.string.binary_diff), stringResource(R.string.binary_diff_body))
                            resultDiff.linesCount == 0 -> Message(stringResource(R.string.diff_empty), stringResource(R.string.diff_empty_body))
                            else -> {
                                val lines = remember(resultDiff) { resultDiff.linesList.map { SourceLine(it.text, it.new, it.old, it.op) } }
                                SourceViewer(lines, diff = true)
                            }
                        }
                    }
                }
            } else {
                val image = imageFile(path)
                val result = remoteLoad(files, path, refresh) {
                    val file = files.read(path, image)
                    withContext(Dispatchers.Default) {
                        FilePreview(file, if (image) "" else file.data.toStringUtf8(), if (image && !file.truncated) decodePreview(file.data.toByteArray()) else null)
                    }
                }
                RemoteContent(result, retry = { refresh++ }) { opened ->
                    Column(Modifier.fillMaxSize()) {
                        Text(dotted(Formatter.formatShortFileSize(context, opened.file.size), if (opened.file.hasModTime()) ago(opened.file.modTime.millis()) else ""),
                            Modifier.padding(horizontal = 12.dp, vertical = 6.dp), style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
                        if (opened.file.truncated) FileNotice(stringResource(if (image) R.string.image_too_large else R.string.file_truncated))
                        when {
                            image && opened.bitmap != null -> Image(opened.bitmap.asImageBitmap(), baseName(path), Modifier.fillMaxSize().padding(8.dp), contentScale = ContentScale.Fit)
                            image -> Message(stringResource(R.string.image_unavailable), stringResource(R.string.image_unavailable_body))
                            opened.file.binary -> Message(stringResource(R.string.binary_file), stringResource(R.string.binary_file_body))
                            opened.text.isEmpty() -> Message(stringResource(R.string.file_empty), baseName(path))
                            preview -> SelectionContainer {
                                LazyColumn(contentPadding = PaddingValues(12.dp)) { item { MarkdownMessage(opened.text) } }
                            }
                            else -> {
                                val lines = remember(opened.text) { opened.text.lines().mapIndexed { index, text -> SourceLine(text, index + 1L) } }
                                if (line > lines.size && opened.file.truncated) FileNotice(stringResource(R.string.file_line_not_loaded))
                                SourceViewer(lines, initialLine = line)
                            }
                        }
                    }
                }
            }
        }
    }
}

/** Shared horizontal scrolling keeps line numbers and diff columns aligned. */
@Composable
internal fun SourceViewer(lines: List<SourceLine>, diff: Boolean = false, initialLine: Long = 0) {
    val list = rememberLazyListState()
    val style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace)
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    val columns = remember(lines) { lines.maxOfOrNull { it.text.length }?.coerceAtMost(240) ?: 0 }
    val letter = measurer.measure("M", style).size.width
    val dark = MaterialTheme.colorScheme.surface.luminance() < 0.5f
    val added = if (dark) Color(0xFF7EE787) else Color(0xFF1A7F37)
    val removed = if (dark) Color(0xFFFF7B72) else Color(0xFFCF222E)
    val muted = MaterialTheme.colorScheme.onSurfaceVariant
    LaunchedEffect(lines, initialLine) {
        if (initialLine > 0 && lines.isNotEmpty()) list.scrollToItem((initialLine - 1).coerceIn(0, lines.lastIndex.toLong()).toInt())
    }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val width = maxOf(maxWidth, with(density) { (columns * letter).toDp() } + if (diff) 112.dp else 64.dp)
        SelectionContainer {
            Box(Modifier.fillMaxSize().horizontalScroll(rememberScrollState())) {
                LazyColumn(state = list, modifier = Modifier.width(width).fillMaxHeight(), contentPadding = PaddingValues(bottom = 24.dp)) {
                    itemsIndexed(lines) { _, row ->
                        val color = when (row.op) { "+" -> added; "-" -> removed; "@" -> MaterialTheme.colorScheme.primary; else -> MaterialTheme.colorScheme.onSurface }
                        val background = when {
                            row.op in listOf("+", "-", "@") -> color.copy(alpha = if (dark) 0.12f else 0.06f)
                            initialLine > 0 && row.number == initialLine -> MaterialTheme.colorScheme.secondaryContainer
                            else -> Color.Transparent
                        }
                        Row(Modifier.fillMaxWidth().background(background).padding(vertical = 2.dp)) {
                            if (diff) Text(row.old.takeIf { it > 0 }?.toString().orEmpty(), Modifier.width(44.dp).padding(end = 6.dp), style = style, color = muted, textAlign = TextAlign.End)
                            Text(row.number.takeIf { it > 0 }?.toString().orEmpty(), Modifier.width(44.dp).padding(end = 6.dp), style = style, color = muted, textAlign = TextAlign.End)
                            Text((if (diff && row.op != "@") row.op + " " else "") + row.text, Modifier.weight(1f).padding(end = 8.dp), style = style, color = color)
                        }
                    }
                }
            }
        }
    }
}

private fun decodePreview(data: ByteArray): Bitmap? {
    if (data.isEmpty()) return null
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(data, 0, data.size, bounds)
    var sample = 1
    while (maxOf(bounds.outWidth, bounds.outHeight) / sample > 2048) sample *= 2
    return BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })
}
