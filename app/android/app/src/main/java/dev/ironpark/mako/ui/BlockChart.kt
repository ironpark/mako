package dev.ironpark.mako.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.text.TextMeasurer
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.UiNode
import dev.ironpark.mako.workspace.Look
import java.util.Locale
import kotlin.math.abs
import kotlin.math.ceil
import kotlin.math.floor
import kotlin.math.log10
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow

// A chart node of a block's tree (revision 33; docs/plugins.md): a line or
// bar chart of its series over its labels, or a sparkline, a line alone,
// as tall as a line of text, as the desktop draws them
// (internal/gui/pluginchart.go). The tree comes from another machine: the
// daemon there checks a chart's bounds, and the app checks them again
// rather than trust them, keeping what is within them.

/** The bounds of a chart, internal/uiblock's. */
private const val MAX_SERIES = 4
private const val MAX_POINTS = 200
private const val MAX_CHART_POINTS = 600
private const val MAX_LABEL = 32
private const val MAX_SERIES_NAME = 64
private const val MAX_VALUE = 1e15

/** The kinds of chart the app draws. */
internal val CHARTS = setOf("line", "bar", "sparkline")

/** The colour tokens a series may name. */
private val TONES = setOf("muted", "faint", "accent", "success", "warning", "danger")

/** A chart's series, as drawn: its name, its colour token, "" for the theme's next, and its values, null where one is not a number in bounds. */
internal data class ChartSeries(val name: String, val tone: String, val values: List<Double?>)

/** A chart node, its bounds checked: what kind, its series, the labels along its axis, and its caption. */
internal data class ChartModel(val kind: String, val series: List<ChartSeries>, val labels: List<String>, val caption: String) {
    /** How many points the longest series has. */
    val points: Int get() = series.maxOfOrNull { it.values.size } ?: 0

    /** The values that are numbers, of every series. */
    val numbers: List<Double> get() = series.flatMap { s -> s.values.filterNotNull() }

    /** Whether there is nothing to draw. */
    val empty: Boolean get() = numbers.isEmpty()
}

/** Text from another machine, fit to show: without control or reordering characters, at most max characters. */
internal fun cleanText(s: String, max: Int): String =
    s.filterNot { Character.isISOControl(it) || it in '‎'..'‏' || it in '‪'..'‮' || it in '⁦'..'⁩' }.take(max)

/**
 * n as a chart, its bounds checked again: at most 4 series of 200 values,
 * 600 in all, each a finite number within ±10^15 or else a gap; labels and
 * names short and without control or reordering characters; tones the
 * tokens. Null for a chart the app does not know, which it does not draw.
 */
internal fun chartModel(n: UiNode): ChartModel? {
    if (n.chart !in CHARTS) return null
    var left = MAX_CHART_POINTS
    val series = n.seriesList.take(MAX_SERIES).map { s ->
        val values = s.valuesList.take(min(MAX_POINTS, left)).map { v -> v.takeIf { it.isFinite() && abs(it) <= MAX_VALUE } }
        left -= values.size
        ChartSeries(cleanText(s.name, MAX_SERIES_NAME), s.tone.takeIf { it in TONES }.orEmpty(), values)
    }
    return ChartModel(n.chart, series, n.labelsList.take(MAX_POINTS).map { cleanText(it, MAX_LABEL) }, cleanText(n.text, 512))
}

/** A value axis: from min to max, with ticks at step apart. */
internal data class Axis(val min: Double, val max: Double, val step: Double) {
    val ticks: List<Double> get() = generateSequence(min) { it + step }.takeWhile { it <= max + step / 2 }.toList()
}

/**
 * An axis that holds lo to hi, ticked about count times at a step of 1, 2,
 * 2.5 or 5 times a power of ten; with zero, when zero is to be in it, as a
 * bar chart's is. A span of nothing is widened around its one value.
 */
