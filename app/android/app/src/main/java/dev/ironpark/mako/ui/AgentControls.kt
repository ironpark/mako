package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.AgentControls
import dev.ironpark.mako.v2.Usage

/** modeName names a permission mode as the window does. */
@Composable
internal fun modeName(mode: String): String = when (mode) {
    "default", "" -> stringResource(R.string.mode_default)
    "acceptEdits" -> stringResource(R.string.mode_accept_edits)
    "plan" -> stringResource(R.string.mode_plan)
    "auto" -> stringResource(R.string.mode_auto)
    "bypassPermissions" -> stringResource(R.string.mode_bypass)
    else -> mode
}

/** shortModel is a model as people say it: claude-opus-5-5 is Opus 5.5. */
internal fun shortModel(id: String): String {
    val rest = id.removePrefix("claude-").substringBefore('[')
    if (rest == id) return id
    val parts = rest.split('-').filter { it.length < 8 } // a date's part is left out
    val name = parts.firstOrNull()?.replaceFirstChar { it.uppercase() } ?: return id
    return (listOf(name) + parts.drop(1).joinToString(".").ifEmpty { null }).filterNotNull().joinToString(" ")
}

/**
 * AgentControlsRow is what the agent may do unasked, and the model it
 * replies with, at its effort, each a menu of what it may be set to; the
 * agent keeps a model or effort set as your default.
 */
@Composable
internal fun AgentControlsRow(c: AgentControls, usage: Usage?, onSet: (mode: String, model: String, effort: String) -> Unit) {
    val mode = usage?.permissionMode.orEmpty()
    val model = usage?.model.orEmpty()
    val effort = usage?.effort.orEmpty()
    Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp), horizontalArrangement = Arrangement.SpaceBetween) {
        if (c.modesCount > 0) Box {
            var open by remember { mutableStateOf(false) }
            TextButton(onClick = { open = true }) { Text(modeName(mode), style = MaterialTheme.typography.labelMedium) }
            DropdownMenu(open, onDismissRequest = { open = false }) {
                c.modesList.forEach { m ->
                    DropdownMenuItem(
                        text = { Text((if (m == mode.ifEmpty { "default" }) "✓ " else "") + modeName(m)) },
                        onClick = { open = false; onSet(m, "", "") },
                    )
                }
            }
        } else Box {}
        if (c.modelsCount + c.effortsCount > 0) Box {
            var open by remember { mutableStateOf(false) }
            val label = listOf(if (model.isEmpty()) stringResource(R.string.model) else shortModel(model), effort).filter { it.isNotEmpty() }.joinToString(" · ")
            TextButton(onClick = { open = true }) { Text(label, style = MaterialTheme.typography.labelMedium) }
            DropdownMenu(open, onDismissRequest = { open = false }) {
                c.modelsList.forEach { m ->
                    DropdownMenuItem(
                        text = { Text((if (model.contains(m)) "✓ " else "") + m.replaceFirstChar { it.uppercase() }) },
                        onClick = { open = false; onSet("", m, "") },
                    )
                }
                if (c.modelsCount > 0 && c.effortsCount > 0) HorizontalDivider()
                c.effortsList.forEach { e ->
                    DropdownMenuItem(
                        text = { Text((if (e == effort) "✓ " else "") + stringResource(R.string.effort_level, e)) },
                        onClick = { open = false; onSet("", "", e) },
                    )
                }
                HorizontalDivider()
                DropdownMenuItem(text = { Text(stringResource(R.string.kept_as_default), style = MaterialTheme.typography.bodySmall) }, onClick = {}, enabled = false)
            }
        }
    }
}
