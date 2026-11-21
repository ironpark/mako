package dev.ironpark.mako.net

import dev.ironpark.mako.v2.BlockUpdate
import dev.ironpark.mako.v2.Envelope
import dev.ironpark.mako.v2.UiNode
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.Closeable
import java.io.IOException
import java.util.concurrent.LinkedBlockingQueue

class WatchBlockFlowTest {
    private class FakeStream(replies: List<Result<Envelope>>) : Closeable {
        val sent = mutableListOf<Envelope>()
        private val queue = LinkedBlockingQueue<Result<Envelope>>().apply { addAll(replies) }
        @Volatile var closed = false

        fun send(env: Envelope) {
            sent += env
        }

        fun recv(): Envelope = queue.take().getOrThrow()

        override fun close() {
            closed = true
            queue.offer(Result.failure(IOException("closed")))
        }
    }

    @Test
    fun watchBlockFlowSendsScopeWhenNonEmptyAndStopsOnGone() = runBlocking {
        val first = BlockUpdate.newBuilder()
            .setBlock("plugin:kanban/board")
            .setScope("project:p1")
            .setTree(UiNode.newBuilder().setId("").setKind("column").build())
            .build()
        val last = BlockUpdate.newBuilder()
            .setBlock("plugin:kanban/board")
            .setScope("project:p1")
            .setGone(true)
            .build()
        val stream = FakeStream(
            listOf(
                Result.success(envelope { blockUpdate = first }),
                Result.success(envelope { blockUpdate = last }),
            )
        )

        val got = watchBlockFlow(
            open = { stream },
            send = FakeStream::send,
            recv = FakeStream::recv,
            node = "node-a",
            block = "plugin:kanban/board",
            scope = "project:p1",
        ).toList()

        assertEquals(1, stream.sent.size)
        val req = stream.sent.single().watchBlock
        assertEquals("node-a", req.nodeId)
        assertEquals("plugin:kanban/board", req.block)
        assertEquals("project:p1", req.scope)
        assertEquals(listOf(first, last), got)
        assertTrue(stream.closed)
    }

    @Test
    fun watchBlockFlowLeavesScopeEmptyForGlobalBlocks() = runBlocking {
        val update = BlockUpdate.newBuilder()
            .setBlock("plugin:ci-status/ci")
            .setGone(true)
            .build()
        val stream = FakeStream(listOf(Result.success(envelope { blockUpdate = update })))

        val got = watchBlockFlow(
            open = { stream },
            send = FakeStream::send,
            recv = FakeStream::recv,
            node = "node-a",
            block = "plugin:ci-status/ci",
        ).toList()

        assertEquals(1, stream.sent.size)
        val req = stream.sent.single().watchBlock
        assertEquals("node-a", req.nodeId)
        assertEquals("plugin:ci-status/ci", req.block)
        assertEquals("", req.scope)
        assertEquals(listOf(update), got)
        assertTrue(stream.closed)
    }
}
