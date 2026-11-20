package dev.ironpark.mako.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class ApprovalViewsTest {
    @Test
    fun editKeepsTheLinesBothShare() {
        val got = editLines("a\nb\nc\nd", "a\nB\nc\nd")
        assertEquals(
            listOf(DiffLine(' ', "a"), DiffLine('-', "b"), DiffLine('+', "B"), DiffLine(' ', "c"), DiffLine(' ', "d")),
            got,
        )
    }

    @Test
    fun aNewFileIsAllPutIn() {
        assertEquals(listOf(DiffLine('+', "x"), DiffLine('+', "y")), editLines("", "x\ny"))
    }

    @Test
    fun anEditOfRepeatedLinesTakesNoLineTwice() {
        // The kept head and tail must not overlap where old is new's prefix.
        assertEquals(listOf(DiffLine(' ', "a"), DiffLine('+', "a")), editLines("a", "a\na"))
    }

    @Test
    fun patchLinesAreMarkedByTheirFirstCharacter() {
        assertEquals(
            listOf(DiffLine(' ', "--- a/x"), DiffLine(' ', "@@"), DiffLine('-', "old"), DiffLine('+', "new")),
            patchLines("--- a/x\n@@\n-old\n+new\n"),
        )
    }
}
