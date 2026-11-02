package dev.ironpark.mako.ui

import com.google.protobuf.Timestamp
import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.Worktree
import org.junit.Assert.assertEquals
import org.junit.Test

class SessionGroupsTest {
    private fun session(
        id: String, node: String, at: Long, project: String = "/p/$id", remote: String = "",
        status: String = "idle", state: String = "running", approval: Boolean = false, repo: String = "",
    ) = SessionInfo.newBuilder().setId(id).setNodeId(node).setProject(project).setRemote(remote)
        .setStatus(status).setState(state).setStatusAt(Timestamp.newBuilder().setSeconds(at))
        .setWorktree(Worktree.newBuilder().setRepo(repo))
        .apply { if (approval) setApproval(Approval.newBuilder().setId("a")) }.build()

    private val names = mapOf("n1" to "zeta", "n2" to "alpha")
    private val sessions = listOf(
        session("a", "n1", 10, remote = "github.com/me/api"),
        session("b", "n2", 30, remote = "github.com/me/api", status = "working"),
        session("c", "n1", 20, project = "/home/me/web", status = "done"),
        session("d", "n2", 40, project = "/x/wt", repo = "/home/me/web", state = "exited"),
        session("e", "n1", 50, status = "waiting"),
    )

    private fun ids(g: SessionGroup) = g.sessions.map { it.id }

    @Test fun computersGoByNameAndTheirSessionsByTime() {
        val groups = groupSessions(sessions, Grouping.Computer) { names.getValue(it) }
        assertEquals(listOf("alpha", "zeta"), groups.map { it.label })
        assertEquals(listOf(listOf("d", "b"), listOf("e", "c", "a")), groups.map(::ids))
    }

    @Test fun projectsJoinClonesByTheirRemoteAndGoByTheirLatest() {
        val groups = groupSessions(sessions, Grouping.Project) { names.getValue(it) }
        // A worktree goes with its repository, the remote joins computers, and
        // two computers' folders of one name say whose they are.
        assertEquals(listOf("e", "web · alpha", "api", "web · zeta"), groups.map { it.label })
        assertEquals(listOf(listOf("e"), listOf("d"), listOf("b", "a"), listOf("c")), groups.map(::ids))
    }

    @Test fun statusesGoInTheirOrder() {
        val groups = groupSessions(sessions, Grouping.Status) { names.getValue(it) }
        assertEquals(listOf("NeedsYou", "Working", "Done", "Idle", "Ended"), groups.map { it.label })
        assertEquals(listOf(listOf("e"), listOf("b"), listOf("c"), listOf("a"), listOf("d")), groups.map(::ids))
    }

    @Test fun byTimeIsOneGroupTheLatestFirst() {
        val groups = groupSessions(sessions, Grouping.Time) { names.getValue(it) }
        assertEquals(listOf(listOf("e", "d", "b", "c", "a")), groups.map(::ids))
        assertEquals(emptyList<SessionGroup>(), groupSessions(emptyList(), Grouping.Time) { it })
    }
}
