package dev.ironpark.mako.ui

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.AutoAwesome
import androidx.compose.material.icons.outlined.KeyboardArrowDown
import androidx.compose.material.icons.outlined.Shield
import androidx.compose.material.icons.rounded.Check
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.AgentControls
import dev.ironpark.mako.v2.AgentModel
import dev.ironpark.mako.v2.Usage

/** modeName names a permission mode as the window does. */
@Composable
internal fun modeName(mode: String): String = when (mode) {
    "default", "" -> stringResource(R.string.mode_default)
    "acceptEdits" -> stringResource(R.string.mode_accept_edits)
    "plan" -> stringResource(R.string.mode_plan)
    "auto" -> stringResource(R.string.mode_auto)
    "bypassPermissions" -> stringResource(R.string.mode_bypass)
    "dontAsk" -> stringResource(R.string.mode_dont_ask)
    else -> mode
}

private val DATE_DASHED_SUFFIX = Regex("-(?:19|20)\\d{2}-\\d{2}-\\d{2}$")
private val DATE_COMPACT_SUFFIX = Regex("-(?:19|20)\\d{6}$")
private val DATE_COMPACT_TOKEN = Regex("^(?:19|20)\\d{6}$")
private val DIGITS_ONLY = Regex("^\\d+$")
private val PARAM_SIZE_TOKEN = Regex("^\\d+[bm]$", RegexOption.IGNORE_CASE)
private val O_SERIES_MODEL = Regex("^o[1-9](?:-.*)?$", RegexOption.IGNORE_CASE)
private val CLAUDE_ALIASES = setOf("opus", "sonnet", "haiku", "fable")
private val EFFORT_TIERS = setOf("low", "medium", "high", "xhigh", "max")

/**
 * Strips bracket tags (`[1m]`, `[preview]`) and trailing date stamps
 * (`-20250514`, `-2025-08-07`) from a raw model ID without altering the model's
 * core name.
 */
internal fun stripModelSuffixes(id: String): String {
    var out = id.trim().substringBefore('[').trim()
    while (true) {
        val next = out
            .replace(DATE_DASHED_SUFFIX, "")
            .replace(DATE_COMPACT_SUFFIX, "")
        if (next == out) break
        out = next
    }
    return out
}

/**
 * shortModel formats a model ID as people say it across supported agent
 * families:
 * - Claude: `claude-opus-4-6` -> `Opus 4.6`, `claude-3-5-sonnet-20241022` -> `Sonnet 3.5`
 * - GPT / Codex: `gpt-5.4` -> `GPT-5.4`, `gpt-6.2-sol` -> `GPT-6.2 Sol`
 * - OpenAI o-series: `o3` -> `o3`, `o4-mini-2025-04-16` -> `o4-mini`
 * - Gemini / agy: `gemini-2.5-pro` -> `Gemini 2.5 Pro`, `gemini-4-neon` -> `Gemini 4 Neon`
 *
 * Date suffixes (`-20250514`, `-2025-08-07`) and `[...]` tags are stripped;
 * unknown model IDs keep their base name unmangled.
 */
internal fun shortModel(id: String): String {
    if (id.isBlank()) return id
    val clean = stripModelSuffixes(id)
    if (clean.isEmpty()) return id
    val lower = clean.lowercase()

    // Bare Claude aliases (e.g. "opus", "sonnet", "haiku", "fable").
    if (lower in CLAUDE_ALIASES) {
        return lower.replaceFirstChar { it.uppercase() }
    }

    // Claude full IDs: claude-opus-4-6, claude-sonnet-4-20250514, claude-3-5-sonnet-20241022.
    if (lower.startsWith("claude-")) {
        val rawParts = clean.substring(7).split('-').filter { it.isNotEmpty() && !DATE_COMPACT_TOKEN.matches(it) }
        if (rawParts.isEmpty()) return clean
        val familyIdx = rawParts.indexOfFirst { it.lowercase() in CLAUDE_ALIASES }
        val ordered = if (familyIdx > 0 && rawParts.take(familyIdx).all { DIGITS_ONLY.matches(it) }) {
            listOf(rawParts[familyIdx]) + rawParts.take(familyIdx) + rawParts.drop(familyIdx + 1)
        } else {
            rawParts
        }
        return formatVersionedTokens(ordered)
    }

    // GPT / Codex IDs: gpt-5.4, gpt-6.2-sol, gpt-5-mini, gpt-4o-mini, gpt-oss-120b.
    if (lower.startsWith("gpt-")) {
        val parts = clean.substring(4).split('-').filter { it.isNotEmpty() && !DATE_COMPACT_TOKEN.matches(it) }
        if (parts.isEmpty()) return "GPT"
        val first = parts[0]
        val head = if (first.first().isDigit()) "GPT-$first" else "GPT-${formatModelWord(first)}"
        val rest = parts.drop(1).map(::formatModelWord)
        return (listOf(head) + rest).joinToString(" ")
    }

    // OpenAI o-series: o3, o4-mini, o3-pro.
    if (O_SERIES_MODEL.matches(clean)) {
        return lower
    }

    // Gemini / agy IDs: gemini-2.5-pro, gemini-4-neon, gemini-2-5-flash-lite.
    if (lower.startsWith("gemini-")) {
        val parts = clean.substring(7).split('-').filter { it.isNotEmpty() && !DATE_COMPACT_TOKEN.matches(it) }
        if (parts.isEmpty()) return "Gemini"
        return "Gemini " + formatVersionedTokens(parts)
    }

    return clean
}

