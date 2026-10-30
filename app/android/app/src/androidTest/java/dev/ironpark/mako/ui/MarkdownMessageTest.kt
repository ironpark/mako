package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

class MarkdownMessageTest {
    @get:Rule val compose = createComposeRule()

    private fun awaitText(text: String) {
        compose.waitUntil(5_000) { compose.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun tablesWrapLongCellsAndLinksOpenFromInsideCells() {
        val longCell = "여러 줄로 표시되어야 하는 긴 셀 내용입니다. 마지막 문장까지 잘리지 않고 모두 읽을 수 있어야 합니다."
        val opened = mutableListOf<String>()
        compose.setContent {
            MaterialTheme {
                CompositionLocalProvider(LocalUriHandler provides object : UriHandler {
                    override fun openUri(uri: String) { opened += uri }
                }) {
                    SelectionContainer {
                        Column(Modifier.width(280.dp).verticalScroll(rememberScrollState())) {
                            MarkdownMessage("""
                                | 이름 | 설명 | 상태 |
                                | --- | --- | --- |
                                | [문서](https://example.com/docs) | $longCell | 완료 |
                            """.trimIndent())
                        }
                    }
                }
            }
        }
        awaitText("문서")
        val table = compose.onNode(SemanticsMatcher.keyIsDefined(SemanticsProperties.CollectionInfo))
        val info = table.fetchSemanticsNode().config[SemanticsProperties.CollectionInfo]
        assertEquals(3, info.columnCount)
        assertEquals(2, info.rowCount)
        compose.onNodeWithText("문서", substring = true).performClick()
        compose.runOnIdle { assertEquals(listOf("https://example.com/docs"), opened) }
        val cell = compose.onNodeWithText(longCell, substring = true).performScrollTo()
        cell.assertIsDisplayed()
        val layouts = mutableListOf<TextLayoutResult>()
        cell.performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertTrue(layouts.single().lineCount > 1)
        assertFalse(layouts.single().didOverflowHeight)
        assertFalse(layouts.single().isLineEllipsized(layouts.single().lineCount - 1))
        compose.onNodeWithText("완료", substring = true).performScrollTo().assertIsDisplayed()
    }

    @Test fun headingsListsQuotesCodeAndReferenceLinksRenderAsText() {
        val opened = mutableListOf<String>()
        compose.setContent {
            MaterialTheme {
                CompositionLocalProvider(LocalUriHandler provides object : UriHandler {
                    override fun openUri(uri: String) { opened += uri }
                }) {
                    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
                        MarkdownMessage("""
                            ## 제목

                            **굵게**와 *기울임*, ~~취소~~, `코드`

                            - 첫 항목
                              - 하위 항목

                            > 인용문

                            ```kotlin
                            val result = 42
                            ```

                            [참조 문서][docs]

                            [docs]: https://example.com/reference
                        """.trimIndent())
                    }
                }
            }
        }
        awaitText("참조 문서")
        for (text in listOf("제목", "굵게와 기울임, 취소,", "코드", "첫 항목", "하위 항목", "인용문", "val result = 42")) {
            compose.onNodeWithText(text, substring = true).performScrollTo().assertIsDisplayed()
        }
        compose.onNodeWithText("참조 문서").performScrollTo().performClick()
        compose.runOnIdle { assertEquals(listOf("https://example.com/reference"), opened) }
    }

    @Test fun incompleteMarkdownCanUpdateIntoATableWithoutLosingTheMessage() {
        val text = mutableStateOf("답변 중 **강조")
        compose.setContent { MaterialTheme { MarkdownMessage(text.value) } }
        awaitText("답변 중")
        compose.runOnIdle {
            text.value = "답변 중 **강조**\n\n| 열 | 결과 |\n| --- | --- |\n| 항목 | 성공 |"
        }
        awaitText("성공")
        compose.onNodeWithText("답변 중 강조").assertIsDisplayed()
        compose.onNodeWithText("항목", substring = true).assertIsDisplayed()
        compose.onNodeWithText("성공", substring = true).assertIsDisplayed()
    }
}
