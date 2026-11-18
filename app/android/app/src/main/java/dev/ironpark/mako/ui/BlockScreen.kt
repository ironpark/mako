package dev.ironpark.mako.ui

import android.widget.Toast
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Extension
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.blocks.BlockForm
import dev.ironpark.mako.blocks.BlockState
import dev.ironpark.mako.blocks.followBlock
import dev.ironpark.mako.mako
import dev.ironpark.mako.v2.BlockEvent
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

// A block of a computer's, a plugin's or an agent's, a page of its own.
// The app draws its frame, over the tree, naming the block and where it
// comes from, so that nothing the block draws passes for the app's own;
// and in the tree's place, how a plugin stands when it does not run,
// which the phone can only tell of: allowing a plugin, enabling it or
// starting it again is the computer's.

/** The plugin states a block's update may hold (protocol.Plugin*). */
private object PluginState {
    const val WAITING = "waiting"
    const val DENIED = "denied"
    const val FAILED = "failed"
    const val DISABLED = "disabled"
    const val BLOCKED = "blocked"
}

/** BlockScreen is node's block, followed while it shows; title is its name until the block says. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun BlockScreen(mesh: Mesh, node: String, block: String, title: String, onBack: () -> Unit) {
    val shown by LocalContext.current.mako.foreground.collectAsState()
    var state by remember(node, block) { mutableStateOf(BlockState()) }
    val form = remember(node, block) { BlockForm() }
    LaunchedEffect(node, block, shown) {
        if (!shown) return@LaunchedEffect
        followBlock(mesh.daemon, node, block).collect {
            form.sync(it.tree)
            state = it
        }
    }
    val context = LocalContext.current
    val uri = LocalUriHandler.current
    val scope = rememberCoroutineScope()
    val tree by rememberUpdatedState(state.tree)
    val events = remember(node, block) {
        BlockEvents { id, kind, value ->
            scope.launch {
                try {
                    val done = mesh.daemon.blockEvent(
                        BlockEvent.newBuilder().setNodeId(node).setBlock(block).setNode(id).setKind(kind).setValue(value)
                            .putAllValues(form.values(tree)).build()
                    )
                    // A link the plugin asked, in answer, to open: a web page alone.
                    val link = done.openUrl
                    if (link.startsWith("https://") || link.startsWith("http://")) runCatching { uri.openUri(link) }
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Toast.makeText(context, e.message, Toast.LENGTH_LONG).show()
                }
            }
        }
    }
    val u = state.update
    val name = u?.title?.ifEmpty { null } ?: title.ifEmpty { block }

    Scaffold(topBar = {
        TopAppBar(
            title = {
                Column {
                    Text(name, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(mesh.nodeName(node), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
        )
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).imePadding()) {
            BlockOrigin(block, state)
            HorizontalDivider()
            Box(Modifier.fillMaxSize()) { BlockBody(state, mesh.nodeName(node), form, events) }
        }
    }
}

/**
 * BlockOrigin is the frame's strip, the app's own: whose block it is, a
 * plugin by its ID, an agent by its name and its session's.
 */
@Composable
internal fun BlockOrigin(block: String, state: BlockState) {
    val u = state.update
    val agent = block.startsWith("agent:")
    val who = when {
        !agent -> u?.plugin?.ifEmpty { null } ?: block.removePrefix("plugin:").substringBefore('/')
        else -> dotted(u?.agent?.ifEmpty { null } ?: stringResource(R.string.block_agent),
            (u?.sessionId?.ifEmpty { null } ?: block.removePrefix("agent:").substringBefore('/')).take(8))
    }
    Surface(color = MaterialTheme.colorScheme.surfaceContainer) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(if (agent) Icons.Outlined.SmartToy else Icons.Outlined.Extension, null, Modifier.size(16.dp),
                tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Spacer(Modifier.width(8.dp))
            Text(stringResource(if (agent) R.string.block_agent else R.string.block_plugin), style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant)
            Spacer(Modifier.width(8.dp))
            Surface(shape = RoundedCornerShape(6.dp), color = MaterialTheme.colorScheme.surfaceVariant) {
                Text(who, Modifier.padding(horizontal = 6.dp, vertical = 2.dp), style = MaterialTheme.typography.labelSmall,
                    fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/** What goes in the frame: the tree, or how the block stands. */
@Composable
private fun BlockBody(state: BlockState, computer: String, form: BlockForm, events: BlockEvents) {
    val u = state.update
    when {
        u == null && state.broken.isNotEmpty() -> Note(stringResource(R.string.block_unreachable), state.broken)
        u == null -> CircularProgressIndicator(Modifier.padding(32.dp).size(32.dp))
        u.gone -> Note(stringResource(R.string.block_closed), "")
        u.state == PluginState.WAITING -> Note(stringResource(R.string.block_waiting, computer), "")
        u.state == PluginState.DENIED -> Note(stringResource(R.string.block_denied, computer), "")
        u.state == PluginState.DISABLED -> Note(stringResource(R.string.block_disabled, computer), "")
        u.state == PluginState.BLOCKED -> Note(stringResource(R.string.block_blocked, computer), u.error)
        else -> Column(Modifier.fillMaxSize()) {
            if (state.broken.isNotEmpty()) Band(stringResource(R.string.block_reconnecting), state.broken, "warning")
            if (u.error.isNotEmpty()) Band(
                stringResource(when {
                    u.stale -> R.string.block_stale
                    u.state == PluginState.FAILED -> R.string.block_failed
                    else -> R.string.block_call_failed
                }),
                u.error, if (u.stale) "warning" else "danger",
            )
            val tree = state.tree
            if (tree == null) {
                if (u.error.isEmpty()) CircularProgressIndicator(Modifier.padding(32.dp).size(32.dp))
            } else Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 12.dp)) {
                UiTree(tree, form, events)
            }
        }
    }
}

/** A band over the tree: why the block, or the stream, failed. */
@Composable
private fun Band(head: String, why: String, tone: String) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(head, style = MaterialTheme.typography.labelLarge, fontWeight = FontWeight.Medium, color = toneColor(tone))
        Text(why, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
            color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
    HorizontalDivider()
}

/** A note in the tree's place. */
@Composable
private fun Note(text: String, detail: String) {
    Column(Modifier.fillMaxWidth().padding(32.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Icon(Icons.Outlined.Extension, null, Modifier.size(28.dp), tint = MaterialTheme.colorScheme.outline)
        Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (detail.isNotEmpty()) Text(detail, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
            color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
