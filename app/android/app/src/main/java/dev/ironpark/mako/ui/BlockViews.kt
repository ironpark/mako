package dev.ironpark.mako.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.ArrowBack
import androidx.compose.material.icons.automirrored.outlined.ArrowForward
import androidx.compose.material.icons.automirrored.outlined.FormatListBulleted
import androidx.compose.material.icons.automirrored.outlined.KeyboardArrowRight
import androidx.compose.material.icons.automirrored.outlined.Label
import androidx.compose.material.icons.automirrored.outlined.MenuBook
import androidx.compose.material.icons.automirrored.outlined.OpenInNew
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.outlined.AccountTree
import androidx.compose.material.icons.outlined.Add
import androidx.compose.material.icons.outlined.AutoAwesome
import androidx.compose.material.icons.outlined.Check
import androidx.compose.material.icons.outlined.CheckCircle
import androidx.compose.material.icons.outlined.Close
import androidx.compose.material.icons.outlined.Code
import androidx.compose.material.icons.outlined.CreateNewFolder
import androidx.compose.material.icons.outlined.CropSquare
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.ErrorOutline
import androidx.compose.material.icons.outlined.ExpandMore
import androidx.compose.material.icons.outlined.FilterList
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.FolderOpen
import androidx.compose.material.icons.outlined.GridView
import androidx.compose.material.icons.outlined.Home
import androidx.compose.material.icons.outlined.Hub
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material.icons.outlined.Layers
import androidx.compose.material.icons.outlined.Link
import androidx.compose.material.icons.outlined.PlayArrow
import androidx.compose.material.icons.outlined.Refresh
import androidx.compose.material.icons.outlined.Schedule
import androidx.compose.material.icons.outlined.Search
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material.icons.outlined.Star
import androidx.compose.material.icons.outlined.Stop
import androidx.compose.material.icons.outlined.WarningAmber
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.VerticalDivider
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.blocks.BlockForm
import dev.ironpark.mako.v2.UiNode
import kotlin.math.max

// A block's tree, a plugin's or an agent's, as the computer's daemon
// checked it, drawn with the app's own components (docs/protocol.md,
// Plugin blocks): the vocabulary is fixed, a colour is a token of the
// app's theme, never a value, and an icon the app does not have is a
// plain square. What a node sends is the daemon's to know: the app says
// only which node the user acted on, and how, with the block's fields.

/** BlockEvents is where a tree's events go: the node's id, the kind, and the value. */
fun interface BlockEvents {
    fun send(node: String, kind: String, value: String)
}

/** A tone's colour, a token of the theme; "" is the text's own. */
@Composable
internal fun toneColor(tone: String): Color = when (tone) {
    "muted" -> MaterialTheme.colorScheme.onSurfaceVariant
    "faint" -> MaterialTheme.colorScheme.outline
    "accent" -> MaterialTheme.colorScheme.primary
    "success" -> StatusColors.Done
    "warning" -> StatusColors.Waiting
    "danger" -> MaterialTheme.colorScheme.error
    else -> MaterialTheme.colorScheme.onSurface
}

@Composable
private fun textStyle(size: String): TextStyle = when (size) {
    "small" -> MaterialTheme.typography.bodySmall
    "large" -> MaterialTheme.typography.titleMedium
    else -> MaterialTheme.typography.bodyMedium
}

private fun weight(w: String): FontWeight? = when (w) {
    "medium" -> FontWeight.Medium
    "semibold" -> FontWeight.SemiBold
    else -> null
}

/** The icons a block may name, of the desktop's set, that the app has one like. */
private val blockIcons: Map<String, ImageVector> = mapOf(
    "check" to Icons.Outlined.Check, "circle-check" to Icons.Outlined.CheckCircle, "x" to Icons.Outlined.Close,
    "plus" to Icons.Outlined.Add, "play" to Icons.Outlined.PlayArrow, "square" to Icons.Outlined.CropSquare,
    "stop-circle" to Icons.Outlined.Stop, "refresh-cw" to Icons.Outlined.Refresh, "rotate-cw" to Icons.Outlined.Refresh,
    "trash" to Icons.Outlined.Delete, "trash-2" to Icons.Outlined.Delete, "pencil" to Icons.Outlined.Edit,
    "search" to Icons.Outlined.Search, "settings" to Icons.Outlined.Settings, "star" to Icons.Outlined.Star,
    "info" to Icons.Outlined.Info, "circle-alert" to Icons.Outlined.ErrorOutline,
    "triangle-alert" to Icons.Outlined.WarningAmber, "shield-alert" to Icons.Outlined.WarningAmber,
    "folder" to Icons.Outlined.Folder, "folder-open" to Icons.Outlined.FolderOpen, "folder-plus" to Icons.Outlined.CreateNewFolder,
    "code" to Icons.Outlined.Code, "github" to Icons.Outlined.Code,
    "git-branch" to Icons.Outlined.AccountTree, "clock" to Icons.Outlined.Schedule,
    "external-link" to Icons.AutoMirrored.Outlined.OpenInNew,
    "file-text" to Icons.Outlined.Description, "file" to Icons.Outlined.Description,
    "arrow-left" to Icons.AutoMirrored.Outlined.ArrowBack,
    "arrow-right" to Icons.AutoMirrored.Outlined.ArrowForward,
    "chevron-right" to Icons.AutoMirrored.Outlined.KeyboardArrowRight,
    "chevron-down" to Icons.Outlined.ExpandMore,
    "network" to Icons.Outlined.Hub,
    "tag" to Icons.AutoMirrored.Outlined.Label,
    "tags" to Icons.AutoMirrored.Outlined.Label,
    "book-open" to Icons.AutoMirrored.Outlined.MenuBook,
    "link" to Icons.Outlined.Link,
    "filter" to Icons.Outlined.FilterList,
    "layers" to Icons.Outlined.Layers,
    "sparkles" to Icons.Outlined.AutoAwesome,
    "home" to Icons.Outlined.Home,
    "list" to Icons.AutoMirrored.Outlined.FormatListBulleted,
    "grid" to Icons.Outlined.GridView,
    "layout-grid" to Icons.Outlined.GridView,
)

