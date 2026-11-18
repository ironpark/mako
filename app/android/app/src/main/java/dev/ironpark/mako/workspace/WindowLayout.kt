package dev.ironpark.mako.workspace

import dev.ironpark.mako.v2.WorkspaceState

// A computer's window is laid out by a document of its workspace's
// (docs/protocol.md, "Layout documents"). The phone does not lay it out:
// it shows the document's blocks a page each, those of the document's
// compact root, variants.compact, or of its root folded, as
// internal/layoutdoc's Pages does. This is a port of that, and of the
// reading and checking of a document that decide whether a window draws
// it or its default, but for the props of blocks other than the window's
// own, whose schemas only the computer has. The unit tests check it
// against a fixture of the Go tests (src/test/resources/blocks).

/** LayoutPage is a block the phone shows a page of: its node's ID in the document, and its name. */
data class LayoutPage(val id: String, val block: String)

/** The document a space of st shows, by its ID: the one it names, or "default"; null for none, the built-in one. */
fun WorkspaceState.windowLayout(space: String): String? {
    val name = visibleSpaces(this).firstOrNull { it.id == space }?.layout?.value.orEmpty().ifEmpty { "default" }
    val entry = windowLayoutsMap[name] ?: return null
    if (entry.deleted || entry.doc.isEmpty) return null
    return entry.doc.toStringUtf8()
}

/**
 * The pages of the document doc, null or "" for the built-in default; a
 * document a window would not draw is shown as the default it draws in
 * its place.
 */
fun layoutPages(doc: String?): List<LayoutPage> {
    val read = if (doc.isNullOrEmpty()) null else runCatching { LayoutDoc.parse(doc) }.getOrNull()
    return pagesOf(read?.let { it.compact ?: it.root } ?: LayoutDoc.default)
}

/** The blocks under root in document order, but for the panes and the window's own frame. */
private fun pagesOf(root: LNode): List<LayoutPage> {
    val out = mutableListOf<LayoutPage>()
    fun walk(n: LNode) {
        if (n.kind == "block" && n.block != "mako.sidebar" && n.block != "mako.header") out += LayoutPage(n.id, n.block)
        n.children.forEach(::walk)
    }
    walk(root)
    return out
}

/** A node of a document, as far as the phone reads it. */
internal class LNode(
    val kind: String,
    val id: String,
    val block: String = "",
    val panes: String = "",
    val children: List<LNode> = emptyList(),
    val size: Double = 0.0,
    val grow: Double = 0.0,
    val gap: Double = 0.0,
    val collapsible: Boolean = false,
    val active: Int = 0,
    val props: Map<*, *>? = null,
)

