package dev.ironpark.mako.workspace

import dev.ironpark.mako.v2.HLC
import dev.ironpark.mako.v2.WorkspaceState
import org.junit.Assert.assertEquals
import org.junit.Test

class WorkspaceEditsTest {
    private val st = WorkspaceState.parseFrom(resource("workspace.pb"))
    private val mark = HLC.newBuilder().setLogical(1).build()

    private fun fixture(name: String) = WorkspaceState.parseFrom(resource(name))

    @Test fun ranksAreMadeAsGoMakesThem() {
        // ranks.tsv is crdt.Between's answer for each pair.
        for (line in String(resource("ranks.tsv")).lines().filter { it.isNotEmpty() }) {
            val (prev, next, want) = line.split('\t')
            assertEquals("between \"$prev\" and \"$next\"", want, rankBetween(prev, next))
        }
    }

    // Each fixture is the delta a desktop window's workspace.Diff makes of
    // the same edit of its document.

    @Test fun pinningASessionPutsItLast() {
        // The sessions pinned are ranked "", F, V, k and k: the first and the
        // tie are ranked anew with it, as the desktop's window ranks them.
        assertEquals(fixture("pin-session-new.pb"), pinSessionDelta(st, "s9", true))
        val ranked = pinSessionDelta(st, "s9", true).pinnedSessionsMap
        assertEquals(mark, ranked.getValue("s9").addedAt)
        assertEquals(mark, ranked.getValue("s9").rankClock)
    }

    @Test fun pinningASessionAgainRanksItAgainstItsOldRank() {
        assertEquals(fixture("pin-session-again.pb"), pinSessionDelta(st, "s3", true))
    }

    @Test fun theFirstPinIsRankedFromNothing() {
        assertEquals(fixture("pin-session-first.pb"), pinSessionDelta(WorkspaceState.getDefaultInstance(), "only", true))
    }

    @Test fun unpinningRemovesAndRanksTheRestAsTheDesktopDoes() {
        // The pins left rise but for the first and the tie, which the
        // desktop's window ranks anew as it takes one out.
        val d = pinSessionDelta(st, "s1", false)
        assertEquals(fixture("unpin-session.pb"), d)
        assertEquals(mark, d.pinnedSessionsMap.getValue("s1").removedAt)
        assertEquals(false, d.pinnedSessionsMap.getValue("s1").hasAddedAt())
    }

    @Test fun projectsPinAsSessionsDo() {
        assertEquals(fixture("pin-project.pb"), pinScopeDelta(st, Scope("pNotes"), true))
        assertEquals(fixture("unpin-project.pb"), pinScopeDelta(st, Scope("repo:github.com/me/web"), false))
    }

    @Test fun whatIsSoAlreadyIsNoEdit() {
        assertEquals(WorkspaceState.getDefaultInstance(), pinSessionDelta(st, "s1", true))
        assertEquals(WorkspaceState.getDefaultInstance(), pinSessionDelta(st, "s3", false))
        assertEquals(WorkspaceState.getDefaultInstance(), pinScopeDelta(st, Scope("pApi", "github.com/acme/web"), true))
    }
}
