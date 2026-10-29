package dev.ironpark.mako.term

import android.content.Context
import dev.ironpark.mako.R
import android.graphics.Paint
import android.graphics.Typeface
import kotlin.math.ceil

/** Bundled, non-ligature D2Coding, shared by rendering and cell measurement. */
fun terminalTypeface(context: Context): Typeface = context.resources.getFont(R.font.d2coding)

/**
 * Cell is the size of a character cell of text textSize pixels high, as
 * TerminalRenderer lays them: a character's width, and a line's height.
 */
data class Cell(val width: Float, val height: Int)

fun cell(textSize: Float, font: Typeface): Cell {
    val paint = Paint().apply {
        typeface = font
        this.textSize = textSize
    }
    return Cell(paint.measureText("X"), ceil(paint.fontSpacing).toInt())
}
