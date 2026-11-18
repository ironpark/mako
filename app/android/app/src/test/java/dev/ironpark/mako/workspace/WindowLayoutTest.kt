package dev.ironpark.mako.workspace

import com.google.protobuf.ByteString
import dev.ironpark.mako.blocks.blocksResource
import dev.ironpark.mako.v2.LayoutEntry
import dev.ironpark.mako.v2.WorkspaceState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class WindowLayoutTest {
    /**
     * pages.json is what internal/layoutdoc's TestPagesFixtureForAndroid
     * makes: documents, whether a window refuses each, and the pages a
     * phone shows for it.
     */
    @Suppress("UNCHECKED_CAST")
    @Test
    fun foldsAsTheDaemonDoes() {
        val cases = Json.parse(String(blocksResource("pages.json").use { it.readBytes() })) as List<Map<String, Any?>>
        for (c in cases) {
            val name = c["name"] as String
            val doc = c["doc"] as String
            val refused = runCatching { LayoutDoc.parse(doc) }.isFailure
            if (doc.isNotEmpty()) assertEquals("$name: refused", c["refused"], refused)
            val want = (c["pages"] as List<Map<String, Any?>>).map { LayoutPage(it["id"] as String, it["block"] as String) }
            assertEquals(name, want, layoutPages(doc))
        }
    }

    @Test
    fun aSpaceShowsTheDocumentItNamesOrTheDefault() {
        fun entry(doc: String, deleted: Boolean = false) =
            LayoutEntry.newBuilder().setDoc(ByteString.copyFromUtf8(doc)).setDeleted(deleted).build()
        val st = WorkspaceState.newBuilder()
            .putSpaces("work", space("work").setLayout(reg("phone")).build())
            .putSpaces("home", space("home").build())
            .putWindowLayouts("default", entry("D"))
            .putWindowLayouts("phone", entry("P"))
            .build()
        assertEquals("P", st.windowLayout("work"))
        assertEquals("D", st.windowLayout("home"))
        assertEquals("D", st.windowLayout(""))
        val gone = st.toBuilder().putWindowLayouts("default", entry("D", deleted = true)).build()
        assertNull(gone.windowLayout("home"))
    }
}