/** The icon of the name, a plain square for one the app does not have. */
internal fun blockIcon(name: String): ImageVector = blockIcons[name] ?: Icons.Outlined.CropSquare

/** Computes responsive grid column count clamped to 1..6 and adapted to available width. */
internal fun responsiveGridColumns(requested: Int, availableWidthDp: Float): Int {
    val clamped = requested.coerceIn(1, 6)
    if (availableWidthDp <= 0f) return clamped
    val maxFit = max(1, (availableWidthDp / 120f).toInt())
    return minOf(clamped, maxFit)
}

/** Resolves a button's effective visual variant from explicit variant and legacy tone. */
internal fun resolveButtonVariant(variant: String, tone: String): String = when {
    variant.isNotEmpty() -> variant
    tone == "danger" -> "danger"
    tone == "accent" -> "primary"
    else -> "outline"
}

/** Routes a markdown link tap: http(s) opens externally, internal links fire UiChange when enabled. */
internal fun handleMarkdownLink(
    link: String,
    allowPluginLink: Boolean,
    openExternal: (String) -> Unit,
    sendPluginLink: (String) -> Unit,
) {
    if (link.startsWith("https://", ignoreCase = true) || link.startsWith("http://", ignoreCase = true)) {
        openExternal(link)
    } else if (allowPluginLink && link.isNotEmpty()) {
        sendPluginLink(link)
    }
}

private data class DragPayload(val value: String, val group: String)

private class BlockDragState {
    var active by mutableStateOf<DragPayload?>(null)

    fun accepts(n: UiNode): Boolean {
        val cur = active ?: return false
        if (!n.drop) return false
        if (n.dropAcceptCount == 0) return true
        return n.dropAcceptList.any { it == cur.group }
    }
}

private val LocalBlockDrag = compositionLocalOf<BlockDragState?> { null }

/** Whether the current subtree has a finite vertical height constraint (e.g. root `fill=true` or split pane). */
private val LocalBoundedHeight = compositionLocalOf { false }

/** UiTree draws a block's tree; form holds what the user set in its fields. */
@Composable
internal fun UiTree(n: UiNode, form: BlockForm, events: BlockEvents, inLine: Boolean = false, modifier: Modifier = Modifier) {
    if (LocalBlockDrag.current == null) {
        val drag = remember { BlockDragState() }
        CompositionLocalProvider(
            LocalBlockDrag provides drag,
            LocalBoundedHeight provides n.fill,
        ) {
            Column(modifier, verticalArrangement = Arrangement.spacedBy(6.dp)) {
                if (drag.active != null) {
                    Surface(
                        Modifier.fillMaxWidth(),
                        shape = RoundedCornerShape(8.dp),
                        color = MaterialTheme.colorScheme.primaryContainer,
                    ) {
                        Row(
                            Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 4.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text(
                                stringResource(R.string.block_dragging),
                                Modifier.weight(1f),
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onPrimaryContainer,
                            )
                            TextButton(onClick = { drag.active = null }) {
                                Text(stringResource(R.string.cancel))
                            }
                        }
                    }
                }
                val rootMod = if (n.fill) Modifier.weight(1f).fillMaxWidth() else Modifier
                UiNodeView(n, form, events, inLine, rootMod)
            }
        }
        return
    }
    UiNodeView(n, form, events, inLine, modifier)
}

@OptIn(ExperimentalFoundationApi::class)
private fun interactive(m: Modifier, n: UiNode, tap: (() -> Unit)?, drag: BlockDragState?, events: BlockEvents): Modifier {
    val canDrop = drag != null && drag.accepts(n)
    if (canDrop) {
        return m.clickable {
            val payload = drag.active?.value.orEmpty()
            drag.active = null
            if (payload.isNotEmpty()) events.send(n.id, "drop", payload)
        }
    }
    if (n.drag.isNotEmpty() && drag != null) {
        return m.combinedClickable(
            onClick = { tap?.invoke() },
            onLongClick = { drag.active = DragPayload(n.drag, n.dragGroup) },
        )
    }
    return if (tap == null) m else m.clickable(onClick = tap)
}

