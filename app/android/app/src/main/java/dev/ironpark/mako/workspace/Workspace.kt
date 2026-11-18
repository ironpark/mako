package dev.ironpark.mako.workspace

import dev.ironpark.mako.v2.ElementMeta
import dev.ironpark.mako.v2.HLC
import dev.ironpark.mako.v2.ProjectEntry
import dev.ironpark.mako.v2.ProjectRootEntry
import dev.ironpark.mako.v2.SpaceEntry
import dev.ironpark.mako.v2.WorkspaceState

// Reading the workspace the node holds, a CRDT (proto/mako/v2/workspace.proto),
// as the desktop does: a port of internal/workspace's view.go, with the
// order and tie-breaking of internal/crdt's hlc.go and set.go, so that the
// phone shows what every desktop window shows of the same state. Only a
// daemon stamps; nothing here makes a clock.
//
// An entity, a space, project or root, shows while it was created later
// than it was deleted. What refers to an entity that does not show, a
// space listing a deleted project, a pin of it, its name or look, is kept
// in the state, and left out here. A list's order is its entries' ranks,
// then their IDs, compared as Go compares strings: by their UTF-8 bytes.

/** Compares strings as Go does, by their UTF-8 bytes, which is by their code points. */
internal fun compareGo(a: String, b: String): Int {
    var i = 0
    var j = 0
    while (i < a.length && j < b.length) {
        val ca = a.codePointAt(i)
        val cb = b.codePointAt(j)
        if (ca != cb) return if (ca < cb) -1 else 1
        i += Character.charCount(ca)
        j += Character.charCount(cb)
    }
    return when {
        i < a.length -> 1
        j < b.length -> -1
        else -> 0
    }
}

/** Orders strings as [compareGo] does. */
internal val GoOrder: Comparator<String> = Comparator(::compareGo)

private fun HLC.isZero(): Boolean = wallTime == 0L && logical == 0 && nodeId.isEmpty()

/** The 64-bit FNV-1a hash of s's UTF-8 bytes, as Go's hash/fnv makes it. */
internal fun fnv64a(s: String): ULong {
    var h = 0xcbf29ce484222325UL
    for (b in s.toByteArray(Charsets.UTF_8)) {
        h = h xor (b.toULong() and 0xffUL)
        h *= 0x100000001b3UL
    }
    return h
}

/**
 * Orders stamps a and b of the entry key as crdt.Compare does: time, then
 * the counter, then a hash of the key and the node, so that which node
 * wins a tie differs from entry to entry, then the nodes themselves. The
 * zero stamp, of an entry never written, is the least.
 */
internal fun compareStamps(a: HLC, b: HLC, key: String): Int {
    if (a.wallTime == b.wallTime && a.logical == b.logical && a.nodeId == b.nodeId) return 0
    if (a.isZero()) return -1
    if (b.isZero()) return 1
    a.wallTime.compareTo(b.wallTime).let { if (it != 0) return it }
    // logical is a uint32, which Java holds in an int.
    Integer.compareUnsigned(a.logical, b.logical).let { if (it != 0) return it.coerceIn(-1, 1) }
    fnv64a(key + a.nodeId).compareTo(fnv64a(key + b.nodeId)).let { if (it != 0) return it.coerceIn(-1, 1) }
    return compareGo(a.nodeId, b.nodeId).coerceIn(-1, 1)
}

/** Whether an entity created and deleted at these stamps shows; key is its key in the state. */
internal fun visible(created: HLC, deleted: HLC, key: String): Boolean = compareStamps(created, deleted, key) > 0

/** Whether an element of a set, key, is in it: added, and not removed since; an add and a removal of one stamp leave it in. */
internal fun ElementMeta.active(key: String): Boolean = !addedAt.isZero() && compareStamps(removedAt, addedAt, key) <= 0

internal fun spaceKey(id: String) = "space\u0000$id"
internal fun projectKey(id: String) = "project\u0000$id"
internal fun rootKey(project: String, id: String) = "$project\u0000root\u0000$id"

/** The elements in a set, by rank and then by themselves. */
internal fun members(set: Map<String, ElementMeta>): List<String> =
    set.filter { (k, e) -> e.active(k) }.keys
        .sortedWith { a, b -> compareGo(set.getValue(a).rank, set.getValue(b).rank).takeIf { it != 0 } ?: compareGo(a, b) }

private fun <T> byRank(rank: (T) -> String, id: (T) -> String) = Comparator<T> { a, b ->
    compareGo(rank(a), rank(b)).takeIf { it != 0 } ?: compareGo(id(a), id(b))
}

/** The spaces that show, by rank and then by ID. */
internal fun visibleSpaces(st: WorkspaceState): List<SpaceEntry> =
    st.spacesMap.filter { (id, sp) -> visible(sp.createdAt, sp.deletedAt, spaceKey(id)) }.values
        .sortedWith(byRank({ it.rank.value }, { it.id }))

/** The projects that show, by rank and then by ID. */
internal fun visibleProjects(st: WorkspaceState): List<ProjectEntry> =
    st.projectsMap.filter { (id, p) -> visible(p.createdAt, p.deletedAt, projectKey(id)) }.values
        .sortedWith(byRank({ it.rank.value }, { it.id }))

