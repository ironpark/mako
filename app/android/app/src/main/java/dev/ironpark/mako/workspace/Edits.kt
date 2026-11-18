package dev.ironpark.mako.workspace

import dev.ironpark.mako.v2.ElementMeta
import dev.ironpark.mako.v2.HLC
import dev.ironpark.mako.v2.WorkspaceState

// Editing the workspace: the app gives its node a delta, a WorkspaceState
// holding only what it changed, and the node stamps every clock in it
// with one tick of its own and merges it (docs/protocol.md, Workspaces).
// A front-end has no clock: an add or a removal is asked for by a stamp
// that is not zero, whatever it holds, so the app puts the placeholder
// the desktop's workspace.Diff does, logical 1. The deltas are the ones a
// desktop window's Diff makes of the same edit of its document.

/** Asks for an add, removal, creation or deletion, which the node stamps: workspace.mark's placeholder. */
private val MARK: HLC = HLC.newBuilder().setLogical(1).build()

// Ranks, ported from internal/crdt/rank.go: a rank is a fraction between
// 0 and 1 in base 62, compared as a string, and moving or adding an item
// rewrites its rank alone, to one between its neighbours'.

private const val DIGITS = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

/** c's value as a digit; a character that is none counts as the first digit above it, or the last. */
private fun digitOf(c: Char): Int {
    for (i in DIGITS.indices) if (DIGITS[i] >= c) return i
    return DIGITS.length - 1
}

/** A rank after prev and before next, "" for either standing for the list's end. */
internal fun rankBetween(prev: String, next: String): String {
    val p = prev.trimEnd('0')
    val trimmed = next.trimEnd('0')
    val n = if (trimmed.isEmpty() || compareGo(p, trimmed) >= 0) "" else trimmed
    return midpoint(p, n)
}

/** A rank between a and b, b "" for 1, neither ending in "0". */
private fun midpoint(a: String, b: String): String {
    if (b.isNotEmpty()) {
        var n = 0
        while (n < b.length && digitAt(a, n) == b[n]) n++
        if (n > 0) return b.substring(0, n) + midpoint(from(a, n), b.substring(n))
    }
    val lo = if (a.isNotEmpty()) digitOf(a[0]) else 0
    val hi = if (b.isNotEmpty()) digitOf(b[0]) else DIGITS.length
    if (hi - lo > 1) return DIGITS[(lo + hi) / 2].toString()
    if (b.length > 1) return b.substring(0, 1)
    return DIGITS[lo].toString() + midpoint(from(a, 1), "")
}

private fun digitAt(s: String, i: Int): Char = if (i < s.length) s[i] else DIGITS[0]

private fun from(s: String, i: Int): String = if (i < s.length) s.substring(i) else ""

/**
 * The ranks to give the items of order, in that order, keeping as many of
 * their ranks as it can, as crdt.Rerank does; only the ranks that change.
 */
internal fun rerank(order: List<String>, ranks: Map<String, String>): Map<String, String> {
    val keep = risingRanks(order, ranks)
    val out = linkedMapOf<String, String>()
    var prev = ""
    for ((i, id) in order.withIndex()) {
        if (keep[i]) {
            prev = ranks[id].orEmpty()
            continue
        }
        var next = ""
        for (j in i + 1 until order.size) if (keep[j]) {
            next = ranks[order[j]].orEmpty()
            break
        }
        val r = rankBetween(prev, next)
        if (r != ranks[id].orEmpty()) out[id] = r
        prev = r
    }
    return out
}

/** Marks the items of order in a longest run whose ranks strictly rise; items without a rank are in none. */
private fun risingRanks(order: List<String>, ranks: Map<String, String>): BooleanArray {
    val n = order.size
    val length = IntArray(n)
    val from = IntArray(n)
    var best = -1
    for ((i, id) in order.withIndex()) {
        val r = ranks[id].orEmpty()
        if (r.isEmpty()) continue
        length[i] = 1
        from[i] = -1
        for (j in 0 until i) {
            if (length[j] > 0 && compareGo(ranks[order[j]].orEmpty(), r) < 0 && length[j] + 1 > length[i]) {
                length[i] = length[j] + 1
                from[i] = j
            }
        }
        if (best < 0 || length[i] > length[best]) best = i
    }
    val keep = BooleanArray(n)
    var i = best
    while (i >= 0) {
        keep[i] = true
        i = from[i]
    }
    return keep
}

/**
 * The delta that adds key to the end of an ordered set, or takes it out,
 * as a window's toggled pin is: the add or the removal, and the ranks the
 * list after it is given, ranked anew against every element's rank as
 * workspace.Diff's rankSet ranks it, which rewrites only what does not
 * already rise. Nothing when the set is already so. now is the set's
 * members as the view shows them, which the window's document holds.
 */
private fun toggleDelta(
    set: Map<String, ElementMeta>, key: String, on: Boolean, now: List<String> = members(set),
): Map<String, ElementMeta> {
    if (on == (key in now)) return emptyMap()
    val out = linkedMapOf<String, ElementMeta.Builder>()
    out[key] = if (on) ElementMeta.newBuilder().setAddedAt(MARK) else ElementMeta.newBuilder().setRemovedAt(MARK)
    for ((k, r) in rerank(if (on) now + key else now - key, set.mapValues { it.value.rank })) {
        out.getOrPut(k) { ElementMeta.newBuilder() }.setRank(r).setRankClock(MARK)
    }
    return out.mapValues { it.value.build() }
}

/** The delta that pins session id to the top of the list, after the sessions pinned, or unpins it. */
fun pinSessionDelta(st: WorkspaceState, id: String, pin: Boolean): WorkspaceState =
    WorkspaceState.newBuilder().putAllPinnedSessions(toggleDelta(st.pinnedSessionsMap, id, pin)).build()

/** The delta that pins a project section, or a folder of one, to the top, or unpins it. */
fun pinScopeDelta(st: WorkspaceState, scope: Scope, pin: Boolean): WorkspaceState {
    // The pins of deleted projects are no window's to order.
    val now = members(st.pinnedProjectsMap).filterNot { deletedProject(st, it.substringBefore('\u0000')) }
    val key = scope.section + "\u0000" + scope.root
    return WorkspaceState.newBuilder().putAllPinnedProjects(toggleDelta(st.pinnedProjectsMap, key, pin, now)).build()
}
