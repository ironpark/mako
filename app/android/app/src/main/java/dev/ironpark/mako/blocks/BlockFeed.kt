package dev.ironpark.mako.blocks

import androidx.compose.runtime.mutableStateMapOf
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.v2.BlockUpdate
import dev.ironpark.mako.v2.UiNode
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow

/**
 * BlockState is a block as its stream last had it: the last update, how
 * the block stands and where it comes from, with the tree whole; and why
 * the stream broke, while it is being started again.
 */
data class BlockState(val update: BlockUpdate? = null, val tree: UiNode? = null, val broken: String = "")

/**
 * The states of node's block, as its updates come: each op applied to the
 * tree the last left. A stream that breaks, or an op the tree cannot take,
 * starts again, for a whole tree; a block that went ends the flow.
 */
fun followBlock(daemon: Daemon, node: String, block: String): Flow<BlockState> = flow {
    var last = BlockState()
    var wait = RETRY
    while (true) {
        var tree: UiNode? = null
        try {
            daemon.watchBlock(node, block).collect { u ->
                tree = when {
                    u.hasTree() -> u.tree
                    u.opsCount > 0 && tree != null -> applyUi(tree!!, u.opsList)
                    else -> tree
                }
                last = BlockState(u, tree)
                wait = RETRY
                emit(last)
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            last = last.copy(broken = e.message ?: e.toString())
            emit(last)
        }
        if (last.update?.gone == true) return@flow
        delay(wait)
        wait = minOf(wait * 2, MAX_RETRY)
    }
}

private const val RETRY = 1_000L
private const val MAX_RETRY = 15_000L

/** The kinds of node whose values a block's events carry. */
private val FIELDS = setOf("input", "toggle", "select")

/**
 * BlockForm is what the user set in a block's inputs, toggles and selects,
 * by node id: kept across the tree's updates, so that typing survives one,
 * and sent with every event, so that a form's submit carries all its
 * fields. A value the tree gives anew replaces the user's, as on the
 * desktop.
 */
class BlockForm {
    private val fields = mutableStateMapOf<String, String>()
    private val given = mutableMapOf<String, String>()

    /** Takes in what tree gives its fields; called as a tree comes, not while one is drawn. */
    fun sync(tree: UiNode?) {
        if (tree == null) return
        walk(tree) { n ->
            val g = given(n)
            if (given[n.id] != g) {
                given[n.id] = g
                fields[n.id] = g
            }
        }
    }

    /** The value of node n as it stands. */
    fun value(n: UiNode): String = fields[n.id] ?: given(n)

    fun set(id: String, value: String) {
        fields[id] = value
    }

    /** The values of tree's fields as they stand, by node id. */
    fun values(tree: UiNode?): Map<String, String> {
        val out = linkedMapOf<String, String>()
        if (tree != null) walk(tree) { out[it.id] = value(it) }
        return out
    }

    private fun given(n: UiNode): String = if (n.kind == "toggle") n.checked.toString() else n.value

    private fun walk(n: UiNode, fn: (UiNode) -> Unit) {
        if (n.kind in FIELDS) fn(n)
        n.childrenList.forEach { walk(it, fn) }
    }
}
