package dev.ironpark.mako.workspace

import dev.ironpark.mako.v2.ElementMeta
import dev.ironpark.mako.v2.HLC
import dev.ironpark.mako.v2.LookEntry
import dev.ironpark.mako.v2.ProjectEntry
import dev.ironpark.mako.v2.ProjectRootEntry
import dev.ironpark.mako.v2.SpaceEntry
import dev.ironpark.mako.v2.StringReg
import dev.ironpark.mako.v2.WorkspaceState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Stamps, registers and set elements, as a daemon would have stamped them. */
internal fun hlc(wall: Long, logical: Int = 0, node: String = "n1"): HLC =
    HLC.newBuilder().setWallTime(wall).setLogical(logical).setNodeId(node).build()

internal fun reg(v: String, at: HLC = hlc(1)): StringReg = StringReg.newBuilder().setValue(v).setClock(at).build()

internal fun added(at: HLC = hlc(1), rank: String = ""): ElementMeta =
    ElementMeta.newBuilder().setAddedAt(at).setRank(rank).setRankClock(at).build()

internal fun removed(add: HLC, remove: HLC, rank: String = ""): ElementMeta =
    ElementMeta.newBuilder().setAddedAt(add).setRemovedAt(remove).setRank(rank).setRankClock(add).build()

internal fun space(id: String, rank: String = "", created: HLC = hlc(1), deleted: HLC? = null, name: String = id) =
    SpaceEntry.newBuilder().setId(id).setCreatedAt(created).setName(reg(name)).setRank(reg(rank))
        .apply { if (deleted != null) setDeletedAt(deleted) }

internal fun folderRoot(id: String, vararg locations: String, remote: String = "", owner: String = "", rank: String = "") =
    ProjectRootEntry.newBuilder().setId(id).setCreatedAt(hlc(1)).setRank(reg(rank)).setRemote(reg(remote)).setOwner(reg(owner))
        .putAllLocations(locations.associateWith { added() })

internal fun project(id: String, name: String = "", rank: String = "", deleted: HLC? = null, roots: List<ProjectRootEntry.Builder> = emptyList()) =
    ProjectEntry.newBuilder().setId(id).setCreatedAt(hlc(1)).setName(reg(name)).setRank(reg(rank))
        .apply { if (deleted != null) setDeletedAt(deleted) }
        .putAllRoots(roots.associate { it.id to it.build() })

internal fun resource(name: String): ByteArray =
    WorkspaceViewTest::class.java.getResourceAsStream("/workspace/$name")!!.use { it.readBytes() }

class WorkspaceViewTest {
    /** The view as workspace.View writes it as JSON, but the pane trees and window layouts, which the app does not read. */
    private fun doc(v: WorkspaceView): Map<String, Any?> {
        val out = linkedMapOf<String, Any?>()
        fun MutableMap<String, Any?>.opt(k: String, v: Any?) {
            when (v) {
                null, false, "" -> {}
                is Collection<*> -> if (v.isNotEmpty()) put(k, v)
                is Map<*, *> -> if (v.isNotEmpty()) put(k, v)
                else -> put(k, v)
            }
        }
        fun folders(l: List<Folder>) = l.map { mapOf("node" to it.node, "path" to it.path) }
        out.opt("spaces", v.spaces.map { sp ->
            linkedMapOf<String, Any?>("id" to sp.id, "name" to sp.name).apply {
                opt("color", sp.color); opt("owners", sp.owners); opt("projects", sp.projects); opt("hidden", sp.hidden); opt("layout", sp.layout)
            }
        })
        out.opt("no_all", v.noAll)
        out.opt("default_space", v.defaultSpace)
        out.opt("projects", v.projects.map { p ->
            linkedMapOf<String, Any?>("id" to p.id).apply {
                opt("name", p.name)
                opt("roots", p.roots.map { r ->
                    linkedMapOf<String, Any?>().apply {
                        opt("id", r.id); opt("label", r.label); opt("remote", r.remote); opt("owner", r.owner)
                        opt("locations", folders(r.locations)); opt("declined", folders(r.declined))
                    }
                })
                opt("no_join", p.noJoin); opt("give_dirs", p.giveDirs)
            }
        })
        out.opt("names", v.names)
        out.opt("looks", v.looks.mapValues { (_, l) -> linkedMapOf<String, Any?>().apply { opt("icon", l.icon); opt("color", l.color) } })
        out.opt("pinned_projects", v.pinnedProjects.map { linkedMapOf<String, Any?>("section" to it.section).apply { opt("root", it.root) } })
        out.opt("pinned", v.pinnedSessions)
        return out
    }

    @Test fun readsTheFixtureAsTheGoViewDoes() {
        // workspace.pb and view.json are a state full of edge cases and
        // what internal/workspace's View makes of it, as that package's
        // TestAndroidFixtures writes them (app/README.md, Spaces and projects).
        val st = WorkspaceState.parseFrom(resource("workspace.pb"))
        @Suppress("UNCHECKED_CAST")
        val want = (Json.parse(String(resource("view.json"))) as Map<String, Any?>) - setOf("layouts", "tab_nodes", "window_layouts")
        assertEquals(want, doc(st.view()))
        assertTrue(st.view().held)
    }

    @Test fun anEmptyStateIsNoWorkspace() {
        val v = WorkspaceState.getDefaultInstance().view()
        assertFalse(v.held)
        assertEquals(WorkspaceView(), v)
    }

