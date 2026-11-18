package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.workspace.GoOrder
import dev.ironpark.mako.workspace.Look
import dev.ironpark.mako.workspace.Project
import dev.ironpark.mako.workspace.Scope
import dev.ironpark.mako.workspace.WorkspaceView
import dev.ironpark.mako.workspace.folderName
import dev.ironpark.mako.workspace.lookOf

/** What the home screen groups the sessions by; within a group, the latest first. */
internal enum class Grouping { Computer, Status, Project, Time }

/**
 * SessionGroup is one heading of the list and its sessions: a computer's
 * name, a project's, or a status; key tells it from the others. A
 * project's, as the workspace has it, carries its look, the pin it toggles
 * and whether it is pinned, and the folder of the project each session
 * is in, by ID, where it has several.
 */
internal data class SessionGroup(
    val key: String, val label: String, val sessions: List<SessionInfo>,
    val look: Look? = null, val pin: Scope? = null, val pinned: Boolean = false,
    val tags: Map<String, String> = emptyMap(),
)

/** The key of the group of the sessions pinned, which heads the list. */
internal const val PINNED = "pinned"

/** The statuses the list is grouped by, in their order: what needs you first, what has ended last. */
internal enum class StatusGroup { NeedsYou, Working, Done, Idle, Ended }

internal fun SessionInfo.statusGroup(): StatusGroup = when {
    needsYou() -> StatusGroup.NeedsYou
    ended() -> StatusGroup.Ended
    status == "working" -> StatusGroup.Working
    status == "done" -> StatusGroup.Done
    else -> StatusGroup.Idle
}

/** The folder a session's project is: its repository's main work tree, else where it runs. */
private fun SessionInfo.projectPath(): String = worktree.repo.ifEmpty { repo.ifEmpty { project } }

/**
 * What tells a session's project from others: its repository's remote,
 * the same for its clones on every computer, else its folder on its own.
 */
internal fun SessionInfo.projectKey(): String = remote.ifEmpty { nodeId + ":" + projectPath() }

/** A project's name: its repository's, else its folder's. */
internal fun SessionInfo.projectName(): String = baseName(remote.ifEmpty { projectPath() }).ifEmpty { "~" }

/**
 * sessions grouped by, each group the latest first: computers by name,
 * projects by their latest, statuses in their order, and by time one
 * group of them all.
 */
internal fun groupSessions(sessions: List<SessionInfo>, by: Grouping, nodeName: (String) -> String): List<SessionGroup> {
    val latest = sessions.sortedByDescending { it.lastActive() }
    return when (by) {
        Grouping.Time -> if (latest.isEmpty()) emptyList() else listOf(SessionGroup("all", "", latest))
        Grouping.Computer -> latest.groupBy { it.nodeId }
            .map { (node, list) -> SessionGroup("node:$node", nodeName(node), list) }
            .sortedBy { it.label.lowercase() }
        // Ordered as their sessions are: the project worked in last first.
        Grouping.Project -> {
            val groups = latest.groupBy { it.projectKey() }.toList()
            // Folders of one name on two computers, not one repository's clones, say whose.
            val shared = groups.groupingBy { it.second.first().projectName() }.eachCount()
            groups.map { (key, list) ->
                val name = list.first().projectName()
                val whose = if (shared.getValue(name) > 1 && list.first().remote.isEmpty()) nodeName(list.first().nodeId) else null
                SessionGroup("project:$key", dotted(name, whose), list)
            }
        }
        Grouping.Status -> latest.groupBy { it.statusGroup() }.toSortedMap()
            .map { (status, list) -> SessionGroup("status:${status.name}", status.name, list) }
    }
}

// The desktop's grouping, for a node that holds a workspace: sessions go
// into the sections the desktop's sidebar shows, a port of internal/gui's
// grouping.go and projects.go (groupProjects, placeIn, byRemote), each
// section placed in a space as projectmenu.go's placeOf places it, and
// pins first as sidebartree.go and sidebaragents.go list them.

/** The main work tree of a session's repository; "" outside one. */
internal fun SessionInfo.repoPath(): String = repo.ifEmpty { worktree.repo }

/** The folder a session's section is for, as the desktop's projectOf: its repository's main work tree, else its own. */
internal fun SessionInfo.sectionFolder(): String = repoPath().ifEmpty { project }