@Composable
private fun UiNodeView(n: UiNode, form: BlockForm, events: BlockEvents, inLine: Boolean, modifier: Modifier) {
    val drag = LocalBlockDrag.current
    val tap: (() -> Unit)? = if (n.tap) ({ events.send(n.id, "tap", "") }) else null
    val mod = interactive(modifier, n, tap, drag, events)
    when (n.kind) {
        "column", "row", "stack", "list", "card", "scroll" -> UiContainer(n, form, events, modifier)
        "wrap" -> UiWrap(n, form, events, modifier)
        "grid" -> UiGrid(n, form, events, modifier)
        "split" -> UiSplit(n, form, events, modifier)
        "collapsible" -> UiCollapsible(n, form, events, modifier)
        "breadcrumb" -> UiBreadcrumb(n, events, modifier)
        "tree" -> UiTreeContainer(n, form, events, modifier)
        "tree_item" -> UiTreeItem(n, form, events, depth = 0, modifier = modifier)
        "table" -> UiTable(n, form, events, modifier)
        "text" -> Text(
            n.text, mod, color = toneColor(n.tone), style = textStyle(n.size), fontWeight = weight(n.weight),
            maxLines = if (inLine) 1 else Int.MAX_VALUE, overflow = if (inLine) TextOverflow.Ellipsis else TextOverflow.Clip,
        )
        // A tappable icon is named, by its text or else its icon's name, for
        // a screen reader to say what is tapped.
        "icon" -> Icon(blockIcon(n.icon), if (tap == null) null else n.text.ifEmpty { n.icon },
            mod.size(if (n.size == "large") 22.dp else if (n.size == "small") 14.dp else 18.dp),
            tint = toneColor(n.tone.ifEmpty { "muted" }))
        "pill" -> Pill(n.text, toneColor(n.tone.ifEmpty { "muted" }), mod)
        "chip" -> Surface(
            mod, shape = RoundedCornerShape(6.dp), color = MaterialTheme.colorScheme.surfaceVariant,
        ) {
            Text(n.text, Modifier.padding(horizontal = 6.dp, vertical = 2.dp), style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        "button" -> UiButton(n, modifier, tap)
        "input" -> UiInput(n, form, events, modifier)
        "textarea" -> UiTextarea(n, form, events, modifier)
        // The row is the switch, its label the switch's, as a screen reader says it.
        "toggle" -> Row(
            modifier.fillMaxWidth().toggleable(
                value = form.value(n) == "true", enabled = !n.disabled, role = Role.Switch,
                onValueChange = { on ->
                    form.set(n.id, on.toString())
                    if (n.change) events.send(n.id, "change", on.toString())
                },
            ),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(n.text, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
            Switch(checked = form.value(n) == "true", onCheckedChange = null, enabled = !n.disabled)
        }
        "select" -> UiSelect(n, form, events, modifier)
        "tabs" -> UiTabs(n, form, events, modifier)
        "markdown" -> BlockMarkdown(n, events, mod)
        "code" -> Surface(mod.fillMaxWidth(), color = MaterialTheme.colorScheme.surfaceContainerHigh, shape = RoundedCornerShape(8.dp)) {
            Text(n.text, Modifier.horizontalScroll(rememberScrollState()).padding(10.dp),
                style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace, softWrap = false)
        }
        "progress" -> LinearProgressIndicator(progress = { n.progress.toFloat() }, modifier = modifier.fillMaxWidth().padding(vertical = 4.dp))
        "divider" -> HorizontalDivider(modifier.padding(vertical = 2.dp))
        "chart" -> UiChart(n, modifier)
        "graph" -> UiGraph(n, events, modifier)
        // A kind newer than the app: said quietly, rather than left a hole.
        else -> UnknownNode(modifier)
    }
}

/**
 * A column, row, stack, list, card or scroll; in a row, a child that grows
 * takes its share of the width left. Each child is drawn under its id, so
 * that what it keeps, an input's focus and cursor, a select's menu, follows
 * the node as siblings come, go and move, not its place.
 */
@Composable
private fun UiContainer(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val drag = LocalBlockDrag.current
    val bounded = LocalBoundedHeight.current
    val tap: (() -> Unit)? = if (n.tap) ({ events.send(n.id, "tap", "") }) else null
    val box = interactive(modifier, n, tap, drag, events)
    val canDrop = drag != null && drag.accepts(n)
    val picked = drag?.active != null && n.drag.isNotEmpty() && drag.active?.value == n.drag
    val content: @Composable (Modifier) -> Unit = { m ->
        when (n.kind) {
            "row" -> {
                val hasColumn = n.childrenList.any {
                    it.kind in setOf("column", "list", "card", "scroll", "grid", "split", "tree", "collapsible")
                }
                Row(
                    m.fillMaxWidth().then(if (n.tap) Modifier.padding(vertical = 6.dp) else Modifier),
                    horizontalArrangement = Arrangement.spacedBy((if (n.gap > 0) n.gap else 8.0).dp),
                    verticalAlignment = if (hasColumn) Alignment.Top else Alignment.CenterVertically,
                ) { n.childrenList.forEach { c -> key(c.id) { RowChild(c, form, events) } } }
            }
            "stack" -> Box(m) { n.childrenList.forEach { c -> key(c.id) { UiNodeView(c, form, events, false, Modifier) } } }
            "scroll" -> {
                val gap = (if (n.gap > 0) n.gap else 6.0).dp
                if (n.direction == "horizontal") {
                    Row(
                        m.fillMaxWidth().horizontalScroll(rememberScrollState()),
                        horizontalArrangement = Arrangement.spacedBy(gap),
                        verticalAlignment = Alignment.Top,
                    ) { n.childrenList.forEach { c -> key(c.id) { UiNodeView(c, form, events, false, Modifier) } } }
                } else {
                    val scrollMod = when {
                        n.maxHeight > 0 -> m.fillMaxWidth().heightIn(max = n.maxHeight.dp).verticalScroll(rememberScrollState())
                        bounded -> m.fillMaxSize().verticalScroll(rememberScrollState())
                        else -> m.fillMaxWidth().heightIn(max = 320.dp).verticalScroll(rememberScrollState())
                    }
                    CompositionLocalProvider(LocalBoundedHeight provides false) {
                        Column(
                            scrollMod,
                            verticalArrangement = Arrangement.spacedBy(gap),
                        ) { n.childrenList.forEach { c -> key(c.id) { UiNodeView(c, form, events, false, Modifier) } } }
                    }
                }
            }
            else -> Column(
                m.fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy((if (n.gap > 0) n.gap else if (n.kind == "list") 0.0 else 6.0).dp),
            ) {
                n.childrenList.forEach { c ->
                    key(c.id) {
                        val canGrow = bounded && n.kind == "column" && c.grow > 0
                        val childMod = if (canGrow) Modifier.weight(c.grow.toFloat()).fillMaxWidth() else Modifier
                        CompositionLocalProvider(LocalBoundedHeight provides canGrow) {
                            if (n.kind == "list" && c.tap) {
                                // The list's row answers the tap, as the desktop's rows do.
                                Box(Modifier.fillMaxWidth().clickable { events.send(c.id, "tap", "") }.padding(vertical = 6.dp)) {
                                    UiNodeView(c.toBuilder().setTap(false).build(), form, events, false, Modifier)
                                }
                            } else UiNodeView(c, form, events, false, childMod)
                        }
                    }
                }
            }
        }
    }
    if (n.kind == "card" || n.drag.isNotEmpty() || n.drop || canDrop || picked) {
        val border: BorderStroke? = when {
            canDrop || picked -> BorderStroke(1.5.dp, MaterialTheme.colorScheme.primary)
            n.kind == "card" && n.variant == "ghost" -> null
            n.kind == "card" && n.variant == "primary" -> BorderStroke(1.dp, MaterialTheme.colorScheme.primary.copy(alpha = 0.5f))
            n.kind == "card" && n.variant == "danger" -> BorderStroke(1.dp, MaterialTheme.colorScheme.error.copy(alpha = 0.5f))
            else -> MakoColors.cardBorderStroke
        }
        val bg = when {
            canDrop -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.28f)
            picked -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.16f)
            n.kind == "card" && (n.variant == "ghost" || n.variant == "outline") -> Color.Transparent
            n.kind == "card" && n.variant == "secondary" -> MaterialTheme.colorScheme.surfaceContainerHigh
            n.kind == "card" && n.variant == "primary" -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.35f)
            n.kind == "card" && n.variant == "danger" -> MaterialTheme.colorScheme.errorContainer.copy(alpha = 0.35f)
            else -> MaterialTheme.colorScheme.surfaceContainerLow
        }
        Surface(
            modifier = box,
            shape = RoundedCornerShape(10.dp),
            color = bg,
            border = border,
        ) {
            content(Modifier.padding(horizontal = 10.dp, vertical = 8.dp))
        }
    } else {
        content(box)
    }
}

