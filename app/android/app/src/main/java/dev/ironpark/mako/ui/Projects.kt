package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.AgentBlock
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.workspace.Folder
import dev.ironpark.mako.workspace.LayoutPage
import dev.ironpark.mako.workspace.Look
import dev.ironpark.mako.workspace.Scope
import dev.ironpark.mako.workspace.WorkspaceView
import dev.ironpark.mako.workspace.lookOf

// The phone's home is projects, and a project is a workspace of tabs, its
// sessions. A project is a section of the desktop's sidebar where the
// computer holds a workspace (a project put together, or the folder or
// repository the sidebar groups by itself), and without one the folder or
// repository its sessions are in, as the list grouped them by project
// before. None of this is the phone's own: the tabs are the sessions on the
// computers, and closing one is stopping it there.

/**
 * ProjectItem is a project as home lists it. key tells it from the others
 * and outlives a restart: "s:" and the section's key with a workspace,
 * "f:" and its folder's or repository's without. nodes are the computers
 * it is on; pins those of the workspace naming it, or a folder of it;
 * scope what pinning it pins, null where it cannot be pinned. tags name a
 * session's folder where the project has several; space is the index of
 * the space it is placed in, 0 for none. folders are where a session of it
 * may start.
 */
internal data class ProjectItem(
    val key: String,
    val label: String,
    val look: Look,
    val sessions: List<SessionInfo>,
    val nodes: List<String> = sessions.map { it.nodeId }.distinct(),
    val pins: List<Scope> = emptyList(),
    val scope: Scope? = null,
    val tags: Map<String, String> = emptyMap(),
    val space: Int = 0,
    val folders: List<Folder> = emptyList(),
) {
    val pinned: Boolean get() = pins.isNotEmpty()
}

/**
 * The projects the sessions are in, in the order the desktop's sidebar has
 * them (by name), every project the workspace defines among them, those
 * without sessions too. Without a workspace, the sessions' folders and
 * repositories, by name.
 */
internal fun projectIndex(view: WorkspaceView, sessions: List<SessionInfo>, nodeName: (String) -> String): List<ProjectItem> {
    if (!view.held) {
        return groupSessions(sessions, Grouping.Project, nodeName).map { g ->
            ProjectItem(
                key = "f:" + g.key.removePrefix("project:"),
                label = g.label,
                look = lookOf(emptyMap(), g.key),
                sessions = g.sessions,
                folders = g.sessions.map { Folder(it.nodeId, it.sectionFolder()) }.distinct(),
            )
        }.sortedWith(ByLabel)
    }
    val sorted = SessionWorkspace(view, sessions)
    val labels = sorted.labels(nodeName)
    return sorted.sections.map { sec ->
        val roots = sec.project?.roots.orEmpty()
        val folders = roots.flatMap { it.locations }.ifEmpty {
            (listOf(Folder(sec.node, sec.folder)) + sec.sessions.map { Folder(it.nodeId, it.sectionFolder()) })
                .filter { it.node.isNotEmpty() && it.path.isNotEmpty() }
        }.distinct()
        ProjectItem(
            key = "s:" + sec.key,
            label = labels.getValue(sec.key),
            look = lookOf(view.looks, sec.key),
            sessions = sec.sessions,
            nodes = (sec.sessions.map { it.nodeId } + folders.map { it.node }).filter { it.isNotEmpty() }.distinct(),
            pins = view.pinnedProjects.filter { it.section == sec.key },
            scope = Scope(sec.key),
            tags = if (roots.size < 2) emptyMap()
            else sec.sessions.mapNotNull { s -> sec.rootOf[s.id]?.let { s.id to "#" + roots[it].label() } }.toMap(),
            space = view.placeOf(sec.key, sec.owners),
            folders = folders,
        )
    }.sortedWith(ByLabel)
}

/**
 * Projects by name, as a person reads a list: letters' case aside, which
 * the desktop's sidebar, sorting by bytes, does not set aside.
 */
private val ByLabel: Comparator<ProjectItem> = compareBy<ProjectItem, String>(String.CASE_INSENSITIVE_ORDER) { it.label }.thenBy { it.key }

/** What a project row says at a glance. headline is the session it names: one that needs you, else one at work, else the latest. */
internal data class ProjectSummary(
    val total: Int,
    val needsYou: Int,
    val working: Int,
    val asleep: Int,
    val latest: Long,
    val headline: SessionInfo?,
)

internal fun ProjectItem.summary(): ProjectSummary {
    val waiting = sessions.filter { it.needsYou() }
    val working = sessions.filter { it.working() }
    val headline = waiting.maxByOrNull { it.waitingSince() }
        ?: working.maxByOrNull { it.lastActive() }
        ?: sessions.maxByOrNull { it.lastActive() }
    return ProjectSummary(
        total = sessions.size,
        needsYou = waiting.size,
        working = working.size,
        asleep = sessions.count { it.state == "stopped" },
        latest = sessions.maxOfOrNull { it.lastActive() } ?: 0L,
        headline = headline,
    )
}

/** Since when a session has waited for you: its approval's, else its status's. */
internal fun SessionInfo.waitingSince(): Long =
    if (hasApproval() && approval.hasSince()) approval.since.millis() else maxOf(statusAt.millis(), lastActive())

/**
 * The projects home shows: those of space (an index of the view's, 0 for
 * every one's), as filter has it, the pinned first in the order they were
 * pinned, then the rest as they come.
 */
