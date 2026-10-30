package dev.ironpark.mako.ui

import android.content.Context
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.test.platform.app.InstrumentationRegistry
import dev.ironpark.mako.NotificationPreferences
import dev.ironpark.mako.R
import dev.ironpark.mako.net.AndroidRelease
import dev.ironpark.mako.net.AppUpdates
import dev.ironpark.mako.net.latestAndroidRelease
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import java.io.File
import java.io.IOException

class SettingsScreenTest {
    @get:Rule val compose = createComposeRule()
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private fun label(id: Int) = context.getString(id)
    private fun awaitText(text: String) = compose.waitUntil(5_000) { compose.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }

    @Test fun notificationChoicesPersistAndMasterSwitchDisablesIndividualControls() {
        val name = "notification-test-${System.nanoTime()}"
        val storage = context.getSharedPreferences(name, Context.MODE_PRIVATE)
        val preferences = NotificationPreferences(storage)
        try {
            compose.setContent { MaterialTheme { NotificationSettingsScreen(preferences) {} } }
            compose.onNodeWithText(label(R.string.notify_done)).assertIsOn().performClick().assertIsOff()
            compose.onNodeWithText(label(R.string.notify_foreground)).performScrollTo().performClick().assertIsOn()
            compose.runOnIdle {
                val restored = NotificationPreferences(storage).options.value
                assertFalse(restored.done)
                assertTrue(restored.foreground)
                assertTrue(restored.approvals)
            }
            compose.onNodeWithText(label(R.string.notifications_enabled)).performScrollTo().performClick().assertIsOff()
            compose.onNodeWithText(label(R.string.notify_approvals)).assertIsNotEnabled()
            compose.runOnIdle { assertFalse(NotificationPreferences(storage).options.value.enabled) }
        } finally { context.deleteSharedPreferences(name) }
    }

    @Test fun updatesShowDownloadFailuresAndOnlyOfferInstallAfterSuccess() {
        val apk = File(context.cacheDir, "updates/test-update.apk").apply { parentFile?.mkdirs() }
        var attempts = 0
        val updates = object : AppUpdates {
            override suspend fun latest() = AndroidRelease("99.0.0", "https://github.com/ironpark/mako/releases/tag/android-v99.0.0", "Release notes", "mako.apk", "", 1024, "", "")
            override suspend fun download(release: AndroidRelease, progress: (Float) -> Unit): File {
                attempts++
                progress(0.5f)
                if (attempts == 1) throw IOException("checksum mismatch")
                apk.writeText("test")
                progress(1f)
                return apk
            }
        }
        try {
            compose.setContent { MaterialTheme { UpdatesScreen(updates) {} } }
            awaitText(label(R.string.download_update))
            compose.onNodeWithText(label(R.string.install_update)).assertDoesNotExist()
            compose.onNodeWithText(label(R.string.download_update)).performClick()
            awaitText("checksum mismatch")
            compose.onNodeWithText(label(R.string.install_update)).assertDoesNotExist()
            compose.onNodeWithText(label(R.string.download_update)).performClick()
            awaitText(label(R.string.install_update))
            compose.onNodeWithText(label(R.string.install_update)).assertIsDisplayed()
            compose.runOnIdle { assertEquals(2, attempts) }
        } finally { apk.delete() }
    }

    @Test fun stableAndroidReleaseSelectionIgnoresDesktopDraftAndIncompleteAssets() {
        fun release(tag: String, pre: Boolean = false, draft: Boolean = false, asset: Boolean = true, url: String = "https://github.com/ironpark/mako/releases/download/$tag/mako-$tag.apk"): JSONObject = JSONObject()
            .put("tag_name", tag).put("draft", draft).put("prerelease", pre)
            .put("html_url", "https://github.com/ironpark/mako/releases/tag/$tag")
            .put("assets", JSONArray().apply { if (asset) put(JSONObject().put("name", "mako-$tag.apk").put("size", 100)
                .put("browser_download_url", url).put("digest", "sha256:" + "a".repeat(64))) })
        val input = JSONArray().put(release("v50.0.0")).put(release("android-v0.9.9"))
            .put(release("android-v0.10.0")).put(release("android-v9.0.0", pre = true))
            .put(release("android-v8.0.0", draft = true)).put(release("android-v7.0.0", asset = false))
            .put(release("android-v6.0.0", url = "https://example.com/app.apk"))
        assertEquals("0.10.0", latestAndroidRelease(input.toString())?.version)
        assertNull(latestAndroidRelease(JSONArray().put(release("android-v1.0.0-rc1")).toString()))
    }
}