private fun formatVersionedTokens(tokens: List<String>): String {
    val out = mutableListOf<String>()
    var i = 0
    while (i < tokens.size) {
        if (DIGITS_ONLY.matches(tokens[i])) {
            val start = i
            while (i < tokens.size && DIGITS_ONLY.matches(tokens[i])) i++
            out += tokens.subList(start, i).joinToString(".")
        } else {
            out += formatModelWord(tokens[i])
            i++
        }
    }
    return out.joinToString(" ")
}

private fun formatModelWord(token: String): String = when {
    token.equals("oss", ignoreCase = true) -> "OSS"
    token.equals("gpt", ignoreCase = true) -> "GPT"
    PARAM_SIZE_TOKEN.matches(token) -> token.uppercase()
    else -> token.replaceFirstChar { it.uppercase() }
}

/**
 * Returns the user-facing display name for a catalog [AgentModel], preferring
 * its driver-provided display name when distinct from its raw ID.
 */
internal fun modelDisplayName(model: AgentModel): String =
    model.name.takeIf { it.isNotBlank() && it != model.id } ?: shortModel(model.id)

/**
 * matchesModel reports whether [current] (the active session model) represents
 * [option] (a candidate model ID or alias) without loose substring false
 * positives (e.g. `gpt-5` never matches `gpt-5-mini`).
 *
 * Matches:
 * 1. Exact case-insensitive match.
 * 2. Base-model match after stripping date stamps and `[...]` tags.
 * 3. Agy/variant effort suffix match (`gemini-4-neon-high` matches base `gemini-4-neon`).
 * 4. Claude family alias match (`claude-opus-4-6` matches `opus`, `claude-3-5-sonnet-20241022` matches `sonnet`).
 */
internal fun matchesModel(current: String, option: String): Boolean {
    if (current.isBlank() || option.isBlank()) return false
    val cur = current.trim().lowercase()
    val opt = option.trim().lowercase()
    if (cur == opt) return true

    val baseCur = stripModelSuffixes(cur)
    val baseOpt = stripModelSuffixes(opt)
    if (baseCur == baseOpt) return true

    // Variant ID with trailing effort tier (e.g. gemini-4-neon-high -> gemini-4-neon).
    val curBaseNoEffort = stripEffortTierSuffix(baseCur)
    if (curBaseNoEffort != null && curBaseNoEffort == baseOpt) return true

    // Claude alias match: "opus" matches "claude-opus-4-6" or "claude-3-opus-20240229", not unrelated names.
    if (baseOpt in CLAUDE_ALIASES && matchesClaudeAlias(baseCur, baseOpt)) return true
    if (baseCur in CLAUDE_ALIASES && matchesClaudeAlias(baseOpt, baseCur)) return true

    return false
}

private fun stripEffortTierSuffix(base: String): String? {
    val idx = base.lastIndexOf('-')
    if (idx <= 0 || idx + 1 >= base.length) return null
    val tier = base.substring(idx + 1)
    return if (tier in EFFORT_TIERS) base.substring(0, idx) else null
}

