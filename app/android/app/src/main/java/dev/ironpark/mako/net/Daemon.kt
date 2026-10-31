package dev.ironpark.mako.net

import com.google.protobuf.ByteString
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
import dev.ironpark.mako.v2.Send
import dev.ironpark.mako.v2.StageFile
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

    suspend fun sessions(): List<SessionInfo> =
        call(envelope { listSessions = ListSessions.getDefaultInstance() }).sessions.sessionsList

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

    /**
     * Types into a session without attaching: text pasted as its program
     * asked for pastes, then keys as they are. Nodes from
     * [dev.ironpark.mako.Mesh.REVISION_SEND] on take it.
     */
    suspend fun send(node: String, id: String, text: String, keys: String) {
        call(envelope {
            send = Send.newBuilder().setNodeId(node).setSessionId(id).setText(text)
                .setKeys(ByteString.copyFromUtf8(keys)).build()
        })
    }

    suspend fun commands(node: String, id: String): List<dev.ironpark.mako.v2.Command> =
        call(envelope {
            listCommands = dev.ironpark.mako.v2.ListCommands.newBuilder().setNodeId(node).setSessionId(id).build()
        }).commands.commandsList

    suspend fun chat(req: ReadChat): Chat = call(envelope { readChat = req }).chat

    /**
     * Sends what input holds to node, a piece at a time, as a file named
     * name its node keeps for a session to be handed; returns where it is
     * kept there. sent hears how many bytes have gone.
     */
    suspend fun stage(node: String, name: String, input: java.io.InputStream, sent: (Long) -> Unit = {}): String =
        withContext(Dispatchers.IO) {
            val buf = ByteArray(STAGE_CHUNK)
            var path = ""
            var offset = 0L
            while (true) {
                var n = 0
                while (n < buf.size) {
                    val r = input.read(buf, n, buf.size - n)
                    if (r < 0) break
                    n += r
                }
                if (n == 0 && offset > 0) break
                path = call(envelope {
                    stageFile = StageFile.newBuilder().setNodeId(node).setName(name).setPath(path)
                        .setOffset(offset).setData(ByteString.copyFrom(buf, 0, n)).build()
                }).staged.path
                offset += n
                sent(offset)
                if (n < buf.size) break
            }
            path
        }

    /**
     * What changes on the mesh, until the flow is cancelled; a stream that
     * breaks ends it with the error. While it is collected, this device
     * answers approvals: an agent asking for one waits for its answer.
     */
    fun watch(): Flow<Changed> = callbackFlow {
        val s = open()
        s.send(envelope { watch = Watch.newBuilder().setApprovals(true).build() })
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

    companion object {
        /** How much of a file one staged piece carries, well inside a frame. */
        const val STAGE_CHUNK = 4 shl 20
    }
}
