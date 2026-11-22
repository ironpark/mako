package dev.ironpark.mako.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.CenterFocusWeak
import androidx.compose.material.icons.outlined.ZoomIn
import androidx.compose.material.icons.outlined.ZoomOut
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextMeasurer
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.UiNode
import kotlin.math.PI
import kotlin.math.atan2
import kotlin.math.cos
import kotlin.math.hypot
import kotlin.math.ln
import kotlin.math.max
import kotlin.math.min
import kotlin.math.sin
import kotlin.math.sqrt

// An interactive node-edge graph canvas for plugin blocks (revision 44).
// Bounds are checked defensively on-device just like BlockChart.kt, and
// node positions are seeded deterministically from node IDs so the layout
// stays stable across recompositions and tree updates.

private const val MAX_GRAPH_NODES = 120
private const val MAX_GRAPH_EDGES = 400
private const val MAX_GRAPH_ID = 128
private const val MAX_GRAPH_LABEL = 128
private const val MAX_GRAPH_GROUP = 64
private const val MAX_GRAPH_SUBTITLE = 160
private const val MAX_GRAPH_WEIGHT = 1e6
private const val MIN_GRAPH_HEIGHT = 120.0
private const val MAX_GRAPH_HEIGHT = 1200.0
private const val DEFAULT_GRAPH_HEIGHT = 280.0

private val GRAPH_TONES = setOf("muted", "faint", "accent", "success", "warning", "danger")

internal data class GraphNodeModel(
    val id: String,
    val label: String,
    val group: String,
    val tone: String,
    val subtitle: String,
    val weight: Double,
)

internal data class GraphEdgeModel(
    val from: String,
    val to: String,
    val label: String,
    val tone: String,
    val dashed: Boolean,
)

internal data class GraphModel(
    val nodes: List<GraphNodeModel>,
    val edges: List<GraphEdgeModel>,
    val caption: String,
    val selectedId: String,
    val heightDp: Double,
)

internal data class GraphPositionedNode(
    val node: GraphNodeModel,
    /** Normalized position in [0, 1] x [0, 1]. */
    val x: Float,
    val y: Float,
    /** Relative visual radius scale (typically 0.85..1.75). */
    val radiusScale: Float,
)

internal data class GraphLayout(
    val nodes: List<GraphPositionedNode>,
    val byId: Map<String, GraphPositionedNode>,
    val edges: List<GraphEdgeModel>,
)

/** Reads and defensively validates a graph UiNode on-device. */
internal fun graphModel(n: UiNode): GraphModel {
    val seen = LinkedHashSet<String>()
    val nodes = ArrayList<GraphNodeModel>(min(n.graphNodesCount, MAX_GRAPH_NODES))
    for (gn in n.graphNodesList) {
        if (nodes.size >= MAX_GRAPH_NODES) break
        val id = cleanText(gn.id, MAX_GRAPH_ID)
        if (id.isEmpty() || !seen.add(id)) continue
        val label = cleanText(gn.label, MAX_GRAPH_LABEL).ifEmpty { id }
        val group = cleanText(gn.group, MAX_GRAPH_GROUP)
        val tone = gn.tone.takeIf { it in GRAPH_TONES }.orEmpty()
        val subtitle = cleanText(gn.subtitle, MAX_GRAPH_SUBTITLE)
        val w = if (gn.weight.isFinite() && gn.weight > 0.0 && gn.weight <= MAX_GRAPH_WEIGHT) gn.weight else 1.0
        nodes += GraphNodeModel(id, label, group, tone, subtitle, w)
    }
    val edges = ArrayList<GraphEdgeModel>(min(n.graphEdgesCount, MAX_GRAPH_EDGES))
    for (ge in n.graphEdgesList) {
        if (edges.size >= MAX_GRAPH_EDGES) break
        val from = cleanText(ge.from, MAX_GRAPH_ID)
        val to = cleanText(ge.to, MAX_GRAPH_ID)
        if (from !in seen || to !in seen) continue
        edges += GraphEdgeModel(
            from = from,
            to = to,
            label = cleanText(ge.label, MAX_GRAPH_LABEL),
            tone = ge.tone.takeIf { it in GRAPH_TONES }.orEmpty(),
            dashed = ge.dashed,
        )
    }
    val selected = cleanText(n.value, MAX_GRAPH_ID).takeIf { it in seen }.orEmpty()
    val height = if (n.maxHeight.isFinite() && n.maxHeight in MIN_GRAPH_HEIGHT..MAX_GRAPH_HEIGHT) {
        n.maxHeight
    } else {
        DEFAULT_GRAPH_HEIGHT
    }
    return GraphModel(nodes, edges, cleanText(n.text, 512), selected, height)
}

