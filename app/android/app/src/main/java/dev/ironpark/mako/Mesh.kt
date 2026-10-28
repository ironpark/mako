package dev.ironpark.mako

import android.content.Context
import android.os.Build
import android.util.Log
import dev.ironpark.mako.core.mobile.Mobile
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.v2.Access
import dev.ironpark.mako.v2.AccessInfo
import dev.ironpark.mako.v2.AnswerApproval
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

    suspend fun refreshSessions() {
        quietly {
            _sessions.value = daemon.sessions()
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

    suspend fun answer(s: SessionInfo, decision: String) = answer(s.nodeId, s.id, s.approval.id, decision)

    /** Answers the approval of a session; the node refuses one answered or withdrawn. */
    suspend fun answer(node: String, session: String, approval: String, decision: String) {
        daemon.answer(
            AnswerApproval.newBuilder()
                .setNodeId(node)
                .setSessionId(session)
                .setApprovalId(approval)
                .setDecision(decision)
                .build()
        )
        refreshSessions()
    }

    /** Whether node takes send, typing into its sessions without an attach. */
    fun canSend(node: String): Boolean =
        node == _self.value?.id || (_nodes.value.firstOrNull { it.id == node }?.revision ?: 0) >= REVISION_SEND

    /** Types into a session: text pasted as its program asked for pastes, then keys as they are. */
    suspend fun send(s: SessionInfo, text: String = "", keys: String = "") = daemon.send(s.nodeId, s.id, text, keys)

    /** Names a session; an empty name gives it back its title. */
    suspend fun renameSession(s: SessionInfo, name: String) {
        daemon.renameSession(s.nodeId, s.id, name)
        refreshSessions()
    }

    suspend fun closeSession(s: SessionInfo) {
        daemon.closeSession(s.nodeId, s.id)
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

        /** The first protocol revision whose daemon takes send (protocol.RevisionSend). */
        const val REVISION_SEND = 8L

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
