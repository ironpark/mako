package dev.ironpark.mako.blocks

import dev.ironpark.mako.v2.UiNode
import dev.ironpark.mako.v2.UiOp

// A block's tree comes whole once, then as the ops that make the last tree
// into the next (docs/protocol.md, Plugin blocks). This is a port of the
// daemon's protocol.ApplyUi (internal/protocol/uiops.go), checked against
// fixtures the Go tests make (src/test/resources/blocks): nodes are found
// by id, the path of keys from the root, in the order the Go finds them,
// so the two apply every op alike, and refuse the same ones.

/** UiOpException is an op the tree cannot take: the stream starts again, for a whole tree. */
class UiOpException(message: String) : Exception(message)

/** The tree ops make of root, which is left as it was; an op naming a node the tree does not hold throws. */
fun applyUi(root: UiNode, ops: List<UiOp>): UiNode = ops.fold(root, ::applyOp)

private fun applyOp(root: UiNode, op: UiOp): UiNode = when (op.op) {
    "replace" ->
        if (op.id == root.id) op.node
        else {
            val path = childPath(root, op.id) ?: throw UiOpException("replace: no node \"${op.id}\"")
            at(root, path) { op.node }
        }
    "update" -> {
        val path = if (op.id == root.id) emptyList() else childPath(root, op.id) ?: throw UiOpException("update: no node \"${op.id}\"")
        // The node's fields become op.node's, its children kept.
        at(root, path) { n -> op.node.toBuilder().clearChildren().addAllChildren(n.childrenList).build() }
    }
    "insert" -> {
        val path = nodePath(root, op.parent)
        val parent = path?.let { node(root, it) }
        if (parent == null || op.index < 0 || op.index > parent.childrenCount) {
            throw UiOpException("insert: no place ${op.index} in \"${op.parent}\"")
        }
        at(root, path) { it.toBuilder().addChildren(op.index, op.node).build() }
    }
    "remove" -> {
        val path = childPath(root, op.id) ?: throw UiOpException("remove: no node \"${op.id}\"")
        at(root, path.dropLast(1)) { it.toBuilder().removeChildren(path.last()).build() }
    }
    "move" -> {
        val path = childPath(root, op.id)
        val parentPath = path?.dropLast(1)
        val parent = parentPath?.let { node(root, it) }
        if (path == null || parent == null || parent.id != op.parent || op.index < 0 || op.index >= parent.childrenCount) {
            throw UiOpException("move: no node \"${op.id}\" in \"${op.parent}\"")
        }
        val moved = parent.getChildren(path.last())
        at(root, parentPath) { it.toBuilder().removeChildren(path.last()).addChildren(op.index, moved).build() }
    }
    else -> throw UiOpException("unknown op \"${op.op}\"")
}

/**
 * The indices from n to the first node under it, not n itself, whose id
 * is id, in the order the daemon looks: a child before its children, its
 * children before its next sibling. Null for none.
 */
private fun childPath(n: UiNode, id: String): List<Int>? {
    for ((i, c) in n.childrenList.withIndex()) {
        if (c.id == id) return listOf(i)
        childPath(c, id)?.let { return listOf(i) + it }
    }
    return null
}

/** The indices from n to the node of id, n itself first; null for none. */
private fun nodePath(n: UiNode, id: String): List<Int>? = if (n.id == id) emptyList() else childPath(n, id)

private fun node(root: UiNode, path: List<Int>): UiNode = path.fold(root) { n, i -> n.getChildren(i) }

/** root with the node at path made over by change, the nodes above it copied. */
private fun at(root: UiNode, path: List<Int>, change: (UiNode) -> UiNode): UiNode {
    if (path.isEmpty()) return change(root)
    val i = path.first()
    return root.toBuilder().setChildren(i, at(root.getChildren(i), path.drop(1), change)).build()
}
