package dev.ironpark.mako.ui

import androidx.compose.animation.AnimatedVisibilityScope
import androidx.compose.animation.ExperimentalSharedTransitionApi
import androidx.compose.animation.SharedTransitionScope
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.Modifier

// Motion that explains: a project's mark and name travel from its row on
// home to the workspace's header, so that the workspace reads as the row
// opened up. Both screens give the elements a key; the navigation's
// transition carries them. Where either scope is missing, as in home's
// preview behind a session, nothing is shared.

@OptIn(ExperimentalSharedTransitionApi::class)
internal val LocalSharedScope = compositionLocalOf<SharedTransitionScope?> { null }

internal val LocalNavScope = compositionLocalOf<AnimatedVisibilityScope?> { null }

/** The element of key shared between the screens a navigation goes between; as it was where there is none. */
@OptIn(ExperimentalSharedTransitionApi::class)
@Composable
internal fun Modifier.shared(key: String): Modifier {
    val shared = LocalSharedScope.current ?: return this
    val nav = LocalNavScope.current ?: return this
    return with(shared) { this@shared.sharedElement(rememberSharedContentState(key), nav) }
}

/** Like [shared], for text whose size or wrap differs at either end: its bounds move, its content fades. */
@OptIn(ExperimentalSharedTransitionApi::class)
@Composable
internal fun Modifier.sharedBounds(key: String): Modifier {
    val shared = LocalSharedScope.current ?: return this
    val nav = LocalNavScope.current ?: return this
    return with(shared) {
        this@sharedBounds.sharedBounds(
            rememberSharedContentState(key), nav,
            resizeMode = SharedTransitionScope.ResizeMode.scaleToBounds(),
        )
    }
}
