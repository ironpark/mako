package dev.ironpark.mako.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.pager.PagerState
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.pinch
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.test.swipeRight
import androidx.compose.ui.test.swipeUp
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.term.TermSession
import dev.ironpark.mako.term.TerminalPane
import dev.ironpark.mako.R
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.launch
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

class SessionPagerTest {
    @get:Rule val compose = createComposeRule()
    private lateinit var pager: PagerState
    private var returnsHome = 0
    private lateinit var switchDescription: String

    private fun content(initial: Int = 1, chat: Boolean = true) {
        compose.setContent {
            MaterialTheme {
                pager = rememberPagerState(initialPage = initial) { if (chat) 3 else 2 }
                val scope = rememberCoroutineScope()
                switchDescription = stringResource(R.string.switch_session_view)
                val term = remember { TermSession(Daemon { null }, "test", "test", scope) }
                SessionPager(pager, onHome = { returnsHome++ }, modifier = Modifier.testTag("pager")) { page ->
                    Box(Modifier.fillMaxSize().background(if (page == 0) Color.Green else Color.White).testTag("page$page")) {
                        Column {
                            if (chat && page > 0) SessionViewSwitch(terminal = page == 2, onTerminal = { terminal ->
                                scope.launch { pager.animateScrollToPage(if (terminal) 2 else 1) }
                            })
                            if (page == if (chat) 2 else 1) {
                                TerminalPane(term, Modifier.fillMaxSize().testTag("terminal"))
                            }
                        }
                    }
                }
            }
        }
    }

    @Test fun toolbarSwitchAndSwipesKeepTheSameModeSelected() {
        content()
        val chatSwitch = compose.onNode(hasContentDescription(switchDescription) and hasAnyAncestor(hasTestTag("page1")))
        chatSwitch.assertIsOff().performClick()
        compose.runOnIdle { assertEquals(2, pager.settledPage) }
        compose.onNode(hasContentDescription(switchDescription) and hasAnyAncestor(hasTestTag("page2")))
            .assertIsOn().performClick()
        compose.runOnIdle { assertEquals(1, pager.settledPage) }
        compose.onNodeWithTag("page1").performTouchInput { swipeLeft() }
        compose.onNode(hasContentDescription(switchDescription) and hasAnyAncestor(hasTestTag("page2"))).assertIsOn()
        compose.onNodeWithTag("terminal").performTouchInput { swipeRight() }
        chatSwitch.assertIsOff()
    }

    @Test fun adjacentTerminalIsVisibleWhileFingerIsStillDown() {
        content()
        compose.onNodeWithTag("pager").performTouchInput {
            down(center)
            moveTo(center - Offset(width * 0.3f, 0f), delayMillis = 300)
        }
        compose.onNodeWithTag("page1").assertIsDisplayed()
        compose.onNodeWithTag("page2").assertIsDisplayed()
        compose.runOnIdle {
            assertTrue(pager.isScrollInProgress)
            assertTrue(pager.currentPageOffsetFraction != 0f)
            assertEquals(0, returnsHome)
        }
        // Reversing before release must leave Chat selected.
        compose.onNodeWithTag("pager").performTouchInput {
            moveTo(center, delayMillis = 300)
            up()
        }
        compose.runOnIdle { assertEquals(1, pager.settledPage) }
    }

    @Test fun homeIsRevealedDuringDragAndNavigationWaitsForRelease() {
        content()
        compose.onNodeWithTag("pager").performTouchInput {
            down(Offset(width * 0.1f, centerY))
            moveTo(Offset(width * 0.6f, centerY), delayMillis = 300)
        }
        compose.onNodeWithTag("page0").assertIsDisplayed()
        compose.onNodeWithTag("page1").assertIsDisplayed()
        compose.runOnIdle { assertEquals(0, returnsHome) }
        compose.onNodeWithTag("pager").performTouchInput {
            moveTo(Offset(width * 0.9f, centerY), delayMillis = 300)
            up()
        }
        compose.runOnIdle {
            assertEquals(0, pager.settledPage)
            assertEquals(1, returnsHome)
        }
    }

    @Test fun terminalAllowsHorizontalPagingButKeepsVerticalScroll() {
        content(initial = 2)
        compose.onNodeWithTag("terminal").performTouchInput { swipeUp() }
        compose.runOnIdle { assertEquals(2, pager.settledPage) }
        compose.onNodeWithTag("terminal").performTouchInput { swipeRight() }
        compose.runOnIdle {
            assertEquals(1, pager.settledPage)
            assertEquals(0, returnsHome)
        }
        compose.onNodeWithTag("page1").performTouchInput { swipeLeft() }
        compose.runOnIdle { assertEquals(2, pager.settledPage) }
        compose.onNodeWithTag("terminal").performTouchInput { swipeRight() }
        compose.onNodeWithTag("page1").performTouchInput { swipeRight() }
        compose.runOnIdle { assertEquals(1, returnsHome) }
    }

    @Test fun terminalOnlySessionsCanAlsoSwipeBackToHome() {
        content(chat = false)
        compose.onNodeWithTag("terminal").performTouchInput { swipeRight() }
        compose.runOnIdle {
            assertEquals(0, pager.settledPage)
            assertEquals(1, returnsHome)
        }
    }

    @Test fun terminalPinchDoesNotTurnIntoPageNavigation() {
        content(initial = 2)
        compose.onNodeWithTag("terminal").performTouchInput {
            pinch(
                center - Offset(width * 0.15f, 0f), center + Offset(width * 0.15f, 0f),
                center - Offset(width * 0.35f, 0f), center + Offset(width * 0.35f, 0f),
            )
        }
        compose.runOnIdle {
            assertEquals(2, pager.settledPage)
            assertEquals(0, returnsHome)
        }
        // Zooming must not take over the next single-finger horizontal swipe.
        compose.onNodeWithTag("terminal").performTouchInput { swipeRight() }
        compose.runOnIdle { assertEquals(1, pager.settledPage) }
    }
}
