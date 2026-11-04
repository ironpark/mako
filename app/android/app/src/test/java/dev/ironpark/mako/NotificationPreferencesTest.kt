package dev.ironpark.mako

import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.SessionInfo
import org.junit.Assert.*
import org.junit.Test

class NotificationPreferencesTest {
    private fun session(status: String, approval: String = "") = SessionInfo.newBuilder().setNodeId("node").setId("session")
        .setState("running").setStatus(status).apply { if (approval.isNotEmpty()) setApproval(Approval.newBuilder().setId(approval)) }.build()

    @Test fun switchesApplyIndependentlyAndMasterAndForegroundAreRespected() {
        val options = NotificationOptions(approvals = false, done = false)
        assertFalse(options.allows(NoticeKind.Approval, false))
        assertTrue(options.allows(NoticeKind.Waiting, false))
        assertFalse(options.allows(NoticeKind.Done, false))
        assertFalse(options.allows(NoticeKind.Waiting, true))
        assertTrue(options.copy(foreground = true).allows(NoticeKind.Waiting, true))
        for (kind in NoticeKind.entries) assertFalse(options.copy(enabled = false, foreground = true).allows(kind, false))
    }

    @Test fun trackerOnlyNotifiesNewTransitionsAndCancelsResolvedAndRemovedSessions() {
        val tracker = NoticeTracker()
        assertTrue(tracker.update(listOf(session("working"))).isEmpty())
        assertEquals(NoticeKind.Approval, tracker.update(listOf(session("working", "a"))).single().kind)
        assertTrue(tracker.update(listOf(session("done", "a"))).isEmpty())
        assertNull(tracker.update(listOf(session("working"))).single().kind)
        assertEquals(NoticeKind.Waiting, tracker.update(listOf(session("waiting"))).single().kind)
        assertEquals(NoticeKind.Done, tracker.update(listOf(session("done"))).single().kind)
        assertNull(tracker.update(emptyList()).single().kind)
    }

    private fun turn(status: String, turns: Long) = session(status).toBuilder().setTurns(turns).build()

    @Test fun aTurnEndedIdleNotifiesAsDoneUntilItWorksAgain() {
        val tracker = NoticeTracker()
        tracker.update(listOf(turn("working", 0)))
        assertEquals(NoticeKind.Done, tracker.update(listOf(turn("idle", 1))).single().kind)
        assertTrue(tracker.update(listOf(turn("idle", 1))).isEmpty())
        assertEquals(NoticeKind.Done, tracker.kind(turn("idle", 1)))
        assertNull(tracker.update(listOf(turn("working", 1))).single().kind)
        // An idle agent that ended no turn, as a node older than turns says, calls for nothing.
        assertTrue(tracker.update(listOf(turn("idle", 0).toBuilder().setId("other").build())).let { it.size == 1 && it.single().kind == null })
    }

    @Test fun aTurnEndedBetweenTwoLooksNotifiesAgain() {
        val tracker = NoticeTracker()
        tracker.update(listOf(turn("done", 1)))
        assertEquals(NoticeKind.Done, tracker.update(listOf(turn("done", 2))).single().kind)
        assertTrue(tracker.update(listOf(turn("done", 2))).isEmpty())
    }

    @Test fun observingWhileDisabledDoesNotReplayOldEventsAfterReenabling() {
        val tracker = NoticeTracker()
        tracker.update(listOf(session("working")))
        val event = tracker.update(listOf(session("done"))).single()
        assertFalse(NotificationOptions(enabled = false).allows(event.kind!!, false))
        assertTrue(tracker.update(listOf(session("done"))).isEmpty())
        val otherNode = session("done").toBuilder().setNodeId("other").build()
        assertTrue(tracker.update(listOf(session("done"), otherNode)).isEmpty())
    }
}
