package dev.ironpark.mako.blocks

import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.workspace.WorkspaceView

/** Manifest-declared scope kinds (protocol.Scope*). */
object BlockScopeKind {
    const val GLOBAL = "global"
    const val SPACE = "space"
    const val PROJECT = "project"
    const val SESSION = "session"
}

/** One selectable target for a scoped plugin block on a computer. */
data class ScopeOption(
    val key: String,
    val label: String,
    val detail: String = "",
)

/** Matches internal/plugin's scopeIDRe: 1..128 safe ASCII identifier chars, no slashes or whitespace. */
private val SCOPE_ID_RE = Regex("^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$")
private val SCOPED_KINDS = setOf(BlockScopeKind.SPACE, BlockScopeKind.PROJECT, BlockScopeKind.SESSION)

/**
 * Normalizes a declared scope kind: "" or "global" (or any unknown value) becomes "",
 * while "space", "project", and "session" are returned as-is.
 */
fun normalizeScopeKind(raw: String): String = when (raw.trim()) {
    BlockScopeKind.SPACE -> BlockScopeKind.SPACE
    BlockScopeKind.PROJECT -> BlockScopeKind.PROJECT
    BlockScopeKind.SESSION -> BlockScopeKind.SESSION
    else -> ""
}

/** Validates the <id> portion of a canonical scope key. */
fun isValidScopeId(id: String): Boolean = SCOPE_ID_RE.matches(id) && ".." !in id

/**
 * Parses a canonical scope key ("space:<id>", "project:<id>", "session:<id>") into
 * (kind, id), or null when empty or malformed.
 */
fun parseScopeKey(scopeKey: String): Pair<String, String>? {
    if (scopeKey.isEmpty()) return null
    val colon = scopeKey.indexOf(':')
    if (colon <= 0 || colon == scopeKey.lastIndex) return null
    val kind = scopeKey.substring(0, colon)
    val id = scopeKey.substring(colon + 1)
    if (kind !in SCOPED_KINDS || !isValidScopeId(id)) return null
    return kind to id
}

/** Builds a canonical scope key ("<kind>:<id>") if valid, or "" otherwise. */
fun scopeKeyOf(kind: String, id: String): String {
    val norm = normalizeScopeKind(kind)
    if (norm.isEmpty() || !isValidScopeId(id)) return ""
    return "$norm:$id"
}

/**
 * Resolves the effective scope kind ("", "space", "project", "session") from the
 * block's declared scope, a layout's pinned scope prop, or a BlockUpdate's scope.
 */
fun scopeKindOf(declaredScope: String = "", pinnedScope: String = "", updateScope: String = ""): String {
    normalizeScopeKind(declaredScope).ifEmpty { null }?.let { return it }
    parseScopeKey(pinnedScope)?.first?.let { return it }
    normalizeScopeKind(pinnedScope).ifEmpty { null }?.let { return it }
    parseScopeKey(updateScope)?.first?.let { return it }
    return ""
}

/**
 * Validates a pinned scope prop against an optional expected scope kind, returning
 * the canonical scope key or "" if invalid or mismatched.
 */
fun pinnedScopeKey(rawProp: String, expectedKind: String = ""): String {
    val (kind, _) = parseScopeKey(rawProp) ?: return ""
    val norm = normalizeScopeKind(expectedKind)
    if (norm.isNotEmpty() && kind != norm) return ""
    return rawProp
}

/**
 * Lists available scope targets on [node] for [scopeKind]:
 * - "space": visible spaces from the workspace
 * - "project": workspace projects with a root on [node] (or a session joined on [node])
 * - "session": running (non-exited, non-stopped) sessions on [node]
 */
