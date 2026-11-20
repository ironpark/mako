package dev.ironpark.mako.ui

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.rounded.Send
import androidx.compose.material.icons.outlined.AccountTree
import androidx.compose.material.icons.outlined.Bolt
import androidx.compose.material.icons.outlined.DesktopAccessDisabled
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.KeyboardArrowDown
import androidx.compose.material.icons.outlined.Refresh
import androidx.compose.material.icons.outlined.Tune
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.net.envelope
import dev.ironpark.mako.v2.AgentModel
import dev.ironpark.mako.v2.ListAgentModels
import dev.ironpark.mako.v2.OpenSession
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.Worktree
import kotlinx.coroutines.CancellationException
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * NewSessionScreen starts an agent on one of the computers that let this
 * device in: which computer, which agent and profile, in which folder, with
 * optional model/effort overrides, first prompt, session name, YOLO mode, and
 * a fresh Git worktree.
 */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
internal fun NewSessionScreen(
    mesh: Mesh,
    project: ProjectItem? = null,
    onBack: () -> Unit,
    onStarted: (SessionInfo) -> Unit,
) {
    val nodes by mesh.nodes.collectAsState()
    val sessions by mesh.sessions.collectAsState()
    val usable = nodes.filter { it.trustsUs && it.agentsCount > 0 }
    // In a project, on the computer of its first folder that can run one.
    val home = project?.folders?.map { it.node }?.firstOrNull { n -> usable.any { it.id == n } }
    var nodeId by rememberSaveable { mutableStateOf(home ?: usable.firstOrNull()?.id.orEmpty()) }
    val node = usable.firstOrNull { it.id == nodeId } ?: usable.firstOrNull()
    // The agent last started on the computer, else its first.
    val usual = sessions.filter { it.nodeId == node?.id && it.agent != SHELL_AGENT }
        .maxByOrNull { it.startedAt.seconds }?.agent
        ?.takeIf { a -> node?.agentsList?.any { it.name == a } == true }
    var agent by rememberSaveable(node?.id) { mutableStateOf(usual ?: node?.agentsList?.firstOrNull()?.name.orEmpty()) }
    var profile by rememberSaveable(node?.id, agent) { mutableStateOf("") }
    // A project's folders on the computer, its first chosen.
    val own = project?.folders.orEmpty().filter { it.node == node?.id }.map { it.path }
    var folder by rememberSaveable(node?.id) { mutableStateOf(own.firstOrNull().orEmpty()) }

    val isShell = agent == SHELL_AGENT
    val supportsOverrides = (node?.revision ?: 0L) >= REVISION_OPEN_OVERRIDES
    val agentInfo = node?.agentsList?.firstOrNull { it.name == agent }
    val profileInfo = agentInfo?.profileDetailsList?.firstOrNull { it.name == profile }
    val defaultYolo = profileInfo?.yolo ?: agentInfo?.yolo ?: false

    // Model & effort overrides (reset when computer or agent changes).
    var selectedModel by rememberSaveable(node?.id, agent) { mutableStateOf("") }
    var selectedEffort by rememberSaveable(node?.id, agent) { mutableStateOf("") }
    var showOverflowModels by rememberSaveable(node?.id, agent) { mutableStateOf(false) }

    // First prompt and expandable launch options.
    var prompt by rememberSaveable { mutableStateOf("") }
    var optionsExpanded by rememberSaveable { mutableStateOf(false) }
    var sessionName by rememberSaveable { mutableStateOf("") }
    var yolo by rememberSaveable(node?.id, agent, profile) { mutableStateOf(defaultYolo) }
    var useWorktree by rememberSaveable { mutableStateOf(false) }
    var branch by rememberSaveable { mutableStateOf("") }

    // Dynamic model catalog fetched per (nodeId, agent) for daemons >= Revision 39.
    val catalogCache = remember { mutableStateMapOf<Pair<String, String>, List<AgentModel>>() }
    var catalogRefreshNonce by remember(node?.id, agent) { mutableIntStateOf(0) }
    var catalogLoading by remember { mutableStateOf(false) }
    val catalogKey = if (node != null && agent.isNotEmpty()) node.id to agent else null

    LaunchedEffect(node?.id, agent, supportsOverrides, catalogRefreshNonce) {
        if (node == null || agent.isEmpty() || isShell || !supportsOverrides) {
            catalogLoading = false
            return@LaunchedEffect
        }
        val key = node.id to agent
        val forceRefresh = catalogRefreshNonce > 0
        if (!forceRefresh && catalogCache.containsKey(key)) {
            catalogLoading = false
            return@LaunchedEffect
        }
        catalogLoading = true
        try {
            val models = mesh.daemon.call(
                envelope {
                    listAgentModels = ListAgentModels.newBuilder()
                        .setNodeId(node.id)
                        .setAgent(agent)
                        .setRefresh(forceRefresh)
                        .build()
                }
            ).agentModels.modelsList
            catalogCache[key] = models
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            // Older daemon or agent without a catalog: keep launch flow uninterrupted.
            catalogCache.putIfAbsent(key, emptyList())
        } finally {
            catalogLoading = false
        }
    }

    val models = catalogKey?.let { catalogCache[it] }.orEmpty()
    val effectiveModel = selectedModel.ifEmpty { profileInfo?.model.orEmpty() }
    val efforts = remember(effectiveModel, models) {
        effortsForModel(effectiveModel, models)
    }

    // Drop any effort that the newly chosen model or profile does not support (including fixed-effort models).
    LaunchedEffect(efforts, effectiveModel) {
        if (selectedEffort.isNotEmpty() && selectedEffort !in efforts) {
            selectedEffort = ""
        }
    }

    var starting by remember { mutableStateOf(false) }
    val act = actor()
    val willSendPrompt = !isShell && prompt.isNotBlank()

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        if (project != null) stringResource(R.string.new_session_in, project.label)
                        else stringResource(R.string.new_session),
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back))
                    }
                },
            )
        },
        bottomBar = {
            if (node != null) Surface(color = MaterialTheme.colorScheme.surface) {
                Column(Modifier.fillMaxWidth().navigationBarsPadding().imePadding()) {
                    HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
                    Box(Modifier.fillMaxWidth().padding(16.dp)) {
                        Button(
                            enabled = agent.isNotEmpty() && !starting,
                            shape = RoundedCornerShape(14.dp),
                            modifier = Modifier.fillMaxWidth().height(52.dp),
                            onClick = {
                                starting = true
                                act {
                                    try {
                                        val req = OpenSession.newBuilder()
                                            .setNodeId(node.id)
                                            .setAgent(agent)
                                            .setProfile(profile)
                                            .setProject(folder.trim())
                                            .setCols(COLS)
                                            .setRows(ROWS)
                                        if (sessionName.isNotBlank()) {
                                            req.setName(sessionName.trim())
                                        }
                                        if (willSendPrompt) {
                                            req.setPrompt(prompt.trim())
                                        }
                                        if (useWorktree) {
                                            val wtBranch = resolveWorktreeBranch(
                                                branchInput = branch,
                                                agent = agent,
                                                sessionName = sessionName,
                                            )
                                            req.setWorktree(
                                                Worktree.newBuilder()
                                                    .setRepo(folder.trim())
                                                    .setBranch(wtBranch)
                                                    .build(),
                                            )
                                        }
                                        if (supportsOverrides && !isShell) {
                                            if (selectedModel.isNotEmpty()) {
                                                req.setModel(selectedModel)
                                            }
                                            if (selectedEffort.isNotEmpty() && selectedEffort in efforts) {
                                                req.setEffort(selectedEffort)
                                            }
                                            if (yolo != defaultYolo) {
                                                req.setYolo(yolo)
                                            }
                                        }
                                        val s = mesh.daemon.startSession(req.build())
                                        mesh.refreshSessions()
                                        onStarted(s)
                                    } finally {
                                        starting = false
                                    }
                                }
                            },
                        ) {
                            AnimatedContent(
                                targetState = Triple(starting, willSendPrompt, agent),
                                transitionSpec = { fadeIn() togetherWith fadeOut() },
                                label = "launchButton",
                            ) { (isStarting, sendPrompt, agentName) ->
                                if (isStarting) {
                                    CircularProgressIndicator(
                                        Modifier.size(18.dp),
                                        strokeWidth = 2.dp,
                                        color = MaterialTheme.colorScheme.onPrimary,
                                    )
                                } else if (sendPrompt) {
                                    Row(
                                        verticalAlignment = Alignment.CenterVertically,
                                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                                    ) {
                                        Icon(Icons.AutoMirrored.Rounded.Send, null, Modifier.size(18.dp))
                                        Text(stringResource(R.string.launch_start_and_send))
                                    }
                                } else {
                                    Text(stringResource(R.string.start_agent, agentName))
                                }
                            }
                        }
                    }
                }
            }
        },
    ) { pad ->
        if (node == null) {
            Box(Modifier.padding(pad)) {
                Message(
                    stringResource(R.string.no_computers_to_run),
                    stringResource(R.string.no_computers_to_run_body),
                    icon = Icons.Outlined.DesktopAccessDisabled,
                )
            }
            return@Scaffold
        }

        Column(
            Modifier
                .fillMaxSize()
                .padding(pad)
                .consumeWindowInsets(pad)
                .imePadding()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            // Card 1: Computer, Agent, and Profile
            LaunchCard {
                CardSectionTitle(stringResource(R.string.computer))
                FlowRow(
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    for (n in usable) {
                        FilterChip(
                            selected = n.id == node.id,
                            onClick = { nodeId = n.id },
                            label = { Text(n.name) },
                        )
                    }
                }

                HorizontalDivider(
                    Modifier.padding(vertical = 8.dp),
                    color = MaterialTheme.colorScheme.outlineVariant,
                )

                CardSectionTitle(stringResource(R.string.agent))
                FlowRow(
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    for (a in node.agentsList) {
                        FilterChip(
                            selected = a.name == agent,
                            onClick = { agent = a.name },
                            label = { Text(a.name) },
                            leadingIcon = { AgentAvatar(a.name, agentColor(nodes, node.id, a.name), null, size = 22.dp) },
                        )
                    }
                }

                val profiles = remember(agentInfo) {
                    agentInfo?.profilesList.orEmpty().ifEmpty {
                        agentInfo?.profileDetailsList.orEmpty().map { it.name }
                    }
                }
                AnimatedVisibility(
                    visible = profiles.isNotEmpty(),
                    enter = expandVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeIn(),
                    exit = shrinkVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeOut(),
                ) {
                    Column(Modifier.padding(top = 8.dp)) {
                        HorizontalDivider(
                            Modifier.padding(bottom = 8.dp),
                            color = MaterialTheme.colorScheme.outlineVariant,
                        )
                        CardSectionTitle(stringResource(R.string.profile))
                        FlowRow(
                            horizontalArrangement = Arrangement.spacedBy(8.dp),
                            verticalArrangement = Arrangement.spacedBy(4.dp),
                        ) {
                            FilterChip(
                                selected = profile.isEmpty(),
                                onClick = { profile = "" },
                                label = { Text(stringResource(R.string.profile_default)) },
                            )
                            for (p in profiles) {
                                FilterChip(
                                    selected = p == profile,
                                    onClick = { profile = p },
                                    label = { Text(p) },
                                )
                            }
                        }
                        val profileDesc = profileInfo?.description.orEmpty()
                        AnimatedVisibility(visible = profileDesc.isNotBlank()) {
                            Text(
                                profileDesc,
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(top = 4.dp),
                            )
                        }
                    }
                }
            }

            // Card 2: Folder + recent folders
            LaunchCard {
                CardSectionTitle(stringResource(R.string.folder))
                OutlinedTextField(
                    value = folder,
                    onValueChange = { folder = it },
                    placeholder = { Text(stringResource(R.string.folder_hint)) },
                    singleLine = true,
                    textStyle = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                    shape = RoundedCornerShape(12.dp),
                    modifier = Modifier.fillMaxWidth(),
                )
                val recent = if (own.isNotEmpty()) own else (
                    sessions.filter { it.nodeId == node.id && it.project.isNotEmpty() }
                        .sortedByDescending { it.startedAt.seconds }
                        .map { it.worktree.repo.ifEmpty { it.project } }
                    ).distinct().take(8)
                AnimatedVisibility(visible = recent.isNotEmpty()) {
                    FlowRow(
                        Modifier.padding(top = 8.dp),
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                        verticalArrangement = Arrangement.spacedBy(4.dp),
                    ) {
                        for (p in recent) {
                            FilterChip(
                                selected = folder == p,
                                onClick = { folder = p },
                                label = { Text(baseName(p)) },
                                leadingIcon = { Icon(Icons.Outlined.Folder, null, Modifier.size(18.dp)) },
                            )
                        }
                    }
                }
            }

            // Card 3: Model & Effort (when node supports overrides and agent exposes a catalog)
            AnimatedVisibility(
                visible = supportsOverrides && !isShell && (catalogLoading || models.isNotEmpty()),
                enter = expandVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeIn(),
                exit = shrinkVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeOut(),
            ) {
                LaunchCard {
                    Row(
                        Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        CardSectionTitle(stringResource(R.string.launch_model_effort), Modifier.padding(bottom = 0.dp))
                        if (catalogLoading) {
                            Row(
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(6.dp),
                            ) {
                                CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 1.8.dp)
                                Text(
                                    stringResource(R.string.launch_loading_models),
                                    style = MaterialTheme.typography.labelSmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                        } else {
                            IconButton(
                                onClick = { catalogRefreshNonce++ },
                                modifier = Modifier.size(32.dp),
                            ) {
                                Icon(
                                    Icons.Outlined.Refresh,
                                    stringResource(R.string.launch_refresh_models),
                                    Modifier.size(18.dp),
                                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                        }
                    }

                    if (models.isNotEmpty()) {
                        Spacer(Modifier.height(6.dp))
                        val primaryModels = remember(models) {
                            models.filterNot { it.overflow }.ifEmpty { models }
                        }
                        val overflowModels = remember(models, primaryModels) {
                            if (primaryModels.size == models.size) emptyList()
                            else models.filter { it.overflow }
                        }
                        val visibleModels = remember(primaryModels, overflowModels, showOverflowModels, selectedModel) {
                            if (showOverflowModels) primaryModels + overflowModels
                            else {
                                val selectedOverflow = overflowModels.filter { it.id == selectedModel }
                                primaryModels + selectedOverflow
                            }
                        }
                        val defaultModelLabel = profileInfo?.model?.takeIf { it.isNotBlank() }?.let { def ->
                            findCatalogModel(models, def)?.let(::modelDisplayName) ?: shortModel(def)
                        }

                        FlowRow(
                            horizontalArrangement = Arrangement.spacedBy(8.dp),
                            verticalArrangement = Arrangement.spacedBy(4.dp),
                        ) {
                            FilterChip(
                                selected = selectedModel.isEmpty(),
                                onClick = { selectedModel = "" },
                                label = {
                                    Text(
                                        if (defaultModelLabel != null) stringResource(R.string.launch_default_named, defaultModelLabel)
                                        else stringResource(R.string.launch_default),
                                    )
                                },
                            )
                            for (m in visibleModels) {
                                FilterChip(
                                    selected = selectedModel == m.id,
                                    onClick = { selectedModel = if (selectedModel == m.id) "" else m.id },
                                    label = { Text(modelDisplayName(m)) },
                                )
                            }
                            if (overflowModels.isNotEmpty()) {
                                FilterChip(
                                    selected = false,
                                    onClick = { showOverflowModels = !showOverflowModels },
                                    label = {
                                        Text(
                                            if (showOverflowModels) stringResource(R.string.launch_fewer_models)
                                            else stringResource(R.string.launch_more_models, overflowModels.size),
                                        )
                                    },
                                )
                            }
                        }

                        AnimatedVisibility(
                            visible = efforts.isNotEmpty(),
                            enter = expandVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeIn(),
                            exit = shrinkVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeOut(),
                        ) {
                            Column(Modifier.padding(top = 8.dp)) {
                                HorizontalDivider(
                                    Modifier.padding(bottom = 8.dp),
                                    color = MaterialTheme.colorScheme.outlineVariant,
                                )
                                CardSectionTitle(stringResource(R.string.launch_effort))
                                val defaultEffortLabel = profileInfo?.effort?.takeIf { it.isNotBlank() }?.let(::effortDisplayName)
                                FlowRow(
                                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                                    verticalArrangement = Arrangement.spacedBy(4.dp),
                                ) {
                                    FilterChip(
                                        selected = selectedEffort.isEmpty(),
                                        onClick = { selectedEffort = "" },
                                        label = {
                                            Text(
                                                if (defaultEffortLabel != null) stringResource(R.string.launch_default_named, defaultEffortLabel)
                                                else stringResource(R.string.launch_default),
                                            )
                                        },
                                    )
                                    for (e in efforts) {
                                        FilterChip(
                                            selected = selectedEffort == e,
                                            onClick = { selectedEffort = if (selectedEffort == e) "" else e },
                                            label = { Text(effortDisplayName(e)) },
                                        )
                                    }
                                }
                            }
                        }
                    }
                }
            }

            // Card 4: First prompt (hidden for shell)
            AnimatedVisibility(
                visible = !isShell,
                enter = expandVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeIn(),
                exit = shrinkVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeOut(),
            ) {
                LaunchCard {
                    CardSectionTitle(stringResource(R.string.launch_prompt))
                    OutlinedTextField(
                        value = prompt,
                        onValueChange = { prompt = it },
                        placeholder = { Text(stringResource(R.string.launch_prompt_hint)) },
                        minLines = 3,
                        maxLines = 6,
                        shape = RoundedCornerShape(12.dp),
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            }

            // Card 5: Expandable Launch Options (Session name, YOLO mode, Git Worktree)
            val activeOptionBadges = buildList {
                if (sessionName.isNotBlank()) add(sessionName.trim())
                if (supportsOverrides && !isShell && yolo) add(stringResource(R.string.launch_yolo_badge))
                if (useWorktree) {
                    add(branch.trim().ifEmpty { stringResource(R.string.launch_worktree_badge) })
                }
            }
            val optionsChevron by animateFloatAsState(
                targetValue = if (optionsExpanded) 180f else 0f,
                animationSpec = spring(stiffness = Spring.StiffnessMediumLow),
                label = "optionsChevron",
            )

            Surface(
                shape = RoundedCornerShape(16.dp),
                color = MaterialTheme.colorScheme.surfaceContainerLow,
                border = BorderStroke(1.dp, MaterialTheme.colorScheme.outlineVariant),
                modifier = Modifier.fillMaxWidth(),
            ) {
                Column(Modifier.animateContentSize(spring(stiffness = Spring.StiffnessMediumLow))) {
                    Row(
                        Modifier
                            .fillMaxWidth()
                            .clickable { optionsExpanded = !optionsExpanded }
                            .padding(horizontal = 16.dp, vertical = 14.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Icon(
                            Icons.Outlined.Tune,
                            contentDescription = null,
                            modifier = Modifier.size(18.dp),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                        Spacer(Modifier.width(10.dp))
                        Column(Modifier.weight(1f)) {
                            Text(
                                stringResource(R.string.launch_options),
                                style = MaterialTheme.typography.titleSmall,
                                fontWeight = FontWeight.Medium,
                            )
                            if (!optionsExpanded && activeOptionBadges.isNotEmpty()) {
                                Text(
                                    activeOptionBadges.joinToString(" · "),
                                    style = MaterialTheme.typography.bodySmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                            }
                        }
                        Icon(
                            Icons.Outlined.KeyboardArrowDown,
                            contentDescription = stringResource(if (optionsExpanded) R.string.expanded else R.string.collapsed),
                            modifier = Modifier.size(20.dp).rotate(optionsChevron),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }

                    AnimatedVisibility(
                        visible = optionsExpanded,
                        enter = expandVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeIn(),
                        exit = shrinkVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeOut(),
                    ) {
                        Column(Modifier.padding(start = 16.dp, end = 16.dp, bottom = 16.dp)) {
                            HorizontalDivider(
                                Modifier.padding(bottom = 12.dp),
                                color = MaterialTheme.colorScheme.outlineVariant,
                            )

                            // Session name
                            CardSectionTitle(stringResource(R.string.launch_session_name))
                            OutlinedTextField(
                                value = sessionName,
                                onValueChange = { sessionName = it },
                                placeholder = { Text(stringResource(R.string.launch_session_name_hint)) },
                                singleLine = true,
                                shape = RoundedCornerShape(12.dp),
                                modifier = Modifier.fillMaxWidth(),
                            )

                            // YOLO mode toggle (hidden for shell or older daemons)
                            AnimatedVisibility(visible = supportsOverrides && !isShell) {
                                Column {
                                    HorizontalDivider(
                                        Modifier.padding(vertical = 12.dp),
                                        color = MaterialTheme.colorScheme.outlineVariant,
                                    )
                                    OptionToggleRow(
                                        title = stringResource(R.string.launch_yolo),
                                        subtitle = stringResource(R.string.launch_yolo_body),
                                        icon = Icons.Outlined.Bolt,
                                        checked = yolo,
                                        onCheckedChange = { yolo = it },
                                    )
                                }
                            }

                            // New Git Worktree toggle + optional branch name
                            HorizontalDivider(
                                Modifier.padding(vertical = 12.dp),
                                color = MaterialTheme.colorScheme.outlineVariant,
                            )
                            OptionToggleRow(
                                title = stringResource(R.string.launch_worktree),
                                subtitle = stringResource(R.string.launch_worktree_body),
                                icon = Icons.Outlined.AccountTree,
                                checked = useWorktree,
                                onCheckedChange = { useWorktree = it },
                            )
                            AnimatedVisibility(
                                visible = useWorktree,
                                enter = expandVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeIn(),
                                exit = shrinkVertically(spring(stiffness = Spring.StiffnessMediumLow)) + fadeOut(),
                            ) {
                                Column(Modifier.padding(top = 10.dp)) {
                                    CardSectionTitle(stringResource(R.string.launch_branch_name))
                                    OutlinedTextField(
                                        value = branch,
                                        onValueChange = { branch = it },
                                        placeholder = { Text(stringResource(R.string.launch_branch_hint)) },
                                        singleLine = true,
                                        textStyle = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                                        shape = RoundedCornerShape(12.dp),
                                        modifier = Modifier.fillMaxWidth(),
                                    )
                                }
                            }
                        }
                    }
                }
            }

            Spacer(Modifier.height(4.dp))
        }
    }
}

@Composable
private fun LaunchCard(
    modifier: Modifier = Modifier,
    content: @Composable ColumnScope.() -> Unit,
) {
    Surface(
        shape = RoundedCornerShape(16.dp),
        color = MaterialTheme.colorScheme.surfaceContainerLow,
        border = BorderStroke(1.dp, MaterialTheme.colorScheme.outlineVariant),
        modifier = modifier.fillMaxWidth(),
    ) {
        Column(
            Modifier
                .fillMaxWidth()
                .animateContentSize(spring(stiffness = Spring.StiffnessMediumLow))
                .padding(16.dp),
            content = content,
        )
    }
}

@Composable
private fun CardSectionTitle(text: String, modifier: Modifier = Modifier) {
    Text(
        text = text,
        style = MaterialTheme.typography.labelLarge,
        fontWeight = FontWeight.Medium,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = modifier.padding(bottom = 6.dp),
    )
}

@Composable
private fun OptionToggleRow(
    title: String,
    subtitle: String,
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
) {
    Row(
        Modifier
            .fillMaxWidth()
            .clickable { onCheckedChange(!checked) }
            .padding(vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(
            icon,
            contentDescription = null,
            modifier = Modifier.size(20.dp),
            tint = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(
                title,
                style = MaterialTheme.typography.bodyLarge,
                fontWeight = FontWeight.Medium,
            )
            Text(
                subtitle,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Spacer(Modifier.width(12.dp))
        Switch(checked = checked, onCheckedChange = onCheckedChange)
    }
}

/** A session started here opens at a desktop window's size, where it is likely seen next. */
private const val COLS = 120
private const val ROWS = 36

/** Agent name for a plain terminal shell, which does not accept AI prompts or model overrides. */
private const val SHELL_AGENT = "shell"

/** First protocol revision supporting OpenSession.model/effort/yolo overrides (protocol.RevisionTemplatesAndDecisions). */
private const val REVISION_OPEN_OVERRIDES = 39L

private val NON_BRANCH_SLUG = Regex("[^a-z0-9]+")

/**
 * Returns the trimmed user-supplied branch name when non-blank, or generates a
 * valid Git branch name (`mako/<slug>-<MMdd-HHmmss>`) because the daemon's
 * `gitstat.AddWorktree` requires a non-empty valid branch name.
 */
internal fun resolveWorktreeBranch(
    branchInput: String,
    agent: String,
    sessionName: String = "",
    nowMillis: Long = System.currentTimeMillis(),
): String {
    val trimmed = branchInput.trim()
    if (trimmed.isNotEmpty()) return trimmed
    val rawSeed = sessionName.trim().ifEmpty { agent.trim() }.lowercase(Locale.ROOT)
    val slug = rawSeed.replace(NON_BRANCH_SLUG, "-").trim('-').take(24).trimEnd('-').ifEmpty { "session" }
    val stamp = SimpleDateFormat("MMdd-HHmmss", Locale.US).format(Date(nowMillis))
    return "mako/$slug-$stamp"
}
