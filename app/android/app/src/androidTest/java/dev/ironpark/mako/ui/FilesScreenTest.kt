package dev.ironpark.mako.ui

import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.ByteString
import dev.ironpark.mako.R
import dev.ironpark.mako.net.ProjectFiles
import dev.ironpark.mako.v2.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test

internal class FakeProjectFiles : ProjectFiles {
    var diffRequest: Triple<String, String, String>? = null
    var failList = false
    val opened = mutableListOf<String>()
    override suspend fun list(path: String): Dir {
        if (failList) error("offline")
        val entries = if (path.endsWith("src")) listOf(DirEntry.newBuilder().setName("main.go").setSize(500).build()) else listOf(
            DirEntry.newBuilder().setName("src").setDir(true).build(),
            DirEntry.newBuilder().setName("README.md").setSize(20).build(),
            DirEntry.newBuilder().setName(".env").setSize(5).build(),
            DirEntry.newBuilder().setName("data.bin").setSize(100).build(),
        )
        return Dir.newBuilder().setPath(if (path == "~") "/work/app" else path).addAllEntries(entries).build()
    }
    override suspend fun read(path: String, image: Boolean): File {
        opened += path
        val text = if (path.endsWith(".md")) "# Project\n\nReadable documentation" else (1..50).joinToString("\n") { if (it == 25) "target match" else "source line $it" }
        return File.newBuilder().setPath(path).setSize(500).setBinary(path.endsWith(".bin"))
            .setData(if (path.endsWith(".bin")) ByteString.EMPTY else ByteString.copyFromUtf8(text)).build()
    }
    override suspend fun search(root: String, query: String, names: Boolean): SearchResult =
        if (names) SearchResult.newBuilder().addFiles("src/main.go").addFiles("README.md").build()
        else SearchResult.newBuilder().addMatches(SearchMatch.newBuilder().setPath("src/main.go").setLine(25).setText("target match")).build()
    override suspend fun changes(root: String): GitChanges = GitChanges.newBuilder().setRoot("/work").setBranch("main")
        .addFiles(GitFile.newBuilder().setPath("app/src/main.go").setFrom("app/src/old.go").setStatus("renamed").setAdded(2).setRemoved(1)).build()
    override suspend fun diff(root: String, path: String, from: String): Diff {
        diffRequest = Triple(root, path, from)
        return Diff.newBuilder().setPath(path)
            .addLines(DiffLine.newBuilder().setOp("@").setText("@@ -1 +1 @@"))
            .addLines(DiffLine.newBuilder().setOp("-").setOld(1).setText("old line"))
            .addLines(DiffLine.newBuilder().setOp("+").setNew(1).setText("new line")).build()
    }
}

class FilesScreenTest {
    @get:Rule val compose = createComposeRule()
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private fun label(id: Int) = context.getString(id)
    private fun awaitText(text: String) = compose.waitUntil(5_000) { compose.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }
    private fun show(files: ProjectFiles, onBack: () -> Unit = {}) {
        compose.setContent { MaterialTheme { FilesScreen(files, "/work/app", "Laptop", onBack) } }
    }

    @Test fun browseHiddenFilesAndReturnFromAFileToItsFolder() {
        val files = FakeProjectFiles()
        show(files)
        awaitText("README.md")
        compose.onNodeWithText(".env").assertDoesNotExist()
        compose.onNodeWithContentDescription(label(R.string.file_options)).performClick()
        compose.onNodeWithText(label(R.string.show_hidden_files)).performClick()
        compose.onNodeWithText(".env").assertIsDisplayed()
        compose.onNodeWithText("src").performClick()
        awaitText("main.go")
        compose.onNodeWithText("main.go").performClick()
        awaitText("source line 1")
        compose.runOnIdle { assertEquals("/work/app/src/main.go", files.opened.last()) }
        compose.onNodeWithContentDescription(label(R.string.back)).performClick()
        compose.onNodeWithText("main.go").assertIsDisplayed()
        compose.onNodeWithContentDescription(label(R.string.back)).performClick()
        awaitText("README.md")
        compose.onNodeWithText("data.bin").performClick()
        awaitText(label(R.string.binary_file_body))
        compose.onNodeWithText(label(R.string.binary_file_body)).assertIsDisplayed()
    }

    @Test fun contentSearchOpensTheCorrectFileAndLine() {
        val files = FakeProjectFiles()
        show(files)
        awaitText("README.md")
        compose.onNodeWithContentDescription(label(R.string.search_files)).performClick()
        compose.onNodeWithText(label(R.string.search_contents)).performClick()
        compose.onNode(hasSetTextAction()).performTextInput("target")
        awaitText("src/main.go:25")
        compose.onNodeWithText("src/main.go:25").performClick()
        awaitText("target match")
        compose.onNodeWithText("target match").assertIsDisplayed()
        compose.runOnIdle { assertEquals("/work/app/src/main.go", files.opened.last()) }
    }

    @Test fun changesUseTheRepositoryRootAndRenameSourceForDiffs() {
        val files = FakeProjectFiles()
        show(files)
        awaitText("README.md")
        compose.onNodeWithText(label(R.string.git_changes)).performClick()
        awaitText("app/src/main.go")
        compose.onNodeWithText("app/src/main.go").performClick()
        awaitText("new line")
        compose.onNodeWithText("- old line").assertIsDisplayed()
        compose.onNodeWithText("+ new line").assertIsDisplayed()
        compose.runOnIdle { assertEquals(Triple("/work", "app/src/main.go", "app/src/old.go"), files.diffRequest) }
        compose.onNodeWithContentDescription(label(R.string.view_file)).performClick()
        awaitText("source line 1")
        compose.runOnIdle { assertEquals("/work/app/src/main.go", files.opened.last()) }
    }

    @Test fun aFailedDirectoryCanBeRetriedWithoutLeavingTheBrowser() {
        val files = FakeProjectFiles().apply { failList = true }
        show(files)
        awaitText("offline")
        compose.runOnIdle { files.failList = false }
        compose.onNodeWithText(label(R.string.retry)).performClick()
        awaitText("README.md")
        compose.onNodeWithText("README.md").performClick()
        awaitText("Readable documentation")
        compose.onNodeWithText("Readable documentation").assertIsDisplayed()
    }
}
