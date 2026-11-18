package dev.ironpark.mako.ui

import android.net.Uri
import androidx.compose.animation.AnimatedContentTransitionScope
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.ExperimentalSharedTransitionApi
import androidx.compose.animation.SharedTransitionLayout
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.navigation.NavBackStackEntry
import androidx.navigation.NavGraphBuilder
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.SessionRef
import dev.ironpark.mako.mako
import dev.ironpark.mako.net.GitHubAppUpdates
import dev.ironpark.mako.net.NodeProjectFiles
import dev.ironpark.mako.workspace.view
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first

/**
 * MakoNav is the app's screens: home, the projects; a project, its tabs;
 * the mesh, reading a pairing code, a new session, a project's files and
 * agents, the panels. A pairing code another app read comes as pairing,
 * which joins its computer; a session a notification names opens in its
 * project.
 */
@OptIn(ExperimentalSharedTransitionApi::class)
@Composable
fun MakoNav(mesh: Mesh, target: MutableStateFlow<SessionRef?>, pairing: MutableStateFlow<String?>) {
    val nav = rememberNavController()
    val homeState = rememberHomeState()
    val open by target.collectAsState()
    val pair by pairing.collectAsState()
    val join = rememberJoin(mesh)
    val context = LocalContext.current
    val sessions by mesh.sessions.collectAsState()
    // The project and tab seen last, as "key\ntab", for a swipe from the right edge of home to bring back.
    var lastSeen by rememberSaveable { mutableStateOf("") }
    val last = lastSeen.substringAfter('\n').takeIf { it.isNotEmpty() }?.let { id -> sessions.firstOrNull { it.id == id } }

    LaunchedEffect(pair) {
        val link = pair ?: return@LaunchedEffect
        pairing.value = null
        nav.popBackStack("home", inclusive = false)
        nav.navigate("mesh")
        // Opened by the link, the app may not have its node running yet.
        mesh.state.first { it == Mesh.State.Running }
        join(link) {}
    }

    fun openProject(key: String, tab: String?) {
        // One project at a time: another opened from one's home preview takes its place.
        if (nav.currentDestination?.route?.startsWith("project") == true) nav.popBackStack()
        nav.navigate("project?key=${routeKey(key)}&tab=${Uri.encode(tab.orEmpty())}")
    }

    LaunchedEffect(open) {
        val ref = open ?: return@LaunchedEffect
        target.value = null
        nav.popBackStack("home", inclusive = false)
        // The session's project is found once its session is known.
        openProject("", ref.id)
    }

    fun newSession(project: ProjectItem?) =
        nav.navigate("new?project=${routeKey(project?.key.orEmpty())}")

    val slideIn: AnimatedContentTransitionScope<NavBackStackEntry>.() -> EnterTransition =
        { slideInHorizontally(tween(260)) { it / 3 } + fadeIn(tween(200)) }
    val slideOut: AnimatedContentTransitionScope<NavBackStackEntry>.() -> ExitTransition =
        { slideOutHorizontally(tween(260)) { -it / 6 } + fadeOut(tween(160)) }
    val popIn: AnimatedContentTransitionScope<NavBackStackEntry>.() -> EnterTransition =
        { slideInHorizontally(tween(260)) { -it / 6 } + fadeIn(tween(200)) }
    val popOut: AnimatedContentTransitionScope<NavBackStackEntry>.() -> ExitTransition =
        { slideOutHorizontally(tween(260)) { it / 3 } + fadeOut(tween(160)) }

    SharedTransitionLayout {
        CompositionLocalProvider(LocalSharedScope provides this) {
            NavHost(
                nav, startDestination = "home",
                enterTransition = slideIn, exitTransition = slideOut, popEnterTransition = popIn, popExitTransition = popOut,
            ) {
                screen("home",
                    // Home opens into a project: the row's mark and name travel, the rest fades.
                    exit = { if (targetState.destination.route?.startsWith("project") == true) fadeOut(tween(220)) else slideOut() },
                    popEnter = { if (initialState.destination.route?.startsWith("project") == true) fadeIn(tween(220)) else popIn() },
                ) {
                    HomeScreen(
                        mesh,
                        onProject = { p, tab -> openProject(p.key, tab) },
                        onMesh = { nav.navigate("mesh") },
                        onNew = ::newSession,
                        onSettings = { nav.navigate("settings") },
                        onScan = { nav.navigate("scan") },
                        onPanels = { node -> nav.navigate("panels/${Uri.encode(node)}") },
                        last = last,
                        onLast = { last?.let { openProject(lastSeen.substringBefore('\n'), it.id) } },
                        state = homeState,
                    )
                }
                screen("project?key={key}&tab={tab}",
                    enter = { if (initialState.destination.route == "home") fadeIn(tween(260)) else slideIn() },
                    popExit = { if (targetState.destination.route == "home") fadeOut(tween(200)) else popOut() },
                ) { entry ->
                    val key = keyOfRoute(entry.arguments?.getString("key").orEmpty())
                    val tab = entry.arguments?.getString("tab").orEmpty().ifEmpty { null }
                    val picked = remember(entry) { entry.savedStateHandle.getStateFlow("tab", "") }
                    val previewState = rememberHomeState(homeState)
                    ProjectScreen(
                        mesh, key, tab, homeState.space.value, picked,
                        onPicked = { entry.savedStateHandle["tab"] = "" },
                        onBack = { nav.popBackStack() },
                        onNew = ::newSession,
                        onBlock = { node, p -> nav.navigate("block/${Uri.encode(node)}/${Uri.encode(p.block)}?title=${Uri.encode(p.title)}") },
                        onFiles = { node, root -> nav.navigate("files/${Uri.encode(node)}/${Uri.encode(root)}") },
                        onAgents = { s, path -> nav.navigate("agents/${Uri.encode(s.nodeId)}/${Uri.encode(path)}/${Uri.encode(s.id)}") },
                        onFile = { s, link ->
                            val node = Uri.encode(s.nodeId)
                            if (link.folder) nav.navigate("files/$node/${Uri.encode(link.path)}")
                            else nav.navigate("file/$node/${Uri.encode(s.project.ifEmpty { remoteParent(link.path) })}/${Uri.encode(link.path)}?line=${link.line}")
                        },
                        onShown = { k, id -> lastSeen = k + "\n" + id },
                        home = {
                            // Home behind the session, as a swipe from its chat shows it: no element of it travels.
                            CompositionLocalProvider(LocalNavScope provides null) {
                                HomeScreen(
                                    mesh,
                                    onProject = { p, t -> openProject(p.key, t) },
                                    onMesh = { nav.navigate("mesh") },
                                    onNew = ::newSession,
                                    onSettings = { nav.navigate("settings") },
                                    onScan = { nav.navigate("scan") },
                                    state = previewState,
                                )
                            }
                        },
                    )
                }
                // The agents of a session's project, on its node.
                screen("agents/{node}/{path}/{self}") { entry ->
                    val a = entry.arguments
                    ProjectAgentsScreen(mesh, a?.getString("node").orEmpty(), a?.getString("path").orEmpty(), a?.getString("self").orEmpty(),
                        onBack = { nav.popBackStack() }, onOpen = { openProject("", it.id) })
                }
                screen("files/{node}/{root}") { entry ->
                    val node = entry.arguments?.getString("node").orEmpty()
                    val root = entry.arguments?.getString("root").orEmpty()
                    val files = remember(mesh.daemon, node) { NodeProjectFiles(mesh.daemon, node) { mesh.copiesFiles(node) } }
                    FilesScreen(files, root, mesh.nodeName(node), onBack = { nav.popBackStack() })
                }
                // A file a conversation links to, on its session's node.
                screen("file/{node}/{root}/{path}?line={line}") { entry ->
                    val a = entry.arguments
                    val node = a?.getString("node").orEmpty()
                    val files = remember(mesh.daemon, node) { NodeProjectFiles(mesh.daemon, node) }
                    ProjectFileScreen(files, a?.getString("root").orEmpty(), a?.getString("path").orEmpty(),
                        a?.getString("line")?.toLongOrNull() ?: 0L, diffPath = "", from = "", deleted = false,
                        onBack = { nav.popBackStack() },
                        onFile = { link ->
                            if (link.folder) nav.navigate("files/${Uri.encode(node)}/${Uri.encode(link.path)}")
                            else nav.navigate("file/${Uri.encode(node)}/${Uri.encode(a?.getString("root").orEmpty())}/${Uri.encode(link.path)}?line=${link.line}")
                        })
                }
                // The panels of a computer, and one of them a page of its own.
                screen("panels/{node}") { entry ->
                    val node = entry.arguments?.getString("node").orEmpty()
                    PanelsScreen(mesh, node, homeState.space.value, onBack = { nav.popBackStack() }, onOpen = { p ->
                        nav.navigate("block/${Uri.encode(node)}/${Uri.encode(p.block)}?title=${Uri.encode(p.title)}")
                    })
                }
                screen("block/{node}/{block}?title={title}") { entry ->
                    val a = entry.arguments
                    BlockScreen(mesh, a?.getString("node").orEmpty(), a?.getString("block").orEmpty(), a?.getString("title").orEmpty(),
                        onBack = { nav.popBackStack() })
                }
                screen("settings") {
                    SettingsScreen(onBack = { nav.popBackStack() }, onNotifications = { nav.navigate("notifications") }, onUpdates = { nav.navigate("updates") })
                }
                screen("notifications") {
                    NotificationSettingsScreen(context.mako.notificationPreferences, onBack = { nav.popBackStack() })
                }
                screen("updates") {
                    val updates = remember(context) { GitHubAppUpdates(context.applicationContext) }
                    UpdatesScreen(updates, onBack = { nav.popBackStack() })
                }
                screen("mesh") { MeshScreen(mesh, onBack = { nav.popBackStack() }, onScan = { nav.navigate("scan") }) }
                screen("scan") { ScanScreen(mesh, onBack = { nav.popBackStack() }) }
                screen("new?project={project}") { entry ->
                    val key = keyOfRoute(entry.arguments?.getString("project").orEmpty())
                    val workspace by mesh.workspace.collectAsState()
                    val view = remember(workspace.state) { workspace.state.view() }
                    val project = remember(view, sessions, key) {
                        if (key.isEmpty()) null else projectIndex(view, sessions, mesh::nodeName).firstOrNull { it.key == key }
                    }
                    NewSessionScreen(mesh, project, onBack = { nav.popBackStack() }, onStarted = { s ->
                        nav.popBackStack()
                        // Back in its project, the new session is the tab shown; from home, its project opens on it.
                        val back = nav.currentBackStackEntry
                        if (back?.destination?.route?.startsWith("project") == true) back.savedStateHandle["tab"] = s.id
                        else openProject(project?.key.orEmpty(), s.id)
                    })
                }
            }
        }
    }
}

/** A destination whose content knows the transition it is in, for the elements it shares. */
private fun NavGraphBuilder.screen(
    route: String,
    enter: (AnimatedContentTransitionScope<NavBackStackEntry>.() -> EnterTransition)? = null,
    exit: (AnimatedContentTransitionScope<NavBackStackEntry>.() -> ExitTransition)? = null,
    popEnter: (AnimatedContentTransitionScope<NavBackStackEntry>.() -> EnterTransition)? = null,
    popExit: (AnimatedContentTransitionScope<NavBackStackEntry>.() -> ExitTransition)? = null,
    content: @Composable (NavBackStackEntry) -> Unit,
) = composable(
    route, enterTransition = enter, exitTransition = exit, popEnterTransition = popEnter, popExitTransition = popExit,
) { entry ->
    CompositionLocalProvider(LocalNavScope provides this) { content(entry) }
}
