package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.ProjectAgent
import dev.ironpark.mako.v2.ProjectAgents
import dev.ironpark.mako.v2.SessionInfo
import org.junit.Assert.assertEquals
import org.junit.Test

class ProjectAgentsTest {
    private fun agent(id: String, parent: String = "", status: String = "idle", pending: Int = 0) = ProjectAgent.newBuilder()
        .setSession(SessionInfo.newBuilder().setId(id).setState("running").setStatus(status))
        .setParentSessionId(parent).setPendingMessages(pending).build()

    @Test fun childrenSitUnderTheirParentAndNoneIsLost() {
        val rows = projectAgentRows(listOf(agent("child", "lead"), agent("lead"), agent("orphan", "gone"), agent("grandchild", "child"),
            agent("a", "b"), agent("b", "a")))
        assertEquals(listOf("lead" to 0, "child" to 1, "grandchild" to 2, "orphan" to 0, "a" to 0, "b" to 1),
            rows.map { it.agent.session.id to it.depth })
    }

    @Test fun countsWhoWorksWhoWaitsAndWhatIsPending() {
        val waiting = agent("w").toBuilder().setSession(agent("w").session.toBuilder().setApproval(Approval.newBuilder().setId("x"))).build()
        val snapshot = ProjectAgents.newBuilder().addAllAgents(listOf(agent("a", status = "working", pending = 2), agent("b", status = "waiting"), waiting, agent("c", pending = 1))).build()
        assertEquals(ProjectAgentCounts(working = 1, waiting = 2, pending = 3), projectAgentCounts(snapshot))
    }
}
