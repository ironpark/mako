package dev.ironpark.mako

import android.content.Context
import android.os.Build
import android.util.Log
import dev.ironpark.mako.core.mobile.Mobile
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.v2.Access
import dev.ironpark.mako.v2.AccessInfo
import dev.ironpark.mako.v2.AnswerApproval
import dev.ironpark.mako.v2.AnswerDialog
import dev.ironpark.mako.v2.ConfigureAgent
import dev.ironpark.mako.v2.AnswerQuestion
import dev.ironpark.mako.v2.Question
import dev.ironpark.mako.v2.QuestionAnswer
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.concurrent.atomic.AtomicInteger

/**
 * Mesh is what the app knows of the mesh, through the node it runs: the
 * nodes, their sessions, and who lets this device in. It starts the node,
 * then follows the node's watch, asking again for what changed; the watch
 * also keeps the daemons waiting for this device's answers to approvals.
 * While the app is on screen, the sessions are asked for now and then
 * besides, for when each last printed, which the watch does not report.
 */
class Mesh(
    private val context: Context,
    private val scope: CoroutineScope,
    private val foreground: StateFlow<Boolean>,
) {
    sealed interface State {
        data object Starting : State
        data object Running : State
        data class Failed(val message: String) : State
    }

    private val _state = MutableStateFlow<State>(State.Starting)
    val state: StateFlow<State> = _state.asStateFlow()

    private val _self = MutableStateFlow<Node?>(null)
    /** This device's node. */
    val self: StateFlow<Node?> = _self.asStateFlow()

    private val _nodes = MutableStateFlow<List<Node>>(emptyList())
    /** The other nodes, as this device's node knows them. */
    val nodes: StateFlow<List<Node>> = _nodes.asStateFlow()

    private val _sessions = MutableStateFlow<List<SessionInfo>>(emptyList())
    val sessions: StateFlow<List<SessionInfo>> = _sessions.asStateFlow()

    private val _loaded = MutableStateFlow(false)
    /** Whether the sessions have come since the node started, or long enough has passed for none to mean none. */
    val loaded: StateFlow<Boolean> = _loaded.asStateFlow()

    private val _access = MutableStateFlow<AccessInfo?>(null)
    val access: StateFlow<AccessInfo?> = _access.asStateFlow()

    private val _peers = MutableStateFlow<List<String>>(emptyList())
    /** The addresses this device always links to. */
    val peers: StateFlow<List<String>> = _peers.asStateFlow()

    val daemon = Daemon { Mobile.socket().ifEmpty { null } }

    /** Other nodes' loopback ports, forwarded to this phone's. */
    val tunnels = dev.ironpark.mako.net.Tunnels(daemon)

    private var job: Job? = null

    /** Starts the node, unless it runs, and follows it. */
    fun start() {
        if (job?.isActive == true) return
        _state.value = State.Starting
        job = scope.launch(Dispatchers.IO) {
            try {
                val dir = File(context.filesDir, "mako").apply { mkdirs() }
                Mobile.start(dir.path, context.cacheDir.path, deviceName())
            } catch (e: Exception) {
                _state.value = State.Failed(e.message ?: e.toString())
                return@launch
            }
            _state.value = State.Running
            refreshPeers()
            // Peers' sessions come with their state, a moment after they
            // link: until then, none may yet mean some.
            launch {
                delay(SETTLE)
                _loaded.value = true
            }
            launch { follow() }
            foreground.collectLatest { shown ->
                while (shown) {
                    refreshSessions()
                    delay(POLL)
                }
            }
        }
    }

    /** Stops following the node, and the node. */
    fun stop() {
        job?.cancel()
        job = null
        scope.launch(Dispatchers.IO) { Mobile.stop() }
    }

    /**
     * Follows the node's watch, starting it again when it breaks. What
     * changes while the last change is asked for is asked for together,
     * once.
     */
    private suspend fun follow() {
        while (true) {
            refreshAll()
            try {
                coroutineScope {
                    val changed = AtomicInteger(0)
                    val wake = Channel<Unit>(Channel.CONFLATED)
                    val asking = launch {
                        for (w in wake) {
                            val c = changed.getAndSet(0)
                            if (c and NODES != 0) refreshNodes()
                            if (c and SESSIONS != 0) refreshSessions()
                            if (c and ACCESS != 0) refreshAccess()
                        }
                    }
                    try {
                        daemon.watch().collect { c ->
                            changed.updateAndGet {
                                it or (if (c.nodes) NODES else 0) or (if (c.sessions) SESSIONS else 0) or (if (c.access) ACCESS else 0)
                            }
                            wake.trySend(Unit)
                        }
                    } finally {
                        asking.cancel()
                    }
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                Log.d(TAG, "watch ended: ${e.message}")
            }
            delay(1000)
        }
    }

    suspend fun refreshAll() {
        refreshNodes()
        refreshSessions()
        refreshAccess()
    }

    suspend fun refreshNodes() {
        quietly {
            val all = daemon.nodes()
            val me = all.firstOrNull { it.source == "self" }
            _self.value = me
            _nodes.value = all.filter { it.source != "self" }.sortedBy { it.name.lowercase() }
        }
    }

    private val closed = ClosedSessions()
    private val sessionsAsked = java.util.concurrent.atomic.AtomicLong()
    private var sessionsShown = 0L

    /**
     * Lists the sessions again. Lists asked for at once, by the watch and
     * after a close, can come back in any order; only one asked for after
     * the list shown replaces it, so an older one never brings back what a
     * newer one left out.
     */
    suspend fun refreshSessions() {
        quietly {
            val asked = sessionsAsked.incrementAndGet()
            val list = daemon.sessions()
            synchronized(closed) {
                if (asked > sessionsShown) {
                    sessionsShown = asked
                    _sessions.value = closed.filter(list, System.currentTimeMillis())
                }
            }
            if (_sessions.value.isNotEmpty()) _loaded.value = true
        }
    }

    suspend fun refreshAccess() {
        quietly { _access.value = daemon.access(Access.getDefaultInstance()) }
    }

    private fun refreshPeers() {
        _peers.value = runCatching { Mobile.peers().lines().filter { it.isNotBlank() } }.getOrDefault(emptyList())
    }

    /** Has the node link to addr from now on; the node starts again for it. */
    suspend fun addPeer(addr: String) = withContext(Dispatchers.IO) {
        Mobile.addPeer(addr)
        refreshPeers()
    }

    suspend fun removePeer(addr: String) = withContext(Dispatchers.IO) {
        Mobile.removePeer(addr)
        refreshPeers()
    }

    suspend fun rename(name: String) = withContext(Dispatchers.IO) {
        Mobile.rename(name)
        refreshNodes()
    }

    /** Asks node to let this device in, saying note. */
    suspend fun askToJoin(node: String, note: String) {
        _access.value = daemon.access(Access.newBuilder().setOp("request").setNodeId(node).setNote(note).build())
    }

    /**
     * Joins the computer whose pairing code's link is link: asks it, with
     * the code's token, to let this device in, which it does at once, and
     * links to it from then on. Returns the computer's name.
     */
    suspend fun join(link: String, note: String): String {
        val info = daemon.access(Access.newBuilder().setOp("join").setKey(link).setNote(note).build())
        _access.value = info
        withContext(Dispatchers.IO) { refreshPeers() }
        refreshNodes()
        return info.joined
    }

    suspend fun answer(
        s: SessionInfo, decision: String, answers: List<QuestionAnswer> = emptyList(), option: String = "", message: String = "",
    ) = answer(s.nodeId, s.id, s.approval.id, decision, answers, option, message)

    /**
     * Answers the approval of a session, and its questions, if it asks
     * any, with answers, one each; allowed with the approval's option of
     * that id, or denied telling the agent message. The node refuses one
     * answered or withdrawn.
     */
    suspend fun answer(
        node: String, session: String, approval: String, decision: String,
        answers: List<QuestionAnswer> = emptyList(), option: String = "", message: String = "",
    ) {
        daemon.answer(
            AnswerApproval.newBuilder()
                .setNodeId(node)
                .setSessionId(session)
                .setApprovalId(approval)
                .setDecision(decision)
                .addAllAnswers(answers)
                .setOption(option)
                .setMessage(message)
                .build()
        )
        refreshSessions()
    }

    /** Sets the session's agent's permission mode, model or effort: those given, not empty. */
    suspend fun configure(s: SessionInfo, mode: String = "", model: String = "", effort: String = "") {
        daemon.configure(
            ConfigureAgent.newBuilder()
                .setNodeId(s.nodeId)
                .setSessionId(s.id)
                .setMode(mode)
                .setModel(model)
                .setEffort(effort)
                .build()
        )
        refreshSessions()
    }

    /** Picks option, by its index, of the dialog the session's agent shows in its terminal. */
    suspend fun answerDialog(s: SessionInfo, option: Int) {
        daemon.answer(
            AnswerDialog.newBuilder()
                .setNodeId(s.nodeId)
                .setSessionId(s.id)
                .setDialogId(s.dialog.id)
                .setOption(option)
                .build()
        )
        refreshSessions()
    }

    /** Answers questions the session's agent asks in its terminal's dialog, typed there. */
    suspend fun answerQuestions(s: SessionInfo, questions: List<Question>, answers: List<QuestionAnswer>) {
        daemon.answer(
            AnswerQuestion.newBuilder()
                .setNodeId(s.nodeId)
                .setSessionId(s.id)
                .addAllQuestions(questions)
                .addAllAnswers(answers)
                .build()
        )
        refreshSessions()
    }

    /** Whether node takes send, typing into its sessions without an attach. */
    fun canSend(node: String): Boolean = revision(node) >= REVISION_SEND

    /** Whether node takes files and tunnels: staged uploads, and its loopback ports forwarded. */
    fun canTransfer(node: String): Boolean =
        (_nodes.value.firstOrNull { it.id == node }?.revision ?: REVISION_TRANSFERS) >= REVISION_TRANSFERS

    /** Whether node's daemon copies files itself, and says how its links stand. */
    fun copiesFiles(node: String): Boolean = revision(node) >= REVISION_LINKS

    /** node's protocol revision; this device's own node reads as current. */
    private fun revision(node: String): Long =
        if (node == _self.value?.id) Long.MAX_VALUE else _nodes.value.firstOrNull { it.id == node }?.revision ?: 0

    /** Has this device's node try its link to node again now. */
    suspend fun relink(node: String) {
        daemon.access(Access.newBuilder().setOp("relink").setNodeId(node).build())
        refreshNodes()
    }

    /** Whether node reads a project's agents. */
    fun readsProjectAgents(node: String): Boolean = revision(node) >= REVISION_PROJECT_AGENTS

    /** Types into a session: text pasted as its program asked for pastes, then keys as they are. */
    suspend fun send(s: SessionInfo, text: String = "", keys: String = "") = daemon.send(s.nodeId, s.id, text, keys)

    /** Names a session; an empty name gives it back its title. */
    suspend fun renameSession(s: SessionInfo, name: String) {
        daemon.renameSession(s.nodeId, s.id, name)
        refreshSessions()
    }

    /** Closes s, gone from the list at once and kept gone; back if the close fails. */
    suspend fun closeSession(s: SessionInfo) {
        closed.close(s.id, System.currentTimeMillis())
        synchronized(closed) { _sessions.value = _sessions.value.filterNot { it.id == s.id } }
        try {
            daemon.closeSession(s.nodeId, s.id)
        } catch (e: Exception) {
            closed.reopen(s.id)
            refreshSessions()
            throw e
        }
        refreshSessions()
    }

    fun session(id: String): SessionInfo? = _sessions.value.firstOrNull { it.id == id }

    fun nodeName(id: String): String =
        if (id == _self.value?.id) _self.value?.name.orEmpty()
        else _nodes.value.firstOrNull { it.id == id }?.name ?: id.take(8)

    private suspend fun quietly(work: suspend () -> Unit) {
        try {
            work()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            Log.d(TAG, "refresh: ${e.message}")
        }
    }

    companion object {
        private const val TAG = "mako"

        /** How often the sessions are asked for while the app is on screen. */
        private const val POLL = 30_000L

        /** Whether text is a computer's pairing code, as its QR code holds it. */
        fun isPairing(text: String): Boolean = text.startsWith("mako://pair?")

        /** The first protocol revision whose daemon stages files and tunnels (protocol.RevisionTransfers). */
        const val REVISION_TRANSFERS = 7L

        /** The first protocol revision whose daemon takes send (protocol.RevisionSend). */
        const val REVISION_SEND = 8L

        /** The first protocol revision whose daemon reads a project's agents (protocol.RevisionProjectAgents). */
        const val REVISION_PROJECT_AGENTS = 15L

        /** The first protocol revision whose daemon copies files and says how its links stand (protocol.RevisionLinks). */
        const val REVISION_LINKS = 18L

        /** The first protocol revision whose daemon asks and answers questions in the app (protocol.RevisionQuestions). */
        const val REVISION_QUESTIONS = 19L

        /** How a node's link stands, as Node.link says (protocol.Link*). */
        const val LINK_LINKING = "linking"
        const val LINK_DOWN = "down"

        /** How long the sessions may take to come, before none is taken to mean none. */
        private const val SETTLE = 8_000L

        // What a watch's Changed says changed, as bits.
        private const val NODES = 1
        private const val SESSIONS = 2
        private const val ACCESS = 4

        fun deviceName(): String {
            val model = Build.MODEL.orEmpty()
            val maker = Build.MANUFACTURER.orEmpty().replaceFirstChar { it.uppercase() }
            return if (model.startsWith(maker, ignoreCase = true)) model else "$maker $model".trim()
        }
    }
}
