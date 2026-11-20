package dev.ironpark.mako.ui

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

// The app's shared surfaces: what a screen with nothing to show says, a
// card of rows under a heading, a count, and a list still loading.

/**
 * Message is a screen with nothing to list: why, and what to do about it,
 * under an icon that says what kind of nothing it is.
 */
@Composable
fun Message(
    title: String, body: String, action: String? = null, onAction: () -> Unit = {},
    icon: ImageVector? = null, secondary: String? = null, onSecondary: () -> Unit = {},
) {
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) { EmptyState(title, body, Modifier, action, onAction, icon, secondary, onSecondary) }
}

/** EmptyState is Message's content, for a place that scrolls already. */
@Composable
fun EmptyState(
    title: String, body: String, modifier: Modifier = Modifier, action: String? = null, onAction: () -> Unit = {},
    icon: ImageVector? = null, secondary: String? = null, onSecondary: () -> Unit = {},
) {
    Column(modifier, horizontalAlignment = Alignment.CenterHorizontally) {
        if (icon != null) {
            Surface(
                shape = RoundedCornerShape(20.dp),
                color = MaterialTheme.colorScheme.surfaceContainerLow,
                border = MakoColors.cardBorderStroke,
                modifier = Modifier.size(64.dp),
            ) {
                Box(contentAlignment = Alignment.Center) {
                    Icon(icon, null, Modifier.size(28.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            Spacer(Modifier.height(20.dp))
        }
        Text(title, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
        if (body.isNotEmpty()) {
            Spacer(Modifier.height(6.dp))
            Text(
                body, style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        if (action != null) {
            Spacer(Modifier.height(24.dp))
            Button(onClick = onAction) { Text(action) }
        }
        if (secondary != null) {
            Spacer(Modifier.height(4.dp))
            TextButton(onClick = onSecondary) { Text(secondary) }
        }
    }
}

/** Section is a card of a screen's settings, under its heading. */
@Composable
fun Section(title: String, modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    Column(modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        if (title.isNotEmpty()) SectionLabel(title, Modifier.padding(start = 4.dp, bottom = 8.dp))
        Card(
            Modifier.fillMaxWidth(),
            shape = RoundedCornerShape(16.dp),
            colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerLow),
            border = MakoColors.cardBorderStroke,
        ) { content() }
    }
}

/** SectionLabel is the small heading over a group of rows, as the desktop's sidebar heads its groups. */
@Composable
fun SectionLabel(text: String, modifier: Modifier = Modifier, color: Color = MaterialTheme.colorScheme.onSurfaceVariant) {
    Text(
        text, style = MaterialTheme.typography.labelLarge, color = color, fontWeight = FontWeight.Medium,
        modifier = modifier.semantics { heading() },
    )
}

/** The shape of the i-th of n rows of one card: its outer corners round, its inner ones square. */
internal fun groupShape(i: Int, n: Int): RoundedCornerShape {
    val outer = 16.dp
    val inner = 0.dp
    return RoundedCornerShape(
        topStart = if (i == 0) outer else inner, topEnd = if (i == 0) outer else inner,
        bottomStart = if (i == n - 1) outer else inner, bottomEnd = if (i == n - 1) outer else inner,
    )
}

/**
 * Draws a seamless hairline perimeter border around an [index]-th item of a [count]-row
 * grouped list card without double horizontal seams between adjacent rows.
 */
fun Modifier.groupedCardBorder(
    index: Int,
    count: Int,
    color: Color,
    cornerRadius: Dp = 16.dp,
    width: Dp = MakoColors.hairlineWidth,
): Modifier = drawWithContent {
    drawContent()
    val stroke = width.toPx()
    val half = stroke / 2f
    val r = cornerRadius.toPx().coerceAtMost(minOf(size.width, size.height) / 2f)
    val left = half
    val right = size.width - half
    val top = half
    val bottom = size.height - half
    val style = Stroke(width = stroke)
    when {
        count <= 1 -> {
            drawRoundRect(
                color = color,
                topLeft = Offset(left, top),
                size = Size(size.width - stroke, size.height - stroke),
                cornerRadius = CornerRadius(r, r),
                style = style,
            )
        }
        index == 0 -> {
            val path = Path().apply {
                moveTo(left, size.height)
                lineTo(left, top + r)
                arcTo(Rect(left, top, left + 2 * r, top + 2 * r), 180f, 90f, false)
                lineTo(right - r, top)
                arcTo(Rect(right - 2 * r, top, right, top + 2 * r), 270f, 90f, false)
                lineTo(right, size.height)
            }
            drawPath(path, color = color, style = style)
        }
        index == count - 1 -> {
            val path = Path().apply {
                moveTo(left, 0f)
                lineTo(left, bottom - r)
                arcTo(Rect(left, bottom - 2 * r, left + 2 * r, bottom), 180f, -90f, false)
                lineTo(right - r, bottom)
                arcTo(Rect(right - 2 * r, bottom - 2 * r, right, bottom), 90f, -90f, false)
                lineTo(right, 0f)
            }
            drawPath(path, color = color, style = style)
        }
        else -> {
            drawLine(color, Offset(left, 0f), Offset(left, size.height), strokeWidth = stroke)
            drawLine(color, Offset(right, 0f), Offset(right, size.height), strokeWidth = stroke)
        }
    }
}

/** CountBadge is a count in a dot of its colour. */
@Composable
internal fun CountBadge(count: Int, color: Color, modifier: Modifier = Modifier) {
    Surface(color = color, shape = CircleShape, modifier = modifier.heightIn(min = 18.dp)) {
        Text(
            "$count",
            color = Color.White,
            style = MaterialTheme.typography.labelSmall.tabular(),
            fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(horizontal = 5.5.dp, vertical = 1.dp),
            textAlign = TextAlign.Center,
        )
    }
}

/** Loading is a screen waiting for something, saying what. */
@Composable
internal fun Loading(what: String) {
    Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        CircularProgressIndicator(Modifier.size(28.dp), strokeWidth = 2.5.dp)
        Spacer(Modifier.height(16.dp))
        Text(what, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/**
 * Placeholder rows is a list's shape while what it lists comes: rows that
 * breathe, without words, so that nothing reads as there being nothing.
 */
@Composable
internal fun PlaceholderRows(count: Int = 4, label: String = "", modifier: Modifier = Modifier) {
    val pulse = rememberInfiniteTransition(label = "placeholder")
    val a by pulse.animateFloat(
        initialValue = 0.42f,
        targetValue = 0.88f,
        animationSpec = infiniteRepeatable(
            tween(1050, easing = MakoMotion.EmphasizedEasing),
            RepeatMode.Reverse,
        ),
        label = "alpha",
    )
    val shade = MaterialTheme.colorScheme.surfaceContainerHigh
    val divider = MakoColors.subtleDivider
    Column(modifier.padding(horizontal = 16.dp, vertical = 12.dp)) {
        if (label.isNotEmpty()) {
            Text(
                label,
                style = MaterialTheme.typography.labelLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(start = 4.dp, bottom = 10.dp),
            )
        }
        Card(
            Modifier.fillMaxWidth().alpha(a),
            shape = RoundedCornerShape(16.dp),
            colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerLow),
            border = MakoColors.cardBorderStroke,
        ) {
            repeat(count) { i ->
                if (i > 0) HorizontalDivider(Modifier.padding(start = 66.dp), color = divider)
                Row(
                    Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Box(Modifier.size(40.dp).clip(RoundedCornerShape(12.dp)).background(shade))
                    Spacer(Modifier.width(12.dp))
                    Column(Modifier.weight(1f)) {
                        Box(Modifier.fillMaxWidth(0.46f).height(14.dp).clip(RoundedCornerShape(4.dp)).background(shade))
                        Spacer(Modifier.height(8.dp))
                        Box(Modifier.fillMaxWidth(0.76f).height(10.dp).clip(RoundedCornerShape(4.dp)).background(shade))
                    }
                }
            }
        }
    }
}