/** FlowRow container for `kind == "wrap"`. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun UiWrap(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val drag = LocalBlockDrag.current
    val tap: (() -> Unit)? = if (n.tap) ({ events.send(n.id, "tap", "") }) else null
    val box = interactive(modifier, n, tap, drag, events)
    val gap = (if (n.gap > 0) n.gap else 6.0).dp
    FlowRow(
        box.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(gap),
        verticalArrangement = Arrangement.spacedBy(gap),
    ) {
        n.childrenList.forEach { c ->
            key(c.id) {
                CompositionLocalProvider(LocalBoundedHeight provides false) {
                    UiNodeView(c, form, events, false, Modifier)
                }
            }
        }
    }
}

/** Responsive N-column grid (1..6 columns) for `kind == "grid"`. */
@Composable
private fun UiGrid(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val drag = LocalBlockDrag.current
    val tap: (() -> Unit)? = if (n.tap) ({ events.send(n.id, "tap", "") }) else null
    val box = interactive(modifier, n, tap, drag, events)
    val gap = (if (n.gap > 0) n.gap else 8.0).dp
    BoxWithConstraints(box.fillMaxWidth()) {
        val cols = responsiveGridColumns(n.columns, maxWidth.value)
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(gap)) {
            n.childrenList.chunked(cols).forEach { rowItems ->
                Row(
                    Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(gap),
                    verticalAlignment = Alignment.Top,
                ) {
                    rowItems.forEach { c ->
                        Box(Modifier.weight(1f)) {
                            key(c.id) {
                                CompositionLocalProvider(LocalBoundedHeight provides false) {
                                    UiNodeView(c, form, events, false, Modifier.fillMaxWidth())
                                }
                            }
                        }
                    }
                    repeat(cols - rowItems.size) {
                        Spacer(Modifier.weight(1f))
                    }
                }
            }
        }
    }
}

