package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.AgentBlock
import dev.ironpark.mako.v2.Panel as Listed
import dev.ironpark.mako.workspace.LayoutPage
import org.junit.Assert.assertEquals
import org.junit.Test

class PanelsTest {
    private fun listed(block: String, title: String, kind: String) = Listed.newBuilder().setBlock(block).setTitle(title).setKind(kind).build()

    private val pages = listOf(
        LayoutPage("a", "mako.files"), LayoutPage("b", "plugin:gone/board"), LayoutPage("c", "plugin:ci-status/ci"),
        LayoutPage("d", "plugin:ci-status/ci"),
    )

    @Test fun anOlderComputersPanelsGoByTheirBlocksNames() {
        val agents = listOf(AgentBlock.newBuilder().setName("agent:s1234567890/board").setSessionId("s1234567890").setAgent("claude").build())
        val got = panels(pages, agents, null)
        assertEquals(listOf("board", "ci", "board"), got.map { it.title })
        assertEquals(listOf(KIND_PLUGIN, KIND_PLUGIN, KIND_AGENT), got.map { it.kind })
        assertEquals("s1234567890", got[2].session)
        assertEquals("claude · s1234567", got[2].detail)
    }

    @Test fun aComputersListTitlesThemAndSaysWhatItLacks() {
        val list = listOf(
            listed("mako.files", "Files", "builtin"),
            listed("plugin:gone/board", "", "missing"),
            listed("plugin:ci-status/ci", "CI", "plugin"),
            listed("agent:s1/order", "Order", "agent"),
            listed("agent:s2/x", "", "agent"),
        )
        val got = panels(pages, emptyList(), list)
        // Laid out in the space's order, the window's own left out, a plugin lacking last.
        assertEquals(listOf("plugin:ci-status/ci", "plugin:gone/board", "agent:s1/order", "agent:s2/x"), got.map { it.block })
        assertEquals(listOf("CI", "board", "Order", "x"), got.map { it.title })
        assertEquals(listOf(KIND_PLUGIN, KIND_MISSING, KIND_AGENT, KIND_AGENT), got.map { it.kind })
        assertEquals(listOf("", "", "s1", "s2"), got.map { it.session })
        // Without the space's layout, the computer's list orders them.
        assertEquals(listOf("plugin:ci-status/ci", "plugin:gone/board", "agent:s1/order", "agent:s2/x"),
            panels(emptyList(), emptyList(), list).map { it.block })
        assertEquals(emptyList<Panel>(), panels(emptyList(), emptyList(), emptyList()))
    }

    @Test fun aTitleFromAnotherMachineIsShownSafely() {
        assertEquals("CI", panelTitle(" C‮I\u0007 ", "x"))
        assertEquals("x", panelTitle("‎ \n", "x"))
        assertEquals(80, panelTitle("y".repeat(200), "x").length)
        assertEquals("s1", agentSession("agent:s1/board"))
    }
}
