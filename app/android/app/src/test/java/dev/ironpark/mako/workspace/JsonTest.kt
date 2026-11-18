package dev.ironpark.mako.workspace

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class JsonTest {
    private fun refused(text: String) = runCatching { Json.raw(text) }.isFailure

    @Test
    fun readsNumbersAsGoDoes() {
        for (bad in listOf(".5", "1.", "01", "+1", "-", "1e", "1e+", "-.5", "0x1", "1.5.5")) assertTrue(bad, refused(bad))
        for (good in listOf("0", "-0", "1.5", "5e1", "1E-2", "-0.5e+3", "1e400")) assertEquals(good, good.toDouble(), Json.parse(good))
    }

    @Test
    fun readsStringsAsGoDoes() {
        assertTrue(refused("\"a\tb\""))
        assertTrue(refused("\"\\u+041\""))
        assertTrue(refused("\"\\x\""))
        assertEquals("a/b\u00e9\n", Json.parse("\"a\\/b\\u00E9\\n\""))
    }

    /** As deep as encoding/json reads, without recursion: a reader takes the members it asks for. */
    @Test
    fun nestsAsDeepAsGoAndSkipsWhatIsNotRead() {
        // In an object, as deep as there is room for.
        val deep = "[".repeat(Json.MAX_DEPTH - 1) + "]".repeat(Json.MAX_DEPTH - 1)
        val doc = Json.raw("""{"far":$deep,"near":"here"}""")
        assertEquals("here", doc.members()!!.getValue("near").string())
        assertTrue(refused("""{"far":[$deep]}"""))
    }

    @Test
    fun findsANumberBeyondADoubleHoweverDeep() {
        val deep = Json.raw("""{"a":["1e400 in a string",""" + "[".repeat(500) + "-2e308" + "]".repeat(500) + "]}")
        assertTrue(deep.overflows())
        assertEquals(false, Json.raw("""{"a":"1e400","b":[1e300]}""").overflows())
    }
}
