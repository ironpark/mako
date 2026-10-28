package dev.ironpark.mako.ui

import android.net.Uri
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.SessionRef
import kotlinx.coroutines.flow.MutableStateFlow

/** MakoNav is the app's screens: the sessions, one session, the mesh, a new session. */
@Composable
fun MakoNav(mesh: Mesh, target: MutableStateFlow<SessionRef?>) {
    val nav = rememberNavController()
    val open by target.collectAsState()

    fun openSession(node: String, id: String) = nav.navigate("session/${Uri.encode(node)}/${Uri.encode(id)}")

    LaunchedEffect(open) {
        val ref = open ?: return@LaunchedEffect
        target.value = null
        nav.popBackStack("home", inclusive = false)
        openSession(ref.node, ref.id)
    }

    NavHost(nav, startDestination = "home") {
        composable("home") {
            SessionsScreen(
                mesh,
                onOpen = { openSession(it.nodeId, it.id) },
                onMesh = { nav.navigate("mesh") },
                onNew = { nav.navigate("new") },
            )
        }
        composable("session/{node}/{id}") { entry ->
            val args = entry.arguments
            SessionScreen(
                mesh,
                node = args?.getString("node").orEmpty(),
                id = args?.getString("id").orEmpty(),
                onBack = { nav.popBackStack() },
            )
        }
        composable("mesh") { MeshScreen(mesh, onBack = { nav.popBackStack() }) }
        composable("new") {
            NewSessionScreen(mesh, onBack = { nav.popBackStack() }, onStarted = {
                nav.popBackStack()
                openSession(it.nodeId, it.id)
            })
        }
    }
}
