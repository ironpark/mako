package dev.ironpark.mako.ui

import androidx.compose.animation.AnimatedVisibilityScope
import androidx.compose.animation.BoundsTransform
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.ExperimentalSharedTransitionApi
import androidx.compose.animation.SharedTransitionScope
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.Easing
import androidx.compose.animation.core.FastOutLinearInEasing
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.LinearOutSlowInEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.SpringSpec
import androidx.compose.animation.core.TweenSpec
import androidx.compose.animation.core.VisibilityThreshold
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.scaleOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.animation.slideOutVertically
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.v2.SessionInfo
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.hypot

// Motion that explains: a project's mark and name travel from its row on
// home to the workspace's header, so that the workspace reads as the row
// opened up. Both screens give the elements a key; the navigation's
// transition carries them. Where either scope is missing, as in home's
// preview behind a session, nothing is shared.

/**
 * Cohesive motion tokens, transitions, and spring specs for Mako Android.
 * Designed to feel crisp, responsive, and restrained — matching the desktop's
 * minimalist aesthetic without floaty overshoot.
 */
object MakoMotion {
    const val FastMs = 150
    const val StandardMs = 240
    const val EmphasizedMs = 300

    /** Emphasized decelerate curve: brisk initial movement with a smooth settle. */
    val EmphasizedEasing: Easing = CubicBezierEasing(0.2f, 0.0f, 0.0f, 1.0f)

    /** Emphasized accelerate curve for exiting elements. */
    val EmphasizedAccelerate: Easing = CubicBezierEasing(0.3f, 0.0f, 0.8f, 0.15f)

    /** Crisp spring for tactile micro-interactions (switches, press feedback, chevrons). */
    fun <T> snappySpring(visibilityThreshold: T? = null): SpringSpec<T> = spring(
        dampingRatio = 0.84f,
        stiffness = 540f,
        visibilityThreshold = visibilityThreshold,
    )

    /** Smooth spring for layout shifts, card expansions, and shared bounds. */
    fun <T> gentleSpring(visibilityThreshold: T? = null): SpringSpec<T> = spring(
        dampingRatio = 0.88f,
        stiffness = 340f,
        visibilityThreshold = visibilityThreshold,
    )

    val SnappySpring: SpringSpec<Float> = snappySpring()
    val GentleSpring: SpringSpec<Float> = gentleSpring()
    val SnappyDpSpring: SpringSpec<Dp> = snappySpring(Dp.VisibilityThreshold)
    val GentleDpSpring: SpringSpec<Dp> = gentleSpring(Dp.VisibilityThreshold)
    val SnappyIntOffsetSpring: SpringSpec<IntOffset> = snappySpring(IntOffset.VisibilityThreshold)
    val GentleIntSizeSpring: SpringSpec<IntSize> = gentleSpring(IntSize.VisibilityThreshold)

    fun <T> standardTween(durationMs: Int = StandardMs): TweenSpec<T> =
        tween(durationMillis = durationMs, easing = EmphasizedEasing)

    fun <T> fastTween(durationMs: Int = FastMs): TweenSpec<T> =
        tween(durationMillis = durationMs, easing = EmphasizedEasing)

    /** Forward drill-down enter: crisp horizontal slide + fade without scale distortion. */
    fun drillEnter(): EnterTransition =
        slideInHorizontally(tween(StandardMs, easing = FastOutSlowInEasing)) { (it * 0.22f).toInt() } +
            fadeIn(tween(180, easing = LinearOutSlowInEasing))

    /** Forward drill-down exit for the underlying screen: subtle parallax recession. */
    fun drillExit(): ExitTransition =
        slideOutHorizontally(tween(StandardMs, easing = FastOutSlowInEasing)) { -(it * 0.08f).toInt() } +
            fadeOut(tween(140, easing = FastOutLinearInEasing))

    /** Back navigation enter for the returning underlying screen. */
    fun drillPopEnter(): EnterTransition =
        slideInHorizontally(tween(220, easing = FastOutSlowInEasing)) { -(it * 0.08f).toInt() } +
            fadeIn(tween(180, easing = LinearOutSlowInEasing))