/** 2-pane split container (horizontal or vertical) with ratio weight for `kind == "split"`. */
@Composable
private fun UiSplit(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val bounded = LocalBoundedHeight.current
    val count = n.childrenCount
    if (count == 0) return
    if (count == 1) {
        val only = n.getChildren(0)
        key(only.id) { UiNodeView(only, form, events, false, modifier) }
        return
    }
    val first = n.getChildren(0)
    val second = n.getChildren(1)
    val ratio = (if (n.ratio in 0.1..0.9) n.ratio else 0.5).toFloat()
    val gap = (if (n.gap > 0) n.gap else 8.0).dp
    val vertical = n.direction == "vertical"

    if (vertical) {
        if (bounded) {
            Column(modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(gap)) {
                Box(Modifier.fillMaxWidth().weight(ratio)) {
                    key(first.id) {
                        CompositionLocalProvider(LocalBoundedHeight provides true) {
                            UiNodeView(first, form, events, false, Modifier.fillMaxSize())
                        }
                    }
                }
                HorizontalDivider(color = MakoColors.subtleDivider)
                Box(Modifier.fillMaxWidth().weight(1f - ratio)) {
                    key(second.id) {
                        CompositionLocalProvider(LocalBoundedHeight provides true) {
                            UiNodeView(second, form, events, false, Modifier.fillMaxSize())
                        }
                    }
                }
            }
        } else {
            Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(gap)) {
                key(first.id) { UiNodeView(first, form, events, false, Modifier.fillMaxWidth()) }
                HorizontalDivider(color = MakoColors.subtleDivider)
                key(second.id) { UiNodeView(second, form, events, false, Modifier.fillMaxWidth()) }
            }
        }
    } else {
        val rowMod = if (bounded) modifier.fillMaxSize() else modifier.fillMaxWidth()
        Row(
            rowMod,
            horizontalArrangement = Arrangement.spacedBy(gap),
            verticalAlignment = Alignment.Top,
        ) {
            Box(if (bounded) Modifier.weight(ratio).fillMaxHeight() else Modifier.weight(ratio)) {
                key(first.id) {
                    CompositionLocalProvider(LocalBoundedHeight provides bounded) {
                        UiNodeView(first, form, events, false, if (bounded) Modifier.fillMaxSize() else Modifier.fillMaxWidth())
                    }
                }
            }
            if (bounded) {
                VerticalDivider(Modifier.fillMaxHeight(), color = MakoColors.subtleDivider)
            }
            Box(if (bounded) Modifier.weight(1f - ratio).fillMaxHeight() else Modifier.weight(1f - ratio)) {
                key(second.id) {
                    CompositionLocalProvider(LocalBoundedHeight provides bounded) {
                        UiNodeView(second, form, events, false, if (bounded) Modifier.fillMaxSize() else Modifier.fillMaxWidth())
                    }
                }
            }
        }
    }
}

/** Expandable disclosure card/section for `kind == "collapsible"`. */
@Composable
private fun UiCollapsible(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val drag = LocalBlockDrag.current
    val canDrop = drag != null && drag.accepts(n)
    val picked = drag?.active != null && n.drag.isNotEmpty() && drag.active?.value == n.drag
    val expanded = form.isExpanded(n)
    val stateLabel = stringResource(if (expanded) R.string.expanded else R.string.collapsed)
    val border = if (canDrop || picked) BorderStroke(1.5.dp, MaterialTheme.colorScheme.primary) else MakoColors.cardBorderStroke
    val bg = when {
        canDrop -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.28f)
        picked -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.16f)
        else -> MaterialTheme.colorScheme.surfaceContainerLow
    }
    val boxMod = interactive(modifier, n, tap = null, drag = drag, events = events)
    Surface(
        modifier = boxMod.fillMaxWidth(),
        shape = RoundedCornerShape(10.dp),
        color = bg,
        border = border,
    ) {
        Column(Modifier.fillMaxWidth()) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .clickable(enabled = !n.disabled) {
                        val next = !expanded
                        form.setExpanded(n.id, next)
                        if (n.change) events.send(n.id, "change", next.toString())
                    }
                    .semantics { stateDescription = stateLabel }
                    .padding(horizontal = 10.dp, vertical = 9.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                Icon(
                    if (expanded) Icons.Outlined.ExpandMore else Icons.AutoMirrored.Outlined.KeyboardArrowRight,
                    contentDescription = null,
                    modifier = Modifier.size(18.dp),
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                if (n.icon.isNotEmpty()) {
                    Icon(
                        blockIcon(n.icon),
                        contentDescription = null,
                        modifier = Modifier.size(16.dp),
                        tint = toneColor(n.tone.ifEmpty { "muted" }),
                    )
                }
                Text(
                    n.text,
                    Modifier.weight(1f),
                    style = MaterialTheme.typography.titleSmall,
                    color = toneColor(n.tone),
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            if (expanded && n.childrenCount > 0) {
                HorizontalDivider(color = MakoColors.subtleDivider)
                Column(
                    Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 8.dp),
                    verticalArrangement = Arrangement.spacedBy((if (n.gap > 0) n.gap else 6.0).dp),
                ) {
                    n.childrenList.forEach { c ->
                        key(c.id) {
                            CompositionLocalProvider(LocalBoundedHeight provides false) {
                                UiNodeView(c, form, events, false, Modifier)
                            }
                        }
                    }
                }
            }
        }
    }
}

