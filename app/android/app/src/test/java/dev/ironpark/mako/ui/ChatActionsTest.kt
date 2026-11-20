package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.BackgroundTask
import dev.ironpark.mako.v2.Chat
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.Command
import dev.ironpark.mako.v2.SessionInfo
import dev.ironpark.mako.v2.SubAgent
import dev.ironpark.mako.v2.Usage
import org.junit.Assert.*
import org.junit.Test

class ChatActionsTest {
    private val running = SessionInfo.newBuilder().setState("running").setStatus("working").build()
    private val question = ChatMessage.newBuilder().setId("q").setRole("tool").setKind("question").setPending(true).build()

    private fun userMsg(text: String, id: String = ""): ChatMessage =
        ChatMessage.newBuilder().setRole("user").setText(text).setId(id).build()

    private fun assistantMsg(text: String, id: String = ""): ChatMessage =
        ChatMessage.newBuilder().setRole("assistant").setText(text).setId(id).build()

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

    @Test fun applyChatPatchHandlesNormalDropKeepMismatchUnchangedAndClamp() {
        val a = userMsg("a", "1")
        val b = assistantMsg("b", "2")
        val c = userMsg("c", "3")
        val d = assistantMsg("d", "4")
        val previous = Chat.newBuilder()
            .setSupported(true)
            .setDigest(100L)
            .setStart(10L)
            .addAllMessages(listOf(a, b, c))
            .build()

        // 1. Unchanged preserves previous snapshot (or returns reply when previous is null).
        val unchangedReply = Chat.newBuilder().setDigest(100L).setUnchanged(true).build()
        assertSame(previous, applyChatPatch(previous, unchangedReply))
        assertSame(unchangedReply, applyChatPatch(null, unchangedReply))

        // 2. Normal append patch (drop = 0, keep = 3) preserves previous messages and updates metadata.
        val updatedUsage = Usage.newBuilder().setContext(500).setWindow(1000).build()
        val appendPatch = Chat.newBuilder()
            .setSupported(true)
            .setDigest(101L)
            .setStart(10L)
            .setBase(100L)
            .setDrop(0)
            .setKeep(3)
            .setUsage(updatedUsage)
            .addMessages(d)
            .build()
        val appended = applyChatPatch(previous, appendPatch)
        assertEquals(listOf(a, b, c, d), appended.messagesList)
        assertEquals(101L, appended.digest)
        assertEquals(updatedUsage, appended.usage)
        assertEquals(0L, appended.base)
        assertEquals(0, appended.drop)
        assertEquals(0, appended.keep)

        // 3. Sliding window drop + keep patch (drop = 1, keep = 2, append d -> [b, c, d]).
        val slidePatch = Chat.newBuilder()
            .setSupported(true)
            .setDigest(102L)
            .setStart(25L)
            .setBase(100L)
            .setDrop(1)
            .setKeep(2)
            .addMessages(d)
            .build()
        val slid = applyChatPatch(previous, slidePatch)
        assertEquals(listOf(b, c, d), slid.messagesList)
        assertEquals(25L, slid.start)
        assertEquals(0L, slid.base)

        // 4. Base mismatch falls back cleanly to reply without crashing.
        val mismatchReply = Chat.newBuilder()
            .setSupported(true)
            .setDigest(103L)
            .setBase(999L)
            .setDrop(1)
            .setKeep(2)
            .addMessages(d)
            .build()
        val mismatched = applyChatPatch(previous, mismatchReply)
        assertEquals(listOf(d), mismatched.messagesList)
        assertEquals(103L, mismatched.digest)
        assertEquals(0L, mismatched.base)

        // 5. Out-of-bounds drop/keep values clamp safely without IndexOutOfBoundsException.
        val clampedHigh = applyChatPatch(
            previous,
            Chat.newBuilder().setDigest(104L).setBase(100L).setDrop(2).setKeep(50).addMessages(d).build(),
        )
        assertEquals(listOf(c, d), clampedHigh.messagesList)

        val clampedNegative = applyChatPatch(
            previous,
            Chat.newBuilder().setDigest(105L).setBase(100L).setDrop(-5).setKeep(-2).addMessages(d).build(),
        )
        assertEquals(listOf(d), clampedNegative.messagesList)

        val clampedDropPastEnd = applyChatPatch(
            previous,
            Chat.newBuilder().setDigest(106L).setBase(100L).setDrop(99).setKeep(10).addMessages(d).build(),
        )
        assertEquals(listOf(d), clampedDropPastEnd.messagesList)
    }

