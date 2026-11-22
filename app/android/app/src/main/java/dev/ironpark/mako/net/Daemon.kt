package dev.ironpark.mako.net

import com.google.protobuf.ByteString
import dev.ironpark.mako.v2.Access
import dev.ironpark.mako.v2.AccessInfo
import dev.ironpark.mako.v2.AnswerApproval
import dev.ironpark.mako.v2.AnswerDialog
import dev.ironpark.mako.v2.ConfigureAgent
import dev.ironpark.mako.v2.AnswerQuestion
import dev.ironpark.mako.v2.AgentBlock
import dev.ironpark.mako.v2.BlockEvent
import dev.ironpark.mako.v2.BlockUpdate
import dev.ironpark.mako.v2.Changed
import dev.ironpark.mako.v2.Chat
import dev.ironpark.mako.v2.ChatLive
import dev.ironpark.mako.v2.CloseSession
import dev.ironpark.mako.v2.Envelope
import dev.ironpark.mako.v2.GetWorkspace
import dev.ironpark.mako.v2.ListAgentBlocks
import dev.ironpark.mako.v2.ListNodes
import dev.ironpark.mako.v2.ListSessions
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.OpenSession
import dev.ironpark.mako.v2.PluginAction
import dev.ironpark.mako.v2.PluginDone
import dev.ironpark.mako.v2.ProjectAgents
import dev.ironpark.mako.v2.ReadProjectAgents
import dev.ironpark.mako.v2.ReadChat
import dev.ironpark.mako.v2.RenameSession
import dev.ironpark.mako.v2.SeeSession
import dev.ironpark.mako.v2.Send
import dev.ironpark.mako.v2.SleepSession
import dev.ironpark.mako.v2.StageFile
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.Watch
import dev.ironpark.mako.v2.WatchBlock
import dev.ironpark.mako.v2.WatchChat
import dev.ironpark.mako.v2.Workspace
import dev.ironpark.mako.v2.WorkspaceState
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.channels.trySendBlocking
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.withContext
import java.io.Closeable
import java.io.IOException
import java.util.Locale

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

    /**
     * Stops the program of a resting agent's session, keeping the session to
     * resume when it is next attached. Nodes from
     * [dev.ironpark.mako.Mesh.REVISION_SLEEP] on take it.
     */
    suspend fun sleepSession(node: String, id: String): SessionInfo =
        call(envelope {
            sleepSession = SleepSession.newBuilder().setNodeId(node).setSessionId(id).build()
        }).sessionInfo

    /**
     * Records that the user has looked at a session's attention up to [seq],
     * so every front-end on the mesh clears its unread state and alerts.
     * Nodes from [dev.ironpark.mako.Mesh.REVISION_SEE_SESSION] on take it.
     */
    suspend fun seeSession(node: String, id: String, seq: Long): SessionInfo =
        call(envelope {
            seeSession = SeeSession.newBuilder().setNodeId(node).setSessionId(id).setSeq(seq).build()
        }).sessionInfo

    suspend fun answer(req: AnswerApproval) {
        call(envelope { answerApproval = req })
    }

    /**
     * Answers the questions a session's agent asks in its terminal's
     * dialog, typed there by its node. Nodes from
     * [dev.ironpark.mako.Mesh.REVISION_QUESTIONS] on take it.
     */
    suspend fun answer(req: AnswerQuestion) {
        call(envelope { answerQuestion = req })
    }

    /** Sets a session's agent's permission mode, model or effort. */
    suspend fun configure(req: ConfigureAgent) {
        call(envelope { configureAgent = req })
    }

    /** Picks an option of the dialog a session's agent shows in its terminal, as the user would there. */
    suspend fun answer(req: AnswerDialog) {
        call(envelope { answerDialog = req })
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

    /**
     * The workspace the node holds: its spaces, projects, names, looks and
     * pins, as a CRDT the node merges with the computers it and they let
     * each other in (docs/protocol.md, Workspaces). A node from before
     * revision 23 answers with no state, which reads as none.
     */
    suspend fun workspace(): Workspace = call(envelope { getWorkspace = GetWorkspace.getDefaultInstance() }).workspace

    /** The workspace node holds, which a node that lets this device in shows it, whether it shares it or not. */
    suspend fun workspaceOf(node: String): Workspace =
        call(envelope { getWorkspace = GetWorkspace.newBuilder().setNodeId(node).build() }).workspace

    /**
     * Gives an edit of the workspace to node, "" for this device's own,
     * delta holding only what changed; the node stamps the delta's clocks
     * itself, whatever they hold, and answers with the workspace it holds
     * after. Another node takes it from revision 25 on, if it lets this
     * device in.
     */
    suspend fun updateWorkspace(delta: WorkspaceState, node: String = ""): Workspace =
        call(envelope { workspace = Workspace.newBuilder().setState(delta).setToNode(node).build() }).workspace

    suspend fun chat(req: ReadChat): Chat = call(envelope { readChat = req }).chat

    /**
     * Follows a session's conversation as its log grows and its agent's
     * reply streams, until the flow is cancelled; a stream that breaks ends
     * it with the error. Nodes from [dev.ironpark.mako.Mesh.REVISION_LIVE_CHAT]
     * on serve it.
     */
    fun watchChat(node: String, sessionId: String, agent: String = ""): Flow<ChatLive> =
        watchChatFlow(open = ::open, send = Stream::send, recv = Stream::recv, node = node, sessionId = sessionId, agent = agent)

    /** The agents of the project path is in on node, and their latest messages to each other. */
    suspend fun projectAgents(node: String, path: String): ProjectAgents =
        call(envelope { readProjectAgents = ReadProjectAgents.newBuilder().setNodeId(node).setPath(path).build() }).projectAgents

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
    fun watch(): Flow<Changed> = readFlow(
        open = { open().also { it.send(envelope { watch = Watch.newBuilder().setApprovals(true).build() }) } },
        read = { it.recv().changed },
    )

    /**
     * The updates of node's block, a plugin's, plugin:<id>/<block>, or an
     * agent's, agent:<session>/<id>: the tree whole, then the ops that
     * change it (docs/protocol.md, Plugin blocks), until the flow is
     * cancelled; a stream that breaks ends it with the error. Nodes from
     * [dev.ironpark.mako.Mesh.REVISION_PEER_BLOCKS] on serve a node they
     * let in; [scope] selects a scoped block instance on nodes from
     * [dev.ironpark.mako.Mesh.REVISION_PLUGIN_SCOPE] on; [locale] carries
     * the device's BCP-47 language tag on nodes from
     * [dev.ironpark.mako.Mesh.REVISION_PLUGIN_RICH_UI_V2] on.
     */
    fun watchBlock(
        node: String,
        block: String,
        scope: String = "",
        locale: String = currentLocaleTag(),
    ): Flow<BlockUpdate> =
        watchBlockFlow(
            open = ::open,
            send = Stream::send,
            recv = Stream::recv,
            node = node,
            block = block,
            scope = scope,
            locale = locale,
        )

    /**
     * Tells a block of the user acting on its node, on the node ev names:
     * the block's source looks up what to send. The answer's open_url is
     * a link the plugin asked, in answer, to open.
     */
    suspend fun blockEvent(ev: BlockEvent): PluginDone {
        val withLocale = if (ev.locale.isNotEmpty()) ev else {
            val tag = currentLocaleTag()
            if (tag.isNotEmpty()) ev.toBuilder().setLocale(tag).build() else ev
        }
        return call(envelope { blockEvent = withLocale }).pluginDone
    }

    /** Acts on a plugin, carrying the device's BCP-47 locale tag when unset. */
    suspend fun pluginAction(req: PluginAction): PluginDone {
        val withLocale = if (req.locale.isNotEmpty()) req else {
            val tag = currentLocaleTag()
            if (tag.isNotEmpty()) req.toBuilder().setLocale(tag).build() else req
        }
        return call(envelope { pluginAction = withLocale }).pluginDone
    }

    /** The blocks node's agents show, in the order shown. */
    suspend fun agentBlocks(node: String): List<AgentBlock> =
        call(envelope { listAgentBlocks = ListAgentBlocks.newBuilder().setNodeId(node).build() }).agentBlocks.blocksList

    /**
     * node's panels, as it lists them for a node it lets in (revision 35):
     * the blocks its window's layouts place and its agents show, each by
     * name, with its title and kind.
     */
    suspend fun panels(node: String): List<dev.ironpark.mako.v2.Panel> =
        call(envelope { listPanels = dev.ironpark.mako.v2.ListPanels.newBuilder().setNodeId(node).build() }).panels.panelsList

    companion object {
        /** How much of a file one staged piece carries, well inside a frame. */
        const val STAGE_CHUNK = 4 shl 20
    }
}

