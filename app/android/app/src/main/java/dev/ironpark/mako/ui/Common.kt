package dev.ironpark.mako.ui

import android.text.format.DateUtils
import android.widget.Toast
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.TextButton
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import dev.ironpark.mako.Mesh
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Computer
import androidx.compose.material.icons.outlined.DesktopWindows
import androidx.compose.material.icons.outlined.Dns
import androidx.compose.material.icons.outlined.LaptopMac
import androidx.compose.material.icons.outlined.PhoneAndroid
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.res.painterResource
import androidx.compose.material3.Icon
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.protobuf.Timestamp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.AgentInfo
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.SessionInfo

/** CapChat is the bit of SessionInfo.caps saying its conversation can be read. */
const val CAP_CHAT = 1

/** What a session is called: its name, else its title, else its agent. */
fun SessionInfo.label(): String = name.ifEmpty { title.ifEmpty { agent } }.trimStart('✳', '◐', '◑', '◒', '◓', ' ')

/** The last element of a path, of either kind of separator. */
fun baseName(path: String): String = path.trimEnd('/', '\\').substringAfterLast('/').substringAfterLast('\\')

/** Whether the session's agent waits for the user, one way or another. */
fun SessionInfo.needsYou(): Boolean = hasApproval() || status == "waiting"

/** Whether the session's program has ended, or not begun again. */
fun SessionInfo.ended(): Boolean = state == "exited" || state == "stopped"

/** Whether the session's agent is at work. */
fun SessionInfo.working(): Boolean = status == "working" && state == "running"

fun SessionInfo.statusColor(): Color = when {
    hasApproval() -> StatusColors.Approval
    ended() -> StatusColors.Idle
    status == "working" -> StatusColors.Working
    status == "waiting" -> StatusColors.Waiting
    status == "done" -> StatusColors.Done
    else -> StatusColors.Idle
}

@Composable
fun SessionInfo.statusText(): String = when {
    hasApproval() -> stringResource(R.string.status_approval)
    state == "exited" -> stringResource(R.string.status_exited)
    state == "stopped" -> stringResource(R.string.status_stopped)
    state == "starting" -> stringResource(R.string.status_starting)
    status == "working" -> stringResource(R.string.status_working)
    status == "waiting" -> stringResource(R.string.status_waiting)
    status == "done" -> stringResource(R.string.status_done)
    else -> stringResource(R.string.status_idle)
}

/** When the session last did anything: printed, or changed its status. */
fun SessionInfo.lastActive(): Long = maxOf(lastOutput.millis(), statusAt.millis(), startedAt.millis())

fun Timestamp.millis(): Long = seconds * 1000 + nanos / 1_000_000

/**
 * How long ago millis was, as "5 min. ago" in the phone's language; under
 * a minute, or ahead of this phone's clock, "just now".
 */
@Composable
fun ago(millis: Long): String {
    if (millis <= 0) return ""
    val now = System.currentTimeMillis()
    if (now - millis < DateUtils.MINUTE_IN_MILLIS) return stringResource(R.string.just_now)
    return DateUtils.getRelativeTimeSpanString(millis, now, DateUtils.MINUTE_IN_MILLIS, DateUtils.FORMAT_ABBREV_RELATIVE).toString()
}

/** An agent's colour, from 0xRRGGBBAA; null for none. */
fun AgentInfo.tint(): Color? {
    if (color == 0) return null
    val c = color.toLong() and 0xFFFFFFFFL
    return Color(
        red = ((c shr 24) and 0xFF).toInt(),
        green = ((c shr 16) and 0xFF).toInt(),
        blue = ((c shr 8) and 0xFF).toInt(),
        alpha = (c and 0xFF).toInt(),
    )
}

/** The colour of agent on its node, or one of its own from its name. */
fun agentColor(nodes: List<Node>, node: String, agent: String): Color =
    nodes.firstOrNull { it.id == node }?.agentsList?.firstOrNull { it.name == agent }?.tint()
        ?: Palette[(agent.hashCode() and 0x7fffffff) % Palette.size]

private val Palette = listOf(
    Color(0xFFD97757), Color(0xFF7C66E8), Color(0xFF3B82F6), Color(0xFF14B8A6),
    Color(0xFFF59E0B), Color(0xFFEC4899), Color(0xFF22C55E),
)

