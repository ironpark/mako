package dev.ironpark.mako.ui

import java.net.URI
import java.net.URLDecoder

/**
 * What a link in a session's conversation opens. A path, absolute or
 * relative to the session's project, is a file on the session's node,
 * which the app reads from there; a web address on that node's loopback is
 * reached through a tunnel; any other address opens as it is.
 */
sealed interface ChatLink {
    /** An address elsewhere, opened as it is. */
    data class Web(val url: String) : ChatLink

    /** An address on the session node's loopback, at port there. */
    data class Loopback(val url: String, val port: Int) : ChatLink {
        val image: Boolean get() = imageFile(runCatching { URI(url).path }.getOrNull().orEmpty())

        /** The address as port here, which reaches the node's, serves it. */
        fun at(local: Int): String {
            val u = URI(url)
            val host = if (u.host == "localhost" || u.host == "127.0.0.1") u.host else "localhost"
            return URI(u.scheme, u.userInfo, host, local, u.path, u.query, u.fragment).toString()
        }
    }

    /** A file or folder on the session's node; line, when not 0, to open it at. */
    data class File(val path: String, val line: Long = 0, val folder: Boolean = false) : ChatLink
}

private val scheme = Regex("^[A-Za-z][A-Za-z0-9+.-]*:")
private val bareLoopback = Regex("""^(localhost|127(?:\.\d{1,3}){3}|0\.0\.0\.0|\[::1?])(:\d+)?([/?#].*)?$""", RegexOption.IGNORE_CASE)
private val lineSuffix = Regex(""":(\d+)(?::\d+)?$""")
private val lineFragment = Regex("""^L?(\d+)(?:[-:].*)?$""", RegexOption.IGNORE_CASE)
private val windowsDrive = Regex("""^[A-Za-z]:[\\/]""")

/** dest, a link's destination in a message of a session in project. */
internal fun chatLink(dest: String, project: String): ChatLink? {
    val d = dest.trim()
    if (d.isEmpty() || d.startsWith("#")) return null
    if (bareLoopback.matches(d)) return loopbackLink("http://$d")
    if (!windowsDrive.containsMatchIn(d) && scheme.containsMatchIn(d)) {
        if (d.startsWith("file:", ignoreCase = true)) {
            val uri = runCatching { URI(d) }.getOrNull() ?: return ChatLink.Web(d)
            return fileLink(uri.path ?: return ChatLink.Web(d), uri.fragment, project)
        }
        return loopbackLink(d) ?: pathWithLine(d, project) ?: ChatLink.Web(d)
    }
    val hash = d.indexOf('#')
    val fragment = if (hash >= 0) d.substring(hash + 1) else null
    var path = if (hash >= 0) d.substring(0, hash) else d
    path = path.substringBefore('?')
    path = runCatching { URLDecoder.decode(path.replace("+", "%2B"), "UTF-8") }.getOrDefault(path)
    return fileLink(path, fragment, project)
}

/** A path with a line, as `src/main.go:12`, which looks like a scheme. */
private fun pathWithLine(d: String, project: String): ChatLink? {
    if (!lineSuffix.containsMatchIn(d) || d.contains("://")) return null
    val head = d.substringBefore(':')
    if (head.contains('.') || head.contains('/')) return fileLink(d, null, project)
    return null
}

private fun loopbackLink(d: String): ChatLink.Loopback? {
    val u = runCatching { URI(d) }.getOrNull() ?: return null
    val scheme = u.scheme?.lowercase() ?: return null
    if (scheme !in setOf("http", "https", "ws", "wss")) return null
    val host = u.host ?: return null
    if (!loopbackHost(host)) return null
    val port = if (u.port > 0) u.port else if (scheme == "https" || scheme == "wss") 443 else 80
    return ChatLink.Loopback(d, port)
}

/** Whether host names the machine it is on, as a server there prints it. */
internal fun loopbackHost(host: String): Boolean {
    val h = host.lowercase().trimEnd('.').removePrefix("[").removeSuffix("]")
    if (h == "localhost" || h.endsWith(".localhost")) return true
    if (h == "::1" || h == "::" || h == "0.0.0.0") return true
    val parts = h.split('.')
    return parts.size == 4 && parts[0] == "127" && parts.all { p -> p.toIntOrNull()?.let { it in 0..255 } == true }
}

