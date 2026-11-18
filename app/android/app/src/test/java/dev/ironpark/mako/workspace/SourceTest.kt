package dev.ironpark.mako.workspace

import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.WorkspaceState
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test

class SourceTest {
    private fun node(id: String, revision: Long = 25, trustsUs: Boolean = true, hops: Int = 1) =
        Node.newBuilder().setId(id).setRevision(revision).setTrustsUs(trustsUs).setHops(hops).build()

    private fun named(name: String) = WorkspaceState.newBuilder().putSpaces(name, space(name).build()).build()

    private val empty = WorkspaceState.getDefaultInstance()

    /** Chooses with the computers' workspaces as held, null for one that cannot be read; asked says whom it asked. */
    private fun choose(
        own: WorkspaceState, nodes: List<Node>, held: Map<String, WorkspaceState?>,
        shown: ShownWorkspace = ShownWorkspace(), asked: MutableList<String> = mutableListOf(),
    ) = runBlocking {
        chooseWorkspace(own, workspaceComputers(nodes), shown) { id -> asked += id; held[id] }
    }

    @Test fun theComputersAreThoseThatLetThisDeviceInNewEnoughByID() {
        val nodes = listOf(
            node("c"), node("a"), node("old", revision = 22), node("stranger", trustsUs = false, hops = 0),
            node("far", trustsUs = false, hops = 2),
        )
        assertEquals(listOf("a", "c", "far"), workspaceComputers(nodes).map { it.id })
    }

    @Test fun theFirstComputerHoldingAWorkspaceIsShown() {
        val asked = mutableListOf<String>()
        val got = choose(empty, listOf(node("b"), node("a"), node("c")), mapOf("a" to empty, "b" to named("B"), "c" to named("C")), asked = asked)
        assertEquals(ShownWorkspace(named("B"), "b", editable = true), got)
        // It stops at the first that holds one.
        assertEquals(listOf("a", "b"), asked)
    }

    @Test fun thisDevicesOwnComesFirst() {
        val asked = mutableListOf<String>()
        val got = choose(named("Own"), listOf(node("a")), mapOf("a" to named("A")), asked = asked)
        assertEquals(ShownWorkspace(named("Own"), null, editable = true), got)
        assertEquals(emptyList<String>(), asked)
    }

    @Test fun aComputerTooOldToTakeEditsIsShownButNotEdited() {
        val got = choose(empty, listOf(node("a", revision = 24)), mapOf("a" to named("A")))
        assertEquals(ShownWorkspace(named("A"), "a", editable = false), got)
    }

    @Test fun withNoneTheWorkspaceIsEmpty() {
        // Nothing reachable, only old computers, or only empty states.
        assertEquals(ShownWorkspace(), choose(empty, emptyList(), emptyMap()))
        assertEquals(ShownWorkspace(), choose(empty, listOf(node("a", revision = 22)), mapOf("a" to named("A"))))
        assertEquals(ShownWorkspace(), choose(empty, listOf(node("a"), node("b")), mapOf("a" to empty, "b" to null)))
        assertEquals(false, ShownWorkspace().state.view().held)
    }

    @Test fun aComputerShownStaysShownWhileItCannotBeRead() {
        val shown = ShownWorkspace(named("B"), "b", editable = true)
        assertEquals(shown, choose(empty, listOf(node("a"), node("b"), node("c")), mapOf("a" to empty, "b" to null, "c" to named("C")), shown))
        // But one before it in order that holds a workspace is shown.
        assertEquals(ShownWorkspace(named("A"), "a", true), choose(empty, listOf(node("a"), node("b")), mapOf("a" to named("A"), "b" to null), shown))
        // And one that cannot be read and was not shown is passed over.
        assertEquals(ShownWorkspace(named("C"), "c", true), choose(empty, listOf(node("b"), node("c")), mapOf("b" to null, "c" to named("C"))))
    }
}
