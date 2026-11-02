package dev.ironpark.mako.ui

import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemGestures
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowLeft
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.SessionInfo
import kotlin.math.abs

/** How far from a side of the screen, past the system's own gesture area, a drag is the edge's. */
private val EdgeWidth = 20.dp

/** How far a drag from the right edge pulls before it opens what it pulls. */
internal val PullDistance = 120.dp

/**
 * edgeSwipes takes the horizontal drags that start at either side of the
 * screen, before what is under them sees them, so a row there does not
 * swipe; a drag from the right edge leftwards pulls, onPull told how far
 * of PullDistance, and onRelease whether it went the whole way. A drag
 * that goes up or down first is left to scroll.
 */
@Composable
internal fun Modifier.edgeSwipes(onPull: (Float) -> Unit, onRelease: (Boolean) -> Unit): Modifier {
    val pull by rememberUpdatedState(onPull)
    val release by rememberUpdatedState(onRelease)
    val density = LocalDensity.current
    val direction = LocalLayoutDirection.current
    val gestures = WindowInsets.systemGestures
    return pointerInput(Unit) {
        awaitEachGesture {
            val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
            val left = gestures.getLeft(density, direction) + EdgeWidth.toPx()
            val right = gestures.getRight(density, direction) + EdgeWidth.toPx()
            val fromLeft = down.position.x < left
            val fromRight = down.position.x > size.width - right
            if (!fromLeft && !fromRight) return@awaitEachGesture
            val slop = viewConfiguration.touchSlop
            val distance = PullDistance.toPx()
            var claimed = false
            var progress = 0f
            var ended = false
            try {
                while (true) {
                    val event = awaitPointerEvent(PointerEventPass.Initial)
                    val change = event.changes.firstOrNull { it.id == down.id } ?: break
                    if (!change.pressed) {
                        if (claimed) change.consume()
                        if (claimed && fromRight) release(progress >= 1f)
                        ended = true
                        break
                    }
                    val d = change.position - down.position
                    if (!claimed) {
                        if (abs(d.x) <= slop && abs(d.y) <= slop) continue
                        // Up or down first: a scroll, not the edge's.
                        if (abs(d.y) > abs(d.x)) break
                        claimed = true
                    }
                    change.consume()
                    if (fromRight) {
                        progress = (-d.x / distance).coerceAtLeast(0f)
                        pull(progress)
                    }
                }
            } finally {
                if (claimed && fromRight && !ended) release(false)
            }
        }
    }
}

/**
 * LastSessionPeek is the session a pull from the right edge opens, coming
 * in from that edge as far as the pull has gone.
 */
@Composable
internal fun LastSessionPeek(s: SessionInfo, nodes: List<Node>, pull: Float, modifier: Modifier = Modifier) {
    if (pull <= 0f) return
    val p = pull.coerceIn(0f, 1f)
    val ready = pull >= 1f
    Surface(
        modifier = modifier.padding(end = 12.dp).widthIn(max = 240.dp).graphicsLayer {
            alpha = p
            translationX = (1f - p) * 64.dp.toPx()
            val scale = if (ready) 1f else 0.9f + 0.1f * p
            scaleX = scale
            scaleY = scale
        },
        shape = RoundedCornerShape(24.dp),
        color = if (ready) MaterialTheme.colorScheme.primaryContainer else MaterialTheme.colorScheme.surfaceContainerHighest,
        shadowElevation = 6.dp,
    ) {
        Row(Modifier.padding(start = 6.dp, end = 14.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.AutoMirrored.Filled.KeyboardArrowLeft, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
            AgentAvatar(s.agent, agentColor(nodes, s.nodeId, s.agent), s.statusColor(), size = 28.dp)
            Spacer(Modifier.width(8.dp))
            Text(s.label(), style = MaterialTheme.typography.labelLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}
