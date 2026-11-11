package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.Approval
import dev.ironpark.mako.v2.ChatMessage
import dev.ironpark.mako.v2.Question
import dev.ironpark.mako.v2.QuestionOption
import dev.ironpark.mako.v2.SessionInfo
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class QuestionFormTest {
    private fun question(text: String, multi: Boolean = false) = Question.newBuilder().setQuestion(text).setMultiSelect(multi)
        .addAllOptions(listOf("A", "B", "C").map { QuestionOption.newBuilder().setLabel(it).build() }).build()

    private val questions = listOf(question("Which fruits?", multi = true), question("Which color?"))

    @Test fun aSingleChoiceTakesOneAndSeveralToggle() {
        var d = QuestionDraft(questions)
        assertFalse(d.answered(0))
        d = d.pick(questions, 0, 2).pick(questions, 0, 0).pick(questions, 0, 1).pick(questions, 0, 1)
        assertEquals(setOf(0, 2), d.picked[0])
        d = d.pick(questions, 1, 0).pick(questions, 1, 1)
        assertEquals(setOf(1), d.picked[1])
        assertTrue(d.answered(0) && d.answered(1))
    }

    @Test fun ownWordsAnswerAlone() {
        val d = QuestionDraft(questions).write(1, "  purple ")
        assertTrue(d.answered(1))
        assertFalse(QuestionDraft(questions).write(1, "  ").answered(1))
        val answers = d.pick(questions, 0, 2).pick(questions, 0, 0).answers()
        assertEquals(listOf(0, 2), answers[0].pickedList)
        assertEquals("", answers[0].text)
        assertEquals(emptyList<Int>(), answers[1].pickedList)
        assertEquals("purple", answers[1].text)
    }

    @Test fun keyedQuestionsComeFromThePendingCallOfANodeThatTypesThem() {
        val call = ChatMessage.newBuilder().setId("q").setRole("tool").setKind("question").setPending(true)
            .addAllQuestions(questions).build()
        val waiting = SessionInfo.newBuilder().setState("running").setStatus("waiting").setCaps(CAP_ANSWER_KEYS).build()
        assertEquals(questions, keyedQuestions(waiting, listOf(call)))
        assertEquals(emptyList<Question>(), keyedQuestions(waiting.toBuilder().setCaps(CAP_CHAT).build(), listOf(call)))
        assertEquals(emptyList<Question>(), keyedQuestions(waiting.toBuilder().setStatus("done").build(), listOf(call)))
        assertEquals(emptyList<Question>(), keyedQuestions(waiting, listOf(call.toBuilder().setPending(false).build())))
    }

    @Test fun anApprovalOfQuestionsAsks() {
        val asking = SessionInfo.newBuilder().setApproval(Approval.newBuilder().setId("a").addAllQuestions(questions)).build()
        assertTrue(asking.asks())
        assertFalse(SessionInfo.newBuilder().setApproval(Approval.newBuilder().setId("a")).build().asks())
        assertFalse(SessionInfo.getDefaultInstance().asks())
    }
}