private fun matchesClaudeAlias(fullModel: String, alias: String): Boolean {
    if (!fullModel.startsWith("claude-")) return false
    return fullModel.substring(7).split('-').any { it == alias }
}

/**
 * Finds the catalog entry matching [modelId] (exact ID first, then [matchesModel]).
 */
internal fun findCatalogModel(catalog: List<AgentModel>, modelId: String): AgentModel? {
    if (modelId.isBlank() || catalog.isEmpty()) return null
    val trimmed = modelId.trim()
    return catalog.firstOrNull { it.id.equals(trimmed, ignoreCase = true) }
        ?: catalog.firstOrNull { matchesModel(trimmed, it.id) }
}

/**
 * Resolves the selectable effort levels for [activeModel] using [catalog] when
 * available:
 * - If [catalog] is empty, falls back to [fallbackEfforts].
 * - If [activeModel] matches a catalog model, returns that model's exact
 *   [AgentModel.getEffortsList] (empty for fixed-effort models such as
 *   Orchestrator or GPT-OSS, hiding effort selection).
 * - Otherwise returns [fallbackEfforts] if non-empty, else the deduplicated
 *   union of efforts across [catalog] (main models first, then overflow).
 */
internal fun effortsForModel(
    activeModel: String,
    catalog: List<AgentModel>,
    fallbackEfforts: List<String> = emptyList(),
): List<String> {
    if (catalog.isEmpty()) return fallbackEfforts
    val matched = findCatalogModel(catalog, activeModel)
    if (matched != null) return matched.effortsList
    if (fallbackEfforts.isNotEmpty()) return fallbackEfforts
    val out = LinkedHashSet<String>()
    for (overflow in listOf(false, true)) {
        for (model in catalog) {
            if (model.overflow != overflow) continue
            for (effort in model.effortsList) {
                if (effort.isNotBlank()) out += effort
            }
        }
    }
    return out.toList()
}

internal fun effortDisplayName(effort: String): String =
    effort.replaceFirstChar { it.uppercase() }

/**
 * AgentControlsRow is what the agent may do unasked, and the model it
 * replies with, at its effort, each a menu of what it may be set to; the
 * agent keeps a model or effort set as your default.
 */
