package dev.ironpark.mako.term

import android.content.Context
import android.text.InputType
import android.view.KeyEvent
import android.view.View
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputMethodManager
import com.termux.terminal.KeyHandler

/**
 * KeyboardView takes what the keyboard types and sends it to a session as
 * it comes, as a terminal does: its input connection hands each commit
 * over, Backspace as DEL, Enter as a carriage return. A word an input
 * method is still composing, as Korean's do, goes once it is done.
 */
class KeyboardView(context: Context) : View(context) {
    var term: TermSession? = null

    init {
        isFocusable = true
        isFocusableInTouchMode = true
    }

    override fun onCheckIsTextEditor(): Boolean = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_ACTION_NONE
        return Connection()
    }

    fun showKeyboard() {
        requestFocus()
        context.getSystemService(InputMethodManager::class.java).showSoftInput(this, 0)
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean = key(event) || super.onKeyDown(keyCode, event)

    /**
     * Sends what a key means to the session: the terminal's code for it,
     * with its modifiers, or the character it types, Ctrl making a control
     * character of it; false for a key it does not take.
     */
    private fun key(event: KeyEvent): Boolean {
        val t = term ?: return false
        if (event.keyCode == KeyEvent.KEYCODE_BACK) return false // navigates, though KeyHandler makes it Esc
        if (event.action != KeyEvent.ACTION_DOWN) return true
        var mods = 0
        if (event.isCtrlPressed) mods = mods or KeyHandler.KEYMOD_CTRL
        if (event.isAltPressed) mods = mods or KeyHandler.KEYMOD_ALT
        if (event.isShiftPressed) mods = mods or KeyHandler.KEYMOD_SHIFT
        if (t.key(event.keyCode, mods)) return true
        val c = event.getUnicodeChar(event.metaState and KeyEvent.META_CTRL_MASK.inv())
        if (c == 0) return false
        val typed = String(Character.toChars(c))
        val control = if (event.isCtrlPressed) controlChar(typed[0]) else null
        if (control != null) t.send(control.toString()) else t.type(typed)
        return true
    }

    private inner class Connection : BaseInputConnection(this@KeyboardView, true) {
        override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
            editable?.clear()
            send(text)
            return true
        }

        override fun finishComposingText(): Boolean {
            val composed = editable?.toString().orEmpty()
            super.finishComposingText()
            editable?.clear()
            send(composed)
            return true
        }

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
            // Nothing stays in the buffer: what is deleted was sent.
            if (editable.isNullOrEmpty()) repeat(beforeLength) { term?.send("\u007f") }
            else super.deleteSurroundingText(beforeLength, afterLength)
            return true
        }

        override fun sendKeyEvent(event: KeyEvent): Boolean = key(event) || super.sendKeyEvent(event)

        private fun send(text: CharSequence) {
            if (text.isNotEmpty()) term?.type(text.toString().replace('\n', '\r'))
        }
    }
}
