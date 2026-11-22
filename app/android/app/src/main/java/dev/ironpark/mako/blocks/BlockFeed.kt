package dev.ironpark.mako.blocks

import androidx.compose.runtime.mutableStateMapOf
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.net.MakoException
import dev.ironpark.mako.net.currentLocaleTag
import dev.ironpark.mako.v2.BlockUpdate
import dev.ironpark.mako.v2.UiNode
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow

/**
 * BlockState is a block as its stream last had it: the last update, how
 * the block stands and where it comes from, with the tree whole; why the
 * stream broke, while it is being started again; and whether the
 * computer has no such plugin, which no retry mends.
 */
data class BlockState(val update: BlockUpdate? = null, val tree: UiNode? = null, val broken: String = "", val missing: Boolean = false)

/**
 * The states of node's block, as its updates come: each op applied to the
 * tree the last left. A stream that breaks, or an op the tree cannot take,
 * starts again, for a whole tree; a block that went, or a plugin the
 * computer does not have, ends the flow. from is the state shown before,
 * kept, with why the stream broke, until a stream gives a new one: a page
 * that comes back with the link down still shows its tree.
 */
fun followBlock(
    daemon: Daemon,
    node: String,
    block: String,
    scope: String = "",
    locale: String = currentLocaleTag(),
    from: BlockState = BlockState(),
): Flow<BlockState> = followUpdates(from) { daemon.watchBlock(node, block, scope, locale) }

/** followBlock of the streams watch opens. */
internal fun followUpdates(from: BlockState, watch: () -> Flow<BlockUpdate>): Flow<BlockState> = flow {
    var last = from
    var wait = RETRY
    while (true) {
        var tree: UiNode? = null
        try {
            watch().collect { u ->
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
        } catch (e: MakoException) {
            last = last.copy(broken = e.message.orEmpty(), missing = e.code == MakoException.NO_PLUGIN)
            emit(last)
            if (last.missing) return@flow
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
private val FIELDS = setOf("input", "textarea", "toggle", "select", "tabs")

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

    /** Expansion state for collapsible and branch tree_item nodes; not part of BlockEvent.values. */
    private val expanded = mutableStateMapOf<String, Boolean>()
    private val givenExpanded = mutableMapOf<String, Boolean>()

    /** What each input last sent, or was last given: leaving one sends a change only if its value is not that. */
    private val sent = mutableMapOf<String, String>()

    /** Takes in what tree gives its fields and expandable containers; called as a tree comes, not while one is drawn. */
    fun sync(tree: UiNode?) {
        if (tree == null) return
        walkAll(tree) { n ->
            if (n.kind in FIELDS) {
                val g = given(n)
                if (given[n.id] != g) {
                    given[n.id] = g
                    fields[n.id] = g
                    sent[n.id] = g
                }
            }
            if (isExpandable(n)) {
                val g = n.checked
                if (givenExpanded[n.id] != g) {
                    givenExpanded[n.id] = g
                    expanded[n.id] = g
                }
            }
        }
    }

    /** The value of node n as it stands. */
    fun value(n: UiNode): String = fields[n.id] ?: given(n)

    fun set(id: String, value: String) {
        fields[id] = value
    }

    /** Whether a collapsible or branch tree_item node is currently expanded. */
    fun isExpanded(n: UiNode): Boolean = expanded[n.id] ?: n.checked

    fun setExpanded(id: String, open: Boolean) {
        expanded[id] = open
    }

    /**
     * The change input n sends as the user leaves it: its value, if the
     * user made it other than what it last sent or the block last gave it;
     * null for none, so that a field entered and left untouched, or one the
     * block's answer set, sends nothing.
     */
    fun leave(n: UiNode): String? {
        val now = value(n)
        if (now == (sent[n.id] ?: given(n))) return null
        sent[n.id] = now
        return now
    }

    /** Input n's value as it is submitted, which counts as sent. */
    fun submit(n: UiNode): String = value(n).also { sent[n.id] = it }

    /** The values of tree's fields as they stand, by node id. */
    fun values(tree: UiNode?): Map<String, String> {
        val out = linkedMapOf<String, String>()
        if (tree != null) walk(tree) { out[it.id] = value(it) }
        return out
    }

    private fun given(n: UiNode): String = if (n.kind == "toggle") n.checked.toString() else n.value

    private fun isExpandable(n: UiNode): Boolean =
        n.kind == "collapsible" || (n.kind == "tree_item" && (n.childrenCount > 0 || n.change))

    private fun walk(n: UiNode, fn: (UiNode) -> Unit) {
        if (n.kind in FIELDS) fn(n)
        n.childrenList.forEach { walk(it, fn) }
    }

    private fun walkAll(n: UiNode, fn: (UiNode) -> Unit) {
        fn(n)
        n.childrenList.forEach { walkAll(it, fn) }
    }
}
