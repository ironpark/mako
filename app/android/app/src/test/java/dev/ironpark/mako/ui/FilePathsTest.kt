package dev.ironpark.mako.ui

import org.junit.Assert.*
import org.junit.Test

class FilePathsTest {
    @Test fun pathsUseTheRemoteComputersSeparators() {
        assertEquals("/work/app/src/main.go", remoteJoin("/work/app/", "src/main.go"))
        assertEquals("/file", remoteJoin("/", "file"))
        assertEquals("C:\\work\\src\\main.go", remoteJoin("C:\\work", "src/main.go"))
        assertEquals("C:\\", remoteParent("C:\\work"))
        assertEquals("/", remoteParent("/work"))
        assertEquals("/", remoteParent("/"))
        assertEquals("C:\\", remoteParent("C:\\"))
    }

    @Test fun projectPrefixesMustEndAtAPathBoundary() {
        assertEquals("src/a.go", relativeRemote("/work/app/", "/work/app/src/a.go"))
        assertEquals("", relativeRemote("/work/app", "/work/app"))
        assertEquals("/work/apple/a.go", relativeRemote("/work/app", "/work/apple/a.go"))
        assertEquals("src/a.go", relativeRemote("C:\\work", "C:\\work\\src\\a.go"))
        assertEquals("file", relativeRemote("/", "/file"))
    }

    @Test fun fileSearchPrefersNamesAndHandlesCaseAndSpaces() {
        val paths = listOf("docs/old-main.txt", "src/main.go", "README.md", "Main Test.kt")
        assertEquals(listOf("src/main.go", "Main Test.kt", "docs/old-main.txt"), matchingFiles(paths, " MAIN "))
        assertEquals(listOf("Main Test.kt"), matchingFiles(paths, "main test"))
        assertTrue(imageFile("SCREENSHOT.PNG"))
        assertTrue(markdownFile("README.Markdown"))
        assertFalse(imageFile("icon.svg"))
    }
}
