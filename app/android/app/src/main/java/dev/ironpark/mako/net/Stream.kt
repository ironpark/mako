package dev.ironpark.mako.net

import android.net.LocalSocket
import android.net.LocalSocketAddress
import dev.ironpark.mako.BuildConfig
import dev.ironpark.mako.v2.Envelope
import dev.ironpark.mako.v2.Hello
import java.io.BufferedInputStream
import java.io.BufferedOutputStream
import java.io.Closeable
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.IOException

/** MakoException is a request the node refused, or a stream that broke. */
class MakoException(message: String) : IOException(message)

/**
 * Stream is one connection to the node's socket, greeted: it carries
 * frames, each an [Envelope] after its length as a big-endian uint32, as
 * docs/protocol.md says. Closing it ends the request it carries, or the
 * attach or watch it turned into.
 */
class Stream private constructor(private val socket: LocalSocket) : Closeable {
    private val input = DataInputStream(BufferedInputStream(socket.inputStream))
    private val output = DataOutputStream(BufferedOutputStream(socket.outputStream))

    fun send(env: Envelope) {
        val bytes = env.toByteArray()
        synchronized(output) {
            output.writeInt(bytes.size)
            output.write(bytes)
            output.flush()
        }
    }

    /** Receives a frame; an error frame is thrown, as the node's refusal. */
    fun recv(): Envelope {
        val n = input.readInt()
        if (n < 0 || n > MAX_FRAME) throw MakoException("a frame of $n bytes")
        val bytes = ByteArray(n)
        input.readFully(bytes)
        val env = Envelope.parseFrom(bytes)
        if (env.hasError()) throw MakoException(env.error.message)
        return env
    }

    /**
     * The stream's bytes as they are, for a stream a tunnel turned into a
     * connection: what is read already, past the last frame, comes first.
     */
    val rawInput: java.io.InputStream get() = input
    val rawOutput: java.io.OutputStream get() = output

    /** Says no more is sent, while what comes back is still read. */
    fun shutdownOutput() {
        runCatching { output.flush() }
        runCatching { socket.shutdownOutput() }
    }

    override fun close() {
        // Shutting the input down wakes a read blocked on it, which closing
        // alone does not.
        runCatching { socket.shutdownInput() }
        runCatching { socket.close() }
    }

    companion object {
        /** A frame is at most this long (protocol.MaxFrameSize). */
        const val MAX_FRAME = 8 shl 20

        /** Opens a stream to the node listening on the socket at path. */
        fun open(path: String): Stream {
            val socket = LocalSocket()
            try {
                socket.connect(LocalSocketAddress(path, LocalSocketAddress.Namespace.FILESYSTEM))
            } catch (e: IOException) {
                socket.close()
                throw e
            }
            val s = Stream(socket)
            try {
                s.send(envelope {
                    hello = Hello.newBuilder().setName("mako-android").setVersion(BuildConfig.VERSION_NAME).build()
                })
                s.recv() // its welcome
            } catch (e: IOException) {
                s.close()
                throw e
            }
            return s
        }
    }
}

/**
 * Builds an [Envelope]; set one body on it, as `hello = …`. Kotlin reaches
 * the builder's setters as properties.
 */
inline fun envelope(build: Envelope.Builder.() -> Unit): Envelope = Envelope.newBuilder().apply(build).build()
