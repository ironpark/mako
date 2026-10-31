package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.ChatMessage
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ChatLinksTest {
    private val project = "/home/me/api"

    @Test fun pathsResolveAgainstTheProjectWithTheirLines() {
        assertEquals(ChatLink.File("/home/me/api/src/main.go"), chatLink("src/main.go", project))
        assertEquals(ChatLink.File("/home/me/api/src/main.go", 12), chatLink("./src/main.go:12", project))
        assertEquals(ChatLink.File("/home/me/api/main.go", 7), chatLink("main.go:7:3", project))
        assertEquals(ChatLink.File("/home/me/api/README.md", 40), chatLink("README.md#L40", project))
        assertEquals(ChatLink.File("/home/me/web/a b.png"), chatLink("../web/a%20b.png", project))
        assertEquals(ChatLink.File("/etc/hosts"), chatLink("/etc/hosts", project))
        assertEquals(ChatLink.File("~/notes.md"), chatLink("~/notes.md", project))
        assertEquals(ChatLink.File("/tmp/x.kt", 3), chatLink("file:///tmp/x.kt#L3", project))
        assertEquals(ChatLink.File("/home/me/api/docs", folder = true), chatLink("docs/", project))
        assertEquals(ChatLink.File("C:\\src\\b.txt"), chatLink("C:\\src\\a\\..\\b.txt", "C:\\src"))
        assertNull(chatLink("#heading", project))
        assertNull(chatLink("", project))
    }

    @Test fun loopbackAddressesGoThroughATunnelAndOthersAsTheyAre() {
        assertEquals(ChatLink.Loopback("http://localhost:5173/", 5173), chatLink("http://localhost:5173/", project))
        assertEquals(ChatLink.Loopback("http://127.0.0.1:8000/x", 8000), chatLink("http://127.0.0.1:8000/x", project))
        assertEquals(ChatLink.Loopback("http://0.0.0.0:3000", 3000), chatLink("http://0.0.0.0:3000", project))
        assertEquals(ChatLink.Loopback("http://[::1]:9000/", 9000), chatLink("http://[::1]:9000/", project))
        assertEquals(ChatLink.Loopback("http://localhost:3000/a", 3000), chatLink("localhost:3000/a", project))
        assertEquals(ChatLink.Loopback("http://app.localhost", 80), chatLink("http://app.localhost", project))
        assertEquals(ChatLink.Web("https://example.com/a.png"), chatLink("https://example.com/a.png", project))
        assertEquals(ChatLink.Web("mailto:me@example.com"), chatLink("mailto:me@example.com", project))
        assertFalse(loopbackHost("128.0.0.1"))
    }

    @Test fun aTunnelServesTheAddressAtItsPortHere() {
        val link = ChatLink.Loopback("http://0.0.0.0:3000/shot.png?x=1#y", 3000)
        assertEquals("http://localhost:3001/shot.png?x=1#y", link.at(3001))
        assertTrue(link.image)
        assertEquals("http://127.0.0.1:5173/", ChatLink.Loopback("http://127.0.0.1:5173/", 5173).at(5173))
    }

    @Test fun draftsHandFilesByTheirWords() {
        val staged = "/tmp/mako-drops/20261031-120000-ab/report.pdf"
        val text = "look at $staged and shot.png please"
        assertEquals(
            listOf(Attachment(staged, staged, false), Attachment("shot.png", "/home/me/api/shot.png", true)),
            attachments(text, project),
        )
        assertEquals("look at and shot.png please ", withoutWord(text, staged))
        assertEquals("hi $staged ", withPath("hi", staged))
        assertEquals("$staged ", withPath("", staged))
        assertEquals("My_Photo_1.jpg", stagedName(" My Photo/1.jpg "))
        assertEquals(listOf("/home/me/api/a.png"), imageRefs("see a.png and ./a.png, https://x/b.png", project))
    }

    @Test fun toolCallsInARowAreOneTurnAndAllButTheLastLongRunFold() {
        fun tool(id: String) = ChatMessage.newBuilder().setId(id).setRole("tool").setTool("Read").build()
        fun said(id: String) = ChatMessage.newBuilder().setId(id).setRole("assistant").setText(id).build()
        val turns = chatTurns(listOf(said("a"), tool("1"), tool("2"), tool("3"), said("b"), tool("4"), tool("5"), tool("6"), said("c")))
        assertEquals(listOf("a", "run:1", "b", "run:4", "c"), turns.map { it.key })
        assertEquals(listOf(false, true, false, false, false), turns.map { it.folded })
        assertEquals(3, turns[1].messages.size)
        assertTrue(turns[3].tools)
        // The same words twice, without IDs, are two turns.
        val twice = ChatMessage.newBuilder().setRole("assistant").setText("same").build()
        val keys = chatTurns(listOf(twice, tool("x"), twice)).map { it.key }
        assertEquals(keys.distinct(), keys)
    }
}
