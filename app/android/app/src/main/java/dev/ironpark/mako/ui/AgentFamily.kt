package dev.ironpark.mako.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.CallSplit
import androidx.compose.material.icons.outlined.Groups
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.SessionInfo

/**
 * SessionFamilyIndex mirrors the desktop's `internal/gui/agentfamily.go`:
 * sessions spawned by another agent through Mako (`parentSessionId`) live
 * inside their root parent's tab rather than flooding the tab bar as flat
 * unrelated siblings.
 */
internal class SessionFamilyIndex(sessions: List<SessionInfo>) {
    private val byID: Map<String, SessionInfo> = sessions.associateBy { it.id }
    private val kids: Map<String, List<SessionInfo>>

    init {
        val grouped = LinkedHashMap<String, MutableList<SessionInfo>>()
        for (s in sessions) {
            val p = parent(s) ?: continue
            grouped.getOrPut(p.id) { ArrayList() }.add(s)
        }
        kids = grouped
    }

    /** The session that spawned [s] on the same node, or null when none is known. */
    fun parent(s: SessionInfo?): SessionInfo? {
        if (s == null || s.parentSessionId.isEmpty()) return null
        val p = byID[s.parentSessionId] ?: return null
        return if (p.nodeId == s.nodeId) p else null
    }

    /** Ancestors that spawned [id], nearest first, stopping on cycles. */
    fun ancestors(id: String): List<String> {
        val out = ArrayList<String>()
        var s = parent(byID[id])
        while (s != null && s.id != id && s.id !in out) {
            out.add(s.id)
            s = parent(s)
        }
        return out
    }

    /** Furthest ancestor of [id] present in [tabIds], or "" if none is present. */
    fun hostIn(tabIds: Collection<String>, id: String): String {
        val set = if (tabIds is Set<String>) tabIds else tabIds.toSet()
        var host = ""
        for (a in ancestors(id)) {
            if (a in set) host = a
        }
        return host
    }

    /** Map of child session ID -> its root host tab ID among [tabs] (breaking cycles). */
    fun tabHosts(tabs: List<SessionInfo>): Map<String, String> {
        val ids = tabs.map { it.id }.toSet()
        val hosts = LinkedHashMap<String, String>()
        for (t in tabs) {
            val h = hostIn(ids, t.id)
            if (h.isNotEmpty()) hosts[t.id] = h
        }
        val cycled = hosts.entries.filter { (_, host) -> host in hosts }.map { it.key }
        for (k in cycled) hosts.remove(k)
        return hosts
    }

    /** Top-level tabs among [tabs]: hides children whose root host is present in [tabs]. */
    fun topLevelTabs(tabs: List<SessionInfo>): List<SessionInfo> {
        val hosted = tabHosts(tabs)
        return tabs.filterNot { it.id in hosted }
    }

    /** All descendants spawned by [rootId], breadth-first (nearest first). */
    fun spawned(rootId: String): List<SessionInfo> {
        val out = ArrayList<SessionInfo>()
        val seen = HashSet<String>().apply { add(rootId) }
        var next = kids[rootId].orEmpty()
        while (next.isNotEmpty()) {
            val deeper = ArrayList<SessionInfo>()
            for (s in next) {
                if (seen.add(s.id)) {
                    out.add(s)
                    deeper.addAll(kids[s.id].orEmpty())
                }
            }
            next = deeper
        }
        return out
    }

    /** [rootId] followed by all descendants it spawned, or empty if unknown. */
    fun family(rootId: String): List<SessionInfo> {
        val root = byID[rootId] ?: return emptyList()
        return listOf(root) + spawned(rootId)
    }

    /** Whether [rootId] or any of its spawned descendants currently needs user attention. */
    fun familyNeedsYou(rootId: String): Boolean = family(rootId).any { it.needsYou() }

    /** Most urgent member of [rootId]'s family that needs user attention, or null. */
    fun waitingMember(rootId: String): SessionInfo? =
        family(rootId).filter { it.needsYou() }.maxByOrNull { it.waitingSince() }
}

/**
 * Ranks spawned subagent sessions for compact status-dot display, matching
 * desktop `familyMarks`: asking/waiting first, then working, done, and idle/ended.
 */
internal fun orderedFamilyMarks(kids: List<SessionInfo>): List<SessionInfo> =
    kids.sortedBy { s ->
        when {
            s.needsYou() -> 0
            s.working() -> 1
            s.status == "done" && !s.ended() -> 2
            else -> 3
        }
    }

/**
 * Compact bordered pill displaying up to 3 live status dots (+ overflow count)
 * for Mako-spawned subagents, matching the desktop tab bar's `familyMarks`.
 */
