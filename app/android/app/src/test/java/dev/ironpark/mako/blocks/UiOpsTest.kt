package dev.ironpark.mako.blocks

import dev.ironpark.mako.v2.BlockUpdate
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

internal fun blocksResource(name: String): java.io.InputStream =
    UiOpsTest::class.java.getResourceAsStream("/blocks/$name") ?: error("no fixture $name")

class UiOpsTest {
    /**
     * applyui.pb is what internal/uiblock's TestApplyUiFixtureForAndroid
     * makes: for each case, the tree before, the ops, and the tree after
     * or the daemon's error.
     */
    @Test
    fun appliesAsTheDaemonDoes() {
        val updates = blocksResource("applyui.pb").use { s ->
            generateSequence { BlockUpdate.parseDelimitedFrom(s) }.toList()
        }
        assertTrue("no cases", updates.size >= 3 * 20)
        assertEquals(0, updates.size % 3)
        for ((before, ops, after) in updates.chunked(3)) {
            val name = before.block
            val got = runCatching { applyUi(before.tree, ops.opsList) }
            if (after.error.isNotEmpty()) {
                val e = got.exceptionOrNull()
                if (e !is UiOpException) fail("$name: applied, where the daemon fails with ${after.error}")
                assertEquals(name, after.error, e!!.message)
            } else {
                assertEquals(name, after.tree, got.getOrElse { throw AssertionError("$name: ${it.message}") })
            }
        }
    }
}
