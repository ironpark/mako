package dev.ironpark.mako.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.outlined.Checklist
import androidx.compose.material.icons.outlined.Shield
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.ApprovalOption
import dev.ironpark.mako.v2.ScreenDialog
import dev.ironpark.mako.v2.SessionInfo
import org.json.JSONObject

/**
 * DiffLine is a line of an edit as shown: ' ' kept, '-' taken out, '+' put
 * in.
 */
internal data class DiffLine(val mark: Char, val text: String)

/**
 * editLines is an edit of old into new as lines: the lines both start and
 * end with kept, and what lies between them taken out, then put in.
 */
internal fun editLines(old: String, new: String): List<DiffLine> {
    val a = if (old.isEmpty()) emptyList() else old.split('\n')
    val b = if (new.isEmpty()) emptyList() else new.split('\n')
    var head = 0
    while (head < a.size && head < b.size && a[head] == b[head]) head++
    var tail = 0
    while (tail < a.size - head && tail < b.size - head && a[a.size - 1 - tail] == b[b.size - 1 - tail]) tail++
    return a.take(head).map { DiffLine(' ', it) } +
        a.subList(head, a.size - tail).map { DiffLine('-', it) } +
        b.subList(head, b.size - tail).map { DiffLine('+', it) } +
        a.takeLast(tail).map { DiffLine(' ', it) }
}

/** patchLines are a patch's lines, marked by how each starts. */
internal fun patchLines(patch: String): List<DiffLine> = patch.trimEnd('\n').split('\n').map { l ->
    when {
        l.startsWith("+") && !l.startsWith("+++") -> DiffLine('+', l.drop(1))
        l.startsWith("-") && !l.startsWith("---") -> DiffLine('-', l.drop(1))
        else -> DiffLine(' ', l)
    }
}

/** maxDiffLines caps the lines an approval's diff shows. */
private const val MAX_DIFF_LINES = 200

/**
 * ApprovalBody is what an approval's call would do as its kind reads: an
 * edit as a diff, a command as it would run; its arguments as they came
 * for a call of no kind, or arguments cut short.
 */
@Composable
private fun ApprovalBody(a: Approval) {
    val colors = MaterialTheme.colorScheme
    val mono = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace)
    val input = remember(a.input) { runCatching { JSONObject(a.input) }.getOrNull() }
    val path = input?.optString("path").orEmpty()
    val lines: List<DiffLine>? = remember(a.kind, input) {
        when (a.kind) {
            "edit" -> input?.let { editLines(it.optString("old"), it.optString("new")) }
            "multiedit" -> input?.optJSONArray("edits")?.let { edits ->
                (0 until edits.length()).flatMap { i ->
                    val e = edits.optJSONObject(i) ?: return@flatMap emptyList()
                    (if (i > 0) listOf(DiffLine(' ', "⋯")) else emptyList()) + editLines(e.optString("old"), e.optString("new"))
                }
            }
            "write" -> input?.let { editLines("", it.optString("content")) }
            "patch" -> input?.let { patchLines(it.optString("patch")) }
            else -> null
        }
    }
    Column(Modifier.heightIn(max = 260.dp).verticalScroll(rememberScrollState())) {
        when {
            lines != null -> {
                if (path.isNotEmpty()) Text(path, style = mono, fontWeight = FontWeight.SemiBold, modifier = Modifier.padding(bottom = 4.dp))
                Column(Modifier.horizontalScroll(rememberScrollState())) {
                    lines.take(MAX_DIFF_LINES).forEach { l ->
                        val bg = when (l.mark) {
                            '+' -> Color(0x3334C759)
                            '-' -> Color(0x33FF3B30)
                            else -> Color.Transparent
                        }
                        Text("${l.mark} ${l.text}", style = mono, softWrap = false, modifier = Modifier.background(bg))
                    }
                    if (lines.size > MAX_DIFF_LINES) Text("⋯", style = mono, color = colors.onSurfaceVariant)
                }
            }
            a.kind == "shell" && input != null -> {
                val description = input.optString("description")
                if (description.isNotEmpty()) Text(description, style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant)
                Text("$ " + input.optString("command"), style = mono)
            }
            else -> Text(a.input, style = mono)
        }
    }
}