    /** Back navigation exit for the popped drill-down screen: exits swiftly without shrinking or lingering. */
    fun drillPopExit(): ExitTransition =
        slideOutHorizontally(tween(200, easing = FastOutLinearInEasing)) { (it * 0.24f).toInt() } +
            fadeOut(tween(160, easing = FastOutLinearInEasing))

    /** Modal / creation screen enter (e.g. New Session, Scan): subtle upward slide + fade. */
    fun modalEnter(): EnterTransition =
        slideInVertically(tween(StandardMs, easing = FastOutSlowInEasing)) { (it * 0.14f).toInt() } +
            fadeIn(tween(180, easing = LinearOutSlowInEasing))

    /** Modal / creation screen exit: crisp downward slide + fade. */
    fun modalPopExit(): ExitTransition =
        slideOutVertically(tween(200, easing = FastOutLinearInEasing)) { (it * 0.14f).toInt() } +
            fadeOut(tween(160, easing = FastOutLinearInEasing))

    /** Underlying screen recession when a modal route opens over it. */
    fun modalBackgroundExit(): ExitTransition =
        fadeOut(tween(160, easing = FastOutLinearInEasing))

    /** Underlying screen return when a modal route closes. */
    fun modalBackgroundPopEnter(): EnterTransition =
        fadeIn(tween(200, easing = LinearOutSlowInEasing))

    /** Clean fade-in for routes linked by shared element transitions (Home <-> Project). */
    fun sharedEnter(): EnterTransition =
        fadeIn(tween(200, easing = LinearOutSlowInEasing))

    /** Clean fade-out for routes linked by shared element transitions. */
    fun sharedExit(): ExitTransition =
        fadeOut(tween(140, easing = FastOutLinearInEasing))

    /** Crisp back exit from a shared-element route (Project -> Home) without shrink or spring tail. */
    fun sharedPopExit(): ExitTransition =
        slideOutHorizontally(tween(200, easing = FastOutLinearInEasing)) { (it * 0.16f).toInt() } +
            fadeOut(tween(140, easing = FastOutLinearInEasing))

    /** Smooth vertical reveal for banners, approval cards, compacting notices, and task panels. */
    val cardEnter: EnterTransition =
        expandVertically(
            animationSpec = gentleSpring(IntSize.VisibilityThreshold),
            expandFrom = Alignment.Top,
        ) + fadeIn(tween(StandardMs, easing = EmphasizedEasing))

    /** Smooth vertical collapse for banners, approval cards, compacting notices, and task panels. */
    val cardExit: ExitTransition =
        shrinkVertically(
            animationSpec = snappySpring(IntSize.VisibilityThreshold),
            shrinkTowards = Alignment.Top,
        ) + fadeOut(tween(FastMs, easing = EmphasizedAccelerate))

    val fadeEnter: EnterTransition = fadeIn(tween(StandardMs, easing = EmphasizedEasing))
    val fadeExit: ExitTransition = fadeOut(tween(FastMs, easing = EmphasizedAccelerate))

    val scaleFadeEnter: EnterTransition =
        scaleIn(snappySpring(), initialScale = 0.92f) + fadeIn(tween(FastMs, easing = EmphasizedEasing))
    val scaleFadeExit: ExitTransition =
        scaleOut(snappySpring(), targetScale = 0.92f) + fadeOut(tween(FastMs, easing = EmphasizedAccelerate))

    /** Finite-duration shared bounds transform so exiting screens never linger on a spring tail. */
    @OptIn(ExperimentalSharedTransitionApi::class)
    internal val SharedBoundsTransform = BoundsTransform { _: Rect, _: Rect ->
        tween(durationMillis = 220, easing = FastOutSlowInEasing)
    }
}

/** Whether a session's status is live and should show a gentle breathing pulse. */
fun SessionInfo.hasActivePulse(): Boolean =
    !ended() && (hasApproval() || status == "working" || status == "waiting")

/**
 * Status dot with a smooth breathing halo & subtle core alpha pulse when [active]
 * (for live `working` / `waiting` / `approval` states), and a zero-overhead static
 * dot when inactive (`idle` / `done`). Keeps exact [size] layout bounds in both states.
 */
