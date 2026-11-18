package dev.ironpark.mako.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.MenuBook
import androidx.compose.material.icons.outlined.Code
import androidx.compose.material.icons.outlined.FavoriteBorder
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.Inventory2
import androidx.compose.material.icons.outlined.Public
import androidx.compose.material.icons.outlined.RocketLaunch
import androidx.compose.material.icons.outlined.Science
import androidx.compose.material.icons.outlined.SportsEsports
import androidx.compose.material.icons.outlined.Star
import androidx.compose.material.icons.outlined.Storage
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.Icon
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.workspace.Look
import dev.ironpark.mako.workspace.lookRgb
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.sin

/** The colour of a look, among the desktop's look colours. */
internal fun Look.tint(): Color = Color(0xFF000000 or lookRgb(color).toLong())

/** The icons a look names besides its shapes, as Material has them closest to the desktop's. */
private val lookIcons: Map<String, ImageVector> = mapOf(
    "folder" to Icons.Outlined.Folder, "package" to Icons.Outlined.Inventory2, "code-xml" to Icons.Outlined.Code,
    "terminal" to Icons.Outlined.Terminal, "globe" to Icons.Outlined.Public, "book-open" to Icons.AutoMirrored.Outlined.MenuBook,
    "rocket" to Icons.Outlined.RocketLaunch, "flask-conical" to Icons.Outlined.Science, "database" to Icons.Outlined.Storage,
    "gamepad-2" to Icons.Outlined.SportsEsports, "star" to Icons.Outlined.Star, "heart" to Icons.Outlined.FavoriteBorder,
)

/**
 * LookGlyph is a project section's icon in its colour, as the desktop's
 * sidebar marks it: a shape outlined as its icons are drawn, or the icon
 * picked for it.
 */
@Composable
internal fun LookGlyph(look: Look, modifier: Modifier = Modifier, size: Dp = 16.dp) {
    val tint = look.tint()
    lookIcons[look.icon]?.let {
        Icon(it, null, modifier.size(size), tint = tint)
        return
    }
    val sides = when {
        look.icon.startsWith("shape-triangle") -> 3
        look.icon.startsWith("shape-square") -> 4
        look.icon.startsWith("shape-pentagon") -> 5
        else -> 6
    }
    val turned = look.icon.endsWith("-45")
    // The desktop's shapes (internal/gui/iconset): on a 24-unit square, a
    // radius of 8.5 about (12, 12.4), a point up, or 45° round from it; an
    // upright square stands on a side.
    val start = when {
        sides == 4 -> if (turned) -90.0 else -45.0
        turned -> -45.0
        else -> -90.0
    }
    Canvas(modifier.size(size)) {
        val unit = this.size.minDimension / 24f
        val path = Path()
        for (k in 0 until sides) {
            val a = (start + 360.0 * k / sides) * PI / 180
            val p = Offset((12 + 8.5 * cos(a)).toFloat() * unit, (12.4 + 8.5 * sin(a)).toFloat() * unit)
            if (k == 0) path.moveTo(p.x, p.y) else path.lineTo(p.x, p.y)
        }
        path.close()
        drawPath(path, tint, style = Stroke(width = 2 * unit, join = StrokeJoin.Round))
    }
}
