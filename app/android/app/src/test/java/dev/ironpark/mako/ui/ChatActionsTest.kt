package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.Command
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.SubAgent
import org.junit.Assert.*
import org.junit.Test

class ChatActionsTest {
    private val running = SessionInfo.newBuilder().setState("running").setStatus("working").build()
    private val question = ChatMessage.newBuilder().setId("q").setRole("tool").setKind("question").setPending(true).build()

    @Test fun subagentSummaryCountsOnlyLiveChildrenAndKeepsHistory() {
        val done = SubAgent.newBuilder().setId("done").build()
        val active = SubAgent.newBuilder().setId("active").setRunning(true).build()
        val agents = listOf(done, active)
        assertEquals(listOf(active) to listOf(done), partitionSubagents(running, agents))
        for (state in listOf("exited", "stopped")) {
            assertEquals(emptyList<SubAgent>() to agents,
                partitionSubagents(running.toBuilder().setState(state).build(), agents))
        }
        assertEquals(emptyList<SubAgent>() to emptyList<SubAgent>(), partitionSubagents(running, emptyList()))
    }

    @Test fun questionsOnlyOpenForCurrentUnansweredCalls() {
        assertEquals("question:q", questionRequest(running, listOf(question)))
        assertEquals("waiting", questionRequest(running.toBuilder().setStatus("waiting").build(), emptyList()))
        for (status in listOf("idle", "done")) {
            assertEquals("", questionRequest(running.toBuilder().setStatus(status).build(), listOf(question)))
        }
        assertEquals("", questionRequest(running.toBuilder().setState("exited").build(), listOf(question)))
        assertEquals("", questionRequest(running.toBuilder().setApproval(Approval.newBuilder().setId("a")).build(), listOf(question)))
        assertEquals("", questionRequest(running, listOf(question.toBuilder().setPending(false).build())))
        assertEquals("", questionRequest(running, listOf(question, ChatMessage.newBuilder().setRole("user").setText("continue").build())))
    }

    @Test fun paginationResolvesQuestionAndKeepsResult() {
        val result = ChatMessage.newBuilder().setId("q").setRole("tool").setOutput(true).setText("Option B").build()
        val joined = joinMessages(listOf(question), listOf(result))
        assertEquals(1, joined.size)
        assertEquals("Option B", joined.single().result)
        assertFalse(joined.single().pending)
        assertEquals("", questionRequest(running, joined))
        // An output outside this page's calls is still shown.
        assertEquals(listOf(result), joinMessages(emptyList(), listOf(result)))
        assertTrue(question.pending) // the input page is immutable
    }

    @Test fun commandCompletionNeverConsumesArgumentsOrOrdinaryText() {
        val commands = listOf("compact", "clear", "help", "clear").map { Command.newBuilder().setName(it).build() }
        assertEquals(listOf("clear", "compact", "help"), matchingCommands("/", commands).map { it.name })
        assertEquals(listOf("clear", "compact"), matchingCommands("/C", commands).map { it.name })
        for (text in listOf("hello", "/clear now", "/clear\n", " /clear", "/unknown")) {
            assertTrue(matchingCommands(text, commands).isEmpty())
        }
    }

    @Test fun everyBuiltInAgentHasItsOwnIcon() {
        val names = listOf("claude", "codex", "agy", "opencode", "omo", "omp", "pi")
        assertEquals(names.size, names.map { agentDrawable(it) }.filterNotNull().distinct().size)
        assertEquals(agentDrawable("agy"), agentDrawable("antigravity"))
        assertNull(agentDrawable("custom"))
    }
}
