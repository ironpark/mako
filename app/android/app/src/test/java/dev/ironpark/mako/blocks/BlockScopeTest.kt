package dev.ironpark.mako.blocks

import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.workspace.Folder
import dev.ironpark.mako.workspace.Project
import dev.ironpark.mako.workspace.Root
import dev.ironpark.mako.workspace.Space
import dev.ironpark.mako.workspace.WorkspaceView
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class BlockScopeTest {
    private val view = WorkspaceView(
        spaces = listOf(
            Space(id = "work", name = "Work", color = "", owners = emptyList(), projects = emptyList(), hidden = emptyList(), layout = ""),
            Space(id = "side", name = "", color = "", owners = emptyList(), projects = emptyList(), hidden = emptyList(), layout = ""),
        ),
        projects = listOf(
            Project(
                id = "p-mac",
                name = "Mako",
                noJoin = false,
                giveDirs = false,
                roots = listOf(
                    Root(
                        id = "r1",
                        label = "mako",
                        remote = "github.com/ironpark/mako",
                        owner = "ironpark",
                        locations = listOf(Folder("mac", "/src/mako")),
                        declined = emptyList(),
                    )
                ),
            ),
            Project(
                id = "p-linux",
                name = "Server",
                noJoin = false,
                giveDirs = false,
                roots = listOf(
                    Root(
                        id = "r2",
                        label = "server",
                        remote = "github.com/ironpark/server",
                        owner = "ironpark",
                        locations = listOf(Folder("linux", "/srv/server")),
                        declined = emptyList(),
                    )
                ),
            ),
            Project(
                id = "p-renamed",
                name = "Original",
                noJoin = false,
                giveDirs = false,
                roots = listOf(
                    Root(
                        id = "r3",
                        label = "tools",
                        remote = "",
                        owner = "",
                        locations = listOf(Folder("mac", "/src/tools")),
                        declined = emptyList(),
                    )
                ),
            ),
        ),
        names = mapOf("p-renamed" to "Renamed Tools"),
        held = true,
    )

    private fun session(id: String, node: String, state: String = "running", name: String = "", agent: String = "claude", remote: String = "") =
        SessionInfo.newBuilder()
            .setId(id)
            .setNodeId(node)
            .setState(state)
            .setName(name)
            .setAgent(agent)
            .setRemote(remote)
            .build()

    @Test
    fun normalizesAndParsesScopeKeys() {
        assertEquals("", normalizeScopeKind(""))
        assertEquals("", normalizeScopeKind("global"))
        assertEquals("", normalizeScopeKind("unknown"))
        assertEquals("space", normalizeScopeKind("space"))
        assertEquals("project", normalizeScopeKind("project"))
        assertEquals("session", normalizeScopeKind("session"))

        assertEquals("project" to "p_1.2-a", parseScopeKey("project:p_1.2-a"))
        assertNull(parseScopeKey(""))
        assertNull(parseScopeKey("global:foo"))
        assertNull(parseScopeKey("project:../bad"))
        assertNull(parseScopeKey("project:a/b"))
        assertNull(parseScopeKey("project:"))

        assertEquals("space:work", scopeKeyOf("space", "work"))
        assertEquals("", scopeKeyOf("global", "work"))
        assertEquals("", scopeKeyOf("space", "a/b"))
    }

    @Test
    fun derivesScopeKindAndValidatesPinnedScope() {
        assertEquals("project", scopeKindOf(declaredScope = "project", pinnedScope = "space:work"))
        assertEquals("project", scopeKindOf(pinnedScope = "project:p1"))
        assertEquals("space", scopeKindOf(pinnedScope = "space"))
        assertEquals("session", scopeKindOf(updateScope = "session:s1"))
        assertEquals("", scopeKindOf(declaredScope = "global"))

        assertEquals("project:p1", pinnedScopeKey("project:p1", "project"))
        assertEquals("", pinnedScopeKey("space:work", "project"))
        assertEquals("", pinnedScopeKey("project", "project"))
        assertEquals("space:work", pinnedScopeKey("space:work"))
    }

    @Test
    fun listsScopedTargetsForNode() {
        val sessions = listOf(
            session("s-run", "mac", state = "running", name = "Build", agent = "claude"),
            session("s-exit", "mac", state = "exited", name = "Old"),
            session("s-stop", "mac", state = "stopped", name = "Asleep"),
            session("s-other", "linux", state = "running", name = "Remote"),
        )

        val spaces = scopeOptions(BlockScopeKind.SPACE, "mac", view, sessions) { "Space $it" }
        assertEquals(listOf("space:work", "space:side"), spaces.map { it.key })
        assertEquals(listOf("Work", "Space 2"), spaces.map { it.label })

        val macProjects = scopeOptions(BlockScopeKind.PROJECT, "mac", view, sessions)
        assertEquals(listOf("project:p-mac", "project:p-renamed"), macProjects.map { it.key })
        assertEquals(listOf("Mako", "Renamed Tools"), macProjects.map { it.label })

        val emptyProjects = scopeOptions(BlockScopeKind.PROJECT, "empty-node", view, sessions)
        assertEquals(emptyList<ScopeOption>(), emptyProjects)

        val macSessions = scopeOptions(BlockScopeKind.SESSION, "mac", view, sessions)
        assertEquals(listOf("session:s-run"), macSessions.map { it.key })
        assertEquals(listOf("Build"), macSessions.map { it.label })
        assertEquals(listOf("claude"), macSessions.map { it.detail })

        val noRunningSessions = scopeOptions(BlockScopeKind.SESSION, "mac", view, listOf(session("s-exit", "mac", state = "exited")))
        assertEquals(emptyList<ScopeOption>(), noRunningSessions)
    }

    @Test
    fun resolvesInitialScopeWithPinnedThenPreferredThenFirst() {
        val options = listOf(
            ScopeOption("project:p1", "Alpha"),
            ScopeOption("project:p2", "Beta"),
        )
        // Global returns empty string.
        assertEquals("", resolveInitialScope("global", "project:p1", options, "p2"))
        // Pinned wins over preferred and first.
        assertEquals("project:p-pinned", resolveInitialScope("project", "project:p-pinned", options, "p2"))
        // Preferred wins over first when present in options.
        assertEquals("project:p2", resolveInitialScope("project", "", options, "p2"))
        // Falls back to first option when preferred is absent.
        assertEquals("project:p1", resolveInitialScope("project", "", options, "missing"))
        // Returns empty when no target exists on that computer.
        assertEquals("", resolveInitialScope("project", "", emptyList(), "p1"))
    }

    @Test
    fun resolvesDisplayLabelsWithWorkspaceAndSessionFallback() {
        val sessions = listOf(session("s1", "mac", name = "Fix bug"))
        assertEquals("Work", scopeDisplayLabel("space:work", emptyList(), view, sessions))
        assertEquals("Mako", scopeDisplayLabel("project:p-mac", emptyList(), view, sessions))
        assertEquals("Renamed Tools", scopeDisplayLabel("project:p-renamed", emptyList(), view, sessions))
        assertEquals("Fix bug", scopeDisplayLabel("session:s1", emptyList(), view, sessions))
        assertEquals("unknown-id", scopeDisplayLabel("project:unknown-id", emptyList(), view, sessions))
    }
}