/** The icon of a computer, by its system. */
fun osIcon(os: String): ImageVector = when (os) {
    "darwin" -> Icons.Outlined.LaptopMac
    "windows" -> Icons.Outlined.DesktopWindows
    "android" -> Icons.Outlined.PhoneAndroid
    "linux" -> Icons.Outlined.Dns
    else -> Icons.Outlined.Computer
}

/** A computer's system, by its name. */
fun osName(os: String): String = when (os) {
    "darwin" -> "macOS"
    "windows" -> "Windows"
    "linux" -> "Linux"
    "android" -> "Android"
    else -> os
}

/** The parts that are there, with dots between. */
fun dotted(vararg parts: String?): String = parts.filterNot { it.isNullOrEmpty() }.joinToString(" · ")

@Composable
fun Dot(color: Color, modifier: Modifier = Modifier, size: Dp = 10.dp) {
    Box(modifier.size(size).background(color, CircleShape))
}

/**
 * AgentAvatar is an agent's desktop glyph in its colour, with the session's
 * status as a dot on its corner.
 */
@Composable
fun AgentAvatar(agent: String, color: Color, status: Color?, modifier: Modifier = Modifier, size: Dp = 40.dp) {
    Box(modifier.size(size)) {
        Box(
            Modifier.size(size).clip(CircleShape).background(color.copy(alpha = 0.18f)),
            contentAlignment = Alignment.Center,
        ) {
            val glyph = agentDrawable(agent)
            if (glyph != null) Icon(painterResource(glyph), agent, Modifier.size(size * 0.62f), tint = color)
            else Text(agent.take(1).uppercase(), color = color, fontWeight = FontWeight.Bold,
                fontSize = (size.value * 0.42f).sp)
        }
        if (status != null) {
            Box(
                Modifier.align(Alignment.BottomEnd).size(size * 0.32f)
                    .border(2.dp, MaterialTheme.colorScheme.surface, CircleShape)
                    .padding(2.dp)
                    .background(status, CircleShape)
            )
        }
    }
}

/** Pill is a small label in a colour of its own: a status, a count. */
@Composable
fun Pill(text: String, color: Color, modifier: Modifier = Modifier) {
    Surface(color = color.copy(alpha = 0.14f), shape = RoundedCornerShape(50), modifier = modifier) {
        Row(
            Modifier.padding(horizontal = 10.dp, vertical = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.Center,
        ) {
            Dot(color, size = 7.dp)
            Spacer(Modifier.width(6.dp))
            Text(text, color = color, style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.SemiBold)
        }
    }
}

/** actor runs work, showing its failure as a toast. */
@Composable
fun actor(): (suspend () -> Unit) -> Unit {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    return { work ->
        scope.launch {
            try {
                work()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                Toast.makeText(context, e.message, Toast.LENGTH_LONG).show()
            }
        }
    }
}

@Composable
fun TextDialog(
    title: String,
    initial: String,
    onDismiss: () -> Unit,
    hint: String = "",
    action: String = title,
    allowEmpty: Boolean = false,
    onDone: (String) -> Unit,
) {
    var text by remember { mutableStateOf(initial) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = {
            OutlinedTextField(
                value = text, onValueChange = { text = it }, singleLine = true,
                placeholder = { if (hint.isNotEmpty()) Text(hint) }, modifier = Modifier.fillMaxWidth(),
            )
        },
        confirmButton = {
            TextButton(enabled = allowEmpty || text.isNotBlank(), onClick = { onDone(text.trim()) }) { Text(action) }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } },
    )
}

/** RenameDialog names a session; left empty, it goes back to its title. */
@Composable
fun RenameDialog(mesh: Mesh, s: SessionInfo, onDismiss: () -> Unit) {
    val act = actor()
    TextDialog(
        title = stringResource(R.string.rename), initial = s.name, hint = s.label(), allowEmpty = true,
        onDismiss = onDismiss,
        onDone = { name ->
            onDismiss()
            act { mesh.renameSession(s, name) }
        },
    )
}

internal fun agentDrawable(agent: String): Int? = when (agent.lowercase()) {
    "claude" -> R.drawable.ic_agent_claude
    "codex" -> R.drawable.ic_agent_codex
    "agy", "antigravity" -> R.drawable.ic_agent_agy
    "opencode" -> R.drawable.ic_agent_opencode
    "omo" -> R.drawable.ic_agent_omo
    "omp" -> R.drawable.ic_agent_omp
    "pi" -> R.drawable.ic_agent_pi
    else -> null
}
