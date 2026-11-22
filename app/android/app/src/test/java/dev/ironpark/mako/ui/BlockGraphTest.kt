package dev.ironpark.mako.ui

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import dev.ironpark.mako.v2.UiGraphEdge
import dev.ironpark.mako.v2.UiGraphNode
import dev.ironpark.mako.v2.UiNode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class BlockGraphTest {
    private fun node(id: String, label: String = id, group: String = "", tone: String = "", weight: Double = 1.0) =
        UiGraphNode.newBuilder().setId(id).setLabel(label).setGroup(group).setTone(tone).setWeight(weight).build()

    private fun edge(from: String, to: String, label: String = "", dashed: Boolean = false) =
        UiGraphEdge.newBuilder().setFrom(from).setTo(to).setLabel(label).setDashed(dashed).build()

    @Test
    fun graphModelValidatesBoundsAndSanitizes() {
        val raw = UiNode.newBuilder()
            .setId("/g")
            .setKind("graph")
            .setText("Knowledge Graph")
            .setValue("b")
            .setMaxHeight(420.0)
            .addGraphNodes(node("a", "Alpha\u0000", tone = "accent", weight = 2.0))
            .addGraphNodes(node("a", "Duplicate")) // dropped
            .addGraphNodes(node("b", "Beta", tone = "bad-tone", weight = -5.0))
            .addGraphEdges(edge("a", "b", "links", dashed = true))
            .addGraphEdges(edge("a", "missing")) // dropped
            .build()

        val model = graphModel(raw)
        assertEquals("Knowledge Graph", model.caption)
        assertEquals("b", model.selectedId)
        assertEquals(420.0, model.heightDp, 0.001)
        assertEquals(2, model.nodes.size)
        assertEquals("Alpha", model.nodes[0].label)
        assertEquals("accent", model.nodes[0].tone)
        assertEquals("", model.nodes[1].tone)
        assertEquals(1.0, model.nodes[1].weight, 0.001)
        assertEquals(1, model.edges.size)
        assertTrue(model.edges[0].dashed)
    }

    @Test
    fun computeGraphLayoutIsDeterministicAndBounded() {
        val raw = UiNode.newBuilder()
            .setId("/g")
            .setKind("graph")
            .addGraphNodes(node("n1", "One", group = "core", weight = 1.0))
            .addGraphNodes(node("n2", "Two", group = "core", weight = 3.0))
            .addGraphNodes(node("n3", "Three", group = "docs", weight = 1.5))
            .addGraphNodes(node("n4", "Four", group = "docs", weight = 1.0))
            .addGraphEdges(edge("n1", "n2"))
            .addGraphEdges(edge("n2", "n3"))
            .addGraphEdges(edge("n3", "n4"))
            .build()

        val m = graphModel(raw)
        val first = computeGraphLayout(m)
        val second = computeGraphLayout(m)

        assertEquals(first.nodes, second.nodes)
        for (p in first.nodes) {
            assertTrue("x out of bounds: ${p.x}", p.x in 0.1f..0.9f)
            assertTrue("y out of bounds: ${p.y}", p.y in 0.1f..0.9f)
        }
        assertTrue(first.byId["n2"]!!.radiusScale > first.byId["n1"]!!.radiusScale)
    }

    @Test
    fun hitTestGraphNodeMatchesWithPanAndZoom() {
        val raw = UiNode.newBuilder()
            .setId("/g")
            .setKind("graph")
            .addGraphNodes(node("a", "A"))
            .addGraphNodes(node("b", "B"))
            .addGraphEdges(edge("a", "b"))
            .build()

        val layout = computeGraphLayout(graphModel(raw))
        val size = Size(400f, 300f)
        val zoom = 1.5f
        val pan = Offset(24f, -18f)

        val target = layout.byId["b"]!!
        val screenPos = graphNodeScreenPosition(target, size, zoom, pan)

        val hit = hitTestGraphNode(layout, screenPos + Offset(3f, -2f), size, zoom, pan, baseRadiusPx = 12f, minHitRadiusPx = 20f)
        assertNotNull(hit)
        assertEquals("b", hit!!.id)

        val miss = hitTestGraphNode(layout, Offset(-500f, -500f), size, zoom, pan, baseRadiusPx = 12f, minHitRadiusPx = 20f)
        assertNull(miss)
    }
}