internal fun homeProjects(items: List<ProjectItem>, space: Int, filter: Filter, pinOrder: List<Scope> = emptyList()): List<ProjectItem> {
    val inSpace = items.filter { space == 0 || it.space == 0 || it.space == space }
    val shown = when (filter) {
        Filter.All -> inSpace
        Filter.NeedsYou -> inSpace.filter { p -> p.sessions.any { it.needsYou() } }
        Filter.Working -> inSpace.filter { p -> p.sessions.any { it.working() } }
    }
    val rank = pinOrder.map { it.section }.distinct().withIndex().associate { (i, k) -> "s:$k" to i }
    val (pinned, rest) = shown.partition { it.pinned }
    return pinned.sortedBy { rank[it.key] ?: Int.MAX_VALUE } + rest
}

/** The sessions waiting for you, the one waiting longest last: the tray lists what came in latest first. */
internal fun needingYou(sessions: List<SessionInfo>): List<SessionInfo> =
    sessions.filter { it.needsYou() }.sortedByDescending { it.waitingSince() }

/** The project session id is in; null for one in none listed. */
internal fun List<ProjectItem>.projectOf(id: String): ProjectItem? = firstOrNull { p -> p.sessions.any { it.id == id } }

// The tabs of a project.

/**
 * A project's tabs, in an order that does not move as its sessions work,
 * as a browser's tabs keep theirs: those pinned first, then the rest by
 * when they started, a new one last; those whose program has exited after
 * all of them.
 */
internal fun tabOrder(sessions: List<SessionInfo>, pinned: Set<String> = emptySet()): List<SessionInfo> {
    val started = compareBy<SessionInfo>({ it.startedAt.millis() }, { it.id })
    val (exited, live) = sessions.partition { it.state == "exited" }
    val (pins, rest) = live.partition { it.id in pinned }
    return pins.sortedWith(started) + rest.sortedWith(started) + exited.sortedWith(started)
}

/**
 * The tab a project opens on: the one that has waited for you since
 * latest, else the one last looked at, else the latest at work, else the
 * latest of all.
 */
internal fun openingTab(tabs: List<SessionInfo>, lastViewed: String?): SessionInfo? =
    tabs.filter { it.needsYou() }.maxByOrNull { it.waitingSince() }
        ?: tabs.firstOrNull { it.id == lastViewed }
        ?: tabs.filter { it.state != "exited" }.maxByOrNull { it.lastActive() }
        ?: tabs.maxByOrNull { it.lastActive() }

/**
 * The tab to show when the one shown, gone, is no longer among tabs: the
 * next of those before it that is still there, else the one before it,
 * as a browser shows the neighbour of a tab closed; null when none is
 * left.
 */
internal fun tabAfter(before: List<String>, gone: String, tabs: List<SessionInfo>): String? {
    val left = tabs.map { it.id }.toSet()
    val at = before.indexOf(gone)
    if (at >= 0) {
        before.drop(at + 1).firstOrNull { it in left }?.let { return it }
        before.take(at).lastOrNull { it in left }?.let { return it }
    }
    return tabs.firstOrNull()?.id
}

/** The tab next to current by step, -1 before it and 1 after; null at either end. */
internal fun neighbourTab(tabs: List<SessionInfo>, current: String, step: Int): String? {
    val at = tabs.indexOfFirst { it.id == current }
    if (at < 0) return null
    return tabs.getOrNull(at + step)?.id
}

/** How many of sessions need you outside project key: what the way back home marks. */
internal fun needYouElsewhere(sessions: List<SessionInfo>, project: ProjectItem?): Int {
    val here = project?.sessions.orEmpty().map { it.id }.toSet()
    return sessions.count { it.needsYou() && it.id !in here }
}

/**
 * The panels that go beside a project: the plugins' blocks the window lays
 * out for the space shown, which the desktop draws beside every project,
 * and the blocks its own sessions' agents show.
 */
internal fun projectPanels(pages: List<LayoutPage>, agents: List<AgentBlock>, sessions: Set<String>): List<Panel> =
    panels(pages, agents.filter { it.sessionId in sessions })

/**
 * A project key as a route carries it: base64 of its UTF-8, safe in a path
 * whatever the key holds (a folder's key holds a NUL and slashes).
 */
internal fun routeKey(key: String): String =
    java.util.Base64.getUrlEncoder().withoutPadding().encodeToString(key.toByteArray(Charsets.UTF_8))

/** The key routeKey carries; "" for what is not one. */
internal fun keyOfRoute(route: String): String =
    runCatching { String(java.util.Base64.getUrlDecoder().decode(route), Charsets.UTF_8) }.getOrDefault("")

/**
 * LastTabs keeps the tab last looked at in each project, so that a
 * project opens on it again, after the app is gone too. At most [LIMIT]
 * projects are kept, those looked at last.
 */
internal class LastTabs(private val load: () -> String, private val save: (String) -> Unit) {
    private fun read(): List<Pair<String, String>> = load().lines().filter { '\t' in it }
        .map { it.substringBefore('\t') to it.substringAfter('\t') }

    operator fun get(project: String): String? = read().firstOrNull { it.first == routeKey(project) }?.second

    operator fun set(project: String, tab: String) {
        val key = routeKey(project)
        val kept = listOf(key to tab) + read().filter { it.first != key }
        save(kept.take(LIMIT).joinToString("\n") { "${it.first}\t${it.second}" })
    }

    companion object {
        const val LIMIT = 64
    }
}
