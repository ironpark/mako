package dev.ironpark.mako.ui

import com.google.protobuf.Timestamp
import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.WorkspaceState
import dev.ironpark.mako.v2.Worktree
import dev.ironpark.mako.workspace.Json
import dev.ironpark.mako.workspace.Scope
import dev.ironpark.mako.workspace.added
import dev.ironpark.mako.workspace.folderRoot
import dev.ironpark.mako.workspace.project
import dev.ironpark.mako.workspace.reg
import dev.ironpark.mako.workspace.resource
import dev.ironpark.mako.workspace.space
import dev.ironpark.mako.workspace.view
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SessionGroupsTest {
    private fun session(
        id: String, node: String, at: Long, project: String = "/p/$id", remote: String = "",
        status: String = "idle", state: String = "running", approval: Boolean = false, repo: String = "",
        owner: String = "", mainRepo: String = "",
    ) = SessionInfo.newBuilder().setId(id).setNodeId(node).setProject(project).setRemote(remote).setOwner(owner).setRepo(mainRepo)
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

    // With a workspace: the desktop's sections, spaces and pins.

    private val acme = "github.com/acme"
    private val workspace = WorkspaceState.newBuilder()
        .putSpaces("work", space("work", rank = "a", name = "Work").putOwners(acme, added())
            .putProjects("repo:github.com/me/web", added()).build())
        .putSpaces("home", space("home", rank = "b", name = "Home").putOwners("github.com/me", added()).build())
        .putProjects("pApi", project("pApi", name = "API", roots = listOf(
            folderRoot("github.com/acme/server", "n1\u0000/w/server", "n2\u0000/srv/server",
                remote = "github.com/acme/server", owner = acme, rank = "a"),
            folderRoot("n1\u0000/w/web", "n1\u0000/w/web", rank = "b"),
        )).build())
        // A project made of a folder's section, which the folder then left.
        .putProjects("n1\u0000/tmp/x", project("n1\u0000/tmp/x", name = "Old", roots = listOf(folderRoot("r", "n1\u0000/elsewhere"))).build())
        .putNames("repo:github.com/me/web", reg("Website"))
        .build()

    private val mesh = listOf(
        session("a", "n1", 10, project = "/w/server/cmd", mainRepo = "/w/server", remote = "github.com/acme/server", owner = acme),
        session("b", "n2", 60, project = "/srv/server", mainRepo = "/srv/server", remote = "github.com/acme/server", owner = acme),
        session("c", "n1", 30, project = "/w/web/src"),
        // A clone on a third computer joins the root by its remote.
        session("d", "n3", 40, project = "/x/server", mainRepo = "/x/server", remote = "github.com/acme/server", owner = acme),
        session("e", "n1", 50, project = "/home/me/web", mainRepo = "/home/me/web", remote = "github.com/me/web", owner = "github.com/me"),
        session("e2", "n2", 5, project = "/src/web", mainRepo = "/src/web", remote = "github.com/me/web", owner = "github.com/me"),
        session("f", "n2", 20, project = "/notes"),
        session("g", "n1", 70, project = "/home/me/dots", mainRepo = "/home/me/dots", remote = "github.com/me/dots", owner = "github.com/me"),
        session("h", "n1", 80, project = "/tmp/x"),
    )

    @Test fun projectsOfSeveralFoldersAreOneSection() {
        val sorted = SessionWorkspace(workspace.view(), mesh)
        val groups = sorted.groups(mesh, Grouping.Project) { names.getOrDefault(it, it) }
        // By name, as the desktop sorts them: capitals first.
        assertEquals(listOf("API", "Website", "dots", "notes", "x"), groups.map { it.label })
        assertEquals(listOf(listOf("b", "d", "c", "a"), listOf("e", "e2"), listOf("g"), listOf("f"), listOf("h")), groups.map(::ids))
        val api = groups[0]
        assertEquals(mapOf("a" to "#server", "b" to "#server", "d" to "#server", "c" to "#web"), api.tags)
        assertEquals(Scope("pApi"), api.pin)
        // A folder's section whose key a project took is set apart from it.
        assertEquals("section:free:n1:/tmp/x", groups[4].key)
        assertEquals("section:repo:github.com/me/web", groups[1].key)
        // Every section has a look; one picked for none is drawn from its key.
        assertTrue(groups.all { it.look != null })
    }

    @Test fun sectionsArePlacedInSpacesAsOnTheDesktop() {
        val view = workspace.view()
        val sorted = SessionWorkspace(view, mesh)
        fun inSpace(i: Int) = mesh.filter { sorted.inSpace(it, i) }.map { it.id }
        // The space of every session; Work: its owner's project, the section
        // moved to it, and, being first, what nothing else places; Home: its owner's.
        assertEquals(mesh.map { it.id }, inSpace(0))
        assertEquals(listOf("a", "b", "c", "d", "e", "e2", "f", "h"), inSpace(1))
        assertEquals(listOf("g"), inSpace(2))
        assertEquals(0, view.firstSpace())
        assertEquals(2, view.spaceIndex("home"))
        assertEquals(0, view.spaceIndex("gone"))
        // The default space takes what nothing else places, and every
        // session's space can be left out.
        val homeFirst = workspace.toBuilder().setDefaultSpace(reg("home"))
            .setNoAll(dev.ironpark.mako.v2.BoolReg.newBuilder().setValue(true)).build().view()
        val moved = SessionWorkspace(homeFirst, mesh)
        assertEquals(listOf("f", "g", "h"), mesh.filter { moved.inSpace(it, 2) }.map { it.id })
        assertEquals(1, homeFirst.firstSpace())
        assertEquals(1, homeFirst.spaceIndex(""))
        // A space that left a section out by its owner does not place it.
        val hidden = workspace.toBuilder().putSpaces("home", space("home", rank = "b").putOwners("github.com/me", added())
            .putHidden("repo:github.com/me/dots", added()).build()).build().view()
        assertFalse(SessionWorkspace(hidden, mesh).inSpace(mesh.single { it.id == "g" }, 2))
    }

    @Test fun pinsComeFirst() {
        val pinned = workspace.toBuilder()
            .putPinnedSessions("f", added())
            .putPinnedProjects("repo:github.com/me/web\u0000", added(rank = "a"))
            .putPinnedProjects("pApi\u0000github.com/acme/server", added(rank = "b"))
            .build()
        val sorted = SessionWorkspace(pinned.view(), mesh)
        val groups = sorted.groups(mesh, Grouping.Project) { it }
        assertEquals(listOf(PINNED, "section:repo:github.com/me/web", "pin:pApi/github.com/acme/server"), groups.take(3).map { it.key })
        assertEquals(listOf("API / server", "API", "dots", "x"), groups.drop(2).map { it.label })
        // The folder pinned on its own keeps its sessions; the project the rest.
        assertEquals(listOf(listOf("f"), listOf("e", "e2"), listOf("b", "d", "a"), listOf("c"), listOf("g"), listOf("h")), groups.map(::ids))
        assertEquals(listOf(false, true, true, false, false, false), groups.map { it.pinned })
        // Pinned sessions head every grouping.
        val byComputer = sorted.groups(mesh, Grouping.Computer) { names.getOrDefault(it, it) }
        assertEquals(PINNED, byComputer.first().key)
        assertFalse(byComputer.drop(1).any { g -> g.sessions.any { it.id == "f" } })
    }

    @Test fun foldersOfOneNameAreToldApartByTheirPaths() {
        val twins = listOf(session("x", "n1", 1, project = "/a/app"), session("y", "n2", 2, project = "/b/app"))
        val groups = SessionWorkspace(workspace.view(), twins).groups(twins, Grouping.Project) { it }
        assertEquals(listOf("a/app", "b/app"), groups.map { it.label })
    }

    @Test fun placingFollowsTheDesktopsPathRules() {
        assertTrue(covers("/w/web", "/w/web/src", ""))
        assertFalse(covers("/w/web", "/w/webapp", ""))
        assertFalse(covers("/home/me", "/home/me/src/mako", "/home/me/src/mako"))
        assertTrue(covers("/w/server", "/tmp/server-fix", "/w/server"))
        assertTrue(covers("/w/mono/web", "/w/mono/web/x", "/w/mono"))
        assertTrue(covers("C:\\w\\web", "C:\\w\\web\\src", ""))
        assertFalse(covers("", "/w", ""))
    }

    @Test fun sectionsAndTheirSpacesAreTheDesktops() {
        // grouping.json is what internal/gui's groupProjects and placeOf make
        // of grouping-state.pb and its sessions: every section, the empty
        // projects' too, by name, with its owners, its sessions and the root
        // of the project each is in, and the space it is placed in.
        @Suppress("UNCHECKED_CAST")
        val fixture = Json.parse(String(resource("grouping.json"))) as Map<String, List<Map<String, Any?>>>
        val sessions = fixture.getValue("sessions").map {
            SessionInfo.newBuilder().setId(it["ID"] as String).setNodeId(it["Node"] as String).setProject(it["Project"] as String)
                .setRepo(it["Repo"] as String).setRemote(it["Remote"] as String).setOwner(it["Owner"] as String).build()
        }
        val view = WorkspaceState.parseFrom(resource("grouping-state.pb")).view()
        val got = sections(sessions, view).map { sec ->
            mapOf(
                "key" to sec.key, "label" to sec.label, "owners" to sec.owners,
                "sessions" to sec.sessions.map { it.id }, "roots" to sec.sessions.map { (sec.rootOf[it.id] ?: -1).toDouble() },
                "space" to view.placeOf(sec.key, sec.owners).toDouble(),
            )
        }
        assertEquals(fixture.getValue("sections"), got)
    }
}
