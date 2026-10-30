package dev.ironpark.mako.ui

import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.PagerState
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Modifier
import kotlinx.coroutines.flow.filter

/** Home, chat, terminal share one drag, with the adjacent page visible before release. */
@Composable
internal fun SessionPager(
    state: PagerState,
    onHome: () -> Unit,
    modifier: Modifier = Modifier,
    content: @Composable (Int) -> Unit,
) {
    val goHome by rememberUpdatedState(onHome)
    LaunchedEffect(state) {
        snapshotFlow { state.settledPage == 0 && !state.isScrollInProgress }
            .filter { it }.collect { goHome() }
    }
    HorizontalPager(state, modifier.fillMaxSize(), beyondViewportPageCount = 1) { page -> content(page) }
}