@Composable
fun PulsingStatusDot(
    color: Color,
    active: Boolean,
    modifier: Modifier = Modifier,
    size: Dp = 8.dp,
) {
    if (!active) {
        Canvas(modifier.size(size)) {
            drawCircle(color = color, radius = this.size.minDimension / 2f)
        }
        return
    }
    val transition = rememberInfiniteTransition(label = "statusPulse")
    val progress by transition.animateFloat(
        initialValue = 0f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 1600, easing = CubicBezierEasing(0.25f, 0.1f, 0.25f, 1f)),
            repeatMode = RepeatMode.Restart,
        ),
        label = "pulseProgress",
    )
    Canvas(modifier.size(size)) {
        val coreRadius = this.size.minDimension / 2f
        val haloRadius = coreRadius * (1f + progress * 1.15f)
        val haloAlpha = (1f - progress) * (1f - progress) * 0.42f
        val coreAlpha = 0.86f + 0.14f * ((cos(progress * 2.0 * PI) + 1.0) * 0.5).toFloat()
        if (haloAlpha > 0.01f) {
            drawCircle(color = color.copy(alpha = haloAlpha), radius = haloRadius)
        }
        drawCircle(color = color.copy(alpha = coreAlpha), radius = coreRadius)
    }
}

/**
 * Subtle tactile press-scale feedback (default 0.98f) driven by a crisp spring.
 * Pass the clickable's [interactionSource] when available; when omitted, passively
 * observes pointer down/up/slop without consuming events or interfering with scrolling.
 */
@Composable
fun Modifier.pressScale(
    interactionSource: MutableInteractionSource? = null,
    pressedScale: Float = 0.98f,
    enabled: Boolean = true,
): Modifier {
    val pressed = if (interactionSource != null) {
        interactionSource.collectIsPressedAsState().value
    } else {
        var down by remember { mutableStateOf(false) }
        val observer = Modifier.pointerInput(enabled) {
            if (!enabled) {
                down = false
                return@pointerInput
            }
            awaitEachGesture {
                val first = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Main)
                down = true
                val slop = viewConfiguration.touchSlop
                try {
                    while (true) {
                        val event = awaitPointerEvent(PointerEventPass.Main)
                        val change = event.changes.firstOrNull { it.id == first.id }
                        if (change == null || !change.pressed || change.isConsumed) break
                        val dx = change.position.x - first.position.x
                        val dy = change.position.y - first.position.y
                        if (hypot(dx, dy) > slop) break
                    }
                } finally {
                    down = false
                }
            }
        }
        val scale by animateFloatAsState(
            targetValue = if (enabled && down) pressedScale else 1f,
            animationSpec = MakoMotion.SnappySpring,
            label = "pressScale",
        )
        return this.then(observer).graphicsLayer {
            scaleX = scale
            scaleY = scale
        }
    }
    val scale by animateFloatAsState(
        targetValue = if (enabled && pressed) pressedScale else 1f,
        animationSpec = MakoMotion.SnappySpring,
        label = "pressScale",
    )
    return this.graphicsLayer {
        scaleX = scale
        scaleY = scale
    }
}

@OptIn(ExperimentalSharedTransitionApi::class)
internal val LocalSharedScope = compositionLocalOf<SharedTransitionScope?> { null }

internal val LocalNavScope = compositionLocalOf<AnimatedVisibilityScope?> { null }

/** The element of key shared between the screens a navigation goes between; as it was where there is none. */
@OptIn(ExperimentalSharedTransitionApi::class)
@Composable
internal fun Modifier.shared(key: String): Modifier {
    val shared = LocalSharedScope.current ?: return this
    val nav = LocalNavScope.current ?: return this
    return with(shared) {
        this@shared.sharedElement(
            rememberSharedContentState(key),
            nav,
            boundsTransform = MakoMotion.SharedBoundsTransform,
        )
    }
}

/** Like [shared], for text whose size or wrap differs at either end: its bounds move, its content fades. */
@OptIn(ExperimentalSharedTransitionApi::class)
@Composable
internal fun Modifier.sharedBounds(key: String): Modifier {
    val shared = LocalSharedScope.current ?: return this
    val nav = LocalNavScope.current ?: return this
    return with(shared) {
        this@sharedBounds.sharedBounds(
            rememberSharedContentState(key),
            nav,
            boundsTransform = MakoMotion.SharedBoundsTransform,
            resizeMode = SharedTransitionScope.ResizeMode.scaleToBounds(),
        )
    }
}
