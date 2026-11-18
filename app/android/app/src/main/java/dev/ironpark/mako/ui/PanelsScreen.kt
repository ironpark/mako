package dev.ironpark.mako.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.outlined.Dashboard
import androidx.compose.material.icons.outlined.Extension
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.mako
import dev.ironpark.mako.v2.AgentBlock
import dev.ironpark.mako.workspace.layoutPages
import dev.ironpark.mako.workspace.windowLayout
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

/** How often a computer's agents' blocks are asked for while the panels show; the computer tells only its own windows. */
private const val PANELS_POLL = 3_000L

/** A panel the list offers: a block, its title, and what it is. */
internal data class Panel(val block: String, val title: String, val detail: String)

/**
 * The panels of node, as space's window layout lays them out (its pages,
 * docs/protocol.md, Layout documents), those the app draws, plugins'
 * blocks; then the blocks its agents show, which none lays out.
 */
internal fun panels(pages: List<dev.ironpark.mako.workspace.LayoutPage>, agents: List<AgentBlock>): List<Panel> {
    val out = mutableListOf<Panel>()
    val seen = mutableSetOf<String>()
    for (p in pages) {
        if (!p.block.startsWith("plugin:") || !seen.add(p.block)) continue
        val (plugin, block) = p.block.removePrefix("plugin:").split('/', limit = 2).let { it[0] to it.getOrElse(1) { "" } }
        out += Panel(p.block, block, plugin)
    }
    for (a in agents) {
        if (!seen.add(a.name)) continue
        out += Panel(a.name, a.title.ifEmpty { a.name.substringAfterLast('/') }, dotted(a.agent, a.sessionId.take(8)))
    }
    return out
}

/**
 * PanelsScreen is the panels of the computer node, whose workspace the
 * app shows, as its window lays out the space shown: each a block of a
 * plugin's or an agent's, opening a page of its own.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun PanelsScreen(mesh: Mesh, node: String, space: String, onBack: () -> Unit, onOpen: (Panel) -> Unit) {
    val shown by LocalContext.current.mako.foreground.collectAsState()
    val workspace by mesh.workspace.collectAsState()
    val supported = mesh.showsBlocks(node)
    var agents by remember(node) { mutableStateOf<List<AgentBlock>>(emptyList()) }
    var error by remember(node) { mutableStateOf("") }
    LaunchedEffect(node, shown, supported) {
        while (shown && supported) {
            try {
                agents = mesh.daemon.agentBlocks(node)
                error = ""
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: e.toString()
            }
            delay(PANELS_POLL)
        }
    }
    val doc = if (workspace.node == node || workspace.node == null) workspace.state.windowLayout(space) else null
    val pages = remember(doc) { layoutPages(doc) }
    val list = panels(pages, agents)

    Scaffold(topBar = {
        TopAppBar(
            title = {
                Column {
                    Text(stringResource(R.string.panels), maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(mesh.nodeName(node), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
        )
    }) { padding ->
        Box(Modifier.fillMaxSize().padding(padding)) {
            when {
                !supported -> Message(stringResource(R.string.panels), stringResource(R.string.panels_unsupported, mesh.nodeName(node)),
                    icon = Icons.Outlined.Dashboard)
                list.isEmpty() -> Message(stringResource(R.string.panels), error.ifEmpty { stringResource(R.string.panels_none, mesh.nodeName(node)) },
                    icon = Icons.Outlined.Dashboard)
                else -> LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(vertical = 8.dp)) {
                    if (error.isNotEmpty()) item {
                        Text(error, Modifier.padding(16.dp, 8.dp), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                    }
                    items(list, key = { it.block }) { p -> PanelRow(p) { onOpen(p) } }
                }
            }
        }
    }
}

@Composable
private fun PanelRow(p: Panel, onClick: () -> Unit) {
    val agent = p.block.startsWith("agent:")
    Row(Modifier.fillMaxWidth().clickable(onClick = onClick).padding(16.dp, 12.dp), verticalAlignment = Alignment.CenterVertically) {
        Icon(if (agent) Icons.Outlined.SmartToy else Icons.Outlined.Extension, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.width(16.dp))
        Column(Modifier.weight(1f)) {
            Text(p.title, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(dotted(stringResource(if (agent) R.string.block_agent else R.string.block_plugin), p.detail),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.outline)
    }
}