internal fun niceAxis(lo: Double, hi: Double, zero: Boolean, count: Int = 4): Axis {
    var a = if (zero) min(lo, 0.0) else lo
    var b = if (zero) max(hi, 0.0) else hi
    if (b - a <= 0.0) {
        val pad = if (a == 0.0) 1.0 else abs(a) * 0.1
        if (!zero || a != 0.0) a -= pad
        b += pad
        if (zero) {
            a = min(a, 0.0)
            b = max(b, 0.0)
        }
    }
    val raw = (b - a) / count
    val mag = 10.0.pow(floor(log10(raw)))
    val step = listOf(1.0, 2.0, 2.5, 5.0, 10.0).map { it * mag }.first { it >= raw * (1 - 1e-9) }
    return Axis(floor(a / step + 1e-9) * step, ceil(b / step - 1e-9) * step, step)
}

/** v as an axis or a reader shows it: short, with k, M, B and T for thousands up, and no more decimals than step needs. */
internal fun formatValue(v: Double, step: Double = 0.0): String {
    val a = abs(v)
    val (scaled, unit) = when {
        a >= 1e12 -> v / 1e12 to "T"
        a >= 1e9 -> v / 1e9 to "B"
        a >= 1e6 -> v / 1e6 to "M"
        a >= 1e4 -> v / 1e3 to "k"
        else -> v to ""
    }
    val divisor = when (unit) { "T" -> 1e12; "B" -> 1e9; "M" -> 1e6; "k" -> 1e3; else -> 1.0 }
    // As many decimals as the step has, so that ticks 2.5 apart read 2.5 and 7.5; a value alone, up to two.
    fun places(x: Double, most: Int) = (0..most).firstOrNull { d ->
        val y = x * 10.0.pow(d)
        abs(y - Math.round(y)) < 1e-6 * max(1.0, abs(y))
    } ?: most
    val decimals = if (step > 0) places(step / divisor, 3) else places(scaled, 2)
    val text = String.format(Locale.ROOT, "%.${decimals}f", scaled).let { if ('.' in it) it.trimEnd('0').trimEnd('.') else it }
    return (if (text == "-0") "0" else text) + unit
}

/** Every step-th label shown, so that labels of width each fit side by side in room; at least the first. */
internal fun labelStep(count: Int, room: Float, width: Float): Int {
    if (count <= 1 || width <= 0f) return 1
    val fit = max(1, (room / width).toInt())
    return max(1, ceil(count / fit.toDouble()).toInt())
}

/** The words a chart is described with, in the app's language. */
internal data class ChartWords(val line: String, val bar: String, val sparkline: String, val series: (Int) -> String, val noData: String)

/**
 * What a screen reader says of a chart: its caption, its kind, and each
 * series by its name with its last value and its range.
 */
internal fun describeChart(m: ChartModel, w: ChartWords): String {
    val kind = when (m.kind) { "bar" -> w.bar; "sparkline" -> w.sparkline; else -> w.line }
    if (m.empty) return listOf(m.caption, kind, w.noData).filter { it.isNotEmpty() }.joinToString(". ")
    val each = m.series.mapIndexed { i, s ->
        val values = s.values.filterNotNull()
        val name = s.name.ifEmpty { w.series(i + 1) }
        if (values.isEmpty()) "$name: ${w.noData}"
        else "$name: ${formatValue(values.last())} (${formatValue(values.min())}–${formatValue(values.max())})"
    }
    return (listOf(m.caption, kind).filter { it.isNotEmpty() } + each).joinToString(". ")
}

/** The theme's chart colours, for a series that names no tone: the look colours of projects, as the desktop's chart colours run. */
private val chartPalette = listOf("blue", "teal", "amber", "purple").map { Look("", it).tint() }

@Composable
private fun seriesColor(s: ChartSeries, i: Int): Color = if (s.tone.isNotEmpty()) toneColor(s.tone) else chartPalette[i % chartPalette.size]

