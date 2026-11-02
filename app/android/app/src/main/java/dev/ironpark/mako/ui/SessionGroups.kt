package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.SessionInfo

/** What the home screen groups the sessions by; within a group, the latest first. */
internal enum class Grouping { Computer, Status, Project, Time }

/**
 * SessionGroup is one heading of the list and its sessions: a computer's
 * name, a project's, or a status; key tells it from the others.
 */
internal data class SessionGroup(val key: String, val label: String, val sessions: List<SessionInfo>)

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