/** A document read: its root, and its compact root, if any. */
internal class LayoutDoc(val root: LNode, val compact: LNode?) {
    companion object {
        /** The latest version this reads strictly (layoutdoc.Version). */
        const val VERSION = 2
        private const val MAX_NODES = 256
        private const val MAX_DEPTH = 16
        private const val MAX_SIZE = 4096.0
        private val KINDS = listOf("row", "column", "stack", "block", "panes")
        private val FIELDS = mapOf(
            "row" to listOf("id", "size", "grow", "gap", "collapsible"),
            "column" to listOf("id", "size", "grow", "gap", "collapsible"),
            "stack" to listOf("id", "size", "grow", "collapsible", "active"),
            "block" to listOf("id", "size", "grow", "collapsible", "props"),
            "panes" to listOf("id", "size", "grow"),
        )
        private val ID = Regex("^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$")
        private val MAKO_BLOCK = Regex("^mako\\.[a-z][a-z0-9-]*$")
        private val PLUGIN_BLOCK = Regex("^plugin:[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$")
        private val AGENT_BLOCK = Regex("^agent:[A-Za-z0-9]+/[A-Za-z0-9][A-Za-z0-9._-]*$")

        /** The window's own blocks, which take no props. */
        private val BUILTIN = setOf("mako.sidebar", "mako.header", "mako.files", "mako.git", "mako.agents", "mako.forge")

        private fun short(name: String) = LNode("block", name, block = name)

        /** The built-in document (layoutdoc.Default), which has no compact root. */
        val default: LNode = LNode("row", "window", children = listOf(
            LNode("block", "sidebar", block = "mako.sidebar", size = 272.0, collapsible = true),
            LNode("column", "workspace", grow = 1.0, children = listOf(
                LNode("block", "header", block = "mako.header"),
                LNode("panes", "main", panes = "main", grow = 1.0),
            )),
            LNode("stack", "details", size = 320.0, collapsible = true,
                children = listOf("mako.files", "mako.git", "mako.agents", "mako.forge").map(::short)),
        ))

        /** Reads text as layoutdoc.Parse does, throwing what it refuses. */
        fun parse(text: String): LayoutDoc {
            val top = Json.parse(text) as? Map<*, *> ?: fail("not an object")
            val v = top["version"] ?: fail("no version")
            val version = (v as? Double)?.takeIf { it >= 1 && it % 1.0 == 0.0 && it < Int.MAX_VALUE }?.toInt() ?: fail("not a version")
            val r = Reader(version > VERSION)
            if (!r.lenient) top.keys.forEach { k ->
                if (k != "version" && k != "root" && (k != "variants" || version < 2)) fail("unknown field $k")
            }
            val root = r.node(top["root"] ?: fail("no root"), 1)
            var compact: LNode? = null
            if (version >= 2 && top.containsKey("variants")) {
                compact = try {
                    r.variants(top["variants"])
                } catch (e: IllegalArgumentException) {
                    if (!r.lenient) throw e
                    null // a later version's, which this cannot read, left out
                }
            }
            check(root, 1..1, version)
            if (compact != null) {
                try {
                    check(compact, 0..1, version)
                    require(count(root) + count(compact) <= MAX_NODES) { "more than $MAX_NODES nodes" }
                } catch (e: IllegalArgumentException) {
                    if (!r.lenient) throw e
                    compact = null
                }
            }
            return LayoutDoc(root, compact)
        }

        private fun fail(why: String): Nothing = throw IllegalArgumentException(why)

        private fun count(n: LNode): Int = 1 + n.children.sumOf(::count)

        private class Reader(val lenient: Boolean) {
            var count = 0

            fun variants(raw: Any?): LNode? {
                if (raw == null) return null
                val vs = raw as? Map<*, *> ?: fail("variants are not an object")
                if (!lenient) vs.keys.forEach { if (it != "compact") fail("no variant $it") }
                val compact = vs["compact"] ?: return null
                return node(compact, 1)
            }

            fun node(raw: Any?, depth: Int): LNode {
                if (++count > MAX_NODES) fail("more than $MAX_NODES nodes")
                if (depth > MAX_DEPTH) fail("nested deeper than $MAX_DEPTH")
                if (raw is String) return short(raw)
                val fields = raw as? Map<*, *> ?: fail("a node is neither an object nor a block's name")
                val keys = fields.keys.map { it as String }
                val present = KINDS.filter { it in keys }
                if (present.size > 1) fail("a node of two kinds")
                if (present.isEmpty()) {
                    if (!lenient) fail("a node of no kind this window knows")
                    // A later version's kind: a leaf, placed as any child.
                    val kind = keys.filter { it !in listOf("id", "size", "grow", "collapsible") }.minOrNull()
                        ?: fail("a node of no kind")
                    return place(kind, fields, emptyList())
                }
                val kind = present.single()
                if (!lenient) keys.forEach { k ->
                    if (k != kind && k !in FIELDS.getValue(kind)) fail("a $kind has no field $k")
                }
                val children = when (kind) {
                    "block", "panes" -> emptyList()
                    else -> (fields[kind] as? List<*> ?: fail("a $kind's children are not a list")).map { node(it, depth + 1) }
                }
                return place(kind, fields, children)
            }

            /** The node of kind of fields, those of its kind's read and typed as the Go reads them. */
            private fun place(kind: String, fields: Map<*, *>, children: List<LNode>): LNode {
                val own = FIELDS[kind] ?: listOf("id", "size", "grow", "collapsible")
                fun has(k: String) = k in own && fields[k] != null
                fun string(k: String): String = if (fields[k] == null) "" else fields[k] as? String ?: fail("$k is not a string")
                fun number(k: String): Double? = if (!has(k)) null else fields[k] as? Double ?: fail("$k is not a number")
                val size = number("size")?.also { if (it <= 0) fail("size not above 0") } ?: 0.0
                val grow = number("grow")?.also { if (it <= 0) fail("grow not above 0") } ?: 0.0
                val gap = number("gap") ?: 0.0
                val collapsible = if (has("collapsible")) fields["collapsible"] as? Boolean ?: fail("collapsible is not a boolean") else false
                val active = number("active")?.also { if (it % 1.0 != 0.0) fail("active is not a whole number") }?.toInt() ?: 0
                val props = if (has("props")) fields["props"] as? Map<*, *> ?: fail("props are not an object") else null
                val id = if (has("id")) string("id") else ""
                return when (kind) {
                    "block" -> string("block").let { LNode(kind, id.ifEmpty { it }, block = it, size = size, grow = grow, collapsible = collapsible, props = props) }
                    "panes" -> string("panes").let { LNode(kind, id.ifEmpty { it }, panes = it, size = size, grow = grow) }
                    else -> LNode(kind, id, children = children, size = size, grow = grow, gap = gap, collapsible = collapsible, active = active)
                }
            }
        }

        /** Checks a root as layoutdoc's Check does, with panes nodes among it. */
        private fun check(root: LNode, panes: IntRange, version: Int) {
            if (root.size > 0 || root.grow > 0 || root.collapsible) fail("the root has a size, grow or collapses")
            val seen = mutableSetOf<String>()
            var count = 0
            var found = 0
            fun walk(n: LNode, depth: Int) {
                if (++count > MAX_NODES) fail("more than $MAX_NODES nodes")
                if (depth > MAX_DEPTH) fail("nested deeper than $MAX_DEPTH")
                if (n.kind !in KINDS && version <= VERSION) fail("a node of no kind this window knows")
                checkNode(n)
                if (!seen.add(n.id)) fail("two nodes have the id ${n.id}")
                if (n.kind == "panes") found++
                n.children.forEach { walk(it, depth + 1) }
            }
            walk(root, 1)
            if (found !in panes) fail("$found panes nodes")
        }

        private fun checkNode(n: LNode) {
            if (!ID.matches(n.id)) fail("an id of other characters: ${n.id}")
            if (!n.size.isFinite() || n.size < 0 || n.size > MAX_SIZE) fail("size out of bounds")
            if (!n.grow.isFinite() || n.grow < 0 || !n.gap.isFinite() || n.gap < 0 || n.gap > MAX_SIZE) fail("grow or gap out of bounds")
            if (n.size > 0 && n.grow > 0) fail("both a size and grow")
            if (n.collapsible && n.size <= 0) fail("collapsible without a size")
            when (n.kind) {
                "row", "column", "stack" -> if (n.children.isEmpty()) fail("a ${n.kind} of no children")
                "block" -> {
                    if (!MAKO_BLOCK.matches(n.block) && !PLUGIN_BLOCK.matches(n.block) && !AGENT_BLOCK.matches(n.block)) fail("block name ${n.block}")
                    if (n.block in BUILTIN && !n.props.isNullOrEmpty()) fail("props the block does not take")
                }
                "panes" -> if (n.panes != "main") fail("panes ${n.panes}")
            }
            if (n.kind == "stack" && (n.active < 0 || n.active >= n.children.size)) fail("active out of the stack")
            if (n.kind == "stack") n.children.forEach { c ->
                if (c.kind != "block") fail("a stack holds blocks alone")
                if (c.size > 0 || c.grow > 0 || c.collapsible) fail("a stack's child is placed")
            }
        }
    }
}
