package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.PagerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.layout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import kotlin.math.roundToInt
import kotlinx.coroutines.flow.filter

/**
 * Home, chat, terminal share one drag, with the adjacent page visible
 * before release. The session's header stays put over chat and terminal
 * alike, and only their content moves between them; it slides away with
 * them only towards home, which has a header of its own.
 */
@Composable
internal fun SessionPager(
    state: PagerState,
    onHome: () -> Unit,
    modifier: Modifier = Modifier,
    header: @Composable () -> Unit = {},
    content: @Composable (Int) -> Unit,
) {
    val goHome by rememberUpdatedState(onHome)
    LaunchedEffect(state) {
        snapshotFlow { state.settledPage == 0 && !state.isScrollInProgress }
            .filter { it }.collect { goHome() }
    }
    var headerHeight by remember { mutableIntStateOf(0) }
    val top = with(LocalDensity.current) { headerHeight.toDp() }
    Box(modifier.fillMaxSize()) {
        HorizontalPager(state, Modifier.fillMaxSize(), beyondViewportPageCount = 1) { page ->
            if (page == 0) content(page)
            else Box(Modifier.fillMaxSize().padding(top = top)) { content(page) }
        }
        Box(
            Modifier.fillMaxWidth().onSizeChanged { headerHeight = it.height }.layout { measurable, constraints ->
                val placeable = measurable.measure(constraints)
                layout(placeable.width, placeable.height) {
                    // Read while placing: a drag moves it without composing again.
                    val at = state.currentPage + state.currentPageOffsetFraction
                    placeable.place(((1f - at).coerceIn(0f, 1f) * placeable.width).roundToInt(), 0)
                }
            },
        ) { header() }
    }
}