/** Horizontal clickable path segments for `kind == "breadcrumb"`. */
@Composable
private fun UiBreadcrumb(n: UiNode, events: BlockEvents, modifier: Modifier) {
    Row(
        modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        n.optionsList.forEachIndexed { index, segment ->
            if (index > 0) {
                Icon(
                    Icons.AutoMirrored.Outlined.KeyboardArrowRight,
                    contentDescription = null,
                    modifier = Modifier.size(16.dp),
                    tint = MaterialTheme.colorScheme.outline,
                )
            }
            val isLast = index == n.optionsCount - 1
            val clickable = !n.disabled && n.change
            Box(
                Modifier
                    .clip(RoundedCornerShape(6.dp))
                    .then(
                        if (clickable) {
                            Modifier.clickable { events.send(n.id, "change", index.toString()) }
                        } else {
                            Modifier
                        },
                    )
                    .padding(horizontal = 6.dp, vertical = 4.dp),
            ) {
                Text(
                    segment,
                    style = textStyle(n.size),
                    fontWeight = if (isLast) FontWeight.SemiBold else FontWeight.Medium,
                    color = when {
                        n.tone.isNotEmpty() -> toneColor(n.tone)
                        isLast -> MaterialTheme.colorScheme.onSurface
                        else -> MaterialTheme.colorScheme.onSurfaceVariant
                    },
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}

/** Hierarchical tree container for `kind == "tree"`. */
@Composable
private fun UiTreeContainer(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val gap = (if (n.gap > 0) n.gap else 2.0).dp
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(gap)) {
        n.childrenList.forEach { item ->
            key(item.id) {
                UiTreeItem(item, form, events, depth = 0, modifier = Modifier)
            }
        }
    }
}

/** Recursive hierarchical tree row for `kind == "tree_item"`. */
@Composable
private fun UiTreeItem(n: UiNode, form: BlockForm, events: BlockEvents, depth: Int, modifier: Modifier) {
    val drag = LocalBlockDrag.current
    val hasChildren = n.childrenCount > 0
    val expandable = hasChildren || n.change
    val expanded = if (expandable) form.isExpanded(n) else false
    val selected = !expandable && n.checked
    val canDrop = drag != null && drag.accepts(n)
    val picked = drag?.active != null && n.drag.isNotEmpty() && drag.active?.value == n.drag

    val toggleExpand = {
        val next = !expanded
        form.setExpanded(n.id, next)
        if (!n.disabled && n.change) events.send(n.id, "change", next.toString())
    }
    val rowTap: (() -> Unit)? = when {
        n.disabled -> null
        n.tap -> ({ events.send(n.id, "tap", "") })
        expandable -> toggleExpand
        else -> null
    }

    val rowBg = when {
        canDrop -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.28f)
        picked -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.16f)
        selected -> MaterialTheme.colorScheme.secondaryContainer
        else -> Color.Transparent
    }
    val rowBorder = if (canDrop || picked) BorderStroke(1.5.dp, MaterialTheme.colorScheme.primary) else null
    val indentDepth = depth.coerceAtMost(8)

    Column(modifier.fillMaxWidth()) {
        Surface(
            modifier = interactive(Modifier.fillMaxWidth(), n, rowTap, drag, events),
            shape = RoundedCornerShape(8.dp),
            color = rowBg,
            border = rowBorder,
        ) {
            Row(
                Modifier.fillMaxWidth().padding(horizontal = 6.dp, vertical = 6.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                if (indentDepth > 0) {
                    Row(horizontalArrangement = Arrangement.spacedBy(0.dp), verticalAlignment = Alignment.CenterVertically) {
                        repeat(indentDepth) {
                            Box(Modifier.width(16.dp).height(20.dp), contentAlignment = Alignment.Center) {
                                Box(Modifier.width(1.dp).fillMaxHeight().background(MakoColors.subtleDivider))
                            }
                        }
                    }
                }
                if (expandable) {
                    Box(
                        Modifier
                            .size(22.dp)
                            .clip(RoundedCornerShape(4.dp))
                            .clickable(enabled = !n.disabled, onClick = toggleExpand),
                        contentAlignment = Alignment.Center,
                    ) {
                        Icon(
                            if (expanded) Icons.Outlined.ExpandMore else Icons.AutoMirrored.Outlined.KeyboardArrowRight,
                            contentDescription = stringResource(if (expanded) R.string.expanded else R.string.collapsed),
                            modifier = Modifier.size(16.dp),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                } else {
                    Spacer(Modifier.width(22.dp))
                }
                if (n.icon.isNotEmpty()) {
                    Icon(
                        blockIcon(n.icon),
                        contentDescription = null,
                        modifier = Modifier.size(16.dp),
                        tint = toneColor(n.tone.ifEmpty { if (selected) "accent" else "muted" }),
                    )
                }
                Text(
                    n.text,
                    Modifier.weight(1f),
                    style = MaterialTheme.typography.bodyMedium,
                    fontWeight = if (selected) FontWeight.SemiBold else weight(n.weight),
                    color = if (n.tone.isNotEmpty()) toneColor(n.tone)
                    else if (selected) MaterialTheme.colorScheme.onSecondaryContainer
                    else MaterialTheme.colorScheme.onSurface,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (n.value.isNotEmpty()) {
                    Text(
                        n.value,
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
        }
        if (hasChildren && expanded) {
            Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                n.childrenList.forEach { child ->
                    key(child.id) {
                        UiTreeItem(child, form, events, depth = depth + 1, modifier = Modifier)
                    }
                }
            }
        }
    }
}

@Composable
private fun RowScope.RowChild(c: UiNode, form: BlockForm, events: BlockEvents) {
    val m = if (c.grow > 0) Modifier.weight(c.grow.toFloat()) else Modifier
    UiNodeView(c, form, events, inLine = true, modifier = m)
}

@Composable
private fun UiButton(n: UiNode, modifier: Modifier, tap: (() -> Unit)?) {
    val enabled = !n.disabled && tap != null
    val variant = resolveButtonVariant(n.variant, n.tone)
    val iconOnly = n.text.isEmpty() && n.icon.isNotEmpty()
    val iconSize = when (n.size) {
        "small" -> 14.dp
        "large" -> 20.dp
        else -> 16.dp
    }
    val labelStyle = when (n.size) {
        "small" -> MaterialTheme.typography.labelSmall
        "large" -> MaterialTheme.typography.titleSmall
        else -> MaterialTheme.typography.labelLarge
    }
    val minHeight = when (n.size) {
        "small" -> 30.dp
        "large" -> 46.dp
        else -> 38.dp
    }
    val contentPadding = when {
        iconOnly && n.size == "small" -> PaddingValues(6.dp)
        iconOnly && n.size == "large" -> PaddingValues(12.dp)
        iconOnly -> PaddingValues(9.dp)
        n.size == "small" -> PaddingValues(horizontal = 10.dp, vertical = 4.dp)
        n.size == "large" -> PaddingValues(horizontal = 18.dp, vertical = 12.dp)
        else -> PaddingValues(horizontal = 14.dp, vertical = 8.dp)
    }
    val sizeMod = if (iconOnly) {
        modifier.defaultMinSize(minWidth = minHeight, minHeight = minHeight)
    } else {
        modifier.defaultMinSize(minWidth = 1.dp, minHeight = minHeight)
    }
    val content: @Composable RowScope.() -> Unit = {
        if (n.icon.isNotEmpty()) {
            Icon(blockIcon(n.icon), if (iconOnly) n.icon else null, Modifier.size(iconSize))
            if (!iconOnly) Spacer(Modifier.width(6.dp))
        }
        if (!iconOnly) {
            Text(n.text, style = labelStyle, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
    when (variant) {
        "primary" -> Button(
            onClick = { tap?.invoke() },
            modifier = sizeMod,
            enabled = enabled,
            contentPadding = contentPadding,
            content = content,
        )
        "secondary" -> FilledTonalButton(
            onClick = { tap?.invoke() },
            modifier = sizeMod,
            enabled = enabled,
            contentPadding = contentPadding,
            content = content,
        )
        "ghost" -> TextButton(
            onClick = { tap?.invoke() },
            modifier = sizeMod,
            enabled = enabled,
            contentPadding = contentPadding,
            colors = ButtonDefaults.textButtonColors(
                contentColor = if (n.tone.isNotEmpty()) toneColor(n.tone) else MaterialTheme.colorScheme.onSurface,
            ),
            content = content,
        )
        "danger" -> Button(
            onClick = { tap?.invoke() },
            modifier = sizeMod,
            enabled = enabled,
            contentPadding = contentPadding,
            colors = ButtonDefaults.buttonColors(
                containerColor = MaterialTheme.colorScheme.error,
                contentColor = MaterialTheme.colorScheme.onError,
            ),
            content = content,
        )
        else -> OutlinedButton(
            onClick = { tap?.invoke() },
            modifier = sizeMod,
            enabled = enabled,
            contentPadding = contentPadding,
            border = MakoColors.cardBorderStroke,
            content = content,
        )
    }
}

/**
 * An input: its text is the form's; Enter submits it, and leaving it
 * changes it, where the node answers, if the user changed it (BlockForm.leave).
 * Its placeholder is its label too, which a screen reader says once text
 * hides the placeholder.
 */
@Composable
private fun UiInput(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    OutlinedTextField(
        value = form.value(n),
        onValueChange = { form.set(n.id, it) },
        modifier = modifier.fillMaxWidth()
            .then(if (n.text.isEmpty()) Modifier else Modifier.semantics { contentDescription = n.text })
            .onFocusChanged { f ->
                if (!f.isFocused && n.change) form.leave(n)?.let { events.send(n.id, "change", it) }
            },
        enabled = !n.disabled,
        singleLine = true,
        placeholder = if (n.text.isEmpty()) null else ({ Text(n.text) }),
        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
        keyboardActions = KeyboardActions(onDone = {
            if (n.change) events.send(n.id, "submit", form.submit(n))
        }),
    )
}

/** A select: a screen reader says its label, its text, if any, then its value. */
@Composable
private fun UiSelect(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    var open by remember { mutableStateOf(false) }
    val value = form.value(n).ifEmpty { "—" }
    Box(modifier) {
        OutlinedButton(onClick = { open = true }, Modifier.semantics {
            role = Role.DropdownList
            if (n.text.isNotEmpty()) {
                contentDescription = n.text
                stateDescription = value
            }
        }, enabled = !n.disabled) {
            Text(value, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Icon(Icons.Filled.ArrowDropDown, null)
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            n.optionsList.forEach { o ->
                DropdownMenuItem(text = { Text(o) }, onClick = {
                    open = false
                    form.set(n.id, o)
                    if (n.change) events.send(n.id, "change", o)
                })
            }
        }
    }
}

/** A multi-line text field; leaving it sends a change if modified. */
@Composable
private fun UiTextarea(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val (minL, maxL) = when (n.size) {
        "small" -> 2 to 4
        "large" -> 5 to 10
        else -> 3 to 6
    }
    OutlinedTextField(
        value = form.value(n),
        onValueChange = { form.set(n.id, it) },
        modifier = modifier.fillMaxWidth()
            .then(if (n.text.isEmpty()) Modifier else Modifier.semantics { contentDescription = n.text })
            .onFocusChanged { f ->
                if (!f.isFocused && n.change) form.leave(n)?.let { events.send(n.id, "change", it) }
            },
        enabled = !n.disabled,
        singleLine = false,
        minLines = minL,
        maxLines = maxL,
        placeholder = if (n.text.isEmpty()) null else ({ Text(n.text) }),
    )
}

/** A horizontally scrollable strip of tab buttons; tapping sends a change with the chosen label. */
@Composable
private fun UiTabs(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    val current = form.value(n)
    Row(
        modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        n.optionsList.forEach { opt ->
            val selected = opt == current
            Surface(
                modifier = if (n.disabled) Modifier else Modifier.clickable {
                    form.set(n.id, opt)
                    if (n.change) events.send(n.id, "change", opt)
                },
                shape = RoundedCornerShape(8.dp),
                color = if (selected) MaterialTheme.colorScheme.primaryContainer else MaterialTheme.colorScheme.surfaceContainerHigh,
                border = if (selected) BorderStroke(1.dp, MaterialTheme.colorScheme.primary) else MakoColors.cardBorderStroke,
            ) {
                Text(
                    opt,
                    Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
                    style = MaterialTheme.typography.labelLarge,
                    fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                    color = if (selected) MaterialTheme.colorScheme.onPrimaryContainer else MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}

/** A structured table with optional column headers and row children. */
@Composable
private fun UiTable(n: UiNode, form: BlockForm, events: BlockEvents, modifier: Modifier) {
    Surface(
        modifier.fillMaxWidth(),
        shape = RoundedCornerShape(8.dp),
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        border = MakoColors.cardBorderStroke,
    ) {
        Column(Modifier.fillMaxWidth()) {
            if (n.labelsCount > 0) {
                Surface(color = MaterialTheme.colorScheme.surfaceContainerHigh) {
                    Row(
                        Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 6.dp),
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        n.labelsList.forEach { label ->
                            Text(
                                label,
                                Modifier.weight(1f),
                                style = MaterialTheme.typography.labelMedium,
                                fontWeight = FontWeight.SemiBold,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                    }
                }
                HorizontalDivider()
            }
            n.childrenList.forEachIndexed { idx, row ->
                key(row.id) {
                    Box(Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 6.dp)) {
                        UiNodeView(row, form, events, false, Modifier)
                    }
                    if (idx < n.childrenCount - 1) HorizontalDivider()
                }
            }
        }
    }
}

/**
 * A block's Markdown: no pictures, which would be fetched from wherever
 * the block names, carrying what it put in their addresses; http(s) links
 * open externally, and when `n.change` is true, internal/non-http links
 * fire a `change` BlockEvent with the link target.
 */
@Composable
private fun BlockMarkdown(n: UiNode, events: BlockEvents, modifier: Modifier) {
    val uri = LocalUriHandler.current
    Column(modifier) {
        MarkdownMessage(n.text, onLink = { link ->
            handleMarkdownLink(
                link = link,
                allowPluginLink = n.change && !n.disabled,
                openExternal = { runCatching { uri.openUri(it) } },
                sendPluginLink = { events.send(n.id, "change", it) },
            )
        })
    }
}
