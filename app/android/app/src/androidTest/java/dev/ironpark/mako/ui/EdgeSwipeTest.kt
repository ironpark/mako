package dev.ironpark.mako.ui

import androidx.compose.foundation.gestures.Orientation
import androidx.compose.foundation.gestures.draggable
import androidx.compose.foundation.gestures.rememberDraggableState
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipe
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

class EdgeSwipeTest {
    @get:Rule val compose = createComposeRule()
    private var dragged = 0f
    private var pulls = mutableListOf<Float>()
    private var releases = mutableListOf<Boolean>()

    private fun content() = compose.setContent {
        Box(Modifier.fillMaxSize().testTag("screen").edgeSwipes({ pulls += it }, { releases += it })) {
            // A row that swipes sideways, as a session's does.
            Box(Modifier.fillMaxSize().draggable(rememberDraggableState { dragged += it }, Orientation.Horizontal))
        }
    }

    @Test fun aSwipeFromTheMiddleIsTheRowsAndOneFromAnEdgeIsNot() {
        content()
        compose.onNodeWithTag("screen").performTouchInput { swipe(center, center - Offset(width * 0.3f, 0f)) }
        compose.runOnIdle {
            assertTrue("the row did not swipe", dragged < 0f)
            assertEquals(emptyList<Float>(), pulls)
        }
        dragged = 0f
        compose.onNodeWithTag("screen").performTouchInput { swipe(Offset(2f, centerY), Offset(width * 0.4f, centerY)) }
        compose.runOnIdle {
            assertEquals(0f, dragged)
            assertEquals(emptyList<Boolean>(), releases)
        }
    }

    @Test fun aLongPullFromTheRightEdgeOpensAndAShortOneDoesNot() {
        content()
        compose.onNodeWithTag("screen").performTouchInput { swipe(Offset(width - 2f, centerY), Offset(width * 0.2f, centerY), 400) }
        compose.runOnIdle {
            assertEquals(0f, dragged)
            assertEquals(listOf(true), releases)
            assertTrue(pulls.last() >= 1f)
        }
        compose.onNodeWithTag("screen").performTouchInput { swipe(Offset(width - 2f, centerY), Offset(width - 60f, centerY), 300) }
        compose.runOnIdle { assertEquals(listOf(true, false), releases) }
    }
}
