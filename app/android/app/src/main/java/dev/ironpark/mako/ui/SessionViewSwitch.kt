package dev.ironpark.mako.ui

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.Chat
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R

private val Segment = 40.dp

/**
 * SessionViewSwitch is the toolbar's choice of chat or terminal: two icons
 * in a pill, the one on show lit. It is one toggle to accessibility, as the
 * swipe between them is one gesture.
 */
@Composable
internal fun SessionViewSwitch(terminal: Boolean, onTerminal: (Boolean) -> Unit) {
    val mode = stringResource(if (terminal) R.string.tab_terminal else R.string.tab_chat)
    val description = stringResource(R.string.switch_session_view)
    val colors = MaterialTheme.colorScheme
    val thumb by animateDpAsState(if (terminal) Segment else 0.dp, label = "thumb")
    Box(
        Modifier.semantics { contentDescription = description; stateDescription = mode }
            .heightIn(min = 48.dp)
            .toggleable(value = terminal, role = Role.Switch, onValueChange = onTerminal)
            .padding(horizontal = 4.dp),
        contentAlignment = Alignment.Center,
    ) {
        Box(Modifier.clip(CircleShape).background(colors.surfaceContainerHighest).padding(3.dp)) {
            Box(Modifier.offset { IntOffset(thumb.roundToPx(), 0) }.size(Segment, 32.dp).clip(CircleShape).background(colors.primary))
            Row {
                SegmentIcon(Icons.AutoMirrored.Outlined.Chat, lit = !terminal)
                SegmentIcon(Icons.Outlined.Terminal, lit = terminal)
            }
        }
    }
}

@Composable
private fun SegmentIcon(icon: ImageVector, lit: Boolean) {
    val tint by animateColorAsState(
        if (lit) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurfaceVariant, label = "tint",
    )
    Box(Modifier.size(Segment, 32.dp), contentAlignment = Alignment.Center) {
        Icon(icon, null, Modifier.size(18.dp), tint = tint)
    }
}