/** Whether dir is root or a folder inside it, written with either separator (pathscope.Within). */
internal fun within(dir: String, root: String): Boolean {
    if (dir == root) return true
    if (root.isEmpty()) return false
    val sep = if ('\\' in root && '/' !in root) "\\" else "/"
    return dir.startsWith(root.removeSuffix(sep) + sep)
}

/**
 * Whether a project's folder loc holds a session working in dir, in the
 * repository whose main work tree is repo (pathscope.Covers): the folders
 * inside it, but not a repository of its own inside it; a repository's
 * folder holds its worktrees, wherever they are.
 */
internal fun covers(loc: String, dir: String, repo: String): Boolean =
    if (repo.isEmpty()) within(dir, loc) else repo == loc || within(dir, loc) && within(loc, repo)

/** Where a session goes among the projects: the project's index, and its root's; join says by its remote alone. */
internal data class Placement(val project: Int, val root: Int, val join: Boolean = false)

/**
 * The project a session on node, working in dir, is in (placeIn): the one
 * with a folder that holds it, the deepest when several do, else the one
 * root whose repository it is a clone of. A clone two roots claim, or one
 * taken out, is in none, and the desktop groups it itself.
 */
internal fun placeIn(defs: List<Project>, node: String, dir: String, repo: String, remote: String): Placement? {
    var best: Placement? = null
    var depth = -1
    for ((i, p) in defs.withIndex()) for ((j, r) in p.roots.withIndex()) for (l in r.locations) {
        if (l.node == node && covers(l.path, dir, repo) && l.path.length > depth) {
            best = Placement(i, j)
            depth = l.path.length
        }
    }
    if (depth >= 0 || remote.isEmpty()) return best
    val at = dev.ironpark.mako.workspace.Folder(node, repo.ifEmpty { dir })
    var claims = 0
    for ((i, p) in defs.withIndex()) for ((j, r) in p.roots.withIndex()) {
        if (r.remote != remote) continue
        claims++
        if (!p.noJoin && at !in r.declined) best = Placement(i, j, join = true)
    }
    return if (claims == 1) best else null
}

/** The key the desktop groups a folder by on its own (implicitKey): its repository's remote, else the node's folder. */
internal fun implicitKey(node: String, folder: String, remote: String): String =
    if (remote.isNotEmpty()) "repo:$remote" else "$node\u0000$folder"

/** The ID of a section grouped by key, set apart from a project's of the same ID (sectionKey). */
internal fun sectionKey(defs: List<Project>, key: String): String = if (defs.any { it.id == key }) "free:$key" else key

/**
 * Section is a section of the desktop's sidebar: a project the user put
 * together, or the sessions the sidebar groups itself, by repository or
 * folder. key is its ID, the one the workspace's names, looks, pins and
 * spaces know it by; node and folder are where it is, by default.
 */
internal data class Section(
    val key: String,
    val name: String,
    val node: String,
    val folder: String,
    val owner: String,
    val remote: String,
    val project: Project? = null,
    val sessions: List<SessionInfo> = emptyList(),
    val rootOf: Map<String, Int> = emptyMap(),
) {
    /** How it is named and sorted (groupName): its name, else its folder's. */
    val label: String get() = name.ifEmpty { folderName(folder) }

    /** The owners of its repositories, its own, its sessions' and its roots' (groupOwners). */
    val owners: List<String>
        get() = (listOf(owner) + sessions.map { it.owner } + project?.roots.orEmpty().map { it.owner })
            .filter { it.isNotEmpty() }.distinct().sortedWith(GoOrder)
}

/** The section of project p, before its sessions (projectGroup): where its first root is, and the owner its roots share. */
private fun projectSection(p: Project): Section {
    val first = p.roots.firstOrNull()?.locations?.firstOrNull() ?: p.roots.flatMap { it.locations }.firstOrNull()
    val owners = p.roots.map { it.owner }.distinct()
    return Section(
        key = p.id, name = p.name, node = first?.node.orEmpty(), folder = first?.path.orEmpty(),
        owner = if (owners.size == 1) owners[0] else "",
        remote = if (p.roots.size == 1) p.roots[0].remote else "", project = p,
    )
}