/**
 * UiChart draws a chart node: its caption, a legend for more than one
 * series, and the chart; a sparkline alone, as tall as a line of text.
 * One the app does not know shows that it cannot be shown here.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun UiChart(n: UiNode, modifier: Modifier = Modifier) {
    val m = remember(n) { chartModel(n) }
    if (m == null) {
        UnknownNode(modifier)
        return
    }
    val words = ChartWords(
        stringResource(R.string.chart_line), stringResource(R.string.chart_bar), stringResource(R.string.chart_sparkline),
        series = { "" }, noData = stringResource(R.string.chart_no_data),
    )
    val seriesWord = stringResource(R.string.chart_series)
    val description = describeChart(m, words.copy(series = { String.format(seriesWord, it) }))
    val colors = m.series.mapIndexed { i, s -> seriesColor(s, i) }
    val axisText = MaterialTheme.typography.labelSmall.copy(color = MaterialTheme.colorScheme.onSurfaceVariant)
    val grid = MaterialTheme.colorScheme.outlineVariant
    val zeroLine = MaterialTheme.colorScheme.outline
    val measurer = rememberTextMeasurer()
    Column(modifier.fillMaxWidth().clearAndSetSemantics { contentDescription = description }) {
        if (m.caption.isNotEmpty()) Text(
            m.caption, style = if (n.size == "large") MaterialTheme.typography.titleSmall else MaterialTheme.typography.bodySmall,
            color = toneColor(n.tone.ifEmpty { "muted" }), modifier = Modifier.padding(bottom = 6.dp),
        )
        if (m.series.size > 1 && m.kind != "sparkline") FlowRow(
            horizontalArrangement = Arrangement.spacedBy(12.dp), modifier = Modifier.padding(bottom = 6.dp),
        ) {
            m.series.forEachIndexed { i, s ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Dot(colors[i], size = 8.dp)
                    Spacer(Modifier.width(5.dp))
                    Text(s.name.ifEmpty { String.format(seriesWord, i + 1) }, style = axisText)
                }
            }
        }
        when {
            m.empty -> Text(stringResource(R.string.chart_no_data), style = axisText, modifier = Modifier.padding(vertical = 8.dp))
            m.kind == "sparkline" -> Canvas(Modifier.fillMaxWidth().height(28.dp)) { sparkline(m, colors) }
            else -> Canvas(Modifier.fillMaxWidth().height(168.dp)) { chart(m, colors, measurer, axisText, grid, zeroLine) }
        }
    }
}

/** A sparkline: each series a thin line over its own range, its last point marked. */
private fun DrawScope.sparkline(m: ChartModel, colors: List<Color>) {
    val numbers = m.numbers
    var lo = numbers.min()
    var hi = numbers.max()
    if (hi - lo <= 0.0) { lo -= 1; hi += 1 }
    val pad = 3.dp.toPx()
    val h = size.height - 2 * pad
    m.series.forEachIndexed { i, s ->
        val n = s.values.size
        fun x(k: Int) = if (n <= 1) size.width / 2 else k * (size.width - 2 * pad) / (n - 1) + pad
        fun y(v: Double) = pad + (h * (1 - (v - lo) / (hi - lo))).toFloat()
        line(s, colors[i], 1.5.dp.toPx(), ::x, ::y, mark = true)
    }
}

