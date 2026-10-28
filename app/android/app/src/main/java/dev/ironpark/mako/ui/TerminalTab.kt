package dev.ironpark.mako.ui

import android.view.KeyEvent
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.ContentPaste
import androidx.compose.material.icons.outlined.Keyboard
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import dev.ironpark.mako.R
import dev.ironpark.mako.term.KeyboardView
import dev.ironpark.mako.term.TermSession
import dev.ironpark.mako.term.TerminalPane
import dev.ironpark.mako.term.cell
import kotlinx.coroutines.launch

/**
 * TerminalTab is the session's terminal, with the keys a phone lacks
 * beneath it, and the keyboard typing into it as it types. Each time
 * [fits] goes up, the session is resized to the pane, at a size of text
 * that reads on a phone.
 */
@Composable
fun TerminalTab(term: TermSession, fits: Int, modifier: Modifier = Modifier) {
    val status by term.status.collectAsState()
    var keyboard by remember { mutableStateOf<KeyboardView?>(null) }
    val density = LocalDensity.current

    Column(modifier) {
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth().background(Color.Black)) {
            val w = constraints.maxWidth
            val h = constraints.maxHeight
            LaunchedEffect(fits) {
                if (fits == 0) return@LaunchedEffect
                val c = cell(with(density) { 12.sp.toPx() })
                term.resize((w / c.width).toInt().coerceAtLeast(20), (h / c.height).coerceAtLeast(5))
            }
            TerminalPane(term, Modifier.matchParentSize(), onTap = { keyboard?.showKeyboard() })
            when (val s = status) {
                TermSession.Status.Connecting -> CircularProgressIndicator(Modifier.align(Alignment.Center).size(32.dp))
                is TermSession.Status.Failed -> Banner(stringResource(R.string.attach_failed, s.message))
                is TermSession.Status.Exited -> Banner(stringResource(R.string.exited_code, s.code))
                TermSession.Status.Live -> {}
            }
        }
        Surface(color = MaterialTheme.colorScheme.surfaceContainer) {
            Row(Modifier.fillMaxWidth().navigationBarsPadding(), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = { keyboard?.showKeyboard() }) { Icon(Icons.Outlined.Keyboard, stringResource(R.string.keyboard)) }
                KeyBar(term, Modifier.weight(1f))
            }
        }
        KeyboardSink(term) { keyboard = it }
    }
}

@Composable
private fun Banner(text: String) {
    Box(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.errorContainer).padding(8.dp)) {
        Text(text, color = MaterialTheme.colorScheme.onErrorContainer, style = MaterialTheme.typography.bodySmall)
    }
}

/** KeyboardSink is the unseen view the keyboard types into. */
@Composable
private fun KeyboardSink(term: TermSession, view: (KeyboardView) -> Unit) {
    AndroidView(
        factory = { KeyboardView(it).also(view) },
        update = { it.term = term },
        modifier = Modifier.size(1.dp),
    )
}

/**
 * KeyBar is the keys a phone's keyboard lacks, for the terminal: Ctrl,
 * held for the next key typed, Esc, Tab, the arrows and the keys that
 * move by pages, the characters shells want that keyboards bury, and
 * pasting the clipboard.
 */
@Composable
private fun KeyBar(term: TermSession, modifier: Modifier = Modifier) {
    val ctrl by term.ctrl.collectAsState()
    val clipboard = LocalClipboard.current
    val scope = rememberCoroutineScope()
    val haptic = LocalHapticFeedback.current
    val keys = listOf(
        "Esc" to KeyEvent.KEYCODE_ESCAPE, "Tab" to KeyEvent.KEYCODE_TAB,
        "↑" to KeyEvent.KEYCODE_DPAD_UP, "↓" to KeyEvent.KEYCODE_DPAD_DOWN,
        "←" to KeyEvent.KEYCODE_DPAD_LEFT, "→" to KeyEvent.KEYCODE_DPAD_RIGHT,
        "Home" to KeyEvent.KEYCODE_MOVE_HOME, "End" to KeyEvent.KEYCODE_MOVE_END,
        "PgUp" to KeyEvent.KEYCODE_PAGE_UP, "PgDn" to KeyEvent.KEYCODE_PAGE_DOWN,
    )
    Row(
        modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 4.dp, vertical = 6.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Key("Ctrl", selected = ctrl) { term.toggleCtrl() }
        for ((label, code) in keys) {
            Key(label) {
                haptic.performHapticFeedback(HapticFeedbackType.TextHandleMove)
                term.key(code)
            }
        }
        Key("^C") { term.send("\u0003") }
        for (c in listOf("|", "~", "/", "-")) Key(c) { term.type(c) }
        IconButton(onClick = {
            scope.launch {
                val text = clipboard.getClipEntry()?.clipData?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.text
                if (!text.isNullOrEmpty()) term.paste(text.toString())
            }
        }) { Icon(Icons.Outlined.ContentPaste, stringResource(R.string.paste)) }
    }
}

@Composable
private fun Key(label: String, selected: Boolean = false, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        shape = RoundedCornerShape(8.dp),
        color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surfaceContainerHighest,
        contentColor = if (selected) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurface,
    ) {
        Text(
            label, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.labelLarge,
            modifier = Modifier.defaultMinSize(minWidth = 40.dp).padding(horizontal = 10.dp, vertical = 8.dp),
            textAlign = TextAlign.Center,
        )
    }
}
