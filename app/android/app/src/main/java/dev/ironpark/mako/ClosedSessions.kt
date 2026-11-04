package dev.ironpark.mako

import dev.ironpark.mako.v2.SessionInfo

/**
 * ClosedSessions are the sessions closed from this device, by when. A list
 * asked for before a close went through still has the session, which would
 * show again until the next list; it stays out until the daemon leaves it
 * out too, or [LINGER] has passed and the close did not end it.
 */
internal class ClosedSessions {
    private val closed = HashMap<String, Long>()

    @Synchronized fun close(id: String, now: Long) { closed[id] = now }

    /** The close of id failed: the session is in the list again. */
    @Synchronized fun reopen(id: String) { closed.remove(id) }

    @Synchronized fun filter(sessions: List<SessionInfo>, now: Long): List<SessionInfo> {
        if (closed.isEmpty()) return sessions
        val listed = sessions.mapTo(HashSet()) { it.id }
        closed.entries.removeAll { (id, at) -> id !in listed || now - at > LINGER }
        return sessions.filterNot { it.id in closed }
    }

    companion object {
        const val LINGER = 15_000L
    }
}