@Composable
internal fun FamilyDotsPill(kids: List<SessionInfo>, modifier: Modifier = Modifier) {
    if (kids.isEmpty()) return
    val ranked = remember(kids) { orderedFamilyMarks(kids) }
    val shown = ranked.take(3)
    val extra = ranked.size - shown.size
    Surface(
        modifier = modifier,
        shape = RoundedCornerShape(999.dp),
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        border = MakoColors.cardBorderStroke,
    ) {
        Row(
            Modifier.padding(horizontal = 6.dp, vertical = 3.dp),
            horizontalArrangement = Arrangement.spacedBy(4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            for (k in shown) {
                PulsingStatusDot(k.statusColor(), active = k.hasActivePulse(), size = 6.dp)
            }
            if (extra > 0) {
                Text(
                    "+$extra",
                    style = MaterialTheme.typography.labelSmall.tabular(),
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

/**
 * AgentFamilyLane mirrors desktop's `agentLane`: shown directly beneath the
 * session header whenever the active session has Mako-spawned subagents (or is
 * itself a spawned subagent), letting the user switch between lead and children
 * with a single tap and jump to the project agents graph/message log.
 */
@Composable
internal fun AgentFamilyLane(
    family: List<SessionInfo>,
    activeId: String,
    nodes: List<Node>,
    onSelect: (String) -> Unit,
    onOpenAgents: (() -> Unit)? = null,
    modifier: Modifier = Modifier,
) {
    if (family.size < 2) return
    Surface(modifier = modifier.fillMaxWidth(), color = MaterialTheme.colorScheme.surface) {
        androidx.compose.foundation.layout.Column {
            Row(
                Modifier
                    .fillMaxWidth()
                    .height(42.dp)
                    .padding(start = 8.dp, end = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Row(
                    Modifier
                        .weight(1f)
                        .horizontalScroll(rememberScrollState()),
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    family.forEachIndexed { index, member ->
                        FamilyMemberChip(
                            session = member,
                            nodes = nodes,
                            selected = member.id == activeId,
                            onClick = { if (member.id != activeId) onSelect(member.id) },
                        )
                        if (index == 0) {
                            Icon(
                                Icons.AutoMirrored.Outlined.CallSplit,
                                contentDescription = null,
                                modifier = Modifier
                                    .padding(horizontal = 2.dp)
                                    .size(15.dp),
                                tint = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.7f),
                            )
                        }
                    }
                    Spacer(Modifier.width(4.dp))
                }
                if (onOpenAgents != null) {
                    IconButton(onClick = onOpenAgents, modifier = Modifier.size(36.dp)) {
                        Icon(
                            Icons.Outlined.Groups,
                            contentDescription = stringResource(R.string.open_project_agents),
                            modifier = Modifier.size(18.dp),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                }
            }
            HorizontalDivider(color = MakoColors.subtleDivider)
        }
    }
}

@Composable
private fun FamilyMemberChip(
    session: SessionInfo,
    nodes: List<Node>,
    selected: Boolean,
    onClick: () -> Unit,
) {
    val tone = session.statusColor()
    val border = when {
        selected -> BorderStroke(1.dp, MaterialTheme.colorScheme.onSurface.copy(alpha = 0.28f))
        session.needsYou() -> BorderStroke(1.dp, tone.copy(alpha = 0.55f))
        else -> BorderStroke(MakoColors.hairlineWidth, MakoColors.cardBorder.copy(alpha = 0.6f))
    }
    val container = when {
        selected -> MaterialTheme.colorScheme.surfaceContainerHighest
        else -> MaterialTheme.colorScheme.surfaceContainerLow
    }
    val interaction = remember { MutableInteractionSource() }
    Surface(
        shape = RoundedCornerShape(10.dp),
        color = container,
        border = border,
        modifier = Modifier
            .semantics {
                this.selected = selected
                role = Role.Tab
            }
            .clickable(interactionSource = interaction, indication = null, onClick = onClick)
            .pressScale(interaction, pressedScale = 0.96f),
    ) {
        Row(
            Modifier.padding(horizontal = 8.dp, vertical = 5.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            AgentAvatar(
                agent = session.agent,
                color = agentColor(nodes, session.nodeId, session.agent),
                status = null,
                size = 16.dp,
            )
            Text(
                text = session.label(),
                style = MaterialTheme.typography.labelMedium,
                fontWeight = if (selected || session.needsYou()) FontWeight.SemiBold else FontWeight.Normal,
                color = if (selected) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.widthIn(max = 128.dp),
            )
            PulsingStatusDot(tone, active = session.hasActivePulse(), size = 6.5.dp)
        }
    }
}
