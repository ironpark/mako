package dev.ironpark.mako

import android.app.Application
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/** MakoApp holds the mesh, for the window and the service alike. */
class MakoApp : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    lateinit var mesh: Mesh
        private set

    private val _foreground = MutableStateFlow(false)
    /** Whether the app is on screen, when what it would notify of shows. */
    val foreground: StateFlow<Boolean> = _foreground.asStateFlow()

    override fun onCreate() {
        super.onCreate()
        mesh = Mesh(this, scope, foreground)
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStart(owner: LifecycleOwner) {
                _foreground.value = true
            }

            override fun onStop(owner: LifecycleOwner) {
                _foreground.value = false
            }
        })
    }
}

val android.content.Context.mako: MakoApp get() = applicationContext as MakoApp