@Composable
internal fun AgentControlsRow(
    c: AgentControls,
    usage: Usage?,
    catalog: List<AgentModel> = emptyList(),
    onSet: (mode: String, model: String, effort: String) -> Unit,
) {
    val mode = usage?.permissionMode.orEmpty()
    val model = usage?.model.orEmpty()
    val effort = usage?.effort.orEmpty()
    val efforts = remember(model, catalog, c.effortsList) {
        effortsForModel(model, catalog, c.effortsList)
    }
    val hasModeMenu = c.modesCount > 0
    val hasModelOrEffortMenu = c.modelsCount > 0 || efforts.isNotEmpty()
    if (!hasModeMenu && !hasModelOrEffortMenu) return

    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (hasModeMenu) {
            Box {
                var open by remember { mutableStateOf(false) }
                val activeMode = mode.ifEmpty { "default" }
                ControlPill(
                    label = modeName(mode),
                    icon = Icons.Outlined.Shield,
                    expanded = open,
                    contentDescription = stringResource(R.string.controls_permission_mode),
                    onClick = { open = true },
                )
                DropdownMenu(open, onDismissRequest = { open = false }) {
                    c.modesList.forEach { m ->
                        val selected = m == activeMode
                        DropdownMenuItem(
                            text = {
                                Text(
                                    modeName(m),
                                    fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                                    color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface,
                                )
                            },
                            trailingIcon = if (selected) {
                                { Icon(Icons.Rounded.Check, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary) }
                            } else null,
                            onClick = { open = false; onSet(m, "", "") },
                        )
                    }
                }
            }
        } else {
            Spacer(Modifier.width(1.dp))
        }

        if (hasModelOrEffortMenu) {
            Spacer(Modifier.width(8.dp))
            Box(Modifier.weight(1f, fill = false)) {
                var open by remember { mutableStateOf(false) }
                val activeModelLabel = remember(model, catalog) {
                    if (model.isEmpty()) null
                    else findCatalogModel(catalog, model)?.let(::modelDisplayName) ?: shortModel(model)
                }
                val shownEffort = effort.takeIf { it.isNotEmpty() && (efforts.isNotEmpty() || catalog.isEmpty()) }
                val label = listOfNotNull(
                    activeModelLabel ?: stringResource(R.string.model),
                    shownEffort,
                ).joinToString(" · ")
                ControlPill(
                    label = label,
                    icon = Icons.Outlined.AutoAwesome,
                    expanded = open,
                    contentDescription = stringResource(R.string.controls_model_effort),
                    onClick = { open = true },
                )
                DropdownMenu(open, onDismissRequest = { open = false }) {
                    if (c.modelsCount > 0) {
                        MenuSectionHeader(stringResource(R.string.controls_models_header))
                        c.modelsList.forEach { m ->
                            val selected = matchesModel(model, m)
                            val itemLabel = findCatalogModel(catalog, m)?.let(::modelDisplayName) ?: shortModel(m)
                            DropdownMenuItem(
                                text = {
                                    Text(
                                        itemLabel,
                                        fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                                        color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface,
                                    )
                                },
                                trailingIcon = if (selected) {
                                    { Icon(Icons.Rounded.Check, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary) }
                                } else null,
                                onClick = { open = false; onSet("", m, "") },
                            )
                        }
                    }
                    if (c.modelsCount > 0 && efforts.isNotEmpty()) {
                        HorizontalDivider(Modifier.padding(vertical = 4.dp), color = MaterialTheme.colorScheme.outlineVariant)
                    }
                    if (efforts.isNotEmpty()) {
                        MenuSectionHeader(stringResource(R.string.controls_efforts_header))
                        efforts.forEach { e ->
                            val selected = e.equals(effort, ignoreCase = true)
                            DropdownMenuItem(
                                text = {
                                    Text(
                                        stringResource(R.string.effort_level, effortDisplayName(e)),
                                        fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                                        color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface,
                                    )
                                },
                                trailingIcon = if (selected) {
                                    { Icon(Icons.Rounded.Check, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary) }
                                } else null,
                                onClick = { open = false; onSet("", "", e) },
                            )
                        }
                    }
                    HorizontalDivider(Modifier.padding(vertical = 4.dp), color = MaterialTheme.colorScheme.outlineVariant)
                    DropdownMenuItem(
                        text = {
                            Text(
                                stringResource(R.string.kept_as_default),
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        },
                        onClick = {},
                        enabled = false,
                    )
                }
            }
        }
    }
}

@Composable
private fun ControlPill(
    label: String,
    icon: ImageVector,
    expanded: Boolean,
    contentDescription: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val containerColor by animateColorAsState(
        targetValue = if (expanded) MaterialTheme.colorScheme.surfaceContainerHigh else MaterialTheme.colorScheme.surfaceContainerLow,
        animationSpec = tween(160),
        label = "controlPillBg",
    )
    val borderColor by animateColorAsState(
        targetValue = if (expanded) MaterialTheme.colorScheme.outline else MaterialTheme.colorScheme.outlineVariant,
        animationSpec = tween(160),
        label = "controlPillBorder",
    )
    val chevronRotation by animateFloatAsState(
        targetValue = if (expanded) 180f else 0f,
        animationSpec = spring(stiffness = Spring.StiffnessMediumLow),
        label = "controlPillChevron",
    )

    Surface(
        onClick = onClick,
        shape = RoundedCornerShape(50),
        color = containerColor,
        border = BorderStroke(1.dp, borderColor),
        modifier = modifier.semantics { this.contentDescription = contentDescription },
    ) {
        Row(
            Modifier.padding(start = 10.dp, end = 8.dp, top = 6.dp, bottom = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(
                icon,
                contentDescription = null,
                modifier = Modifier.size(14.dp),
                tint = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.width(6.dp))
            Text(
                label,
                style = MaterialTheme.typography.labelMedium,
                fontWeight = FontWeight.Medium,
                color = MaterialTheme.colorScheme.onSurface,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f, fill = false),
            )
            Spacer(Modifier.width(2.dp))
            Icon(
                Icons.Outlined.KeyboardArrowDown,
                contentDescription = null,
                modifier = Modifier.size(16.dp).rotate(chevronRotation),
                tint = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun MenuSectionHeader(text: String) {
    Text(
        text = text,
        style = MaterialTheme.typography.labelSmall,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        fontWeight = FontWeight.Medium,
        modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp),
    )
}