/** Deterministic 64-bit FNV-1a hash used to seed initial node placement. */
private fun fnv1a64(s: String): Long {
    var h = -0x340d631b7bdddcdbL
    for (ch in s) {
        h = (h xor ch.code.toLong()) * 0x100000001b3L
    }
    return h
}

/**
 * Computes a deterministic force-directed layout normalized into [0.12, 0.88]
 * so nodes and labels sit comfortably inside any canvas aspect ratio.
 */
internal fun computeGraphLayout(model: GraphModel): GraphLayout {
    val count = model.nodes.size
    if (count == 0) return GraphLayout(emptyList(), emptyMap(), emptyList())
    if (count == 1) {
        val single = model.nodes[0]
        val placed = GraphPositionedNode(single, 0.5f, 0.5f, nodeRadiusScale(single.weight))
        return GraphLayout(listOf(placed), mapOf(single.id to placed), model.edges)
    }

    val indexOf = HashMap<String, Int>(count * 2)
    val px = DoubleArray(count)
    val py = DoubleArray(count)
    val goldenAngle = 2.399963229728653

    for (i in 0 until count) {
        val node = model.nodes[i]
        indexOf[node.id] = i
        val h = fnv1a64(node.id)
        val u = ((h ushr 16) and 0xFFFFL).toDouble() / 65535.0
        val angle = i * goldenAngle + (u - 0.5) * 0.45
        val radius = 0.25 + 0.65 * sqrt((i + 0.5) / count.toDouble())
        px[i] = cos(angle) * radius
        py[i] = sin(angle) * radius
    }

    val edgePairs = model.edges.mapNotNull { e ->
        val a = indexOf[e.from]
        val b = indexOf[e.to]
        if (a != null && b != null && a != b) a to b else null
    }

    val k = sqrt(2.2 / count.toDouble()).coerceAtLeast(0.18)
    val dx = DoubleArray(count)
    val dy = DoubleArray(count)
    val iterations = if (count <= 40) 90 else 65
    var temp = 0.35

    repeat(iterations) {
        for (i in 0 until count) {
            dx[i] = 0.0
            dy[i] = 0.0
        }
        // Pairwise repulsion + gentle same-group cohesion.
        for (i in 0 until count) {
            val gi = model.nodes[i].group
            for (j in i + 1 until count) {
                var vx = px[i] - px[j]
                var vy = py[i] - py[j]
                val distSq = vx * vx + vy * vy + 1e-4
                val dist = sqrt(distSq)
                val rep = (k * k) / dist
                val rx = (vx / dist) * rep
                val ry = (vy / dist) * rep
                dx[i] += rx
                dy[i] += ry
                dx[j] -= rx
                dy[j] -= ry

                if (gi.isNotEmpty() && gi == model.nodes[j].group) {
                    val pull = dist * 0.04
                    val gx = (vx / dist) * pull
                    val gy = (vy / dist) * pull
                    dx[i] -= gx
                    dy[i] -= gy
                    dx[j] += gx
                    dy[j] += gy
                }
            }
        }
        // Edge spring attraction.
        for ((a, b) in edgePairs) {
            val vx = px[a] - px[b]
            val vy = py[a] - py[b]
            val dist = sqrt(vx * vx + vy * vy + 1e-4)
            val attract = (dist * dist) / (k * 1.15)
            val ax = (vx / dist) * attract
            val ay = (vy / dist) * attract
            dx[a] -= ax
            dy[a] -= ay
            dx[b] += ax
            dy[b] += ay
        }
        // Center gravity + clamped step.
        for (i in 0 until count) {
            dx[i] -= px[i] * 0.14
            dy[i] -= py[i] * 0.14
            val disp = sqrt(dx[i] * dx[i] + dy[i] * dy[i])
            if (disp > 1e-6) {
                val step = min(disp, temp)
                px[i] += (dx[i] / disp) * step
                py[i] += (dy[i] / disp) * step
            }
        }
        temp *= 0.94
    }

    var minX = px[0]
    var maxX = px[0]
    var minY = py[0]
    var maxY = py[0]
    for (i in 1 until count) {
        if (px[i] < minX) minX = px[i]
        if (px[i] > maxX) maxX = px[i]
        if (py[i] < minY) minY = py[i]
        if (py[i] > maxY) maxY = py[i]
    }
    val spanX = max(maxX - minX, 1e-3)
    val spanY = max(maxY - minY, 1e-3)
    val marginX = 0.12
    val marginY = 0.15

    val placed = ArrayList<GraphPositionedNode>(count)
    val byId = LinkedHashMap<String, GraphPositionedNode>(count * 2)
    for (i in 0 until count) {
        val node = model.nodes[i]
        val nx = (marginX + ((px[i] - minX) / spanX) * (1.0 - 2.0 * marginX)).toFloat()
        val ny = (marginY + ((py[i] - minY) / spanY) * (1.0 - 2.0 * marginY)).toFloat()
        val item = GraphPositionedNode(node, nx, ny, nodeRadiusScale(node.weight))
        placed += item
        byId[node.id] = item
    }
    return GraphLayout(placed, byId, model.edges)
}

