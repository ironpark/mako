package dev.ironpark.mako.ui

import android.content.Context
import android.content.SharedPreferences
import androidx.compose.runtime.Composable
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.edit

/**
 * Drafts keeps what was typed to each session and not sent, past leaving
 * its screen and the app's end, until it is sent or cleared; the list
 * reads them as they change.
 */
internal class Drafts(private val prefs: SharedPreferences) {
    private val texts = mutableStateMapOf<String, String>().apply {
        for ((k, v) in prefs.all) if (v is String) put(k, v)
    }

    operator fun get(node: String, id: String): String = texts[key(node, id)].orEmpty()

    operator fun set(node: String, id: String, text: String) {
        val k = key(node, id)
        if (texts[k].orEmpty() == text) return
        if (text.isBlank()) texts.remove(k) else texts[k] = text
        prefs.edit { if (text.isBlank()) remove(k) else putString(k, text) }
    }

    private fun key(node: String, id: String) = "$node/$id"

    companion object {
        @Volatile private var shared: Drafts? = null

        fun of(context: Context): Drafts = shared ?: synchronized(this) {
            shared ?: Drafts(context.applicationContext.getSharedPreferences("drafts", Context.MODE_PRIVATE)).also { shared = it }
        }
    }
}

@Composable
internal fun rememberDrafts(): Drafts {
    val context = LocalContext.current
    return remember { Drafts.of(context) }
}