fun scopeOptions(
    scopeKind: String,
    node: String,
    view: WorkspaceView,
    sessions: List<SessionInfo>,
    fallbackSpaceName: (Int) -> String = { "Space $it" },
): List<ScopeOption> = when (normalizeScopeKind(scopeKind)) {
    BlockScopeKind.SPACE -> view.spaces.mapIndexedNotNull { i, sp ->
        val key = scopeKeyOf(BlockScopeKind.SPACE, sp.id)
        if (key.isEmpty()) null
        else ScopeOption(key = key, label = sp.name.ifEmpty { fallbackSpaceName(i + 1) })
    }
    BlockScopeKind.PROJECT -> {
        val nodeRemotes = sessions.asSequence().filter { it.nodeId == node && it.remote.isNotEmpty() }.map { it.remote }.toSet()
        view.projects.mapNotNull { p ->
            val key = scopeKeyOf(BlockScopeKind.PROJECT, p.id)
            val onNode = p.roots.any { r ->
                r.locations.any { it.node == node } || (!p.noJoin && r.remote.isNotEmpty() && r.remote in nodeRemotes)
            }
            if (key.isEmpty() || !onNode) null
            else {
                val name = view.names[p.id]?.ifEmpty { null }
                    ?: p.name.ifEmpty { p.roots.firstOrNull()?.label()?.ifEmpty { null } ?: p.id }
                ScopeOption(key = key, label = name)
            }
        }
    }
    BlockScopeKind.SESSION -> sessions
        .filter { it.nodeId == node && it.state != "exited" && it.state != "stopped" }
        .mapNotNull { s ->
            val key = scopeKeyOf(BlockScopeKind.SESSION, s.id)
            if (key.isEmpty()) null
            else {
                val title = s.name.ifEmpty { s.title.ifEmpty { s.agent } }.trimStart('✳', '◐', '◑', '◒', '◓', ' ')
                ScopeOption(key = key, label = title.ifEmpty { s.id.take(8) }, detail = s.agent)
            }
        }
    else -> emptyList()
}

/**
 * Picks the initial canonical scope key for a block:
 * 1. Global / unscoped -> ""
 * 2. Valid pinned scope matching [scopeKind] -> pinnedScope
 * 3. Preferred target (e.g. active space / project / session) if present in [options] -> "<kind>:<preferredId>"
 * 4. First available option in [options] -> options.first().key
 * 5. None available -> ""
 */
fun resolveInitialScope(
    scopeKind: String,
    pinnedScope: String,
    options: List<ScopeOption>,
    preferredId: String = "",
): String {
    val kind = normalizeScopeKind(scopeKind)
    if (kind.isEmpty()) return ""
    val pinned = pinnedScopeKey(pinnedScope, kind)
    if (pinned.isNotEmpty()) return pinned
    if (preferredId.isNotEmpty()) {
        val prefKey = scopeKeyOf(kind, preferredId)
        if (prefKey.isNotEmpty() && options.any { it.key == prefKey }) return prefKey
    }
    return options.firstOrNull()?.key.orEmpty()
}

/** Human-readable label for a canonical scope key, falling back to workspace/session metadata or raw ID. */
fun scopeDisplayLabel(
    scopeKey: String,
    options: List<ScopeOption>,
    view: WorkspaceView,
    sessions: List<SessionInfo>,
): String {
    options.firstOrNull { it.key == scopeKey }?.let { return it.label }
    val (kind, id) = parseScopeKey(scopeKey) ?: return scopeKey
    return when (kind) {
        BlockScopeKind.SPACE -> view.spaces.firstOrNull { it.id == id }?.name?.ifEmpty { null } ?: id
        BlockScopeKind.PROJECT -> view.names[id]?.ifEmpty { null }
            ?: view.projects.firstOrNull { it.id == id }?.name?.ifEmpty { null }
            ?: id
        BlockScopeKind.SESSION -> sessions.firstOrNull { it.id == id }?.let { s ->
            s.name.ifEmpty { s.title.ifEmpty { s.agent } }.trimStart('✳', '◐', '◑', '◒', '◓', ' ').ifEmpty { null }
        } ?: id.take(8)
        else -> id
    }
}
