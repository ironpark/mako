package dev.ironpark.mako.ui

import com.google.protobuf.Timestamp
import dev.ironpark.mako.v2.AgentBlock
import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.WorkspaceState
import dev.ironpark.mako.workspace.Folder
import dev.ironpark.mako.workspace.LayoutPage
import dev.ironpark.mako.workspace.Scope
import dev.ironpark.mako.workspace.WorkspaceView
import dev.ironpark.mako.workspace.added
import dev.ironpark.mako.workspace.folderRoot
import dev.ironpark.mako.workspace.project
import dev.ironpark.mako.workspace.reg
import dev.ironpark.mako.workspace.space
import dev.ironpark.mako.workspace.view
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ProjectsTest {
    private fun ts(s: Long) = Timestamp.newBuilder().setSeconds(s).build()

    private fun session(
        id: String, node: String = "n1", project: String = "/p/$id", remote: String = "", owner: String = "", repo: String = "",
        status: String = "idle", state: String = "running", started: Long = 0, at: Long = started, approvalSince: Long = 0,
    ) = SessionInfo.newBuilder().setId(id).setNodeId(node).setProject(project).setRemote(remote).setOwner(owner).setRepo(repo)
        .setStatus(status).setState(state).setStartedAt(ts(started)).setStatusAt(ts(at))
        .apply { if (approvalSince > 0) setApproval(Approval.newBuilder().setId("ap-$id").setSince(ts(approvalSince))) }
        .build()

    private val acme = "github.com/acme"
    private val workspace = WorkspaceState.newBuilder()
        .setDefaultSpace(reg("home"))
        .putSpaces("work", space("work", rank = "a", name = "Work").putOwners(acme, added()).build())
        .putSpaces("home", space("home", rank = "b", name = "Home").putOwners("github.com/me", added()).build())
        .putProjects("pShop", project("pShop", name = "Shop", roots = listOf(
            folderRoot("github.com/acme/api", "n1\u0000/w/api", remote = "github.com/acme/api", owner = acme, rank = "a"),
            folderRoot("github.com/acme/web", "n1\u0000/w/web", remote = "github.com/acme/web", owner = acme, rank = "b"),
        )).build())
        .putProjects("pEmpty", project("pEmpty", name = "recipes", roots = listOf(folderRoot("r", "n2\u0000/r"))).build())
        .putNames("repo:github.com/me/blog", reg("My blog"))
        .putPinnedProjects("repo:github.com/me/blog\u0000", added(rank = "a"))
        .putPinnedSessions("w2", added())
        .build()

    private val sessions = listOf(
        session("a1", project = "/w/api", repo = "/w/api", remote = "github.com/acme/api", owner = acme, started = 10, status = "working"),
        session("w1", project = "/w/web", repo = "/w/web", remote = "github.com/acme/web", owner = acme, started = 20, approvalSince = 90),
        session("w2", project = "/w/web/src", repo = "/w/web", remote = "github.com/acme/web", owner = acme, started = 30, status = "done", at = 95),
        session("b1", project = "/m/blog", repo = "/m/blog", remote = "github.com/me/blog", owner = "github.com/me", started = 5, status = "waiting", at = 80),
        session("n1", project = "/notes", started = 1, state = "stopped"),
    )

    private val names = mapOf("n1" to "studio", "n2" to "laptop")
    private fun index(view: WorkspaceView = workspace.view(), list: List<SessionInfo> = sessions) =
        projectIndex(view, list) { names.getOrDefault(it, it) }

    @Test fun aWorkspacesSectionsAreTheProjects() {
        val items = index()
        // By name, letters' case aside; a project without sessions too.
        assertEquals(listOf("My blog", "notes", "recipes", "Shop"), items.map { it.label })
        val shop = items.single { it.label == "Shop" }
        assertEquals("s:pShop", shop.key)
        assertEquals(listOf("a1", "w1", "w2"), shop.sessions.map { it.id })
        assertEquals(mapOf("a1" to "#api", "w1" to "#web", "w2" to "#web"), shop.tags)
        assertEquals(listOf(Folder("n1", "/w/api"), Folder("n1", "/w/web")), shop.folders)
        assertEquals(Scope("pShop"), shop.scope)
        assertEquals(1, shop.space) // Work, by its owner
        val blog = items.single { it.label == "My blog" }
        assertTrue(blog.pinned)
        assertEquals(2, blog.space)
        // An empty project is where its folders are, its computer named.
        val empty = items.single { it.label == "recipes" }
        assertEquals(emptyList<SessionInfo>(), empty.sessions)
        assertEquals(listOf("n2"), empty.nodes)
        assertEquals(listOf(Folder("n2", "/r")), empty.folders)
    }

    @Test fun withoutAWorkspaceTheFoldersAreTheProjects() {
        val items = index(WorkspaceState.getDefaultInstance().view())
        assertEquals(listOf("api", "blog", "notes", "web"), items.map { it.label })
        assertTrue(items.all { it.key.startsWith("f:") && it.scope == null && it.space == 0 })
        assertEquals(listOf("w1", "w2"), items.single { it.label == "web" }.sessions.map { it.id }.sorted())
        // Each still has a look, drawn from its key, the same every time.
        assertEquals(items.map { it.look }, index(WorkspaceState.getDefaultInstance().view()).map { it.look })
    }

    @Test fun aSummarySaysWhatNeedsYouFirst() {
        val shop = index().single { it.label == "Shop" }.summary()
        assertEquals(3, shop.total)
        assertEquals(1, shop.needsYou)
        assertEquals(1, shop.working)
        assertEquals("w1", shop.headline?.id)
        val notes = index().single { it.label == "notes" }.summary()
        assertEquals(1, notes.asleep)
        assertEquals("n1", notes.headline?.id)
        assertNull(index().single { it.label == "recipes" }.summary().headline)
    }

    @Test fun homeShowsASpaceAndAFilterWithPinsFirst() {
        val items = index()
        val view = workspace.view()
        assertEquals(listOf("My blog", "notes", "recipes", "Shop"), homeProjects(items, 0, Filter.All, view.pinnedProjects).map { it.label })
        // Work holds its owner's project; Home, the default, the rest.
        assertEquals(listOf("Shop"), homeProjects(items, 1, Filter.All).map { it.label })
        assertEquals(listOf("My blog", "notes", "recipes"), homeProjects(items, 2, Filter.All).map { it.label })
        assertEquals(listOf("My blog", "Shop"), homeProjects(items, 0, Filter.NeedsYou).map { it.label })
        assertEquals(listOf("Shop"), homeProjects(items, 0, Filter.Working).map { it.label })
        // Pinned in the order pinned.
        val two = items.map { if (it.label == "Shop") it.copy(pins = listOf(Scope("pShop"))) else it }
        val order = listOf(Scope("pShop"), Scope("repo:github.com/me/blog"))
        assertEquals(listOf("Shop", "My blog", "notes", "recipes"), homeProjects(two, 0, Filter.All, order).map { it.label })
    }

    @Test fun theTrayListsWhatWaitsLatestFirst() {
        assertEquals(listOf("w1", "b1"), needingYou(sessions).map { it.id })
        val shop = index().single { it.label == "Shop" }
        assertEquals(1, needYouElsewhere(sessions, shop))
        assertEquals(2, needYouElsewhere(sessions, null))
        assertEquals("s:pShop", index().projectOf("w2")?.key)
        assertNull(index().projectOf("nope"))
    }

    @Test fun tabsKeepTheirOrderAsTheyWork() {
        val tabs = listOf(
            session("c", started = 30, at = 999, status = "working"),
            session("x", started = 5, state = "exited"),
            session("a", started = 10),
            session("p", started = 40),
            session("b", started = 10, at = 500),
        )
        // Pinned first; then by start, a tie by ID; those exited last.
        assertEquals(listOf("p", "a", "b", "c", "x"), tabOrder(tabs, setOf("p")).map { it.id })
        // Working, or printing, moves none of them.
        val busier = tabs.map { if (it.id == "a") it.toBuilder().setStatusAt(ts(5000)).build() else it }
        assertEquals(tabOrder(tabs, setOf("p")).map { it.id }, tabOrder(busier, setOf("p")).map { it.id })
    }

    @Test fun aProjectOpensOnWhatNeedsYouElseWhereYouWere() {
        val waits = listOf(session("a", started = 1, at = 50), session("b", started = 2, approvalSince = 60), session("c", started = 3, at = 70))
        assertEquals("b", openingTab(waits, "a")?.id)
        val calm = listOf(session("a", started = 1, at = 50), session("b", started = 2, at = 60), session("x", started = 3, at = 99, state = "exited"))
        assertEquals("a", openingTab(calm, "a")?.id)
        // A tab looked at that is gone: the latest that has not exited.
        assertEquals("b", openingTab(calm, "gone")?.id)
        assertEquals("x", openingTab(listOf(calm[2]), null)?.id)
        assertNull(openingTab(emptyList(), "a"))
    }

    @Test fun aTabGoneLeavesItsNeighbourShown() {
        val now = listOf(session("a"), session("c"), session("d"))
        assertEquals("c", tabAfter(listOf("a", "b", "c", "d"), "b", now))
        assertEquals("d", tabAfter(listOf("a", "c", "d", "e"), "e", now))
        assertEquals("a", tabAfter(listOf("x"), "y", now))
        assertNull(tabAfter(listOf("a"), "a", emptyList()))
        assertEquals("c", neighbourTab(now, "a", 1))
        assertEquals("a", neighbourTab(now, "c", -1))
        assertNull(neighbourTab(now, "a", -1))
        assertNull(neighbourTab(now, "d", 1))
        assertNull(neighbourTab(now, "gone", 1))
    }

    @Test fun aProjectsPanelsAreTheSpacesPluginsAndItsOwnAgents() {
        val pages = listOf(LayoutPage("p", "plugin:ci-status/ci"), LayoutPage("q", "mako.git"))
        val agents = listOf(
            AgentBlock.newBuilder().setName("agent:w1/board").setSessionId("w1").setTitle("Board").setAgent("claude").build(),
            AgentBlock.newBuilder().setName("agent:zz/form").setSessionId("zz").setTitle("Form").build(),
        )
        val got = projectPanels(pages, agents, setOf("a1", "w1"))
        assertEquals(listOf("plugin:ci-status/ci", "agent:w1/board"), got.map { it.block })
        assertEquals("Board", got[1].title)
    }

    @Test fun aRouteCarriesAnyKey() {
        for (key in listOf("s:pShop", "s:n1\u0000/w/a b/c", "f:github.com/me/x", "s:한글/프로젝트", "")) {
            assertEquals(key, keyOfRoute(routeKey(key)))
            assertFalse(routeKey(key).any { it == '/' || it == '?' || it == '&' || it == '\u0000' })
        }
        assertEquals("", keyOfRoute("%%%"))
    }

    @Test fun lastTabsOutliveTheAppAndForgetTheOldest() {
        var stored = ""
        val last = LastTabs({ stored }, { stored = it })
        last["s:a"] = "1"
        last["s:b\u0000/x"] = "2"
        last["s:a"] = "3"
        // Read again as after a restart.
        val again = LastTabs({ stored }, { stored = it })
        assertEquals("3", again["s:a"])
        assertEquals("2", again["s:b\u0000/x"])
        assertNull(again["s:c"])
        repeat(LastTabs.LIMIT + 5) { again["p$it"] = "t$it" }
        assertNull(again["s:a"])
        assertEquals("t${LastTabs.LIMIT + 4}", again["p${LastTabs.LIMIT + 4}"])
        assertEquals(LastTabs.LIMIT, stored.lines().size)
    }
}
