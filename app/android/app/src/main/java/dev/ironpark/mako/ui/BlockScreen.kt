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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.outlined.Dashboard
import androidx.compose.material.icons.outlined.Extension
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import dev.ironpark.mako.blocks.BlockScopeKind
import dev.ironpark.mako.blocks.BlockState
import dev.ironpark.mako.blocks.ScopeOption
import dev.ironpark.mako.blocks.followBlock
import dev.ironpark.mako.blocks.parseScopeKey
import dev.ironpark.mako.blocks.pinnedScopeKey
import dev.ironpark.mako.blocks.resolveInitialScope
import dev.ironpark.mako.blocks.scopeDisplayLabel
import dev.ironpark.mako.blocks.scopeKindOf
import dev.ironpark.mako.blocks.scopeOptions
import dev.ironpark.mako.mako
import dev.ironpark.mako.v2.BlockEvent
import dev.ironpark.mako.workspace.view
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

// A block of a computer's, a plugin's or an agent's, a page of its own.
// The app draws its frame, over the tree, naming the block and where it
// comes from, so that nothing the block draws passes for the app's own:
// the top bar holds the app's words alone, and the block's title, which
// is the block's to choose, goes in the frame's strip, by its origin;
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

/**
 * BlockScreen is node's block, followed while it shows; title is its name
 * until the block says. Coming back to the page, the tree last shown
 * stays, with why the link is down, until the stream gives it anew.
 *
 * Scoped plugin blocks ("space", "project", "session") pick an initial target
 * from [pinnedScope] or the active context on [node], show a scope switcher
 * chip in [BlockOrigin], and render a note instead of subscribing when no
 * target exists on [node].
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun BlockScreen(
    mesh: Mesh,
    node: String,
    block: String,
    title: String,
    pinnedScope: String = "",
    declaredScope: String = "",
    activeSpace: String = "",
    activeProject: String = "",
    activeSession: String = "",
    onBack: () -> Unit,
) {
    val context = LocalContext.current
    val shown by context.mako.foreground.collectAsState()
    val workspace by mesh.workspace.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val view = remember(workspace.state) { workspace.state.view() }
    val supportsScope = remember(nodes, node) { mesh.supportsPluginScope(node) }
    val isPluginBlock = block.startsWith("plugin:")

    var inferredScopeKind by remember(node, block) { mutableStateOf("") }
    val effectiveScopeKind = remember(isPluginBlock, supportsScope, declaredScope, pinnedScope, inferredScopeKind) {
        if (!isPluginBlock || !supportsScope) ""
        else scopeKindOf(declaredScope = declaredScope, pinnedScope = pinnedScope).ifEmpty { inferredScopeKind }
    }
    val validPinned = remember(pinnedScope, effectiveScopeKind) {
        pinnedScopeKey(pinnedScope, effectiveScopeKind)
    }
    val fallbackSpaceNames = view.spaces.indices.map { stringResource(R.string.space_n, it + 1) }
    val options = remember(effectiveScopeKind, node, view, sessions, fallbackSpaceNames) {
        scopeOptions(effectiveScopeKind, node, view, sessions) { idx ->
            fallbackSpaceNames.getOrElse(idx - 1) { "Space $idx" }
        }
    }
    val preferredId = when (effectiveScopeKind) {
        BlockScopeKind.SPACE -> activeSpace
        BlockScopeKind.PROJECT -> activeProject
        BlockScopeKind.SESSION -> activeSession
        else -> ""
    }
    var userScope by remember(node, block, effectiveScopeKind, validPinned) { mutableStateOf<String?>(null) }
    val activeScope = remember(effectiveScopeKind, validPinned, options, preferredId, userScope) {
        if (effectiveScopeKind.isEmpty()) ""
        else {
            val chosen = userScope
            if (chosen != null && (options.any { it.key == chosen } || chosen == validPinned)) chosen
            else resolveInitialScope(effectiveScopeKind, validPinned, options, preferredId)
        }
    }
    val needsScopeTarget = effectiveScopeKind.isNotEmpty() && activeScope.isEmpty()

    var state by remember(node, block, activeScope) { mutableStateOf(BlockState()) }
    val form = remember(node, block, activeScope) { BlockForm() }
    LaunchedEffect(node, block, activeScope, needsScopeTarget, shown) {
        if (!shown || needsScopeTarget) return@LaunchedEffect
        followBlock(mesh.daemon, node, block, scope = activeScope, from = state).collect {
            val uScope = it.update?.scope.orEmpty()
            if (uScope.isNotEmpty() && effectiveScopeKind.isEmpty()) {
                parseScopeKey(uScope)?.first?.let { k -> inferredScopeKind = k }
            }
            form.sync(it.tree)
            state = it
        }
    }
    val uri = LocalUriHandler.current
    val coroutineScope = rememberCoroutineScope()
    val tree by rememberUpdatedState(state.tree)
    val events = remember(node, block, activeScope) {
        BlockEvents { id, kind, value ->
            coroutineScope.launch {
                try {
                    val req = BlockEvent.newBuilder()
                        .setNodeId(node)
                        .setBlock(block)
                        .setNode(id)
                        .setKind(kind)
                        .setValue(value)
                        .putAllValues(form.values(tree))
                    if (activeScope.isNotEmpty()) req.setScope(activeScope)
                    val done = mesh.daemon.blockEvent(req.build())
                    // A link the plugin asked, in answer, to open: a web page alone.
                    val link = done.openUrl
                    if (link.startsWith("https://") || link.startsWith("http://")) runCatching { uri.openUri(link) }
                    if (done.toast.isNotEmpty()) Toast.makeText(context, done.toast, Toast.LENGTH_SHORT).show()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Toast.makeText(context, e.message, Toast.LENGTH_LONG).show()
                }
            }
        }
    }
    val name = state.update?.title?.ifEmpty { null } ?: title.ifEmpty { block }
    val scopeLabel = remember(activeScope, options, view, sessions) {
        if (activeScope.isEmpty()) "" else scopeDisplayLabel(activeScope, options, view, sessions)
    }

    Scaffold(topBar = {
        TopAppBar(
            title = {
                Column {
                    Text(stringResource(R.string.panel), maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(mesh.nodeName(node), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
        )
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).imePadding()) {
            BlockOrigin(
                block = block,
                name = name,
                state = state,
                scopeKind = effectiveScopeKind,
                activeScope = activeScope,
                scopeLabel = scopeLabel,
                scopeOptions = options,
                onSelectScope = { userScope = it },
            )
            HorizontalDivider()
            Box(Modifier.fillMaxSize()) {
                BlockBody(
                    state = state,
                    computer = mesh.nodeName(node),
                    form = form,
                    events = events,
                    missingScopeKind = if (needsScopeTarget) effectiveScopeKind else "",
                )
            }
        }
    }
}

/**
 * BlockOrigin is the frame's strip, the app's own: whose block it is, a
 * plugin by its ID, an agent by its name and its session's; then the
 * block's name, as the block gives it, after its origin; and for a scoped
 * plugin block, a compact chip to switch its active scope.
 */
