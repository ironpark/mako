package dev.ironpark.mako.ui

import android.net.Uri
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.SessionRef
import dev.ironpark.mako.mako
import dev.ironpark.mako.net.GitHubAppUpdates
import dev.ironpark.mako.net.NodeProjectFiles
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first

/**
 * MakoNav is the app's screens: the sessions, one session, the mesh,
 * reading a pairing code, a new session. A pairing code another app read
 * comes as pairing, which joins its computer.
 */
@Composable
fun MakoNav(mesh: Mesh, target: MutableStateFlow<SessionRef?>, pairing: MutableStateFlow<String?>) {
    val nav = rememberNavController()
    val homeState = rememberSessionsScreenState()
    val open by target.collectAsState()
    val pair by pairing.collectAsState()
    val join = rememberJoin(mesh)
    val context = LocalContext.current

    LaunchedEffect(pair) {
        val link = pair ?: return@LaunchedEffect
        pairing.value = null
        nav.popBackStack("home", inclusive = false)
        nav.navigate("mesh")
        // Opened by the link, the app may not have its node running yet.
        mesh.state.first { it == Mesh.State.Running }
        join(link) {}
    }

    fun openSession(node: String, id: String) = nav.navigate("session/${Uri.encode(node)}/${Uri.encode(id)}")

    LaunchedEffect(open) {
        val ref = open ?: return@LaunchedEffect
        target.value = null
        nav.popBackStack("home", inclusive = false)
        openSession(ref.node, ref.id)
    }

    NavHost(nav, startDestination = "home") {
        composable("home", popEnterTransition = { EnterTransition.None }) {
            SessionsScreen(
                mesh,
                onOpen = { openSession(it.nodeId, it.id) },
                onMesh = { nav.navigate("mesh") },
                onNew = { nav.navigate("new") },
                onSettings = { nav.navigate("settings") },
                screenState = homeState,
            )
        }
        composable("session/{node}/{id}", popExitTransition = { ExitTransition.None }) { entry ->
            val args = entry.arguments
            val previewState = rememberSessionsScreenState(homeState)
            SessionScreen(
                mesh,
                node = args?.getString("node").orEmpty(),
                id = args?.getString("id").orEmpty(),
                onBack = { nav.popBackStack() },
                onFiles = { root -> nav.navigate("files/${Uri.encode(args?.getString("node").orEmpty())}/${Uri.encode(root)}") },
                onFile = { link ->
                    val node = Uri.encode(args?.getString("node").orEmpty())
                    val project = mesh.session(args?.getString("id").orEmpty())?.project.orEmpty()
                    if (link.folder) nav.navigate("files/$node/${Uri.encode(link.path)}")
                    else nav.navigate("file/$node/${Uri.encode(project.ifEmpty { remoteParent(link.path) })}/${Uri.encode(link.path)}?line=${link.line}")
                },
                home = {
                    SessionsScreen(
                        mesh,
                        onOpen = { openSession(it.nodeId, it.id) },
                        onMesh = { nav.navigate("mesh") },
                        onNew = { nav.navigate("new") },
                        onSettings = { nav.navigate("settings") },
                        screenState = previewState,
                    )
                },
            )
        }
        composable("files/{node}/{root}") { entry ->
            val node = entry.arguments?.getString("node").orEmpty()
            val root = entry.arguments?.getString("root").orEmpty()
            val files = remember(mesh.daemon, node) { NodeProjectFiles(mesh.daemon, node) }
            FilesScreen(files, root, mesh.nodeName(node), onBack = { nav.popBackStack() })
        }
        // A file a conversation links to, on its session's node.
        composable("file/{node}/{root}/{path}?line={line}") { entry ->
            val a = entry.arguments
            val node = a?.getString("node").orEmpty()
            val files = remember(mesh.daemon, node) { NodeProjectFiles(mesh.daemon, node) }
            ProjectFileScreen(files, a?.getString("root").orEmpty(), a?.getString("path").orEmpty(),
                a?.getString("line")?.toLongOrNull() ?: 0L, diffPath = "", from = "", deleted = false,
                onBack = { nav.popBackStack() })
        }
        composable("settings") {
            SettingsScreen(onBack = { nav.popBackStack() }, onNotifications = { nav.navigate("notifications") }, onUpdates = { nav.navigate("updates") })
        }
        composable("notifications") {
            NotificationSettingsScreen(context.mako.notificationPreferences, onBack = { nav.popBackStack() })
        }
        composable("updates") {
            val updates = remember(context) { GitHubAppUpdates(context.applicationContext) }
            UpdatesScreen(updates, onBack = { nav.popBackStack() })
        }
        composable("mesh") { MeshScreen(mesh, onBack = { nav.popBackStack() }, onScan = { nav.navigate("scan") }) }
        composable("scan") { ScanScreen(mesh, onBack = { nav.popBackStack() }) }
        composable("new") {
            NewSessionScreen(mesh, onBack = { nav.popBackStack() }, onStarted = {
                nav.popBackStack()
                openSession(it.nodeId, it.id)
            })
        }
    }
}