/** Current default BCP-47 language tag (e.g. "en-US", "ko-KR"), or "" if undetermined. */
internal fun currentLocaleTag(): String =
    Locale.getDefault().toLanguageTag().takeUnless { it.isBlank() || it == "und" }.orEmpty()

internal fun <S : Closeable> watchBlockFlow(
    open: () -> S,
    send: (S, Envelope) -> Unit,
    recv: (S) -> Envelope,
    node: String,
    block: String,
    scope: String = "",
    locale: String = "",
): Flow<BlockUpdate> = readFlow(
    open = {
        open().also {
            val req = WatchBlock.newBuilder().setNodeId(node).setBlock(block)
            if (scope.isNotEmpty()) req.setScope(scope)
            if (locale.isNotEmpty()) req.setLocale(locale)
            send(it, envelope { watchBlock = req.build() })
        }
    },
    read = { recv(it).blockUpdate },
    last = { it.gone },
)

internal fun <S : Closeable> watchChatFlow(
    open: () -> S,
    send: (S, Envelope) -> Unit,
    recv: (S) -> Envelope,
    node: String,
    sessionId: String,
    agent: String = "",
): Flow<ChatLive> = readFlow(
    open = {
        open().also {
            send(
                it,
                envelope {
                    watchChat = WatchChat.newBuilder()
                        .setNodeId(node)
                        .setSessionId(sessionId)
                        .setAgent(agent)
                        .build()
                },
            )
        }
    },
    read = { recv(it).chatLive },
)

/**
 * What read reads, one value at a time, from the stream open opens, as a
 * flow: on a thread of its own, the reads block, until a value is the
 * last, the stream breaks, which ends the flow with the error, or the flow
 * is cancelled, which closes the stream. A value waits for the collector
 * to take the one before rather than be dropped: a stream's values build
 * on each other, and a lost patch would leave a block's tree wrong, a
 * lost gone the block followed for ever.
 */
internal fun <S : Closeable, T> readFlow(open: () -> S, read: (S) -> T, last: (T) -> Boolean = { false }): Flow<T> = callbackFlow {
    val s = open()
    val reader = Thread {
        try {
            while (true) {
                val v = read(s)
                // Fails only once the flow is cancelled, and nothing waits for more.
                if (trySendBlocking(v).isFailure || last(v)) break
            }
            close()
        } catch (e: IOException) {
            close(e)
        }
    }
    reader.isDaemon = true
    reader.start()
    awaitClose { s.close() }
}.flowOn(Dispatchers.IO)
