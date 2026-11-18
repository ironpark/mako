package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.UiNode
import dev.ironpark.mako.v2.UiSeries
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class BlockChartTest {
    private fun series(name: String, vararg v: Double, tone: String = "") =
        UiSeries.newBuilder().setName(name).setTone(tone).addAllValues(v.toList()).build()

    private fun chart(kind: String, vararg s: UiSeries, labels: List<String> = emptyList(), text: String = "") =
        UiNode.newBuilder().setKind("chart").setChart(kind).addAllSeries(s.toList()).addAllLabels(labels).setText(text).build()

    @Test fun aChartIsCheckedAgainOnThePhone() {
        assertNull(chartModel(chart("pie", series("a", 1.0))))
        val big = (1..10).map { series("s$it", *DoubleArray(300) { k -> k.toDouble() }) }.toTypedArray()
        val m = chartModel(chart("line", *big, labels = List(300) { "l$it" }))!!
        // At most 4 series of 200 points, 600 in all, and 200 labels.
        assertEquals(4, m.series.size)
        assertEquals(listOf(200, 200, 200, 0), m.series.map { it.values.size })
        assertEquals(200, m.labels.size)
        val odd = chartModel(chart("bar", series("n‮a\u0000me", Double.NaN, 1e16, -3.0, Double.POSITIVE_INFINITY, tone = "#ff0000"),
            labels = listOf("a".repeat(40), "b⁦", "c", "d")))!!
        assertEquals(listOf(null, null, -3.0, null), odd.series[0].values)
        assertEquals("", odd.series[0].tone)
        assertEquals("name", odd.series[0].name)
        assertEquals(32, odd.labels[0].length)
        assertEquals("b", odd.labels[1])
        assertTrue(chartModel(chart("sparkline", series("e")))!!.empty)
        assertFalse(chartModel(chart("sparkline", series("one", 5.0)))!!.empty)
        assertEquals("success", chartModel(chart("line", series("t", 1.0, tone = "success")))!!.series[0].tone)
    }

    @Test fun axesTickAtRoundSteps() {
        assertEquals(Axis(0.0, 10.0, 2.5), niceAxis(0.3, 9.7, zero = true))
        assertEquals(listOf(0.0, 2.5, 5.0, 7.5, 10.0), niceAxis(0.3, 9.7, zero = true).ticks)
        assertEquals(Axis(-5.0, 15.0, 5.0), niceAxis(-2.0, 14.0, zero = true))
        // A single value, or a flat line, is widened around it.
        assertEquals(Axis(0.0, 6.0, 2.0), niceAxis(5.0, 5.0, zero = true))
        val flat = niceAxis(3.0, 3.0, zero = false)
        assertTrue(flat.min < 3.0 && flat.max > 3.0)
        assertEquals(Axis(-1.0, 1.0, 0.5), niceAxis(0.0, 0.0, zero = false))
        val tiny = niceAxis(0.001, 0.004, zero = false)
        assertTrue(tiny.step > 0 && tiny.min <= 0.001 && tiny.max >= 0.004)
    }

    @Test fun valuesReadShort() {
        assertEquals("2.5", formatValue(2.5, 2.5))
        assertEquals("10", formatValue(10.0, 2.5))
        assertEquals("1234", formatValue(1234.0))
        assertEquals("12.5k", formatValue(12_500.0))
        assertEquals("3M", formatValue(3_000_000.0, 1_000_000.0))
        assertEquals("1.5T", formatValue(1.5e12))
        assertEquals("-0.4", formatValue(-0.4, 0.2))
        assertEquals("0", formatValue(-0.0, 1.0))
        assertEquals("3.14", formatValue(3.14159))
    }

    @Test fun labelsAreThinnedToFit() {
        assertEquals(1, labelStep(5, 500f, 40f))
        assertEquals(10, labelStep(100, 300f, 30f))
        assertEquals(1, labelStep(1, 10f, 400f))
        assertEquals(3, labelStep(3, 10f, 400f))
    }

    @Test fun aChartIsDescribedByItsSeriesLastValues() {
        val words = ChartWords("Line chart", "Bar chart", "Sparkline", { "Series $it" }, "No data")
        val m = chartModel(chart("line", series("p50", 3.0, 4.5, 5.0), series("", 9.0, Double.NaN), text = "Latency"))!!
        assertEquals("Latency. Line chart. p50: 5 (3–5). Series 2: 9 (9–9)", describeChart(m, words))
        assertEquals("Bar chart. No data", describeChart(chartModel(chart("bar", series("a")))!!, words))
    }
}
