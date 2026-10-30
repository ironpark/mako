package dev.ironpark.mako.net

import dev.ironpark.mako.v2.Dir
import dev.ironpark.mako.v2.Diff
import dev.ironpark.mako.v2.File
import dev.ironpark.mako.v2.GitChanges
import dev.ironpark.mako.v2.GitDiff
import dev.ironpark.mako.v2.GitStatus
import dev.ironpark.mako.v2.ListDir
import dev.ironpark.mako.v2.ReadFile
import dev.ironpark.mako.v2.Search
import dev.ironpark.mako.v2.SearchResult

/** Files belong to the session's computer; no phone filesystem paths are used. */
internal interface ProjectFiles {
    suspend fun list(path: String): Dir
    suspend fun read(path: String, image: Boolean = false): File
    suspend fun search(root: String, query: String, names: Boolean): SearchResult
    suspend fun changes(root: String): GitChanges
    suspend fun diff(root: String, path: String, from: String): Diff
}

internal class NodeProjectFiles(private val daemon: Daemon, private val node: String) : ProjectFiles {
    override suspend fun list(path: String): Dir = daemon.call(envelope {
        listDir = ListDir.newBuilder().setNodeId(node).setPath(path).build()
    }).dir

    override suspend fun read(path: String, image: Boolean): File = daemon.call(envelope {
        readFile = ReadFile.newBuilder().setNodeId(node).setPath(path).setMax(256 * 1024L).setImage(image).build()
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
}
