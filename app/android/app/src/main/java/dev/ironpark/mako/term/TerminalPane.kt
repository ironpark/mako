package dev.ironpark.mako.term

import android.graphics.Typeface
import androidx.compose.ui.platform.LocalContext
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.drawscope.drawIntoCanvas
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.dp
import com.termux.terminal.TextStyle
import com.termux.view.TerminalRenderer
import kotlin.math.floor
import kotlin.math.roundToInt

/**
 * TerminalPane draws a session's screen at the session's own size, which
 * its desktop window set: the text is sized for its columns to fill the
 * width, then pinching zooms and dragging pans and scrolls back.
 */
@Composable
fun TerminalPane(term: TermSession, modifier: Modifier = Modifier, onTap: () -> Unit = {}) {
    val frame by term.frame.collectAsState()
    val density = LocalDensity.current
    val context = LocalContext.current
    val font = remember(context) { terminalTypeface(context) }
    var zoom by remember(term) { mutableFloatStateOf(1f) }
    var panX by remember(term) { mutableFloatStateOf(0f) }
    var scroll by remember(term) { mutableFloatStateOf(0f) } // rows back into the scrollback

    BoxWithConstraints(modifier) {
        val width = constraints.maxWidth.toFloat()
        val height = constraints.maxHeight.toFloat()
        val columns = synchronized(term.lock) { term.emulator.mColumns }
        val fit = remember(columns, width, density, font) { fitTextSize(width, columns, with(density) { 7.dp.toPx() }, font) }
        val size = (fit * zoom).roundToInt().coerceIn(4, 96)
        val renderer = remember(size, font) { TerminalRenderer(size, font) }
        val maxPan = (renderer.fontWidth * columns - width).coerceAtLeast(0f)
        // The gestures go on through a pinch, as the renderer changes under them.
        val current by rememberUpdatedState(renderer)
        val bound by rememberUpdatedState(maxPan)

        Canvas(
            Modifier
                .fillMaxSize()
                .pointerInput(term) {
                    detectTapGestures(
                        onTap = { onTap() },
                        onDoubleTap = { zoom = 1f; panX = 0f; scroll = 0f },
                    )
                }
                .pointerInput(term) {
                    detectTransformGestures { _, pan, gesture, _ ->
                        zoom = (zoom * gesture).coerceIn(0.5f, 4f)
                        panX = (panX - pan.x).coerceIn(0f, bound)
                        val back = synchronized(term.lock) { term.emulator.screen.activeTranscriptRows }
                        scroll = (scroll + pan.y / current.fontLineSpacing).coerceIn(0f, back.toFloat())
                    }
                }
        ) {
            frame // drawn again on each change
            drawIntoCanvas { canvas ->
                val native = canvas.nativeCanvas
                synchronized(term.lock) {
                    val emu = term.emulator
                    native.drawColor(emu.mColors.mCurrentColors[TextStyle.COLOR_INDEX_BACKGROUND])
                    val back = emu.screen.activeTranscriptRows
                    val top = -scroll.roundToInt().coerceIn(0, back)
                    // A screen taller than the pane shows its bottom, where the cursor is.
                    val over = (renderer.fontLineSpacing * emu.mRows - height).coerceAtLeast(0f)
                    native.save()
                    native.translate(-panX.coerceAtMost(maxPan), -over)
                    renderer.render(emu, native, top, -1, -1, -1, -1)
                    native.restore()
                }
            }
        }
    }
}

/** The text size whose cells lay columns across width, no smaller than min. */
private fun fitTextSize(width: Float, columns: Int, min: Float, font: Typeface): Int {
    val perPixel = cell(100f, font).width / 100f
    return floor(width / columns / perPixel).toInt().coerceAtLeast(min.roundToInt())
}
