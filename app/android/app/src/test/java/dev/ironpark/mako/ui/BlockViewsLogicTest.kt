package dev.ironpark.mako.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class BlockViewsLogicTest {
    @Test
    fun responsiveGridColumnsClampsAndAdaptsToWidth() {
        assertEquals(1, responsiveGridColumns(0, 360f))
        assertEquals(2, responsiveGridColumns(2, 360f))
        assertEquals(3, responsiveGridColumns(6, 360f))
        assertEquals(1, responsiveGridColumns(4, 150f))
        assertEquals(6, responsiveGridColumns(12, 900f))
        assertEquals(4, responsiveGridColumns(4, 0f))
    }

    @Test
    fun buttonVariantResolvesExplicitAndLegacyTone() {
        assertEquals("ghost", resolveButtonVariant("ghost", "accent"))
        assertEquals("secondary", resolveButtonVariant("secondary", ""))
        assertEquals("primary", resolveButtonVariant("", "accent"))
        assertEquals("danger", resolveButtonVariant("", "danger"))
        assertEquals("outline", resolveButtonVariant("", ""))
    }

    @Test
    fun markdownLinksRouteExternalVsPluginChange() {
        val external = mutableListOf<String>()
        val plugin = mutableListOf<String>()

        handleMarkdownLink("https://example.com/docs", allowPluginLink = true, external::add, plugin::add)
        handleMarkdownLink("http://localhost:8080", allowPluginLink = false, external::add, plugin::add)
        handleMarkdownLink("okf:notes/intro.md", allowPluginLink = true, external::add, plugin::add)
        handleMarkdownLink("notes/ignored.md", allowPluginLink = false, external::add, plugin::add)

        assertEquals(listOf("https://example.com/docs", "http://localhost:8080"), external)
        assertEquals(listOf("okf:notes/intro.md"), plugin)
    }
}