/** optionLabel says what allowing with option o does besides. */
@Composable
internal fun optionLabel(o: ApprovalOption): String = when {
    o.rulesCount > 0 -> stringResource(R.string.always_allow, o.rulesList.joinToString(", "))
    o.mode == "acceptEdits" -> stringResource(R.string.allow_accept_edits)
    o.mode == "auto" -> stringResource(R.string.allow_auto)
    else -> stringResource(R.string.allow_mode, o.mode)
}

/** planOptionLabel says how work goes on once a plan is approved into mode. */
@Composable
internal fun planOptionLabel(mode: String): String = when (mode) {
    "auto" -> stringResource(R.string.plan_auto)
    "acceptEdits" -> stringResource(R.string.plan_accept_edits)
    "default" -> stringResource(R.string.plan_default)
    else -> stringResource(R.string.allow_mode, mode)
}

/**
 * ApprovalCard is the tool call an agent waits for you to allow: allowed,
 * once or as one of the ways the agent offers to allow such calls from then
 * on, denied, or left to the agent's own question in its terminal, from the
 * corner. Unfolded, it shows what the call would do, an edit as a diff, and
 * takes what to tell the agent with a denial.
 */
@Composable
internal fun ApprovalCard(
    s: SessionInfo, expanded: Boolean, onExpand: () -> Unit,
    onAnswer: (decision: String, option: String, message: String) -> Unit,
) {
    val a = s.approval
    val colors = MaterialTheme.colorScheme
    var why by rememberSaveable(a.id) { mutableStateOf("") }
    WaitCard(Icons.Outlined.Shield, stringResource(R.string.approval_title, a.tool), onTerminal = { onAnswer("terminal", "", "") }) {
        val more = a.input.isNotEmpty() || a.detail.lines().size > 4 || a.detail.length > 160
        if (a.detail.isNotEmpty() || more) Surface(
            color = colors.surfaceContainerHighest,
            shape = RoundedCornerShape(10.dp),
            modifier = Modifier.fillMaxWidth().padding(top = 4.dp),
        ) {
            Column(
                Modifier.then(if (more) Modifier.clickable(
                    role = Role.Button,
                    onClickLabel = stringResource(if (expanded) R.string.less else R.string.details),
                    onClick = onExpand,
                ) else Modifier).animateContentSize().padding(10.dp),
            ) {
                Row(verticalAlignment = Alignment.Top) {
                    Text(
                        a.detail.ifEmpty { a.tool }, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                        maxLines = if (expanded) Int.MAX_VALUE else 4, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f),
                    )
                    if (more) Icon(
                        if (expanded) Icons.Filled.KeyboardArrowUp else Icons.Filled.KeyboardArrowDown, null,
                        Modifier.size(18.dp), tint = colors.onSurfaceVariant,
                    )
                }
                if (expanded && a.input.isNotEmpty()) {
                    HorizontalDivider(Modifier.padding(vertical = 8.dp))
                    ApprovalBody(a)
                }
            }
        }
        if (expanded) OutlinedTextField(
            why, { why = it }, singleLine = true,
            placeholder = { Text(stringResource(R.string.deny_reason_hint)) },
            modifier = Modifier.fillMaxWidth().padding(top = 8.dp),
        )
        Row(Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { onAnswer("deny", "", why.trim()) }, modifier = Modifier.weight(1f)) {
                Text(stringResource(R.string.deny))
            }
            Button(onClick = { onAnswer("allow", "", "") }, modifier = Modifier.weight(1f)) { Text(stringResource(R.string.allow)) }
        }
        a.optionsList.forEach { o ->
            OutlinedButton(onClick = { onAnswer("allow", o.id, "") }, modifier = Modifier.fillMaxWidth().padding(top = 4.dp)) {
                Text(optionLabel(o), maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/**
 * PlanCard is a plan an agent puts to you, in brief: what it is called,
 * and where to review it, or to leave it to the agent's terminal.
 */
@Composable
internal fun PlanCard(a: Approval, onReview: () -> Unit, onTerminal: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    WaitCard(Icons.Outlined.Checklist, stringResource(R.string.plan_ready), onTerminal = onTerminal) {
        if (a.detail.isNotEmpty()) Text(a.detail, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
        Row(Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.End) {
            Button(onClick = onReview) { Text(stringResource(R.string.review)) }
        }
    }
}

/**
 * PlanReview is a plan put to you in full: approved into one of the modes
 * the agent offers to carry it out in, or sent back with what to change.
 */
@Composable
internal fun PlanReview(a: Approval, onApprove: (option: String) -> Unit, onKeepPlanning: (message: String) -> Unit, modifier: Modifier = Modifier) {
    val plan = remember(a.input) { runCatching { JSONObject(a.input).optString("plan") }.getOrNull().orEmpty().ifEmpty { a.detail } }
    var why by rememberSaveable(a.id) { mutableStateOf("") }
    Column(modifier) {
        Surface(
            color = MaterialTheme.colorScheme.surfaceContainerHighest, shape = RoundedCornerShape(12.dp),
            modifier = Modifier.fillMaxWidth().weight(1f, fill = false),
        ) {
            MarkdownMessage(plan, Modifier.verticalScroll(rememberScrollState()).padding(12.dp))
        }
        a.optionsList.forEachIndexed { i, o ->
            val label = planOptionLabel(o.mode)
            if (i == 0) Button(onClick = { onApprove(o.id) }, modifier = Modifier.fillMaxWidth().padding(top = 12.dp)) { Text(label) }
            else OutlinedButton(onClick = { onApprove(o.id) }, modifier = Modifier.fillMaxWidth().padding(top = 4.dp)) { Text(label) }
        }
        OutlinedTextField(
            why, { why = it },
            placeholder = { Text(stringResource(R.string.plan_change_hint)) },
            modifier = Modifier.fillMaxWidth().padding(top = 12.dp), maxLines = 4,
        )
        OutlinedButton(onClick = { onKeepPlanning(why.trim()) }, modifier = Modifier.fillMaxWidth().padding(top = 4.dp)) {
            Text(stringResource(R.string.keep_planning))
        }
    }
}

/**
 * DialogCard is a dialog of options the agent shows in its terminal, where
 * no hook holds it, as a folder's trust: what it asks, and its options,
 * one picked there as you would; or the terminal, from the corner. The
 * option the dialog's cursor is on is marked, as the terminal marks it.
 */
@Composable
internal fun DialogCard(d: ScreenDialog, onTerminal: () -> Unit, onPick: (Int) -> Unit) {
    val colors = MaterialTheme.colorScheme
    WaitCard(Icons.Outlined.Terminal, stringResource(R.string.dialog_title), onTerminal = onTerminal) {
        if (d.text.isNotEmpty()) Text(
            d.text, style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant,
            maxLines = 6, overflow = TextOverflow.Ellipsis,
        )
        d.optionsList.forEachIndexed { i, o ->
            OutlinedButton(onClick = { onPick(i) }, modifier = Modifier.fillMaxWidth().padding(top = 4.dp)) {
                Text(if (i == d.selected) "❯ $o" else o, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/**
 * WaitCard frames what an agent waits on you for: what it is, under an
 * icon, its content, and its own terminal, from the corner.
 */
@Composable
internal fun WaitCard(icon: ImageVector, title: String, onTerminal: () -> Unit, content: @Composable ColumnScope.() -> Unit) {
    val colors = MaterialTheme.colorScheme
    Card(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp),
        colors = CardDefaults.cardColors(containerColor = colors.surfaceContainerHigh),
        shape = RoundedCornerShape(20.dp),
    ) {
        Column(Modifier.padding(start = 16.dp, end = 8.dp, top = 8.dp, bottom = 16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(icon, null, tint = StatusColors.Approval, modifier = Modifier.size(20.dp))
                Spacer(Modifier.width(8.dp))
                Text(title, style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
                IconButton(onClick = onTerminal) {
                    Icon(Icons.Outlined.Terminal, stringResource(R.string.in_terminal), tint = colors.onSurfaceVariant)
                }
            }
            Column(Modifier.padding(end = 8.dp), content = content)
        }
    }
}