/** The roots of p that show, by rank and then by ID. */
internal fun visibleRoots(p: ProjectEntry): List<ProjectRootEntry> =
    p.rootsMap.filter { (id, r) -> visible(r.createdAt, r.deletedAt, rootKey(projectKey(p.id), id)) }.values
        .sortedWith(byRank({ it.rank.value }, { it.id }))

/** Whether id is a project that does not show; a section's key that is no project's is not. */
internal fun deletedProject(st: WorkspaceState, id: String): Boolean {
    val p = st.projectsMap[id] ?: return false
    return !visible(p.createdAt, p.deletedAt, projectKey(id))
}

/** A folder on a node, as a root's location key holds it: the node, a NUL, the path. */
data class Folder(val node: String, val path: String)

/** A project's root: one repository, or one folder outside any, where it is on each node. */
data class Root(
    val id: String, val label: String, val remote: String, val owner: String,
    val locations: List<Folder>, val declined: List<Folder>,
) {
    /** How the root's sessions name it: its label, else its folder's name. */
    fun label(): String = label.ifEmpty {
        locations.firstOrNull()?.path?.let(::folderName) ?: remote.takeIf { it.isNotEmpty() }?.let(::folderName) ?: "~"
    }

    /** How a pin of the root names it, as the desktop's rootKey: its repository, else where it first is. */
    fun key(): String = when {
        remote.isNotEmpty() -> remote
        locations.isNotEmpty() -> locations[0].node + "\u0000" + locations[0].path
        else -> label
    }
}

/** A project: folders the user put together, on any node. */
data class Project(val id: String, val name: String, val noJoin: Boolean, val giveDirs: Boolean, val roots: List<Root>)

/** A space: a named set of project sections. */
data class Space(
    val id: String, val name: String, val color: String,
    val owners: List<String>, val projects: List<String>, val hidden: List<String>, val layout: String,
)

/** A section's icon and colour, by their names among the desktop's; "" for none. */
data class Look(val icon: String, val color: String)

/** What a pin of a project names: a section, by its key, or a folder of one, by [Root.key]; root is "" for the section. */
data class Scope(val section: String, val root: String = "")

/**
 * WorkspaceView is the workspace as the phone reads it, the same as
 * workspace.View makes of it for a desktop window: the spaces, projects
 * and roots that show, in order; the names and looks given; and the pins,
 * all but what refers to what does not show.
 */
data class WorkspaceView(
    val noAll: Boolean = false,
    val defaultSpace: String = "",
    val spaces: List<Space> = emptyList(),
    val projects: List<Project> = emptyList(),
    val names: Map<String, String> = emptyMap(),
    val looks: Map<String, Look> = emptyMap(),
    val pinnedProjects: List<Scope> = emptyList(),
    val pinnedSessions: List<String> = emptyList(),
    /** Whether the node holds a workspace at all; without one the app lists as it always has. */
    val held: Boolean = false,
)

/** The folders of locations' keys. */
private fun folders(keys: List<String>): List<Folder> = keys.mapNotNull { k ->
    val at = k.indexOf('\u0000')
    if (at < 0) null else Folder(k.substring(0, at), k.substring(at + 1))
}

/** A folder's name, as filepath.Base gives it; "~" for none. */
internal fun folderName(path: String): String {
    val trimmed = path.trimEnd('/', '\\')
    if (trimmed.isEmpty()) return if (path.isEmpty()) "~" else path.take(1)
    return trimmed.substringAfterLast('/').substringAfterLast('\\')
}

/** st as the app reads it; see [WorkspaceView]. */
fun WorkspaceState.view(): WorkspaceView {
    val st = this
    return WorkspaceView(
        noAll = noAll.value,
        defaultSpace = defaultSpace.value,
        spaces = visibleSpaces(st).map { sp ->
            Space(
                sp.id, sp.name.value, sp.color.value, members(sp.ownersMap),
                members(sp.projectsMap).filterNot { deletedProject(st, it) },
                members(sp.hiddenMap).filterNot { deletedProject(st, it) },
                sp.layout.value,
            )
        },
        projects = visibleProjects(st).map { p ->
            Project(p.id, p.name.value, p.noJoin.value, p.giveDirs.value, visibleRoots(p).map { r ->
                Root(r.id, r.label.value, r.remote.value, r.owner.value, folders(members(r.locationsMap)), folders(members(r.declinedMap)))
            })
        },
        names = st.namesMap.filter { (k, n) -> n.value.isNotEmpty() && !deletedProject(st, k) }.mapValues { it.value.value },
        looks = st.looksMap.mapValues { (_, l) -> Look(l.icon.value, l.color.value) }
            .filter { (k, l) -> l != Look("", "") && !deletedProject(st, k) },
        pinnedProjects = members(st.pinnedProjectsMap).filterNot { deletedProject(st, it.substringBefore('\u0000')) }
            .map { Scope(it.substringBefore('\u0000'), it.substringAfter('\u0000', "")) },
        pinnedSessions = members(st.pinnedSessionsMap),
        held = st.serializedSize > 0,
    )
}
