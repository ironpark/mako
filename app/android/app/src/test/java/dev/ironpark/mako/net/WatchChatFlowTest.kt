package dev.ironpark.mako.net

import dev.ironpark.mako.v2.ChatLive
import dev.ironpark.mako.v2.Envelope
import dev.ironpark.mako.v2.LiveBlock
import kotlinx.coroutines.flow.take
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.Closeable
import java.io.IOException
import java.util.concurrent.LinkedBlockingQueue

class WatchChatFlowTest {
    private class FakeStream(private val replies: List<Result<Envelope>>) : Closeable {
        val sent = mutableListOf<Envelope>()
        private val queue = LinkedBlockingQueue<Result<Envelope>>().apply { addAll(replies) }
        @Volatile var closed = false

        fun send(env: Envelope) {
            sent += env
        }

        fun recv(): Envelope {
            val next = queue.take()
            return next.getOrThrow()
        }

        override fun close() {
            closed = true
            // Unblock a reader parked in recv() on cancellation.
            queue.offer(Result.failure(IOException("closed")))
        }
    }

    @Test
    fun watchChatFlowSendsWatchChatEnvelopeAndEmitsLiveUpdates() = runBlocking {
        val first = ChatLive.newBuilder()
            .setLogged(true)
            .addBlocks(LiveBlock.newBuilder().setIndex(0).setKind("text").setText("hello"))
            .build()
        val second = ChatLive.newBuilder()
            .setLogged(false)
            .addBlocks(LiveBlock.newBuilder().setIndex(0).setKind("text").setText("hello world"))
            .build()
        val stream = FakeStream(
            listOf(
                Result.success(envelope { chatLive = first }),
                Result.success(envelope { chatLive = second }),
            )
        )

        val got = watchChatFlow(
            open = { stream },
            send = FakeStream::send,
            recv = FakeStream::recv,
            node = "node-a",
            sessionId = "sess-1",
            agent = "sub-2",
        ).take(2).toList()

        assertEquals(1, stream.sent.size)
        val req = stream.sent.single().watchChat
        assertEquals("node-a", req.nodeId)
        assertEquals("sess-1", req.sessionId)
        assertEquals("sub-2", req.agent)
        assertEquals(listOf(first, second), got)
        assertTrue(stream.closed)
    }

    @Test
    fun watchChatFlowPropagatesStreamErrorsAndClosesStream() = runBlocking {
        val stream = FakeStream(
            listOf(
                Result.success(envelope { chatLive = ChatLive.newBuilder().setLogged(true).build() }),
                Result.failure(MakoException("unknown session", "not_found")),
            )
        )
        val received = mutableListOf<ChatLive>()
        try {
            watchChatFlow(
                open = { stream },
                send = FakeStream::send,
                recv = FakeStream::recv,
                node = "node-a",
                sessionId = "missing",
            ).collect { received += it }
            fail("expected MakoException")
        } catch (e: MakoException) {
            assertEquals("unknown session", e.message)
            assertEquals("not_found", e.code)
        }
        assertEquals(1, received.size)
        assertTrue(stream.closed)
    }
}
