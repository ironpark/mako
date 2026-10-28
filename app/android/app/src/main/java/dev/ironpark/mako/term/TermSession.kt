package dev.ironpark.mako.term

import android.util.Log
import com.termux.terminal.KeyHandler
import com.termux.terminal.TerminalEmulator
import com.termux.terminal.TerminalOutput
import com.termux.terminal.TerminalSession
import com.termux.terminal.TerminalSessionClient
import com.google.protobuf.ByteString
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.net.Stream
import dev.ironpark.mako.net.envelope
import dev.ironpark.mako.v2.Attach
import dev.ironpark.mako.v2.Input
import dev.ironpark.mako.v2.Resize
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.ByteArrayOutputStream
import java.io.IOException

/**
 * TermSession is a session attached: a stream to it through the node, and
 * the terminal its output draws, which the screen shows. What the terminal
 * sends back, and what is typed, goes to the session as input. A stream
 * that breaks is attached again, with the screen replayed.
 *
 * The emulator is not safe across threads: everything that touches it
 * holds [lock], the drawing too.
 */
class TermSession(
    private val daemon: Daemon,
    val sessionId: String,
    val nodeId: String,
    private val scope: CoroutineScope,
) : TerminalOutput() {
    sealed interface Status {
        data object Connecting : Status
        data object Live : Status
        data class Exited(val code: Long) : Status
        data class Failed(val message: String) : Status
    }

    val lock = Any()

    private val _frame = MutableStateFlow(0L)
    /** Changes whenever the screen does, for the view to draw again. */
    val frame: StateFlow<Long> = _frame.asStateFlow()

    private val _status = MutableStateFlow<Status>(Status.Connecting)
    val status: StateFlow<Status> = _status.asStateFlow()

    /** The terminal, made after the flows its callbacks touch, and again at the session's size by each snapshot. */
    var emulator: TerminalEmulator = TerminalEmulator(this, 80, 24, TRANSCRIPT, Client)
        private set

    private val outbox = Channel<ByteArray>(Channel.UNLIMITED)
    @Volatile private var stream: Stream? = null
    private var job: Job? = null

    fun start() {
        if (job != null) return
        job = scope.launch(Dispatchers.IO) {
            launch { pump() }
            while (isActive) {
                try {
                    attach()
                    return@launch // exited
                } catch (e: IOException) {
                    if (!isActive) return@launch
                    Log.d("mako", "attach $sessionId: ${e.message}")
                    _status.value = Status.Failed(e.message ?: e.toString())
                }
                delay(2000)
                _status.value = Status.Connecting
            }
        }
    }

    fun stop() {
        job?.cancel()
        job = null
        stream?.close()
        stream = null
    }

    /** Attaches, and reads the session's output until it ends. */
    private fun attach() {
        val s = daemon.open()
        stream = s
        try {
            s.send(envelope {
                attach = Attach.newBuilder().setSessionId(sessionId).setNodeId(nodeId).setReplay(true).build()
            })
            while (true) {
                val env = s.recv()
                when {
                    env.hasSnapshot() -> {
                        val snap = env.snapshot
                        synchronized(lock) {
                            emulator = TerminalEmulator(
                                this, snap.cols.coerceAtLeast(1), snap.rows.coerceAtLeast(1),
                                TRANSCRIPT, Client,
                            )
                        }
                        _status.value = Status.Live
                        changed()
                    }
                    env.hasOutput() -> {
                        val data = env.output.data.toByteArray()
                        synchronized(lock) { emulator.append(data, data.size) }
                        _status.value = Status.Live
                        changed()
                    }
                    env.hasExited() -> {
                        _status.value = Status.Exited(env.exited.exitCode)
                        return
                    }
                }
            }
        } finally {
            s.close()
            if (stream === s) stream = null
        }
    }

    /** Sends what is queued for the session, a batch to a frame. */
    private suspend fun pump() {
        val batch = ByteArrayOutputStream()
        for (first in outbox) {
            batch.reset()
            batch.write(first)
            while (true) batch.write(outbox.tryReceive().getOrNull() ?: break)
            val s = stream ?: continue // not attached: what was typed is lost, as on a terminal
            try {
                s.send(envelope {
                    input = Input.newBuilder().setSessionId(sessionId).setData(ByteString.copyFrom(batch.toByteArray())).build()
                })
            } catch (e: IOException) {
                s.close() // the reader attaches again
            }
        }
    }

    private fun changed() {
        _frame.value = _frame.value + 1
    }

    private val _ctrl = MutableStateFlow(false)
    /** Whether Ctrl is held for the next key typed, as the key bar's Ctrl holds it. */
    val ctrl: StateFlow<Boolean> = _ctrl.asStateFlow()

    fun toggleCtrl() {
        _ctrl.value = !_ctrl.value
    }

    /**
     * Sends what the keyboard typed: with Ctrl held, its first character
     * as its control character, which lets Ctrl go.
     */
    fun type(text: String) {
        if (text.isEmpty()) return
        if (_ctrl.value) {
            _ctrl.value = false
            val control = controlChar(text[0])
            if (control != null) {
                send(control + text.substring(1))
                return
            }
        }
        send(text)
    }

    /**
     * Sends what key means to the terminal, with mods of KeyHandler's
     * KEYMOD bits, in the modes the program set; false for a key that
     * types nothing a terminal knows.
     */
    fun key(keyCode: Int, mods: Int = 0): Boolean {
        val code = synchronized(lock) {
            KeyHandler.getCode(keyCode, mods, emulator.isCursorKeysApplicationMode, emulator.isKeypadApplicationMode)
        } ?: return false
        send(code)
        return true
    }

    /** Sends bytes to the session, as typed. */
    fun send(bytes: ByteArray) {
        outbox.trySend(bytes)
    }

    fun send(text: String) = send(text.toByteArray())

    /** Pastes text, as the session's program asked for pastes, and presses Enter. */
    fun submit(text: String) {
        paste(text)
        send("\r")
    }

    /** Pastes text as the program asked for pastes. */
    fun paste(text: String) {
        synchronized(lock) { emulator.paste(text) }
    }

    /**
     * Follows the session's size, as another window changes it: the
     * screen's output is laid out for it.
     */
    fun follow(cols: Int, rows: Int) {
        if (cols <= 0 || rows <= 0) return
        synchronized(lock) {
            if (emulator.mColumns == cols && emulator.mRows == rows) return
            emulator.resize(cols, rows)
        }
        changed()
    }

    /** Resizes the session itself, for every window showing it. */
    fun resize(cols: Int, rows: Int) {
        val s = stream ?: return
        scope.launch(Dispatchers.IO) {
            runCatching {
                s.send(envelope {
                    resize = Resize.newBuilder().setSessionId(sessionId).setCols(cols).setRows(rows).build()
                })
            }
        }
        follow(cols, rows)
    }

    // What the emulator sends back goes to the session, as a terminal's does.
    override fun write(data: ByteArray, offset: Int, count: Int) = send(data.copyOfRange(offset, offset + count))

    override fun titleChanged(oldTitle: String?, newTitle: String?) {}
    override fun onCopyTextToClipboard(text: String?) {}
    override fun onPasteTextFromClipboard() {}
    override fun onBell() {}
    override fun onColorsChanged() = changed()

    /** The emulator's client, for its logs and cursor alone. */
    private object Client : TerminalSessionClient {
        override fun onTextChanged(changedSession: TerminalSession) {}
        override fun onTitleChanged(changedSession: TerminalSession) {}
        override fun onSessionFinished(finishedSession: TerminalSession) {}
        override fun onCopyTextToClipboard(session: TerminalSession, text: String?) {}
        override fun onPasteTextFromClipboard(session: TerminalSession?) {}
        override fun onBell(session: TerminalSession) {}
        override fun onColorsChanged(session: TerminalSession) {}
        override fun onTerminalCursorStateChange(state: Boolean) {}
        override fun getTerminalCursorStyle(): Int = TerminalEmulator.TERMINAL_CURSOR_STYLE_BLOCK
        override fun logError(tag: String?, message: String?) {}
        override fun logWarn(tag: String?, message: String?) {}
        override fun logInfo(tag: String?, message: String?) {}
        override fun logDebug(tag: String?, message: String?) {}
        override fun logVerbose(tag: String?, message: String?) {}
        override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) {}
        override fun logStackTrace(tag: String?, e: Exception?) {}
    }

    companion object {
        /** Lines of scrollback kept. */
        const val TRANSCRIPT = 2000
    }
}

/** The control character Ctrl makes of c, as a terminal's keyboard does; null for none. */
fun controlChar(c: Char): Char? = when (val l = c.lowercaseChar()) {
    in 'a'..'z' -> Char(l - 'a' + 1)
    ' ', '@', '2' -> '\u0000'
    '[', '3' -> '\u001b'
    '\\', '4' -> '\u001c'
    ']', '5' -> '\u001d'
    '^', '6' -> '\u001e'
    '_', '-', '7' -> '\u001f'
    '?', '8' -> '\u007f'
    else -> null
}
