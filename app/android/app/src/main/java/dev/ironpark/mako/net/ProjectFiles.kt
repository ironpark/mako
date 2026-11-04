package dev.ironpark.mako.net

import dev.ironpark.mako.v2.Dir
import dev.ironpark.mako.v2.Diff
import dev.ironpark.mako.v2.File
import dev.ironpark.mako.v2.FileOp
import dev.ironpark.mako.v2.GitChanges
import dev.ironpark.mako.v2.GitDiff
import dev.ironpark.mako.v2.GitStatus
import dev.ironpark.mako.v2.ListDir
import dev.ironpark.mako.v2.ReadFile
import dev.ironpark.mako.v2.Search
import dev.ironpark.mako.v2.SearchResult
import dev.ironpark.mako.v2.WriteFile
import dev.ironpark.mako.ui.baseName
import dev.ironpark.mako.ui.remoteJoin
import kotlinx.coroutines.flow.MutableStateFlow

/** Files belong to the session's computer; no phone filesystem paths are used. */
internal interface ProjectFiles {
    /** The node the files are on, "" where that is not known. */
    val node: String get() = ""
    suspend fun list(path: String): Dir
    suspend fun read(path: String, image: Boolean = false): File
    suspend fun search(root: String, query: String, names: Boolean): SearchResult
    suspend fun changes(root: String): GitChanges
    suspend fun diff(root: String, path: String, from: String): Diff

    /** The file's size and when it last changed, its contents left out but a byte. */
    suspend fun stat(path: String): File = read(path)

    /** Makes ([FileOps.CREATE], [FileOps.MKDIR]), renames or deletes a file or folder. */
    suspend fun op(op: String, path: String, to: String = ""): File = throw UnsupportedOperationException()

    /**
     * Copies what [clip] holds into the folder dir under a name not taken
     * there, or with cut moves it; returns where it went.
     */
    suspend fun paste(clip: FileClip, dir: String): String = throw UnsupportedOperationException()
}

/** A file or folder copied or cut on a node, for Paste in any node's files. */
internal data class FileClip(val node: String, val path: String, val dir: Boolean, val cut: Boolean)

internal object FileOps {
    const val CREATE = "create"
    const val MKDIR = "mkdir"
    const val RENAME = "rename"
    const val DELETE = "delete"
    const val COPY = "copy"

    /** What was copied or cut last, for as long as the app runs. */
    val clip = MutableStateFlow<FileClip?>(null)

    /** name, or with a number before its extension, whichever taken does not hold. */
    fun freeName(name: String, taken: (String) -> Boolean): String {
        if (!taken(name)) return name
        val dot = name.lastIndexOf('.')
        val (stem, ext) = if (dot > 0) name.substring(0, dot) to name.substring(dot) else name to ""
        var i = 2
        while (true) {
            val next = "$stem $i$ext"
            if (!taken(next)) return next
            i++
        }
    }
}

/**
 * The files of node through the daemon; [copies] says whether a node's
 * daemon copies files itself, which one too old for it has come through.
 */
internal class NodeProjectFiles(
    private val daemon: Daemon,
    override val node: String,
    private val copies: () -> Boolean = { false },
) : ProjectFiles {
    override suspend fun list(path: String): Dir = list(node, path)

    private suspend fun list(node: String, path: String): Dir = daemon.call(envelope {
        listDir = ListDir.newBuilder().setNodeId(node).setPath(path).build()
    }).dir

    override suspend fun read(path: String, image: Boolean): File = daemon.call(envelope {
        readFile = ReadFile.newBuilder().setNodeId(node).setPath(path).setMax(256 * 1024L).setImage(image).build()
    }).file

    override suspend fun stat(path: String): File = daemon.call(envelope {
        readFile = ReadFile.newBuilder().setNodeId(node).setPath(path).setMax(1).build()
    }).file

    override suspend fun search(root: String, query: String, names: Boolean): SearchResult = daemon.call(envelope {
        search = Search.newBuilder().setNodeId(node).setRoot(root).setQuery(query).setFiles(names).build()
    }).searchResult

    override suspend fun changes(root: String): GitChanges = daemon.call(envelope {
        gitStatus = GitStatus.newBuilder().setNodeId(node).setPath(root).build()
    }).gitChanges

    override suspend fun diff(root: String, path: String, from: String): Diff = daemon.call(envelope {
        gitDiff = GitDiff.newBuilder().setNodeId(node).setRoot(root).setPath(path).setFrom(from).build()
    }).diff

    override suspend fun op(op: String, path: String, to: String): File = op(node, op, path, to)

    private suspend fun op(node: String, op: String, path: String, to: String = ""): File = daemon.call(envelope {
        fileOp = FileOp.newBuilder().setNodeId(node).setOp(op).setPath(path).setTo(to).build()
    }).file

    override suspend fun paste(clip: FileClip, dir: String): String {
        val taken = list(dir).entriesList.mapTo(HashSet()) { it.name }
        val to = remoteJoin(dir, FileOps.freeName(baseName(clip.path)) { it in taken })
        when {
            clip.node == node && clip.cut -> op(FileOps.RENAME, clip.path, to)
            clip.node == node && copies() -> op(FileOps.COPY, clip.path, to)
            else -> {
                copyAcross(clip.node, clip.path, clip.dir, to)
                if (clip.cut) op(clip.node, FileOps.DELETE, clip.path)
            }
        }
        return to
    }

    /** Copies the file or folder at from on fromNode to the new path to here, a piece at a time. */
    private suspend fun copyAcross(fromNode: String, from: String, dir: Boolean, to: String) {
        if (dir) {
            val entries = list(fromNode, from).entriesList
            op(FileOps.MKDIR, to)
            for (e in entries) copyAcross(fromNode, remoteJoin(from, e.name), e.dir, remoteJoin(to, e.name))
            return
        }
        op(FileOps.CREATE, to)
        var offset = 0L
        while (true) {
            val piece = daemon.call(envelope {
                readFile = ReadFile.newBuilder().setNodeId(fromNode).setPath(from).setOffset(offset).setRaw(true)
                    .setMax(Daemon.STAGE_CHUNK.toLong()).build()
            }).file
            if (piece.data.isEmpty) return
            daemon.call(envelope {
                writeFile = WriteFile.newBuilder().setNodeId(node).setPath(to).setData(piece.data).setOffset(offset).build()
            })
            offset += piece.data.size()
            if (!piece.truncated) return
        }
    }
}
