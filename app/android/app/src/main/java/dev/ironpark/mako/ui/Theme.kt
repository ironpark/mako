package dev.ironpark.mako.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

// The app's colours are the desktop's: neutral greys, a near-black or
// near-white for what acts, and no tint of the wallpaper's, so that the
// colours of the projects' looks and of the sessions' statuses are the
// only colours on screen, and read as what they mean.

private val Light: ColorScheme = lightColorScheme(
    primary = Color(0xFF1B1B1D), onPrimary = Color(0xFFFFFFFF),
    primaryContainer = Color(0xFFE4E4E7), onPrimaryContainer = Color(0xFF1B1B1D),
    secondary = Color(0xFF52525B), onSecondary = Color(0xFFFFFFFF),
    secondaryContainer = Color(0xFFE7E7EA), onSecondaryContainer = Color(0xFF1B1B1D),
    tertiary = Color(0xFF52525B), onTertiary = Color(0xFFFFFFFF),
    tertiaryContainer = Color(0xFFE7E7EA), onTertiaryContainer = Color(0xFF1B1B1D),
    background = Color(0xFFF8F8F9), onBackground = Color(0xFF18181B),
    surface = Color(0xFFF8F8F9), onSurface = Color(0xFF18181B),
    surfaceVariant = Color(0xFFE9E9EC), onSurfaceVariant = Color(0xFF5F5F67),
    surfaceTint = Color(0xFF1B1B1D),
    surfaceBright = Color(0xFFFFFFFF), surfaceDim = Color(0xFFE2E2E5),
    surfaceContainerLowest = Color(0xFFFFFFFF), surfaceContainerLow = Color(0xFFF2F2F4),
    surfaceContainer = Color(0xFFEDEDF0), surfaceContainerHigh = Color(0xFFE7E7EA),
    surfaceContainerHighest = Color(0xFFE1E1E5),
    inverseSurface = Color(0xFF27272A), inverseOnSurface = Color(0xFFF4F4F5), inversePrimary = Color(0xFFE4E4E7),
    outline = Color(0xFFB4B4BB), outlineVariant = Color(0xFFDDDDE1),
    error = Color(0xFFC62828), onError = Color(0xFFFFFFFF),
    errorContainer = Color(0xFFFBE3E1), onErrorContainer = Color(0xFF7A1C1C),
    scrim = Color(0xFF000000),
)

private val Dark: ColorScheme = darkColorScheme(
    primary = Color(0xFFEDEDEF), onPrimary = Color(0xFF151517),
    primaryContainer = Color(0xFF2E2E33), onPrimaryContainer = Color(0xFFEDEDEF),
    secondary = Color(0xFFB4B4BC), onSecondary = Color(0xFF151517),
    secondaryContainer = Color(0xFF2B2B30), onSecondaryContainer = Color(0xFFE8E8EB),
    tertiary = Color(0xFFB4B4BC), onTertiary = Color(0xFF151517),
    tertiaryContainer = Color(0xFF2B2B30), onTertiaryContainer = Color(0xFFE8E8EB),
    background = Color(0xFF131315), onBackground = Color(0xFFE6E6E9),
    surface = Color(0xFF131315), onSurface = Color(0xFFE6E6E9),
    surfaceVariant = Color(0xFF2A2A2F), onSurfaceVariant = Color(0xFF9E9EA7),
    surfaceTint = Color(0xFFEDEDEF),
    surfaceBright = Color(0xFF38383D), surfaceDim = Color(0xFF131315),
    surfaceContainerLowest = Color(0xFF0E0E10), surfaceContainerLow = Color(0xFF1A1A1D),
    surfaceContainer = Color(0xFF1E1E21), surfaceContainerHigh = Color(0xFF252529),
    surfaceContainerHighest = Color(0xFF2E2E33),
    inverseSurface = Color(0xFFE6E6E9), inverseOnSurface = Color(0xFF1B1B1D), inversePrimary = Color(0xFF27272A),
    outline = Color(0xFF55555D), outlineVariant = Color(0xFF2F2F35),
    error = Color(0xFFF2726F), onError = Color(0xFF2B0B0B),
    errorContainer = Color(0xFF4A1E1E), onErrorContainer = Color(0xFFFFD9D6),
    scrim = Color(0xFF000000),
)

/** MakoTheme is Material's, in the desktop's greys, light or dark as the phone is. */
@Composable
fun MakoTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = if (isSystemInDarkTheme()) Dark else Light, content = content)
}

/** The colours of a session's status, as the desktop's window shows them. */
object StatusColors {
    val Working = Color(0xFF3B82F6)
    val Waiting = Color(0xFFF59E0B)
    val Done = Color(0xFF22C55E)
    val Idle = Color(0xFF9CA3AF)
    val Approval = Color(0xFFEF4444)
}
