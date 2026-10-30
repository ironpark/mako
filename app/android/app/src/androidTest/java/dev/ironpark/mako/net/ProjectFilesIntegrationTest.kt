package dev.ironpark.mako.net

import androidx.test.platform.app.InstrumentationRegistry
import dev.ironpark.mako.core.mobile.Mobile
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import java.io.File

class ProjectFilesIntegrationTest {
    @Test fun nativeDaemonListsReadsAndSearchesActualProjectFiles() = runBlocking {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val directory = File(context.cacheDir, "native-${System.nanoTime().toString(36)}").apply { mkdirs() }
        val project = File(directory, "project").apply { mkdirs() }
        File(project, "hello.txt").writeText("first line\n찾을 단어\nlast line\n")
        File(project, "binary.bin").writeBytes(byteArrayOf(1, 0, 2))
        try {
            Mobile.start(File(directory, "node").path, directory.path, "File integration test")
            val api = NodeProjectFiles(Daemon { Mobile.socket() }, "")
            assertEquals(setOf("binary.bin", "hello.txt"), api.list(project.path).entriesList.map { it.name }.toSet())
            val read = api.read(File(project, "hello.txt").path)
            assertTrue(read.data.toStringUtf8().contains("찾을 단어"))
            assertFalse(read.truncated)
            assertTrue(api.read(File(project, "binary.bin").path).binary)
            assertTrue(api.search(project.path, "", true).filesList.contains("hello.txt"))
            val match = api.search(project.path, "찾을 단어", false).matchesList.single()
            assertEquals("hello.txt", match.path)
            assertEquals(2L, match.line)
        } finally {
            Mobile.stop()
            directory.deleteRecursively()
        }
    }
}
