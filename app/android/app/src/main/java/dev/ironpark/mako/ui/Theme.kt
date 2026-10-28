package dev.ironpark.mako.ui

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

private val Violet = Color(0xFF7C66E8)
private val VioletLight = Color(0xFFB8A6FF)

/** MakoTheme is Material's, in the wallpaper's colours where Android has them. */
@Composable
fun MakoTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val context = LocalContext.current
    val colors = when {
        Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        dark -> darkColorScheme(primary = VioletLight, secondary = VioletLight)
        else -> lightColorScheme(primary = Violet, secondary = Violet)
    }
    MaterialTheme(colorScheme = colors, content = content)
}

/** The colours of a session's status, as the desktop's window shows them. */
object StatusColors {
    val Working = Color(0xFF3B82F6)
    val Waiting = Color(0xFFF59E0B)
    val Done = Color(0xFF22C55E)
    val Idle = Color(0xFF9CA3AF)
    val Approval = Color(0xFFEF4444)
}
