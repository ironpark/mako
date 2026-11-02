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
import androidx.compose.foundation.relocation.BringIntoViewRequester
import androidx.compose.foundation.relocation.bringIntoViewRequester
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.text.TextStyle
import com.mikepenz.markdown.compose.components.MarkdownComponentModel
import com.mikepenz.markdown.compose.elements.MarkdownHeader
import org.intellij.markdown.ast.ASTNode
import com.mikepenz.markdown.utils.getUnescapedTextInNode
import kotlinx.coroutines.launch
import org.intellij.markdown.IElementType
import org.intellij.markdown.MarkdownTokenTypes
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
 * A link to a heading of the message itself, as `#usage`, scrolls to it,
 * after onAnchor, which can keep whatever scrolls it from scrolling back.
 */
@Composable
internal fun MarkdownMessage(
    text: String, modifier: Modifier = Modifier, onLink: ((String) -> Unit)? = null, onAnchor: () -> Unit = {},
) {
    val state = rememberMarkdownState(text, retainState = true)
    val type = MaterialTheme.typography
    val context = LocalContext.current
    val uriHandler = LocalUriHandler.current
    val follow = rememberUpdatedState(onLink)
    val anchored = rememberUpdatedState(onAnchor)
    val scope = rememberCoroutineScope()
    // The message's headings by where they start in it, to go to by their slugs.
    val headings = remember { mutableStateMapOf<Int, Pair<String, BringIntoViewRequester>>() }
    val links = remember(uriHandler, context) {
        object : UriHandler {
            override fun openUri(uri: String) {
                if (uri.startsWith("#")) {
                    val ordered = headings.entries.sortedBy { it.key }.map { it.value }
                    val i = anchorIndex(ordered.map { it.first }, uri.drop(1))
                    if (i >= 0) {
                        anchored.value()
                        scope.launch { ordered[i].second.bringIntoView() }
                        return
                    }
                }
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
            components = markdownComponents(
                heading1 = { Heading(it, it.typography.h1, MarkdownTokenTypes.ATX_CONTENT, headings) },
                heading2 = { Heading(it, it.typography.h2, MarkdownTokenTypes.ATX_CONTENT, headings) },
                heading3 = { Heading(it, it.typography.h3, MarkdownTokenTypes.ATX_CONTENT, headings) },
                heading4 = { Heading(it, it.typography.h4, MarkdownTokenTypes.ATX_CONTENT, headings) },
                heading5 = { Heading(it, it.typography.h5, MarkdownTokenTypes.ATX_CONTENT, headings) },
                heading6 = { Heading(it, it.typography.h6, MarkdownTokenTypes.ATX_CONTENT, headings) },
                setextHeading1 = { Heading(it, it.typography.h1, MarkdownTokenTypes.SETEXT_CONTENT, headings) },
                setextHeading2 = { Heading(it, it.typography.h2, MarkdownTokenTypes.SETEXT_CONTENT, headings) },
                checkbox = { MarkdownCheckBox(it.content, it.node, it.typography.text) }, table = { model ->
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

/** Heading is a heading of a message, known to it by its text, to scroll to. */
@Composable
private fun Heading(
    model: MarkdownComponentModel, style: TextStyle, content: IElementType,
    headings: MutableMap<Int, Pair<String, BringIntoViewRequester>>,
) {
    val requester = remember { BringIntoViewRequester() }
    val start = model.node.startOffset
    val title = model.node.childOfType(content)?.getUnescapedTextInNode(model.content)
        ?: model.node.getUnescapedTextInNode(model.content)
    DisposableEffect(start, title) {
        headings[start] = title to requester
        onDispose { if (headings[start]?.second === requester) headings.remove(start) }
    }
    Box(Modifier.bringIntoViewRequester(requester)) { MarkdownHeader(model.content, model.node, style, content) }
}

/** The first node under this, at any depth, of type. */
private fun ASTNode.childOfType(type: IElementType): ASTNode? =
    children.firstOrNull { it.type == type } ?: children.firstNotNullOfOrNull { it.childOfType(type) }

private val inlineLink = Regex("""!?\[([^\]]*)]\([^)]*\)""")
private val notSlug = Regex("""[^\p{L}\p{N}\s_-]""")

/** The slug GitHub gives a heading: lower case, without punctuation, spaces as hyphens. */
internal fun headingSlug(title: String): String =
    title.trim().replace(inlineLink, "$1").lowercase().replace(notSlug, "").replace(Regex("\\s"), "-")

/**
 * Which of titles, the headings of a message in order, anchor goes to: by
 * GitHub's slugs, a repeated one numbered from its second; -1 for none.
 */
internal fun anchorIndex(titles: List<String>, anchor: String): Int {
    val want = runCatching { java.net.URLDecoder.decode(anchor.replace("+", "%2B"), "UTF-8") }.getOrDefault(anchor).lowercase()
    val seen = HashMap<String, Int>()
    titles.forEachIndexed { i, t ->
        val base = headingSlug(t)
        val n = seen[base] ?: 0
        seen[base] = n + 1
        if ((if (n == 0) base else "$base-$n") == want) return i
    }
    return -1
}
