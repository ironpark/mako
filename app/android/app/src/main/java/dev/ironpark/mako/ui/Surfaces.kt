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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
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
            Surface(shape = RoundedCornerShape(20.dp), color = MaterialTheme.colorScheme.surfaceContainerHigh, modifier = Modifier.size(64.dp)) {
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
            Modifier.fillMaxWidth(), shape = RoundedCornerShape(16.dp),
            colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerLow),
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

/** CountBadge is a count in a dot of its colour. */
@Composable
internal fun CountBadge(count: Int, color: Color, modifier: Modifier = Modifier) {
    Surface(color = color, shape = CircleShape, modifier = modifier.heightIn(min = 18.dp)) {
        Text(
            "$count", color = Color.White, style = MaterialTheme.typography.labelSmall, fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(horizontal = 5.dp, vertical = 1.dp), textAlign = TextAlign.Center,
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
    val a by pulse.animateFloat(0.45f, 0.9f, infiniteRepeatable(tween(900), RepeatMode.Reverse), label = "alpha")
    val shade = MaterialTheme.colorScheme.surfaceContainerHigh
    Column(modifier.padding(horizontal = 16.dp, vertical = 8.dp).alpha(a)) {
        if (label.isNotEmpty()) Text(label, style = MaterialTheme.typography.labelLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp, bottom = 12.dp))
        repeat(count) {
            Row(Modifier.fillMaxWidth().padding(vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(40.dp).clip(RoundedCornerShape(12.dp)).background(shade))
                Spacer(Modifier.width(14.dp))
                Column(Modifier.weight(1f)) {
                    Box(Modifier.fillMaxWidth(0.5f).height(14.dp).clip(RoundedCornerShape(4.dp)).background(shade))
                    Spacer(Modifier.height(8.dp))
                    Box(Modifier.fillMaxWidth(0.8f).height(10.dp).clip(RoundedCornerShape(4.dp)).background(shade))
                }
            }
        }
    }
}
