package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.Chat
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R

/** One compact control in the toolbar replaces the separate tab strip. */
@Composable
internal fun SessionViewSwitch(terminal: Boolean, onTerminal: (Boolean) -> Unit) {
    val mode = stringResource(if (terminal) R.string.tab_terminal else R.string.tab_chat)
    val description = stringResource(R.string.switch_session_view)
    Row(
        Modifier.semantics { contentDescription = description; stateDescription = mode }
            .toggleable(value = terminal, role = Role.Switch, onValueChange = onTerminal)
            .heightIn(min = 48.dp).padding(horizontal = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Text(mode, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Switch(checked = terminal, onCheckedChange = null, thumbContent = {
            Icon(if (terminal) Icons.Outlined.Terminal else Icons.AutoMirrored.Outlined.Chat, null, Modifier.size(16.dp))
        })
    }
}
