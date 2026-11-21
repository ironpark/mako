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
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.blocks.pinnedScopeKey
import dev.ironpark.mako.blocks.scopeKindOf
import dev.ironpark.mako.mako
import dev.ironpark.mako.v2.AgentBlock
import dev.ironpark.mako.workspace.layoutPages
import dev.ironpark.mako.workspace.windowLayout
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

/** How often a computer's panels are asked for while they show; the computer tells only its own windows. */
private const val PANELS_POLL = 3_000L

/** The kinds of panel the app lists, as a computer's list_panels says them; its builtin ones the app has screens of its own for. */
internal const val KIND_PLUGIN = "plugin"
internal const val KIND_AGENT = "agent"
internal const val KIND_MISSING = "missing"

/**
 * A panel the list offers: its block, its title, what it is (its plugin's
 * ID; an agent's session's, filled in by who lists it), its kind, for an
 * agent's block the session showing it, any pinned canonical scope key from
 * layout props ("space:<id>", "project:<id>", "session:<id>"), and the block's
 * declared/inferred scope kind ("", "space", "project", "session").
 */
internal data class Panel(
    val block: String,
    val title: String,
    val detail: String,
    val kind: String = KIND_PLUGIN,
    val session: String = "",
    val pinnedScope: String = "",
    val scopeKind: String = "",
)

/** The plugin's ID and the block's name of a plugin's block, plugin:<id>/<block>. */
private fun pluginParts(block: String): Pair<String, String> =
    block.removePrefix("plugin:").split('/', limit = 2).let { it[0] to it.getOrElse(1) { "" } }

/** The session of an agent's block, agent:<session>/<id>. */
internal fun agentSession(block: String): String = block.removePrefix("agent:").substringBefore('/')

/**
 * A title another machine gave, fit to show: without control or
 * reordering characters, trimmed, at most a line's worth; fallback when
 * nothing is left.
 */
internal fun panelTitle(title: String, fallback: String): String {
    val clean = title.filterNot { Character.isISOControl(it) || it in '\u200e'..'\u200f' || it in '\u202a'..'\u202e' || it in '\u2066'..'\u2069' }
        .trim().take(80)
    return clean.ifEmpty { fallback }
}

/**
 * The panels of node: the plugins' blocks space's window layout lays out
 * (its pages, docs/protocol.md, Layout documents), then the blocks its
 * agents show, which none lays out. listed is what the computer says of
 * its panels (list_panels, revision 35), which titles them and says which
 * plugins it lacks; null from an older computer, whose panels go by their
 * block's name, and whose agents' blocks are agents. A plugin the
 * computer lacks comes after those it has. The window's own panels, which
 * the app has screens of its own for, are left out.
 */
internal fun panels(
    pages: List<dev.ironpark.mako.workspace.LayoutPage>, agents: List<AgentBlock>, listed: List<dev.ironpark.mako.v2.Panel>? = null,
): List<Panel> {
    val said = listed.orEmpty().associateBy { it.block }
    val seen = mutableSetOf<String>()
    val plugins = mutableListOf<Panel>()
    // The space's layout orders the plugins'; without one, the computer's list does.
    val laid = pages.filter { it.block.startsWith("plugin:") }.map { it.block to it.scope }
    val order = laid.ifEmpty {
        listed.orEmpty().filter { it.kind == KIND_PLUGIN || it.kind == KIND_MISSING }.map { it.block to "" }
    }
    for ((b, propScope) in order) {
        val scopeKind = scopeKindOf(pinnedScope = propScope)
        val pinned = pinnedScopeKey(propScope, scopeKind)
        if (!seen.add(b + "\u0000" + pinned)) continue
        val (plugin, name) = pluginParts(b)
        val p = said[b]
        val kind = if (p?.kind == KIND_MISSING) KIND_MISSING else KIND_PLUGIN
        plugins += Panel(b, panelTitle(p?.title.orEmpty(), name), plugin, kind, pinnedScope = pinned, scopeKind = scopeKind)
    }
    val out = plugins.filter { it.kind != KIND_MISSING } + plugins.filter { it.kind == KIND_MISSING }
    val shown = if (listed == null) agents.map { a -> Panel(a.name, panelTitle(a.title, a.name.substringAfterLast('/')), dotted(a.agent, a.sessionId.take(8)), KIND_AGENT, a.sessionId) }
    else listed.filter { it.kind == KIND_AGENT }.map { p ->
        Panel(p.block, panelTitle(p.title, p.block.substringAfterLast('/')), agentSession(p.block).take(8), KIND_AGENT, agentSession(p.block))
    }
    return out + shown.filter { seen.add(it.block + "\u0000") }
}

