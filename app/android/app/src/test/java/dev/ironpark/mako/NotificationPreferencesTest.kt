package dev.ironpark.mako

import com.google.protobuf.Timestamp
import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.AuthError
import dev.ironpark.mako.v2.BackgroundTask
import dev.ironpark.mako.v2.Node
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

    private fun numbered(status: String, attention: Long, seen: Long) =
        session(status).toBuilder().setStateSeq(attention + 1).setAttentionSeq(attention).setSeenSeq(seen).build()

    @Test fun aTurnSeenInAWindowCancelsItsNotice() {
        val tracker = NoticeTracker()
        tracker.update(listOf(numbered("working", 0, 0)))
        assertEquals(NoticeKind.Done, tracker.update(listOf(numbered("done", 2, 0))).single().kind)
        assertNull(tracker.update(listOf(numbered("done", 2, 2))).single().kind)
        // Done again between two looks, as its node numbers it.
        assertEquals(NoticeKind.Done, tracker.update(listOf(numbered("done", 4, 2))).single().kind)
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

    @Test fun seenElsewhereSuppressesDoneAndWaitingAndCancelsWhenSeenOnDesktop() {
        val tracker = NoticeTracker()
        tracker.update(listOf(numbered("working", 0, 0)))
        // Already seen on desktop before phone polls -> suppressed cleanly.
        assertTrue(tracker.update(listOf(numbered("done", 2, 2))).isEmpty())
        assertNull(tracker.kind(numbered("done", 2, 2)))

        // Waiting alerts until desktop marks it seen, which emits null to cancel.
        assertEquals(NoticeKind.Waiting, tracker.update(listOf(numbered("waiting", 3, 2))).single().kind)
        assertNull(tracker.update(listOf(numbered("waiting", 3, 3))).single().kind)

        // Works even when only attentionSeq/seenSeq are populated (stateSeq == 0).
        val seenWithoutStateSeq = session("done").toBuilder().setAttentionSeq(5).setSeenSeq(5).build()
        assertTrue(seenWithoutStateSeq.seenElsewhere())
        assertTrue(tracker.update(listOf(seenWithoutStateSeq)).isEmpty())
    }

    @Test fun approvalsAreNotSuppressedBySeenSeq() {
        val tracker = NoticeTracker()
        tracker.update(listOf(numbered("working", 0, 0)))
        val approvalSeen = numbered("waiting", 2, 2).toBuilder()
            .setApproval(Approval.newBuilder().setId("app-1"))
            .build()
        assertFalse(approvalSeen.seenElsewhere())
        assertEquals(NoticeKind.Approval, tracker.update(listOf(approvalSeen)).single().kind)
    }

    @Test fun olderNodesWithZeroAttentionSeqRemainBackwardCompatible() {
        val tracker = NoticeTracker()
        val working = session("working")
        val done = session("done")
        val waiting = session("waiting")
        assertFalse(done.seenElsewhere())
        tracker.update(listOf(working))
        assertEquals(NoticeKind.Done, tracker.update(listOf(done)).single().kind)
        assertEquals(NoticeKind.Waiting, tracker.update(listOf(waiting)).single().kind)
    }

    @Test fun waitingAgainBetweenPollsNotifiesAgainWhenAttentionAdvances() {
        val tracker = NoticeTracker()
        tracker.update(listOf(numbered("waiting", 2, 0)))
        assertEquals(NoticeKind.Waiting, tracker.update(listOf(numbered("waiting", 4, 0))).single().kind)
        assertTrue(tracker.update(listOf(numbered("waiting", 4, 0))).isEmpty())
    }

    @Test fun idleSessionNotifiesWhenAttentionAdvancesAndCancelsWhenSeen() {
        val tracker = NoticeTracker()
        tracker.update(listOf(numbered("working", 0, 0)))
        assertEquals(NoticeKind.Done, tracker.update(listOf(numbered("idle", 2, 0))).single().kind)
        assertNull(tracker.update(listOf(numbered("idle", 2, 2))).single().kind)
    }

    @Test fun seenTrackerDeduplicatesPerRunHandlesRestartAndRollback() {
        val tracker = SeenTracker()
        val t1 = Timestamp.newBuilder().setSeconds(100).build()
        val t2 = Timestamp.newBuilder().setSeconds(200).build()
        fun s(att: Long, seen: Long, started: Timestamp = t1) = session("done").toBuilder()
            .setAttentionSeq(att).setSeenSeq(seen).setStartedAt(started).build()

        // No-op when nothing unseen.
        assertFalse(tracker.tryStart(s(0, 0)))
        assertFalse(tracker.tryStart(s(2, 2)))

        // Deduplicates duplicate / lower seq within the same run.
        val first = s(2, 0)
        assertTrue(tracker.tryStart(first))
        assertFalse(tracker.tryStart(first))
        assertFalse(tracker.tryStart(s(1, 0)))

        // Advances when attentionSeq grows.
        val next = s(4, 0)
        assertTrue(tracker.tryStart(next))
        // Cancelling an older failed attempt must not clobber a newer reservation.
        tracker.cancel(first)
        assertFalse(tracker.tryStart(next))
        // Cancelling the current reservation allows retry.
        tracker.cancel(next)
        assertTrue(tracker.tryStart(next))

        // Session restart (new startedAt) resets sequence tracking.
        assertTrue(tracker.tryStart(s(1, 0, started = t2)))

        // Retaining an empty session list clears entries.
        tracker.retain(emptyList())
        assertTrue(tracker.tryStart(s(1, 0, started = t2)))
    }

    @Test fun multicastLockIsOnlyNeededInForegroundOrWithoutTrustedNodes() {
        val untrusted = Node.newBuilder().setId("n1").setTrustsUs(false).build()
        val trusted = Node.newBuilder().setId("n2").setTrustsUs(true).build()
        assertTrue(MeshService.needsMulticastLock(foreground = true, nodes = listOf(trusted)))
        assertTrue(MeshService.needsMulticastLock(foreground = false, nodes = emptyList()))
        assertTrue(MeshService.needsMulticastLock(foreground = false, nodes = listOf(untrusted)))
        assertFalse(MeshService.needsMulticastLock(foreground = false, nodes = listOf(untrusted, trusted)))
    }

    @Test fun mergeSeenSessionPreservesEnrichedFieldsAndAdvancesSequenceCounters() {
        val started = Timestamp.newBuilder().setSeconds(1000).build()
        val lastMsg = Timestamp.newBuilder().setSeconds(1050).build()
        val enriched = session("done").toBuilder()
            .setStartedAt(started)
            .setBranch("main")
            .setRepo("/repos/mako")
            .setOwner("ironpark")
            .setRemote("github.com/ironpark/mako")
            .setGitStamp("abc123")
            .setLastMessage(lastMsg)
            .setAgentScope("project:p1")
            .addTasks(BackgroundTask.newBuilder().setId("t1").setRunning(true))
            .setStateSeq(4)
            .setAttentionSeq(3)
            .setSeenSeq(1)
            .build()
        // Raw handleSeeSession reply omits enriched git/task/scope fields.
        val rawReply = session("done").toBuilder()
            .setStartedAt(started)
            .setStateSeq(5)
            .setAttentionSeq(3)
            .setSeenSeq(3)
            .build()

        val merged = mergeSeenSession(enriched, rawReply)
        assertEquals(3L, merged.seenSeq)
        assertEquals(5L, merged.stateSeq)
        assertEquals(3L, merged.attentionSeq)
        assertEquals("main", merged.branch)
        assertEquals("/repos/mako", merged.repo)
        assertEquals("ironpark", merged.owner)
        assertEquals("github.com/ironpark/mako", merged.remote)
        assertEquals("abc123", merged.gitStamp)
        assertEquals(lastMsg, merged.lastMessage)
        assertEquals("project:p1", merged.agentScope)
        assertEquals(1, merged.tasksCount)

        // Reply from a different session run (startedAt mismatch) does not overwrite.
        val otherRun = rawReply.toBuilder().setStartedAt(Timestamp.newBuilder().setSeconds(2000)).setSeenSeq(9).build()
        assertSame(enriched, mergeSeenSession(enriched, otherRun))
    }

    @Test fun authErrorNotifiesAsWaitingAndCancelsWhenCleared() {
        val tracker = NoticeTracker()
        val authErr = AuthError.newBuilder()
            .setKind("oauth_expired")
            .setProvider("claude")
            .setSummary("Claude Code login expired on the host")
            .setCommand("claude auth login")
            .build()
        tracker.update(listOf(numbered("working", 0, 0)))

        val failed = numbered("idle", 2, 0).toBuilder().setAuthError(authErr).build()
        assertEquals(NoticeKind.Waiting, tracker.update(listOf(failed)).single().kind)

        // Seen on desktop cancels the phone alert.
        val seen = failed.toBuilder().setSeenSeq(2).build()
        assertNull(tracker.update(listOf(seen)).single().kind)

        // Clearing auth error on retry also leaves no pending notice.
        val recovered = numbered("working", 2, 2)
        assertTrue(tracker.update(listOf(recovered)).isEmpty())
    }
}