/** A line or bar chart: ticks and grid along the values, labels along the points, then the series. */
private fun DrawScope.chart(m: ChartModel, colors: List<Color>, measurer: TextMeasurer, text: TextStyle, grid: Color, zeroLine: Color) {
    val numbers = m.numbers
    val bars = m.kind == "bar"
    val axis = niceAxis(numbers.min(), numbers.max(), zero = bars)
    val ticks = axis.ticks
    val tickTexts = ticks.map { measurer.measure(formatValue(it, axis.step), text) }
    val gutter = (tickTexts.maxOfOrNull { it.size.width } ?: 0) + 8.dp.toPx()
    val labelHeight = if (m.labels.isEmpty()) 0f else measurer.measure("Ag", text).size.height + 6.dp.toPx()
    val top = (tickTexts.firstOrNull()?.size?.height ?: 0) / 2f
    val plot = Size(size.width - gutter, size.height - labelHeight - top - 2.dp.toPx())
    fun y(v: Double) = top + (plot.height * (1 - (v - axis.min) / (axis.max - axis.min))).toFloat()
    // The values' ticks: a hairline each, the zero line stronger where the values cross it.
    ticks.forEachIndexed { k, t ->
        val yy = y(t)
        drawLine(if (t == 0.0 && (bars || axis.min < 0)) zeroLine else grid, Offset(gutter, yy), Offset(size.width, yy), 1f)
        val tt = tickTexts[k]
        drawText(tt, topLeft = Offset(gutter - 8.dp.toPx() - tt.size.width, yy - tt.size.height / 2f))
    }
    val n = m.points
    val slot = plot.width / max(1, n)
    fun x(k: Int) = when {
        bars -> gutter + slot * (k + 0.5f)
        n <= 1 -> gutter + plot.width / 2
        else -> gutter + 4.dp.toPx() + k * (plot.width - 8.dp.toPx()) / (n - 1)
    }
    // The labels along the points, as many as fit.
    if (m.labels.isNotEmpty()) {
        val widest = m.labels.take(n).maxOf { measurer.measure(it, text).size.width } + 8.dp.toPx()
        val every = labelStep(min(n, m.labels.size), plot.width, widest)
        for (k in 0 until min(n, m.labels.size) step every) {
            val lt = measurer.measure(m.labels[k], text)
            val cx = x(k).coerceIn(gutter + lt.size.width / 2f, size.width - lt.size.width / 2f)
            drawText(lt, topLeft = Offset(cx - lt.size.width / 2f, top + plot.height + 6.dp.toPx()))
        }
    }
    if (bars) {
        val groups = m.series.size
        val width = max(1f, slot * 0.72f / groups)
        val base = y(0.0)
        val radius = CornerRadius(min(2.dp.toPx(), width / 2))
        m.series.forEachIndexed { i, s ->
            s.values.forEachIndexed { k, v ->
                // A gap, or nothing, draws nothing: the zero line says it.
                if (v == null || v == 0.0) return@forEachIndexed
                val left = x(k) - slot * 0.36f + i * width
                val yy = y(v)
                drawRoundRect(colors[i].copy(alpha = 0.85f), Offset(left, min(yy, base)), Size(width * 0.9f, max(1f, abs(base - yy))), radius)
            }
        }
    } else {
        m.series.forEachIndexed { i, s -> line(s, colors[i], 2.dp.toPx(), ::x, ::y, mark = n > 1) }
    }
}

/**
 * Series s as a line through its points, a gap where a value is missing,
 * a lone point a dot; mark puts a dot on its last point.
 */
private fun DrawScope.line(s: ChartSeries, color: Color, width: Float, x: (Int) -> Float, y: (Double) -> Float, mark: Boolean) {
    val path = Path()
    var open = false
    var lastAt: Offset? = null
    var run = 0
    s.values.forEachIndexed { k, v ->
        if (v == null) {
            if (run == 1) lastAt?.let { drawCircle(color, width * 1.4f, it) }
            open = false
            run = 0
            return@forEachIndexed
        }
        val p = Offset(x(k), y(v))
        if (open) path.lineTo(p.x, p.y) else path.moveTo(p.x, p.y)
        open = true
        run++
        lastAt = p
    }
    drawPath(path, color, style = Stroke(width, cap = StrokeCap.Round, join = StrokeJoin.Round))
    val end = lastAt ?: return
    if (run == 1 || mark) drawCircle(color, width * 1.6f, end)
}

/** UnknownNode stands for a node the app cannot draw, a kind newer than it: quietly, saying so. */
@Composable
internal fun UnknownNode(modifier: Modifier = Modifier) {
    Row(
        modifier.clip(RoundedCornerShape(8.dp)).background(MaterialTheme.colorScheme.surfaceContainerHigh)
            .padding(horizontal = 10.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Outlined.Info, null, Modifier.size(14.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.width(6.dp))
        Text(stringResource(R.string.block_unknown_kind), style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
