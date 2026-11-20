package dev.ironpark.mako.ui

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.interaction.MutableInteractionSource
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
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R

private val SegmentWidth = 40.dp
private val SegmentHeight = 32.dp

/**
 * SessionViewSwitch is the toolbar's choice of chat or terminal: two icons
 * in a pill, the one on show lit. It is one toggle to accessibility, as the
 * swipe between them is one gesture.
 */
@Composable
internal fun SessionViewSwitch(
    terminal: Boolean,
    onTerminal: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
) {
    val mode = stringResource(if (terminal) R.string.tab_terminal else R.string.tab_chat)
    val description = stringResource(R.string.switch_session_view)
    val colors = MaterialTheme.colorScheme
    val interaction = remember { MutableInteractionSource() }
    val thumbOffset by animateDpAsState(
        targetValue = if (terminal) SegmentWidth else 0.dp,
        animationSpec = MakoMotion.SnappyDpSpring,
        label = "thumb",
    )

    Box(
        modifier
            .semantics { contentDescription = description; stateDescription = mode }
            .heightIn(min = 48.dp)
            .toggleable(
                value = terminal,
                interactionSource = interaction,
                indication = null,
                role = Role.Switch,
                onValueChange = onTerminal,
            )
            .padding(horizontal = 4.dp),
        contentAlignment = Alignment.Center,
    ) {
        Box(
            Modifier
                .pressScale(interactionSource = interaction, pressedScale = 0.96f)
                .clip(CircleShape)
                .background(colors.surfaceContainerHigh)
                .border(MakoColors.hairlineWidth, MakoColors.cardBorder, CircleShape)
                .padding(3.dp),
        ) {
            Box(
                Modifier
                    .offset { IntOffset(thumbOffset.roundToPx(), 0) }
                    .size(SegmentWidth, SegmentHeight)
                    .clip(CircleShape)
                    .background(colors.primary),
            )
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
        targetValue = if (lit) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurfaceVariant,
        animationSpec = MakoMotion.fastTween(),
        label = "tint",
    )
    val scale by animateFloatAsState(
        targetValue = if (lit) 1f else 0.92f,
        animationSpec = MakoMotion.SnappySpring,
        label = "iconScale",
    )
    Box(Modifier.size(SegmentWidth, SegmentHeight), contentAlignment = Alignment.Center) {
        Icon(
            icon,
            contentDescription = null,
            modifier = Modifier
                .size(18.dp)
                .graphicsLayer {
                    scaleX = scale
                    scaleY = scale
                },
            tint = tint,
        )
    }
}