    @Test fun pendingMessagesSettleByAnchorExactMatchCountAheadAndExpiry() {
        val t0 = 1_000_000L
        val u1 = userMsg("hello")
        val a1 = assistantMsg("hi")
        val u2 = userMsg("hello") // duplicate text tests occurrence anchoring
        val baseMessages = listOf(u1, a1, u2)

        // Recording blank text is a no-op.
        assertTrue(notePending(emptyList(), baseMessages, "   ", nowMs = t0, id = 1L).isEmpty())

        // Anchor records the 2nd occurrence of "hello".
        val p1List = notePending(emptyList(), baseMessages, "fix tests", nowMs = t0, id = 1L)
        assertEquals(1, p1List.size)
        assertEquals(messageKey(u2), p1List[0].afterKey)
        assertEquals(2, p1List[0].occurrence)
        assertEquals(0, p1List[0].ahead)

        // Before the new message appears in the transcript, p1 stays pending.
        assertEquals(p1List, settlePending(p1List, baseMessages, nowMs = t0 + 5_000L))

        // Exact text match after the anchor settles p1.
        val withReply = baseMessages + userMsg("  fix tests  ")
        assertTrue(settlePending(p1List, withReply, nowMs = t0 + 1_000L).isEmpty())

        // Count-ahead match: two pending messages sent before transcript catches up.
        val p2List = notePending(p1List, baseMessages, "second prompt", nowMs = t0 + 100L, id = 2L)
        assertEquals(2, p2List.size)
        assertEquals(1, p2List[1].ahead)

        // One user message after anchor (even reformatted) settles p1 (ahead=0) and keeps p2 (ahead=1).
        val oneLogged = baseMessages + userMsg("reformatted first")
        val afterOne = settlePending(p2List, oneLogged, nowMs = t0 + 1_000L)
        assertEquals(1, afterOne.size)
        assertEquals(2L, afterOne.single().id)

        // Second user message after anchor settles p2 as well.
        val twoLogged = oneLogged + userMsg("reformatted second")
        assertTrue(settlePending(afterOne, twoLogged, nowMs = t0 + 2_000L).isEmpty())

        // Slash command expires after 10 seconds, whereas normal prompts wait 2 minutes.
        val cmdList = notePending(emptyList(), baseMessages, "/compact", nowMs = t0, id = 3L)
        assertEquals(1, settlePending(cmdList, baseMessages, nowMs = t0 + PENDING_COMMAND_MS).size)
        assertTrue(settlePending(cmdList, baseMessages, nowMs = t0 + PENDING_COMMAND_MS + 1L).isEmpty())

        assertEquals(1, settlePending(p1List, baseMessages, nowMs = t0 + PENDING_PROMPT_MS).size)
        assertTrue(settlePending(p1List, baseMessages, nowMs = t0 + PENDING_PROMPT_MS + 1L).isEmpty())
    }

    @Test fun usageFormattersHandleLimitsResetsAndCosts() {
        assertEquals("Quota", limitLabel(0L))
        assertEquals("Quota", limitLabel(-60L))
        assertEquals("할당량", limitLabel(0L, quotaLabel = "할당량"))
        assertEquals("5h", limitLabel(5L * 60L))
        assertEquals("Week", limitLabel(7L * 24L * 60L))
        assertEquals("1d", limitLabel(24L * 60L))
        assertEquals("30d", limitLabel(30L * 24L * 60L))
        assertEquals("2h", limitLabel(120L))
        assertEquals("90m", limitLabel(90L))

        val nowMs = 1_700_000_000_000L
        val nowSec = nowMs / 1000L
        assertEquals("", formatReset(0L, nowMs))
        assertEquals("", formatReset(nowSec, nowMs))
        assertEquals("", formatReset(nowSec - 10L, nowMs))
        assertEquals("<1m", formatReset(nowSec + 30L, nowMs))
        assertEquals("15m", formatReset(nowSec + 15L * 60L, nowMs))
        assertEquals("2h", formatReset(nowSec + 2L * 3600L, nowMs))
        assertEquals("2h 14m", formatReset(nowSec + 2L * 3600L + 14L * 60L, nowMs))
        assertEquals("1d 2h", formatReset(nowSec + 26L * 3600L, nowMs))
        assertEquals("2d", formatReset(nowSec + 48L * 3600L, nowMs))

        assertEquals("", formatCost(0.0))
        assertEquals("", formatCost(-1.0))
        assertEquals("", formatCost(Double.NaN))
        assertEquals("<$0.01", formatCost(0.004))
        assertEquals("$0.12", formatCost(0.12))
        assertEquals("$1.50", formatCost(1.5))
    }

    @Test fun liveToolArgsPreviewAndBackgroundTaskSorting() {
        assertEquals("Check repo status", liveToolArgsPreview("""{"command":"git status","description":"Check repo status"""))
        assertEquals("git status", liveToolArgsPreview("{\"command\":\"git status\\"))
        assertEquals("echo \"hi\"", liveToolArgsPreview("{\"command\":\"echo \\\"hi\\\"\"}"))
        assertEquals("/tmp/a.kt", liveToolArgsPreview("""{"file_path":"/tmp/a.kt","old_string":"foo"}"""))
        assertEquals("", liveToolArgsPreview("   "))

        val t1 = BackgroundTask.newBuilder().setId("1").setRunning(false).build()
        val t2 = BackgroundTask.newBuilder().setId("2").setRunning(true).build()
        val t3 = BackgroundTask.newBuilder().setId("3").setRunning(false).build()
        val t4 = BackgroundTask.newBuilder().setId("4").setRunning(true).build()
        assertEquals(listOf("4", "2", "3", "1"), sortBackgroundTasks(listOf(t1, t2, t3, t4)).map { it.id })
    }
}