    @Test fun deletedEntitiesAreHidden() {
        val st = WorkspaceState.newBuilder()
            .putSpaces("made", space("made").build())
            .putSpaces("deleted", space("deleted", created = hlc(1), deleted = hlc(2)).build())
            .putSpaces("again", space("again", created = hlc(3), deleted = hlc(2)).build())
            // A creation and a deletion of one stamp leave it deleted.
            .putSpaces("tie", space("tie", created = hlc(2), deleted = hlc(2)).build())
            .putProjects("p", project("p", roots = listOf(
                folderRoot("kept", "n1\u0000/a"),
                folderRoot("cut", "n1\u0000/b").setDeletedAt(hlc(5)),
            )).build())
            .putProjects("gone", project("gone", deleted = hlc(9)).build())
            .build()
        val v = st.view()
        assertEquals(listOf("again", "made"), v.spaces.map { it.id })
        assertEquals(listOf("p"), v.projects.map { it.id })
        assertEquals(listOf("kept"), v.projects[0].roots.map { it.id })
    }

    @Test fun aTieOfTimeAndCountGoesByTheHashOfKeyAndNode() {
        // Whichever way the hash falls, exactly one of a pair whose creation
        // and deletion swap nodes shows, and it is the same on every node.
        for (id in listOf("a", "b", "c", "d", "e")) {
            val x = hlc(5, 1, "nA")
            val y = hlc(5, 1, "nB")
            val one = visible(x, y, spaceKey(id))
            val other = visible(y, x, spaceKey(id))
            assertTrue(one != other)
        }
        assertEquals(0, compareStamps(hlc(5, 1, "nA"), hlc(5, 1, "nA"), "k"))
        // The zero stamp is the least, and time and count come before the node.
        assertTrue(compareStamps(HLC.getDefaultInstance(), hlc(0, 1, ""), "k") < 0)
        assertTrue(compareStamps(hlc(5, 0, "z"), hlc(6, 0, "a"), "k") < 0)
        assertTrue(compareStamps(hlc(5, 2, "a"), hlc(5, 1, "z"), "k") > 0)
        // The count is unsigned, as the proto's uint32.
        assertTrue(compareStamps(hlc(5, -1, "a"), hlc(5, 1, "a"), "k") > 0)
    }

    @Test fun referencesToDeletedProjectsAreLeftOut() {
        val st = WorkspaceState.newBuilder()
            .putProjects("live", project("live").build())
            .putProjects("dead", project("dead", deleted = hlc(2)).build())
            .putSpaces("s", space("s").putProjects("live", added(rank = "a")).putProjects("dead", added(rank = "b"))
                .putProjects("repo:x", added(rank = "c")).putHidden("dead", added()).build())
            .putPinnedProjects("dead\u0000", added())
            .putPinnedProjects("dead\u0000root", added())
            .putPinnedProjects("live\u0000", added(rank = "V"))
            .putNames("dead", reg("Dead"))
            .putNames("repo:x", reg("X"))
            .putNames("cleared", reg(""))
            .putLooks("dead", LookEntry.newBuilder().setIcon(reg("star")).build())
            .putLooks("blank", LookEntry.newBuilder().setIcon(reg("")).setColor(reg("")).build())
            .putLooks("live", LookEntry.newBuilder().setColor(reg("teal")).build())
            .build()
        val v = st.view()
        // A section that is no project's is not a deleted one.
        assertEquals(listOf("live", "repo:x"), v.spaces[0].projects)
        assertEquals(emptyList<String>(), v.spaces[0].hidden)
        assertEquals(listOf(Scope("live")), v.pinnedProjects)
        assertEquals(mapOf("repo:x" to "X"), v.names)
        assertEquals(mapOf("live" to Look("", "teal")), v.looks)
    }

    @Test fun listsGoByRankThenIdAsGoComparesThem() {
        val st = WorkspaceState.newBuilder()
            .putSpaces("b", space("b", rank = "V").build())
            .putSpaces("a", space("a", rank = "k").build())
            .putSpaces("none", space("none").build())
            // One rank: by ID, by UTF-8 bytes, where UTF-16 would put the emoji first.
            .putSpaces("😀", space("😀", rank = "z").build())
            .putSpaces("ｚ", space("ｚ", rank = "z").build())
            .build()
        assertEquals(listOf("none", "b", "a", "ｚ", "😀"), st.view().spaces.map { it.id })
        assertTrue(compareGo("ｚ", "😀") < 0)
        assertTrue("ｚ" > "😀") // as Kotlin compares them
        assertTrue(compareGo("a", "ab") < 0 && compareGo("ab", "a") > 0 && compareGo("", "") == 0)
    }

    @Test fun anElementIsInWhileItsAddIsNotOlderThanItsRemoval() {
        val set = mapOf(
            "added" to added(hlc(1), "b"),
            "removed" to removed(hlc(1), hlc(2)),
            "again" to removed(hlc(3), hlc(2), "a"),
            "both" to removed(hlc(2), hlc(2), "c"),
            "never" to ElementMeta.newBuilder().setRemovedAt(hlc(1)).build(),
            "rankless" to added(hlc(1)),
        )
        assertEquals(listOf("rankless", "again", "added", "both"), members(set))
    }
}