private fun nodeRadiusScale(weight: Double): Float =
    (0.85f + 0.28f * ln(1.0 + weight.coerceIn(0.1, 100.0)).toFloat()).coerceIn(0.85f, 1.75f)

/** Converts a normalized graph coordinate to canvas pixel space given pan & zoom. */
internal fun graphNodeScreenPosition(
    node: GraphPositionedNode,
    canvasSize: Size,
    zoom: Float,
    pan: Offset,
): Offset {
    val cx = canvasSize.width / 2f
    val cy = canvasSize.height / 2f
    val bx = node.x * canvasSize.width
    val by = node.y * canvasSize.height
    return Offset(
        x = cx + (bx - cx) * zoom + pan.x,
        y = cy + (by - cy) * zoom + pan.y,
    )
}

/**
 * Returns the closest graph node whose hit circle contains [tap], or null if
 * no node is within hit range.
 */
internal fun hitTestGraphNode(
    layout: GraphLayout,
    tap: Offset,
    canvasSize: Size,
    zoom: Float,
    pan: Offset,
    baseRadiusPx: Float,
    minHitRadiusPx: Float,
): GraphNodeModel? {
    if (canvasSize.width <= 0f || canvasSize.height <= 0f) return null
    var best: GraphNodeModel? = null
    var bestDist = Float.POSITIVE_INFINITY
    for (p in layout.nodes) {
        val pos = graphNodeScreenPosition(p, canvasSize, zoom, pan)
        val visualR = baseRadiusPx * p.radiusScale * zoom.coerceIn(0.75f, 2.0f)
        val hitR = max(visualR + 6f, minHitRadiusPx)
        val d = hypot((tap.x - pos.x).toDouble(), (tap.y - pos.y).toDouble()).toFloat()
        if (d <= hitR && d < bestDist) {
            bestDist = d
            best = p.node
        }
    }
    return best
}

@Composable
private fun graphToneColor(tone: String, group: String, fallbackIndex: Int): Color = when {
    tone.isNotEmpty() -> toneColor(tone)
    group.isNotEmpty() -> chartPalette[(fnv1a64(group).ushr(1) % chartPalette.size.toLong()).toInt()]
    else -> chartPalette[fallbackIndex % chartPalette.size]
}

