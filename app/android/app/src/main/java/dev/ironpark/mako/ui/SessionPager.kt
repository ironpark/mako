package dev.ironpark.mako.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.Measurable
import androidx.compose.ui.layout.MeasureResult
import androidx.compose.ui.layout.MeasureScope
import androidx.compose.ui.layout.layout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Constraints
import kotlin.math.roundToInt
import kotlinx.coroutines.flow.filter

/**
 * Home, chat, terminal share one drag, with the adjacent page visible
 * before release. The session's header and its bar at the bottom stay put
 * over chat and terminal alike, and only their content moves between
 * them; they slide away with them only towards home, which has its own.
 * footerShown hides the bar, as while the keyboard is up; the content then
 * reaches the bottom.
 */
@Composable
internal fun SessionPager(
    state: PagerState,
    onHome: () -> Unit,
    modifier: Modifier = Modifier,
    header: @Composable () -> Unit = {},
    footer: @Composable () -> Unit = {},
    footerShown: Boolean = true,
    content: @Composable (Int) -> Unit,
) {
    val goHome by rememberUpdatedState(onHome)
    LaunchedEffect(state) {
        snapshotFlow { state.settledPage == 0 && !state.isScrollInProgress }
            .filter { it }.collect { goHome() }
    }
    var headerHeight by remember { mutableIntStateOf(0) }
    var footerHeight by remember { mutableIntStateOf(0) }
    val density = LocalDensity.current
    val top = with(density) { headerHeight.toDp() }
    val bottom = with(density) { if (footerShown) footerHeight.toDp() else androidx.compose.ui.unit.Dp(0f) }
    // Read while placing: a drag moves them without composing again.
    fun MeasureScope.follow(measurable: Measurable, constraints: Constraints): MeasureResult {
        val placeable = measurable.measure(constraints)
        return layout(placeable.width, placeable.height) {
            val at = state.currentPage + state.currentPageOffsetFraction
            placeable.place(((1f - at).coerceIn(0f, 1f) * placeable.width).roundToInt(), 0)
        }
    }
    Box(modifier.fillMaxSize()) {
        HorizontalPager(state, Modifier.fillMaxSize(), beyondViewportPageCount = 1) { page ->
            if (page == 0) content(page)
            else Box(Modifier.fillMaxSize().padding(top = top, bottom = bottom)) { content(page) }
        }
        Box(Modifier.fillMaxWidth().onSizeChanged { headerHeight = it.height }.layout { m, c -> follow(m, c) }) { header() }
        AnimatedVisibility(
            footerShown, Modifier.align(Alignment.BottomCenter),
            enter = slideInVertically { it }, exit = slideOutVertically { it },
        ) {
            Box(Modifier.fillMaxWidth().onSizeChanged { footerHeight = it.height }.layout { m, c -> follow(m, c) }) { footer() }
        }
    }
}
