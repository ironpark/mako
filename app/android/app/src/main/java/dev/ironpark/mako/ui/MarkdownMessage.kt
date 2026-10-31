package dev.ironpark.mako.ui

import android.widget.Toast
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.compose.elements.MarkdownTable
import com.mikepenz.markdown.compose.elements.MarkdownTableHeader
import com.mikepenz.markdown.compose.elements.MarkdownTableRow
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.m3.markdownTypography
import com.mikepenz.markdown.m3.elements.MarkdownCheckBox
import com.mikepenz.markdown.model.markdownAnimations
import com.mikepenz.markdown.model.markdownDimens
import com.mikepenz.markdown.model.markdownPadding
import com.mikepenz.markdown.model.rememberMarkdownState
import dev.ironpark.mako.R

/**
 * GFM rendered as selectable native text, with scrollable tables and
 * clickable links: onLink, when given, follows each, else the system does.
 */
@Composable
internal fun MarkdownMessage(text: String, modifier: Modifier = Modifier, onLink: ((String) -> Unit)? = null) {
    val state = rememberMarkdownState(text, retainState = true)
    val type = MaterialTheme.typography
    val context = LocalContext.current
    val uriHandler = LocalUriHandler.current
    val follow = rememberUpdatedState(onLink)
    val links = remember(uriHandler, context) {
        object : UriHandler {
            override fun openUri(uri: String) {
                runCatching { follow.value?.invoke(uri) ?: uriHandler.openUri(uri) }.onFailure {
                    Toast.makeText(context, R.string.link_open_failed, Toast.LENGTH_SHORT).show()
                }
            }
        }
    }
    CompositionLocalProvider(LocalUriHandler provides links) {
        Markdown(
            markdownState = state,
            modifier = modifier.fillMaxWidth(),
            typography = markdownTypography(
                h1 = type.titleLarge, h2 = type.titleMedium, h3 = type.titleSmall,
                h4 = type.titleSmall, h5 = type.titleSmall, h6 = type.titleSmall,
                text = type.bodyMedium, paragraph = type.bodyMedium,
                ordered = type.bodyMedium, bullet = type.bodyMedium, list = type.bodyMedium,
                code = type.bodySmall.copy(fontFamily = FontFamily.Monospace),
                inlineCode = type.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                table = type.bodySmall,
                textLink = TextLinkStyles(SpanStyle(color = MaterialTheme.colorScheme.primary, textDecoration = TextDecoration.Underline)),
            ),
            padding = markdownPadding(
                block = 4.dp, list = 2.dp, listItemTop = 2.dp, listItemBottom = 2.dp,
                listIndent = 12.dp, codeBlock = PaddingValues(10.dp),
                blockQuote = PaddingValues(horizontal = 8.dp),
            ),
            dimens = markdownDimens(tableCellWidth = 144.dp, tableCellPadding = 8.dp),
            animations = markdownAnimations(animateTextSize = { this }),
            components = markdownComponents(checkbox = { MarkdownCheckBox(it.content, it.node, it.typography.text) }, table = { model ->
                MarkdownTable(
                    model.content, model.node, model.typography.table,
                    headerBlock = { content, node, width, style ->
                        MarkdownTableHeader(content, node, width, style, maxLines = Int.MAX_VALUE)
                    },
                    rowBlock = { content, node, width, style ->
                        MarkdownTableRow(content, node, width, style, maxLines = Int.MAX_VALUE)
                    },
                )
            }),
            loading = { Box(it.heightIn(min = 24.dp)) },
            error = { Text(text, modifier = it, style = type.bodyMedium) },
        )
    }
}
