package dev.ironpark.mako.net

import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.Closeable

class ReadFlowTest {
    private class Numbers(val count: Int) : Closeable {
        var next = 0
        var closed = false
        override fun close() {
            closed = true
        }
    }

    /**
     * A collector slower than the stream gets every value, the last too:
     * the reader waits for it rather than drop what it has no room for.
     */
    @Test
    fun aSlowCollectorMissesNothing() = runBlocking {
        val src = Numbers(1_000)
        val got = readFlow({ src }, read = { it.next++ }, last = { it == src.count - 1 })
            .map { if (it == 0) Thread.sleep(300); it }
            .toList()
        assertEquals((0 until src.count).toList(), got)
        assertTrue(src.closed)
    }
}