/**
 * The sections of the desktop's sidebar the sessions are in, by name as
 * it sorts them (groupProjects): the projects', each with the sessions a
 * folder of it holds, and those grouped by repository or folder of the
 * rest, the clones of one repository on several computers together.
 */
internal fun sections(sessions: List<SessionInfo>, view: WorkspaceView): List<Section> {
    val defs = view.projects
    val own = defs.map { projectSection(it) }.toMutableList()
    val rest = mutableListOf<SessionInfo>()
    for (s in sessions) {
        val pl = placeIn(defs, s.nodeId, s.project, s.repoPath(), s.remote)
        if (pl == null) {
            rest += s
            continue
        }
        val sec = own[pl.project]
        own[pl.project] = sec.copy(sessions = sec.sessions + s, rootOf = sec.rootOf + (s.id to pl.root))
    }
    // By node and folder in the order each first comes, then the clones of
    // one repository together under it (bucket, byRemote).
    val buckets = rest.groupBy { it.nodeId + "\u0000" + it.sectionFolder() }.values
    val grouped = mutableListOf<Section>()
    val byRemote = mutableMapOf<String, Int>()
    for (list in buckets) {
        val s = list.first()
        val i = byRemote[s.remote]
        if (s.remote.isEmpty() || i == null) {
            if (s.remote.isNotEmpty()) byRemote[s.remote] = grouped.size
            grouped += Section(
                key = implicitKey(s.nodeId, s.sectionFolder(), s.remote), name = "",
                node = s.nodeId, folder = s.sectionFolder(), owner = s.owner, remote = s.remote, sessions = list,
            )
        } else {
            grouped[i] = grouped[i].copy(sessions = grouped[i].sessions + list)
        }
    }
    val implicit = grouped.sortedWith(compareBy(GoOrder) { folderName(it.folder) }).map {
        val key = sectionKey(defs, it.key)
        it.copy(key = key, name = view.names[key].orEmpty())
    }
    return (implicit + own).sortedWith(compareBy(GoOrder) { it.label })
}

/** The index of the space a section nothing else places goes to: the one chosen, else the first; 0, every session's, with none. */
internal fun WorkspaceView.defaultSpaceIndex(): Int =
    if (spaces.isEmpty()) 0 else maxOf(spaces.indexOfFirst { it.id == defaultSpace }, 0) + 1

/**
 * The index of the space the section of key, whose repositories owners
 * hold, is in (placeOf): the first it was moved to, else the first that
 * holds one of its owners and did not leave it out, else the default.
 */
internal fun WorkspaceView.placeOf(key: String, owners: List<String>): Int {
    spaces.forEachIndexed { i, sp -> if (key in sp.projects) return i + 1 }
    spaces.forEachIndexed { i, sp -> if (key !in sp.hidden && owners.any { it in sp.owners }) return i + 1 }
    return defaultSpaceIndex()
}

/** The index of the first space offered: every session's, 0, unless it is left out for the others. */
internal fun WorkspaceView.firstSpace(): Int = if (noAll && spaces.isNotEmpty()) 1 else 0

/** The index of the space of ID id, or the first offered when there is none ("" is every session's). */
internal fun WorkspaceView.spaceIndex(id: String): Int = maxOf(spaces.indexOfFirst { it.id == id } + 1, firstSpace())

/** The ID of space i, "" for every session's. */
internal fun WorkspaceView.spaceId(i: Int): String = spaces.getOrNull(i - 1)?.id.orEmpty()

/**
 * SessionWorkspace is the sessions as the workspace sorts them: into the
 * desktop's sections, each in its space, with what is pinned first.
 */
internal class SessionWorkspace(val view: WorkspaceView, sessions: List<SessionInfo>) {
    val sections: List<Section> = sections(sessions, view)
    private val sectionOf: Map<String, Section> = sections.flatMap { sec -> sec.sessions.map { it.id to sec } }.toMap()

    /** Whether space i shows session s: every session's shows every one, the others the sections placed in them. */
    fun inSpace(s: SessionInfo, i: Int): Boolean {
        if (i !in 1..view.spaces.size) return true
        val sec = sectionOf[s.id] ?: return true
        return view.placeOf(sec.key, sec.owners) == i
    }

