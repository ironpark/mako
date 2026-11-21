package dev.ironpark.mako.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
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
import androidx.compose.material.icons.automirrored.outlined.OpenInNew
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.outlined.AccountTree
import androidx.compose.material.icons.outlined.Add
import androidx.compose.material.icons.outlined.Check
import androidx.compose.material.icons.outlined.CheckCircle
import androidx.compose.material.icons.outlined.Close
import androidx.compose.material.icons.outlined.Code
import androidx.compose.material.icons.outlined.CropSquare
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.ErrorOutline
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.Info
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
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
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
    "info" to Icons.Outlined.Info, "circle-alert" to Icons.Outlined.ErrorOutline, "triangle-alert" to Icons.Outlined.WarningAmber,
    "folder" to Icons.Outlined.Folder, "code" to Icons.Outlined.Code, "github" to Icons.Outlined.Code,
    "git-branch" to Icons.Outlined.AccountTree, "clock" to Icons.Outlined.Schedule,
    "external-link" to Icons.AutoMirrored.Outlined.OpenInNew,
    "file-text" to Icons.Outlined.Description,
    "arrow-left" to Icons.AutoMirrored.Outlined.ArrowBack,
    "arrow-right" to Icons.AutoMirrored.Outlined.ArrowForward,
)

/** The icon of the name, a plain square for one the app does not have. */
internal fun blockIcon(name: String): ImageVector = blockIcons[name] ?: Icons.Outlined.CropSquare

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

/** UiTree draws a block's tree; form holds what the user set in its fields. */
@Composable
internal fun UiTree(n: UiNode, form: BlockForm, events: BlockEvents, inLine: Boolean = false, modifier: Modifier = Modifier) {
    if (LocalBlockDrag.current == null) {
        val drag = remember { BlockDragState() }
        CompositionLocalProvider(LocalBlockDrag provides drag) {
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
                UiNodeView(n, form, events, inLine, Modifier)
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
        "markdown" -> BlockMarkdown(n.text, mod)
        "code" -> Surface(mod.fillMaxWidth(), color = MaterialTheme.colorScheme.surfaceContainerHigh, shape = RoundedCornerShape(8.dp)) {
            Text(n.text, Modifier.horizontalScroll(rememberScrollState()).padding(10.dp),
                style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace, softWrap = false)
        }
        "progress" -> LinearProgressIndicator(progress = { n.progress.toFloat() }, modifier = modifier.fillMaxWidth().padding(vertical = 4.dp))
        "divider" -> HorizontalDivider(modifier.padding(vertical = 2.dp))
        "chart" -> UiChart(n, modifier)
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
    val tap: (() -> Unit)? = if (n.tap) ({ events.send(n.id, "tap", "") }) else null
    val box = interactive(modifier, n, tap, drag, events)
    val canDrop = drag != null && drag.accepts(n)
    val picked = drag?.active != null && n.drag.isNotEmpty() && drag.active?.value == n.drag
    val content: @Composable (Modifier) -> Unit = { m ->
        when (n.kind) {
            "row" -> {
                val hasColumn = n.childrenList.any { it.kind in setOf("column", "list", "card", "scroll") }
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
                    val maxH = if (n.maxHeight > 0) n.maxHeight.dp else 320.dp
                    Column(
                        m.fillMaxWidth().heightIn(max = maxH).verticalScroll(rememberScrollState()),
                        verticalArrangement = Arrangement.spacedBy(gap),
                    ) { n.childrenList.forEach { c -> key(c.id) { UiNodeView(c, form, events, false, Modifier) } } }
                }
            }
            else -> Column(
                m.fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy((if (n.gap > 0) n.gap else if (n.kind == "list") 0.0 else 6.0).dp),
            ) {
                n.childrenList.forEach { c ->
                    key(c.id) {
                        if (n.kind == "list" && c.tap) {
                            // The list's row answers the tap, as the desktop's rows do.
                            Box(Modifier.fillMaxWidth().clickable { events.send(c.id, "tap", "") }.padding(vertical = 6.dp)) {
                                UiNodeView(c.toBuilder().setTap(false).build(), form, events, false, Modifier)
                            }
                        } else UiNodeView(c, form, events, false, Modifier)
                    }
                }
            }
        }
    }
    if (n.kind == "card" || n.drag.isNotEmpty() || n.drop || canDrop || picked) {
        val border = when {
            canDrop || picked -> BorderStroke(1.5.dp, MaterialTheme.colorScheme.primary)
            else -> MakoColors.cardBorderStroke
        }
        val bg = when {
            canDrop -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.28f)
            picked -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.16f)
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

@Composable
private fun RowScope.RowChild(c: UiNode, form: BlockForm, events: BlockEvents) {
    val m = if (c.grow > 0) Modifier.weight(c.grow.toFloat()) else Modifier
    UiNodeView(c, form, events, inLine = true, modifier = m)
}

@Composable
private fun UiButton(n: UiNode, modifier: Modifier, tap: (() -> Unit)?) {
    val enabled = !n.disabled && tap != null
    val content: @Composable RowScope.() -> Unit = {
        if (n.icon.isNotEmpty()) {
            Icon(blockIcon(n.icon), null, Modifier.size(16.dp))
            Spacer(Modifier.width(6.dp))
        }
        Text(n.text, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
    when (n.tone) {
        "accent" -> Button(onClick = { tap?.invoke() }, modifier, enabled = enabled, content = content)
        "danger" -> Button(
            onClick = { tap?.invoke() }, modifier, enabled = enabled, content = content,
            colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error, contentColor = MaterialTheme.colorScheme.onError),
        )
        else -> OutlinedButton(onClick = { tap?.invoke() }, modifier, enabled = enabled, content = content)
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
        minLines = 3,
        maxLines = 6,
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
 * the block names, carrying what it put in their addresses; web links
 * open as the user taps them, and nothing else does.
 */
@Composable
private fun BlockMarkdown(text: String, modifier: Modifier) {
    val uri = androidx.compose.ui.platform.LocalUriHandler.current
    Column(modifier) {
        MarkdownMessage(text, onLink = { link ->
            if (link.startsWith("https://") || link.startsWith("http://")) uri.openUri(link)
        })
    }
}