private fun fileLink(raw: String, fragment: String?, project: String): ChatLink.File? {
    var path = raw
    var line = fragment?.let { lineFragment.matchEntire(it)?.groupValues?.get(1)?.toLongOrNull() } ?: 0L
    lineSuffix.find(path)?.let { m ->
        if (line == 0L) line = m.groupValues[1].toLongOrNull() ?: 0L
        path = path.substring(0, m.range.first)
    }
    if (path.isEmpty()) return null
    val folder = path.endsWith('/') || path.endsWith('\\')
    return ChatLink.File(resolveRemote(project, path), line, folder)
}

/**
 * rel from dir on a node, cleaned, or rel itself when it is absolute
 * there; `~` stays for the node to expand.
 */
internal fun resolveRemote(dir: String, rel: String): String {
    val windows = windowsDrive.containsMatchIn(rel) || (windowsDrive.containsMatchIn(dir) && '/' !in dir)
    val absolute = rel.startsWith("/") || rel.startsWith("~") || windowsDrive.containsMatchIn(rel) || rel.startsWith("\\\\")
    val joined = if (absolute || dir.isEmpty()) rel else remoteJoin(dir, rel)
    return cleanRemote(joined, if (windows) '\\' else '/')
}

/** path with its `.` and `..` resolved, and no trailing separator. */
private fun cleanRemote(path: String, separator: Char): String {
    val lead = when {
        path.startsWith("\\\\") -> "\\\\"
        path.startsWith("/") -> "/"
        else -> ""
    }
    val out = ArrayList<String>()
    for (part in path.removePrefix(lead).split('/', '\\')) {
        when {
            part.isEmpty() || part == "." -> {}
            part == ".." && out.isNotEmpty() && out.last() != ".." && !(out.size == 1 && out[0].endsWith(':')) -> out.removeAt(out.lastIndex)
            part == ".." && lead.isNotEmpty() -> {}
            else -> out += part
        }
    }
    val body = out.joinToString(separator.toString())
    return (lead + body).ifEmpty { lead.ifEmpty { "." } }
}

/**
 * The paths of the images text names, as words of their own, each once:
 * those relative to project resolved against it.
 */
internal fun imageRefs(text: String, project: String): List<String> = text.split(Regex("\\s+"))
    .map { it.trim('"', '\'', '`', '(', ')', '[', ']', '<', '>', ',', ';') }
    .filter { it.isNotEmpty() && imageFile(it) && !it.contains("://") }
    .map { resolveRemote(project, it) }
    .distinct()

/** text with word, the first time it is a word of its own, taken out. */
internal fun withoutWord(text: String, word: String): String {
    val words = text.split(Regex("\\s+")).filter { it.isNotEmpty() }.toMutableList()
    val i = words.indexOfFirst { it == word }
    if (i < 0) return text
    words.removeAt(i)
    return if (words.isEmpty()) "" else words.joinToString(" ") + " "
}

/** text with path added as a word of its own, and room to write on after it. */
internal fun withPath(text: String, path: String): String =
    (if (text.isEmpty() || text.last().isWhitespace()) text else "$text ") + path + " "

/** A file's name as a staged file keeps it: a word of its own, without separators. */
internal fun stagedName(name: String): String =
    name.trim().replace(Regex("[\\s/\\\\]+"), "_").trim('_').ifEmpty { "attachment" }

/** A word of a draft that hands the agent a file: one staged on its node, or an image it names. */
internal data class Attachment(val word: String, val path: String, val image: Boolean)

/** The words of a draft that hand the agent files, each once, in order. */
internal fun attachments(text: String, project: String): List<Attachment> = text.split(Regex("\\s+"))
    .filter { it.isNotEmpty() && !it.contains("://") }
    .mapNotNull { word ->
        val bare = word.trim('"', '\'', '`', '(', ')', '[', ']', '<', '>', ',', ';')
        val staged = bare.contains("/mako-drops/") || bare.contains("\\mako-drops\\")
        if (!staged && !imageFile(bare)) null else Attachment(word, resolveRemote(project, bare), imageFile(bare))
    }
    .distinctBy { it.path }
