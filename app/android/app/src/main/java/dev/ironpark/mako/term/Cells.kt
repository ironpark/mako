package dev.ironpark.mako.term

import android.graphics.Paint
import android.graphics.Typeface
import kotlin.math.ceil

/** The terminal's typeface, as its renderer draws it. */
val TerminalTypeface: Typeface = Typeface.MONOSPACE

/**
 * Cell is the size of a character cell of text textSize pixels high, as
 * TerminalRenderer lays them: a character's width, and a line's height.
 */
data class Cell(val width: Float, val height: Int)

fun cell(textSize: Float): Cell {
    val paint = Paint().apply {
        typeface = TerminalTypeface
        this.textSize = textSize
    }
    return Cell(paint.measureText("X"), ceil(paint.fontSpacing).toInt())
}
