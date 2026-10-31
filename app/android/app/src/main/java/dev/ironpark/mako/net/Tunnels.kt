package dev.ironpark.mako.net

import android.util.Log
import dev.ironpark.mako.v2.Tunnel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket

/**
 * Tunnels forwards ports on other nodes' loopback to this phone's, as the
 * desktop window does: a web address a session there printed for the
 * server it started, such as `http://localhost:5173`, opens here in the
 * browser at the same port when it is free, else at another. Each
 * connection made here is carried to that port of the node, through the
 * node this app runs. A tunnel stays while the app does.
 */
class Tunnels(private val daemon: Daemon) {
    private data class Key(val node: String, val port: Int)

    private val mutex = Mutex()
    private val open = mutableMapOf<Key, ServerSocket>()

    /**
     * The port here that reaches port on node, opened unless it is: once a
     * connection there says something answers on it.
     */
    suspend fun forward(node: String, port: Int): Int = mutex.withLock {
        open[Key(node, port)]?.takeUnless { it.isClosed }?.let { return@withLock it.localPort }
        withContext(Dispatchers.IO) {
            dial(node, port).close() // nothing answering is said now, not on the first request
            val loopback = InetAddress.getByName("127.0.0.1")
            val server = try {
                ServerSocket(port, 50, loopback)
            } catch (_: IOException) { // taken here, or under 1024: any port will do
                ServerSocket(0, 50, loopback)
            }
            open[Key(node, port)] = server
            Thread({ serve(server, node, port) }, "tunnel-$port").apply { isDaemon = true }.start()
            server.localPort
        }
    }

    /** Connects to port on node: a stream that carries its bytes from then on. */
    private fun dial(node: String, port: Int): Stream {
        val s = daemon.open()
        try {
            s.send(envelope { tunnel = Tunnel.newBuilder().setNodeId(node).setPort(port).build() })
            s.recv() // Tunneled
        } catch (e: IOException) {
            s.close()
            throw e
        }
        return s
    }

    private fun serve(server: ServerSocket, node: String, port: Int) {
        while (true) {
            val conn = try { server.accept() } catch (_: IOException) { return }
            Thread({
                val far = try { dial(node, port) } catch (e: IOException) {
                    Log.d("mako", "tunnel to $port: ${e.message}")
                    runCatching { conn.close() }
                    return@Thread
                }
                splice(conn, far)
            }, "tunnel-$port-conn").apply { isDaemon = true }.start()
        }
    }

    /** Copies both ways until both have ended, then closes both. */
    private fun splice(conn: Socket, far: Stream) {
        val back = Thread {
            pipe(far.rawInput, conn.getOutputStream())
            runCatching { conn.shutdownOutput() }
        }.apply { isDaemon = true; start() }
        pipe(conn.getInputStream(), far.rawOutput)
        far.shutdownOutput()
        back.join()
        runCatching { conn.close() }
        far.close()
    }

    private fun pipe(from: InputStream, to: OutputStream) {
        val buf = ByteArray(32 * 1024)
        try {
            while (true) {
                val n = from.read(buf)
                if (n < 0) break
                to.write(buf, 0, n)
                to.flush()
            }
        } catch (_: IOException) {
        }
    }
}
