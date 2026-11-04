package dev.ironpark.mako

import dev.ironpark.mako.v2.SessionInfo
import org.junit.Assert.assertEquals
import org.junit.Test

class ClosedSessionsTest {
    private fun list(vararg ids: String) = ids.map { SessionInfo.newBuilder().setId(it).build() }
    private fun ids(l: List<SessionInfo>) = l.map { it.id }

    @Test fun aStaleListDoesNotBringAClosedSessionBack() {
        val c = ClosedSessions()
        c.close("a", 0)
        assertEquals(listOf("b"), ids(c.filter(list("a", "b"), 1_000)))
        // The daemon has let it go; an ID listed again later shows.
        c.filter(list("b"), 2_000)
        assertEquals(listOf("a", "b"), ids(c.filter(list("a", "b"), 3_000)))
    }

    @Test fun aSessionTheCloseDidNotEndShowsAgain() {
        val c = ClosedSessions()
        c.close("a", 0)
        assertEquals(listOf("a"), ids(c.filter(list("a"), ClosedSessions.LINGER + 1)))
    }

    @Test fun aFailedCloseShowsTheSessionAgain() {
        val c = ClosedSessions()
        c.close("a", 0)
        c.reopen("a")
        assertEquals(listOf("a"), ids(c.filter(list("a"), 1)))
    }
}