/**
 * What the panels of a computer come from: its window's pages for the
 * space, its agents' blocks or, from revision 35, its own list of its
 * panels, null before, and why they could not be read.
 */
internal data class PanelSources(
    val pages: List<dev.ironpark.mako.workspace.LayoutPage>, val agents: List<AgentBlock>, val error: String,
    val listed: List<dev.ironpark.mako.v2.Panel>? = null,
) {
    /** The panels these make; see [panels]. */
    val panels: List<Panel> get() = panels(pages, agents, listed)
}

/**
 * The panels' sources of node, followed while the app shows: the pages of
 * the window layout of space, from the workspace shown when it is node's,
 * and what node says of its panels, or of its agents' blocks before
 * revision 35, asked for every poll milliseconds.
 */
@Composable
internal fun rememberPanelSources(mesh: Mesh, node: String?, space: String, poll: Long = PANELS_POLL): PanelSources {
    val shown by LocalContext.current.mako.foreground.collectAsState()
    val workspace by mesh.workspace.collectAsState()
    val supported = node != null && mesh.showsBlocks(node)
    val lists = node != null && mesh.listsPanels(node)
    var agents by remember(node) { mutableStateOf<List<AgentBlock>>(emptyList()) }
    var listed by remember(node) { mutableStateOf<List<dev.ironpark.mako.v2.Panel>?>(null) }
    var error by remember(node) { mutableStateOf("") }
    LaunchedEffect(node, shown, supported, lists) {
        while (shown && supported && node != null) {
            try {
                if (lists) listed = mesh.daemon.panels(node) else agents = mesh.daemon.agentBlocks(node)
                error = ""
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: e.toString()
            }
            delay(poll)
        }
    }
    val doc = if (supported && (workspace.node == node || workspace.node == null)) workspace.state.windowLayout(space) else null
    val pages = remember(doc) { layoutPages(doc) }
    return PanelSources(pages, agents, error, listed.takeIf { lists })
}

/**
 * PanelsScreen is the panels of the computer node, whose workspace the
 * app shows, as its window lays out the space shown: each a block of a
 * plugin's or an agent's, opening a page of its own.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun PanelsScreen(mesh: Mesh, node: String, space: String, onBack: () -> Unit, onOpen: (Panel) -> Unit) {
    val supported = mesh.showsBlocks(node)
    val sources = rememberPanelSources(mesh, node, space)
    val error = sources.error
    val list = sources.panels

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
                    items(list, key = { "${it.block}\u0000${it.pinnedScope}" }) { p -> PanelRow(p, mesh.nodeName(node)) { onOpen(p) } }
                }
            }
        }
    }
}

/**
 * PanelRow is a panel in a list: its kind's icon, its title, and what it
 * is. A plugin's panel its computer lacks is listed quietly, dimmed and
 * saying so; opened, its page says the same.
 */
@Composable
internal fun PanelRow(p: Panel, computer: String, onClick: () -> Unit) {
    val agent = p.kind == KIND_AGENT
    val missing = p.kind == KIND_MISSING
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(16.dp, 12.dp).alpha(if (missing) 0.55f else 1f),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(if (agent) Icons.Outlined.SmartToy else Icons.Outlined.Extension, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.width(16.dp))
        Column(Modifier.weight(1f)) {
            Text(p.title, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(
                if (missing) stringResource(R.string.panel_missing, computer)
                else dotted(stringResource(if (agent) R.string.block_agent else R.string.block_plugin), p.detail),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.outline)
    }
}