    /**
     * sessions grouped by, as [groupSessions] does, but with the sessions
     * pinned first, the latest first, and by project the desktop's
     * sections: those pinned, and the folders of projects pinned on their
     * own, in the order pinned, then the rest by name.
     */
    fun groups(sessions: List<SessionInfo>, by: Grouping, nodeName: (String) -> String): List<SessionGroup> {
        val pins = view.pinnedSessions.toSet()
        val (pinned, rest) = sessions.partition { it.id in pins }
        val head = if (pinned.isEmpty()) emptyList()
        else listOf(SessionGroup(PINNED, "", pinned.sortedByDescending { it.lastActive() }))
        return head + if (by == Grouping.Project) projectGroups(rest, nodeName) else groupSessions(rest, by, nodeName)
    }

    private fun projectGroups(sessions: List<SessionInfo>, nodeName: (String) -> String): List<SessionGroup> {
        val shown = sessions.map { it.id }.toSet()
        val whole = view.pinnedProjects.filter { it.root.isEmpty() }.map { it.section }.toSet()
        // Sessions in folders pinned on their own show there, not again under their project.
        val alone = mutableMapOf<String, String>()
        val pinnedRows = mutableListOf<SessionGroup>()
        val labels = labels(nodeName)
        for (pin in view.pinnedProjects) {
            val sec = sections.firstOrNull { it.key == pin.section } ?: continue
            if (pin.root.isEmpty()) {
                group(sec, shown, labels, emptySet())?.let { pinnedRows += it.copy(pinned = true) }
                continue
            }
            if (sec.key in whole) continue
            val roots = sec.project?.roots ?: continue
            val j = roots.indexOfFirst { it.key() == pin.root }
            if (j < 0) continue
            val list = sec.sessions.filter { it.id in shown && sec.rootOf[it.id] == j }.sortedByDescending { it.lastActive() }
            list.forEach { alone[it.id] = sec.key }
            if (list.isNotEmpty()) pinnedRows += SessionGroup(
                "pin:" + groupKey(sec.key) + "/" + pin.root, labels.getValue(sec.key) + " / " + roots[j].label(), list,
                look = lookOf(view.looks, sec.key), pin = pin, pinned = true,
            )
        }
        val rows = sections.filter { it.key !in whole }.mapNotNull { group(it, shown, labels, alone.keys) }
        return pinnedRows + rows
    }

    /** Section sec's group of the sessions shown, but those left out; null when none is left. */
    private fun group(sec: Section, shown: Set<String>, labels: Map<String, String>, leave: Set<String>): SessionGroup? {
        val list = sec.sessions.filter { it.id in shown && it.id !in leave }.sortedByDescending { it.lastActive() }
        if (list.isEmpty()) return null
        val roots = sec.project?.roots.orEmpty()
        val tags = if (roots.size < 2) emptyMap()
        else list.mapNotNull { s -> sec.rootOf[s.id]?.let { s.id to "#" + roots[it].label() } }.toMap()
        return SessionGroup(
            "section:" + groupKey(sec.key), labels.getValue(sec.key), list,
            look = lookOf(view.looks, sec.key), pin = Scope(sec.key), tags = tags,
        )
    }

    /**
     * How the sections are named (projectLabels): their names, and those of
     * one folder name the desktop names by more of their paths, or by
     * their computers when even that does not set them apart.
     */
    internal fun labels(nodeName: (String) -> String): Map<String, String> {
        val out = sections.associate { it.key to it.label }.toMutableMap()
        val twins = sections.filter { it.folder.isNotEmpty() && it.name.isEmpty() }.groupBy { folderName(it.folder) }
        for ((name, same) in twins) {
            if (same.size < 2) continue
            val depth = (2..4).firstOrNull { d -> same.map { tail(it.folder, d) }.distinct().size == same.size }
            for (sec in same) out[sec.key] = if (depth != null) tail(sec.folder, depth) else name + " · " + nodeName(sec.node)
        }
        return out
    }
}

/** The last n elements of path, slash-separated. */
internal fun tail(path: String, n: Int): String = path.replace('\\', '/').split('/').filter { it.isNotEmpty() }.takeLast(n).joinToString("/")

/** A group's key from a section's, which a preference can hold: without the NUL a folder's key has. */
private fun groupKey(section: String): String = section.replace('\u0000', ':')