@Composable
internal fun BlockOrigin(
    block: String,
    name: String,
    state: BlockState,
    scopeKind: String = "",
    activeScope: String = "",
    scopeLabel: String = "",
    scopeOptions: List<ScopeOption> = emptyList(),
    onSelectScope: (String) -> Unit = {},
) {
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
            Surface(Modifier.weight(1f, fill = false), shape = RoundedCornerShape(6.dp), color = MaterialTheme.colorScheme.surfaceVariant) {
                Text(who, Modifier.padding(horizontal = 6.dp, vertical = 2.dp), style = MaterialTheme.typography.labelSmall,
                    fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Spacer(Modifier.width(8.dp))
            Text(name, Modifier.weight(1f), style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (scopeKind.isNotEmpty() && (activeScope.isNotEmpty() || scopeOptions.isNotEmpty())) {
                Spacer(Modifier.width(8.dp))
                ScopeChip(scopeKind, activeScope, scopeLabel, scopeOptions, onSelectScope)
            }
        }
    }
}

/** Compact clickable scope chip with dropdown switcher for scoped plugin blocks. */
@Composable
private fun ScopeChip(
    scopeKind: String,
    activeScope: String,
    scopeLabel: String,
    options: List<ScopeOption>,
    onSelect: (String) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    val kindLabel = stringResource(
        when (scopeKind) {
            BlockScopeKind.SPACE -> R.string.block_scope_space
            BlockScopeKind.PROJECT -> R.string.block_scope_project
            else -> R.string.block_scope_session
        }
    )
    val icon = when (scopeKind) {
        BlockScopeKind.SPACE -> Icons.Outlined.Dashboard
        BlockScopeKind.PROJECT -> Icons.Outlined.Folder
        else -> Icons.Outlined.Terminal
    }
    Box {
        Surface(
            onClick = { if (options.isNotEmpty()) expanded = true },
            enabled = options.isNotEmpty(),
            shape = RoundedCornerShape(8.dp),
            color = MaterialTheme.colorScheme.secondaryContainer,
        ) {
            Row(
                Modifier.padding(start = 8.dp, end = if (options.isNotEmpty()) 4.dp else 8.dp, top = 4.dp, bottom = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(4.dp),
            ) {
                Icon(icon, kindLabel, Modifier.size(14.dp), tint = MaterialTheme.colorScheme.onSecondaryContainer)
                Text(
                    scopeLabel.ifEmpty { kindLabel },
                    Modifier.widthIn(max = 120.dp),
                    style = MaterialTheme.typography.labelSmall,
                    fontWeight = FontWeight.Medium,
                    color = MaterialTheme.colorScheme.onSecondaryContainer,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (options.isNotEmpty()) {
                    Icon(
                        Icons.Filled.ArrowDropDown,
                        stringResource(R.string.block_scope_switch, kindLabel),
                        Modifier.size(16.dp),
                        tint = MaterialTheme.colorScheme.onSecondaryContainer,
                    )
                }
            }
        }
        DropdownMenu(expanded = expanded, onDismissRequest = { expanded = false }) {
            for (opt in options) {
                val selected = opt.key == activeScope
                DropdownMenuItem(
                    text = {
                        Column {
                            Text(
                                opt.label,
                                fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                            if (opt.detail.isNotEmpty()) {
                                Text(
                                    opt.detail,
                                    style = MaterialTheme.typography.bodySmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                            }
                        }
                    },
                    trailingIcon = if (selected) ({ Icon(Icons.Filled.Check, null, Modifier.size(16.dp)) }) else null,
                    onClick = {
                        expanded = false
                        onSelect(opt.key)
                    },
                )
            }
        }
    }
}

/** What goes in the frame: the tree, or how the block stands. */
@Composable
private fun BlockBody(
    state: BlockState,
    computer: String,
    form: BlockForm,
    events: BlockEvents,
    missingScopeKind: String = "",
) {
    val u = state.update
    when {
        missingScopeKind == BlockScopeKind.SPACE -> Note(
            stringResource(R.string.block_scope_needs_space, computer),
            stringResource(R.string.block_scope_needs_space_detail),
        )
        missingScopeKind == BlockScopeKind.PROJECT -> Note(
            stringResource(R.string.block_scope_needs_project, computer),
            stringResource(R.string.block_scope_needs_project_detail),
        )
        missingScopeKind == BlockScopeKind.SESSION -> Note(
            stringResource(R.string.block_scope_needs_session, computer),
            stringResource(R.string.block_scope_needs_session_detail),
        )
        state.missing -> Note(stringResource(R.string.block_missing, computer), "")
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
