package dev.ironpark.mako.blocks

import dev.ironpark.mako.net.MakoException
import dev.ironpark.mako.v2.BlockUpdate
import dev.ironpark.mako.v2.UiNode
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

class BlockFeedTest {
    private fun input(value: String) = UiNode.newBuilder().setId("q").setKind("input").setValue(value).setChange(true).build()
    private fun tree(value: String) = UiNode.newBuilder().setId("").setKind("column").addChildren(input(value)).build()

    /** A field entered and left untouched sends no change, nor one whose value the block's answer set. */
    @Test
    fun leavingAnInputSendsOnlyWhatTheUserChanged() {
        val form = BlockForm()
        form.sync(tree("a"))
        assertNull(form.leave(input("a")))
        form.set("q", "b")
        assertEquals("b", form.leave(input("a")))
        assertNull(form.leave(input("a")))
        // The block answers the change with a value of its own.
        form.sync(tree("c"))
        assertEquals("c", form.value(input("c")))
        assertNull(form.leave(input("c")))
        form.set("q", "d")
        assertEquals("d", form.submit(input("c")))
        assertNull(form.leave(input("c")))
    }

    @Test
    fun textareaAndTabsValuesAreTrackedInBlockForm() {
        val ta = UiNode.newBuilder().setId("/note").setKind("textarea").setValue("first").setChange(true).build()
        val tabs = UiNode.newBuilder().setId("/mode").setKind("tabs").setValue("Board").addOptions("Board").addOptions("Table").setChange(true).build()
        val root = UiNode.newBuilder().setId("").setKind("column").addChildren(ta).addChildren(tabs).build()
        val form = BlockForm()
        form.sync(root)
        form.set("/note", "second\nline")
        form.set("/mode", "Table")
        val vals = form.values(root)
        assertEquals("second\nline", vals["/note"])
        assertEquals("Table", vals["/mode"])
    }

    private val shown = BlockState(BlockUpdate.newBuilder().setTree(tree("a")).build(), tree("a"))

    /** A page coming back with the link down keeps its tree, with why. */
    @Test
    fun aBrokenStreamKeepsTheTreeShown() = runBlocking {
        val got = followUpdates(shown) { failing(IOException("no route")) }.first()
        assertEquals(tree("a"), got.tree)
        assertEquals("no route", got.broken)
        assertFalse(got.missing)
    }

    /** A plugin the computer does not have ends the flow, where a stream that breaks is tried again. */
    @Test
    fun aMissingPluginIsNotTriedAgain() = runBlocking {
        var watched = 0
        val got = withTimeout(5_000) {
            followUpdates(BlockState()) {
                watched++
                failing(MakoException("no plugin x on this node", MakoException.NO_PLUGIN))
            }.toList()
        }
        assertEquals(1, watched)
        assertEquals(1, got.size)
        assertTrue(got.single().missing)
    }

    private fun failing(e: Exception): Flow<BlockUpdate> = flow { throw e }
}
