package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import dev.ironpark.mako.R
import kotlinx.coroutines.CancellationException

internal sealed interface RemoteLoad<out T> {
    data object Loading : RemoteLoad<Nothing>
    data class Ready<T>(val value: T) : RemoteLoad<T>
    data class Failed(val message: String) : RemoteLoad<Nothing>
}

@Composable
internal fun <T> remoteLoad(vararg keys: Any?, load: suspend () -> T): RemoteLoad<T> {
    val state = remember(*keys) { mutableStateOf<RemoteLoad<T>>(RemoteLoad.Loading) }
    LaunchedEffect(*keys) {
        state.value = try { RemoteLoad.Ready(load()) }
        catch (e: CancellationException) { throw e }
        catch (e: Exception) { RemoteLoad.Failed(e.message ?: e.toString()) }
    }
    return state.value
}

@Composable
internal fun <T> RemoteContent(result: RemoteLoad<T>, retry: () -> Unit, content: @Composable (T) -> Unit) {
    Box(Modifier.fillMaxSize()) {
        when (result) {
            RemoteLoad.Loading -> CircularProgressIndicator(Modifier.align(Alignment.Center))
            is RemoteLoad.Failed -> Message(stringResource(R.string.files_failed), result.message, stringResource(R.string.retry), retry)
            is RemoteLoad.Ready -> content(result.value)
        }
    }
}
