package dev.ironpark.mako.net

import dev.ironpark.mako.v2.Access
import dev.ironpark.mako.v2.AccessInfo
import dev.ironpark.mako.v2.AnswerApproval
import dev.ironpark.mako.v2.Changed
import dev.ironpark.mako.v2.Chat
import dev.ironpark.mako.v2.CloseSession
import dev.ironpark.mako.v2.Envelope
import dev.ironpark.mako.v2.ListNodes
import dev.ironpark.mako.v2.ListSessions
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.OpenSession
import dev.ironpark.mako.v2.ReadChat
import dev.ironpark.mako.v2.RenameSession
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.Watch
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.withContext
import java.io.IOException

/**
 * Daemon is the app's link to its node, which relays to the rest of the
 * mesh on its behalf: each request a stream of its own, as the desktop's
 * clients do. [socket] is where the node listens, null while it is not
 * running.
 */
class Daemon(private val socket: () -> String?) {
    /** Opens a greeted stream to the node. */
    fun open(): Stream = Stream.open(socket() ?: throw MakoException("This device's node is not running"))

    /** Sends req on a stream of its own, and returns the reply. */
    suspend fun call(req: Envelope): Envelope = withContext(Dispatchers.IO) {
        open().use { s ->
            s.send(req)
            s.recv()
        }
    }

    suspend fun nodes(): List<Node> =
        call(envelope { listNodes = ListNodes.getDefaultInstance() }).nodes.nodesList

    /**
     * The mesh's sessions. Asking says this device answers approvals, so
     * an agent asking for one waits for it while the app keeps asking.
     */
    suspend fun sessions(): List<SessionInfo> =
        call(envelope { listSessions = ListSessions.newBuilder().setApprovals(true).build() }).sessions.sessionsList

    suspend fun access(op: Access): AccessInfo = call(envelope { access = op }).accessInfo

    suspend fun startSession(req: OpenSession): SessionInfo = call(envelope { openSession = req }).sessionInfo

    suspend fun closeSession(node: String, id: String) {
        call(envelope { closeSession = CloseSession.newBuilder().setNodeId(node).setSessionId(id).build() })
    }

    suspend fun renameSession(node: String, id: String, name: String): SessionInfo =
        call(envelope {
            renameSession = RenameSession.newBuilder().setNodeId(node).setSessionId(id).setName(name).build()
        }).sessionInfo

    suspend fun answer(req: AnswerApproval) {
        call(envelope { answerApproval = req })
    }

    suspend fun chat(req: ReadChat): Chat = call(envelope { readChat = req }).chat

    /**
     * What changes on the mesh, until the flow is cancelled; a stream that
     * breaks ends it with the error.
     */
    fun watch(): Flow<Changed> = callbackFlow {
        val s = open()
        s.send(envelope { watch = Watch.getDefaultInstance() })
        val reader = Thread {
            try {
                while (true) trySend(s.recv().changed)
            } catch (e: IOException) {
                close(e)
            }
        }
        reader.isDaemon = true
        reader.start()
        awaitClose { s.close() }
    }.flowOn(Dispatchers.IO)
}
