package dev.ironpark.mako.ui

internal fun remoteJoin(root: String, name: String): String {
    val separator = if ('\\' in root && '/' !in root) '\\' else '/'
    return root.trimEnd('/', '\\') + separator + name.trimStart('/', '\\').replace('/', separator)
}

internal fun remoteParent(path: String): String {
    val clean = path.trimEnd('/', '\\')
    val slash = clean.indexOfLast { it == '/' || it == '\\' }
    return when {
        slash == 0 -> clean.take(1)
        slash == 2 && clean.getOrNull(1) == ':' -> clean.take(3)
        slash > 0 -> clean.take(slash)
        else -> path
    }
}

internal fun relativeRemote(root: String, path: String): String {
    val base = root.replace('\\', '/').trimEnd('/')
    val full = path.replace('\\', '/')
    return when {
        full.trimEnd('/') == base -> ""
        full.startsWith("$base/") -> full.removePrefix("$base/")
        else -> full
    }
}

internal fun matchingFiles(paths: List<String>, query: String): List<String> = paths
    .filter { it.contains(query.trim(), ignoreCase = true) }
    .sortedWith(compareBy<String> { !baseName(it).startsWith(query.trim(), ignoreCase = true) }.thenBy { it.length }.thenBy { it })

internal fun imageFile(path: String): Boolean = path.substringAfterLast('.').lowercase() in
    setOf("png", "jpg", "jpeg", "webp", "gif", "bmp", "avif")

internal fun markdownFile(path: String): Boolean = path.substringAfterLast('.').lowercase() in setOf("md", "markdown", "mdown")
