package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.SessionInfo
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AgentFamilyTest {
    private fun session(
        id: String,
        node: String = "n1",
        parent: String = "",
        state: String = "running",
        status: String = "idle",
        approvalId: String = "",
    ): SessionInfo = SessionInfo.newBuilder()
        .setId(id)
        .setNodeId(node)
        .setAgent("claude")
        .setParentSessionId(parent)
        .setState(state)
        .setStatus(status)
        .apply {
            if (approvalId.isNotEmpty()) {
                setApproval(Approval.newBuilder().setId(approvalId).setTool("Bash").build())
            }
        }
        .build()

    @Test
    fun groupsDescendantsUnderRootAndRespectsNodeBoundaries() {
        val lead = session("lead", node = "n1")
        val build = session("build", node = "n1", parent = "lead", status = "working")
        val review = session("review", node = "n1", parent = "lead", approvalId = "a1")
        val deep = session("deep", node = "n1", parent = "build")
        val away = session("away", node = "n2", parent = "lead") // different node
        val orphan = session("orphan", node = "n1", parent = "missing")

        val all = listOf(lead, build, review, deep, away, orphan)
        val index = SessionFamilyIndex(all)

        assertNull(index.parent(lead))
        assertEquals("lead", index.parent(build)?.id)
        assertNull(index.parent(away))
        assertNull(index.parent(orphan))

        assertEquals(listOf("build", "lead"), index.ancestors("deep"))
        assertEquals("lead", index.hostIn(all.map { it.id }, "deep"))
        assertEquals("", index.hostIn(listOf("build", "deep"), "lead"))
        assertEquals("build", index.hostIn(listOf("build", "deep"), "deep"))

        assertEquals(listOf("lead", "away", "orphan"), index.topLevelTabs(all).map { it.id })
        assertEquals(listOf("build", "review", "deep"), index.spawned("lead").map { it.id })
        assertEquals(listOf("lead", "build", "review", "deep"), index.family("lead").map { it.id })
        assertTrue(index.familyNeedsYou("lead"))
        assertEquals("review", index.waitingMember("lead")?.id)
        assertFalse(index.familyNeedsYou("orphan"))
    }

    @Test
    fun cyclesDoNotLoopOrHideAllTabs() {
        val a = session("a", parent = "b")
        val b = session("b", parent = "a")
        val index = SessionFamilyIndex(listOf(a, b))

        assertEquals(listOf("b"), index.ancestors("a"))
        assertEquals(listOf("a", "b"), index.topLevelTabs(listOf(a, b)).map { it.id })
        assertEquals(listOf("b"), index.spawned("a").map { it.id })
    }

    @Test
    fun orderedFamilyMarksPrioritizesWaitingThenWorkingThenDoneThenIdle() {
        val idle = session("idle", status = "idle")
        val done = session("done", status = "done")
        val working = session("working", status = "working")
        val waiting = session("waiting", approvalId = "ap-1")
        val exited = session("exited", state = "exited", status = "done")

        val ordered = orderedFamilyMarks(listOf(exited, idle, done, working, waiting))
        assertEquals(listOf("waiting", "working", "done", "exited", "idle"), ordered.map { it.id })
    }
}