/** Interactive Compose Canvas graph view for Revision 44 `kind == "graph"`. */
@Composable
internal fun UiGraph(n: UiNode, events: BlockEvents, modifier: Modifier = Modifier) {
    val model = remember(n) { graphModel(n) }
    val layout = remember(model) { computeGraphLayout(model) }
    var zoom by remember(n.id) { mutableFloatStateOf(1f) }
    var pan by remember(n.id) { mutableStateOf(Offset.Zero) }
    var canvasIntSize by remember { mutableStateOf(IntSize.Zero) }
    var localSelected by remember(n.id, model.selectedId) { mutableStateOf(model.selectedId) }

    val activeId = localSelected.takeIf { it in layout.byId }.orEmpty()
    val selectedNode = layout.byId[activeId]?.node

    val density = LocalDensity.current
    val baseRadiusPx = with(density) { 11.dp.toPx() }
    val minHitRadiusPx = with(density) { 24.dp.toPx() }
    val measurer = rememberTextMeasurer()

    val summary = stringResource(R.string.graph_summary, model.nodes.size, model.edges.size)
    val a11y = if (model.caption.isNotEmpty()) "${model.caption}. $summary" else summary

    val nodeColors = layout.nodes.mapIndexed { idx, p ->
        p.node.id to graphToneColor(p.node.tone, p.node.group, idx)
    }.toMap()
    val edgeColors = layout.edges.map { e ->
        if (e.tone.isNotEmpty()) toneColor(e.tone) else MaterialTheme.colorScheme.outline
    }
    val accentColor = MaterialTheme.colorScheme.primary
    val surfaceColor = MaterialTheme.colorScheme.surfaceContainerLow
    val pillBg = MaterialTheme.colorScheme.surface.copy(alpha = 0.88f)
    val labelStyle = MaterialTheme.typography.labelSmall.copy(color = MaterialTheme.colorScheme.onSurface)
    val selectedLabelStyle = labelStyle.copy(fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.primary)
    val edgeLabelStyle = MaterialTheme.typography.labelSmall.copy(color = MaterialTheme.colorScheme.onSurfaceVariant)

    Column(modifier.fillMaxWidth().semantics { contentDescription = a11y }) {
        if (model.caption.isNotEmpty()) {
            Text(
                model.caption,
                style = if (n.size == "large") MaterialTheme.typography.titleSmall else MaterialTheme.typography.bodySmall,
                color = toneColor(n.tone.ifEmpty { "muted" }),
                modifier = Modifier.padding(bottom = 6.dp),
            )
        }
        Surface(
            Modifier.fillMaxWidth(),
            shape = RoundedCornerShape(10.dp),
            color = surfaceColor,
            border = MakoColors.cardBorderStroke,
        ) {
            if (layout.nodes.isEmpty()) {
                Box(
                    Modifier.fillMaxWidth().height(120.dp),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        stringResource(R.string.graph_empty),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            } else {
                Column(Modifier.fillMaxWidth()) {
                    Box(
                        Modifier
                            .fillMaxWidth()
                            .height(model.heightDp.dp)
                            .clipToBounds()
                            .onSizeChanged { canvasIntSize = it }
                            .pointerInput(n.id) {
                                detectTransformGestures { centroid, gesturePan, gestureZoom, _ ->
                                    val oldZoom = zoom
                                    val newZoom = (oldZoom * gestureZoom).coerceIn(0.45f, 3.5f)
                                    val factor = newZoom / oldZoom
                                    val cx = canvasIntSize.width / 2f
                                    val cy = canvasIntSize.height / 2f
                                    val dx = centroid.x - cx
                                    val dy = centroid.y - cy
                                    pan = Offset(
                                        x = (pan.x - dx) * factor + dx + gesturePan.x,
                                        y = (pan.y - dy) * factor + dy + gesturePan.y,
                                    )
                                    zoom = newZoom
                                }
                            }
                            .pointerInput(layout, n.id, n.change, n.disabled) {
                                detectTapGestures { tap ->
                                    val sz = Size(canvasIntSize.width.toFloat(), canvasIntSize.height.toFloat())
                                    val hit = hitTestGraphNode(layout, tap, sz, zoom, pan, baseRadiusPx, minHitRadiusPx)
                                    if (hit != null) {
                                        localSelected = hit.id
                                        if (!n.disabled && n.change) {
                                            events.send(n.id, "change", hit.id)
                                        }
                                    }
                                }
                            },
                    ) {
                        Canvas(Modifier.fillMaxSize()) {
                            drawGraphCanvas(
                                layout = layout,
                                zoom = zoom,
                                pan = pan,
                                baseRadiusPx = baseRadiusPx,
                                selectedId = activeId,
                                nodeColors = nodeColors,
                                edgeColors = edgeColors,
                                accentColor = accentColor,
                                surfaceColor = surfaceColor,
                                pillBg = pillBg,
                                labelStyle = labelStyle,
                                selectedLabelStyle = selectedLabelStyle,
                                edgeLabelStyle = edgeLabelStyle,
                                measurer = measurer,
                            )
                        }
                        Surface(
                            Modifier.align(Alignment.TopEnd).padding(8.dp),
                            shape = RoundedCornerShape(8.dp),
                            color = MaterialTheme.colorScheme.surface.copy(alpha = 0.9f),
                            border = MakoColors.cardBorderStroke,
                        ) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                IconButton(
                                    onClick = { zoom = (zoom * 1.25f).coerceAtMost(3.5f) },
                                    modifier = Modifier.size(30.dp),
                                ) {
                                    Icon(Icons.Outlined.ZoomIn, stringResource(R.string.graph_zoom_in), Modifier.size(16.dp))
                                }
                                IconButton(
                                    onClick = { zoom = (zoom / 1.25f).coerceAtLeast(0.45f) },
                                    modifier = Modifier.size(30.dp),
                                ) {
                                    Icon(Icons.Outlined.ZoomOut, stringResource(R.string.graph_zoom_out), Modifier.size(16.dp))
                                }
                                IconButton(
                                    onClick = {
                                        zoom = 1f
                                        pan = Offset.Zero
                                    },
                                    modifier = Modifier.size(30.dp),
                                ) {
                                    Icon(Icons.Outlined.CenterFocusWeak, stringResource(R.string.graph_zoom_reset), Modifier.size(16.dp))
                                }
                            }
                        }
                    }
                    if (selectedNode != null) {
                        HorizontalDivider(color = MakoColors.subtleDivider)
                        Row(
                            Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(8.dp),
                        ) {
                            Dot(nodeColors[selectedNode.id] ?: accentColor, size = 8.dp)
                            Column(Modifier.weight(1f)) {
                                Text(
                                    selectedNode.label,
                                    style = MaterialTheme.typography.labelMedium,
                                    fontWeight = FontWeight.SemiBold,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                                if (selectedNode.subtitle.isNotEmpty()) {
                                    Text(
                                        selectedNode.subtitle,
                                        style = MaterialTheme.typography.bodySmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis,
                                    )
                                }
                            }
                            if (selectedNode.group.isNotEmpty()) {
                                Surface(
                                    shape = RoundedCornerShape(6.dp),
                                    color = MaterialTheme.colorScheme.surfaceVariant,
                                ) {
                                    Text(
                                        selectedNode.group,
                                        Modifier.padding(horizontal = 6.dp, vertical = 2.dp),
                                        style = MaterialTheme.typography.labelSmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                        maxLines = 1,
                                    )
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

private fun DrawScope.drawGraphCanvas(
    layout: GraphLayout,
    zoom: Float,
    pan: Offset,
    baseRadiusPx: Float,
    selectedId: String,
    nodeColors: Map<String, Color>,
    edgeColors: List<Color>,
    accentColor: Color,
    surfaceColor: Color,
    pillBg: Color,
    labelStyle: TextStyle,
    selectedLabelStyle: TextStyle,
    edgeLabelStyle: TextStyle,
    measurer: TextMeasurer,
) {
    val positions = HashMap<String, Offset>(layout.nodes.size * 2)
    val radii = HashMap<String, Float>(layout.nodes.size * 2)
    val radiusZoom = zoom.coerceIn(0.75f, 2.0f)
    for (p in layout.nodes) {
        positions[p.node.id] = graphNodeScreenPosition(p, size, zoom, pan)
        radii[p.node.id] = baseRadiusPx * p.radiusScale * radiusZoom
    }

    val dashEffect = PathEffect.dashPathEffect(floatArrayOf(10f, 7f), 0f)
    val arrowLen = 7.dp.toPx()
    val arrowAngle = PI / 7.0

    // 1. Draw edges with directional arrowheads and optional dashed strokes & labels.
    layout.edges.forEachIndexed { i, edge ->
        val start = positions[edge.from] ?: return@forEachIndexed
        val end = positions[edge.to] ?: return@forEachIndexed
        val rStart = radii[edge.from] ?: baseRadiusPx
        val rEnd = radii[edge.to] ?: baseRadiusPx
        val dx = end.x - start.x
        val dy = end.y - start.y
        val dist = hypot(dx.toDouble(), dy.toDouble()).toFloat()
        if (dist <= rStart + rEnd + 2f) return@forEachIndexed

        val ux = dx / dist
        val uy = dy / dist
        val p1 = Offset(start.x + ux * (rStart + 2f), start.y + uy * (rStart + 2f))
        val p2 = Offset(end.x - ux * (rEnd + 3f), end.y - uy * (rEnd + 3f))

        val incident = selectedId.isNotEmpty() && (edge.from == selectedId || edge.to == selectedId)
        val baseColor = edgeColors[i]
        val strokeColor = if (incident) baseColor.copy(alpha = 0.92f) else baseColor.copy(alpha = 0.52f)
        val strokeWidth = (if (incident) 2.2f else 1.4f).dp.toPx()

        drawLine(
            color = strokeColor,
            start = p1,
            end = p2,
            strokeWidth = strokeWidth,
            cap = StrokeCap.Round,
            pathEffect = if (edge.dashed) dashEffect else null,
        )

        // Arrowhead pointing into target node.
        val theta = atan2(dy.toDouble(), dx.toDouble())
        val a1 = Offset(
            x = p2.x - (arrowLen * cos(theta - arrowAngle)).toFloat(),
            y = p2.y - (arrowLen * sin(theta - arrowAngle)).toFloat(),
        )
        val a2 = Offset(
            x = p2.x - (arrowLen * cos(theta + arrowAngle)).toFloat(),
            y = p2.y - (arrowLen * sin(theta + arrowAngle)).toFloat(),
        )
        val arrowPath = Path().apply {
            moveTo(p2.x, p2.y)
            lineTo(a1.x, a1.y)
            lineTo(a2.x, a2.y)
            close()
        }
        drawPath(arrowPath, color = strokeColor)

        if (edge.label.isNotEmpty() && zoom >= 0.85f) {
            val short = if (edge.label.length > 18) edge.label.take(17) + "…" else edge.label
            val measured = measurer.measure(short, edgeLabelStyle)
            val midX = (p1.x + p2.x) / 2f - measured.size.width / 2f
            val midY = (p1.y + p2.y) / 2f - measured.size.height / 2f
            val padX = 4.dp.toPx()
            val padY = 1.dp.toPx()
            drawRoundRect(
                color = pillBg,
                topLeft = Offset(midX - padX, midY - padY),
                size = Size(measured.size.width + padX * 2f, measured.size.height + padY * 2f),
                cornerRadius = CornerRadius(4.dp.toPx()),
            )
            drawText(measured, topLeft = Offset(midX, midY))
        }
    }

    // 2. Draw nodes and crisp label pills.
    val labelPadH = 5.dp.toPx()
    val labelPadV = 2.dp.toPx()
    val labelGap = 4.dp.toPx()
    for (p in layout.nodes) {
        val center = positions[p.node.id] ?: continue
        val r = radii[p.node.id] ?: baseRadiusPx
        val fill = nodeColors[p.node.id] ?: accentColor
        val selected = p.node.id == selectedId

        if (selected) {
            drawCircle(color = fill.copy(alpha = 0.22f), radius = r + 7.dp.toPx(), center = center)
            drawCircle(
                color = accentColor,
                radius = r + 3.dp.toPx(),
                center = center,
                style = Stroke(width = 2.dp.toPx()),
            )
        }
        drawCircle(color = fill, radius = r, center = center)
        drawCircle(
            color = surfaceColor,
            radius = r,
            center = center,
            style = Stroke(width = 1.5.dp.toPx()),
        )

        val short = if (p.node.label.length > 22) p.node.label.take(21) + "…" else p.node.label
        val measured = measurer.measure(short, if (selected) selectedLabelStyle else labelStyle)
        val lx = center.x - measured.size.width / 2f
        val ly = center.y + r + labelGap
        drawRoundRect(
            color = pillBg,
            topLeft = Offset(lx - labelPadH, ly - labelPadV),
            size = Size(measured.size.width + labelPadH * 2f, measured.size.height + labelPadV * 2f),
            cornerRadius = CornerRadius(4.dp.toPx()),
        )
        drawText(measured, topLeft = Offset(lx, ly))
    }
}
