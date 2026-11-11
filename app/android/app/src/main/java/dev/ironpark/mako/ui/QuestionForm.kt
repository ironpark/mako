package dev.ironpark.mako.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Question
import dev.ironpark.mako.v2.QuestionAnswer

/**
 * QuestionDraft is what has been answered so far to questions, one entry
 * each: the options picked, by index, and words of the user's own.
 */
internal data class QuestionDraft(val picked: List<Set<Int>>, val text: List<String>) {
    constructor(questions: List<Question>) : this(List(questions.size) { emptySet() }, List(questions.size) { "" })

    /** The draft with option of question i picked: one more of several, or in place of the one before. */
    fun pick(questions: List<Question>, i: Int, option: Int): QuestionDraft {
        val now = picked[i]
        val next = when {
            !questions[i].multiSelect -> setOf(option)
            option in now -> now - option
            else -> now + option
        }
        return copy(picked = picked.toMutableList().also { it[i] = next })
    }

    /** The draft with words of the user's own for question i. */
    fun write(i: Int, words: String): QuestionDraft = copy(text = text.toMutableList().also { it[i] = words })

    /** Whether question i has an answer. */
    fun answered(i: Int): Boolean = picked[i].isNotEmpty() || text[i].isNotBlank()

    /** The answers, one for each question, in order, options in the order they are offered. */
    fun answers(): List<QuestionAnswer> = picked.indices.map { i ->
        QuestionAnswer.newBuilder().addAllPicked(picked[i].sorted()).setText(text[i].trim()).build()
    }
}

/**
 * QuestionForm asks questions a step at a time, as the agent's own dialog
 * would: an option picked, several where the question allows, or words of
 * the user's own; and gives onSubmit the answers once each has one.
 */
@Composable
fun QuestionForm(questions: List<Question>, onSubmit: (List<QuestionAnswer>) -> Unit, modifier: Modifier = Modifier) {
    var step by remember(questions) { mutableIntStateOf(0) }
    var draft by remember(questions) { mutableStateOf(QuestionDraft(questions)) }
    if (questions.isEmpty()) return
    val q = questions[step]
    val colors = MaterialTheme.colorScheme
    Column(modifier, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(
            listOfNotNull(
                if (questions.size > 1) stringResource(R.string.question_progress, step + 1, questions.size) else null,
                q.header.ifEmpty { null },
            ).joinToString(" · "),
            style = MaterialTheme.typography.labelMedium, color = colors.onSurfaceVariant,
        )
        Text(q.question, style = MaterialTheme.typography.titleMedium, modifier = Modifier.padding(bottom = 8.dp))
        q.optionsList.forEachIndexed { j, o ->
            val on = j in draft.picked[step]
            val select = if (q.multiSelect) Modifier.toggleable(on, role = Role.Checkbox) { draft = draft.pick(questions, step, j) }
            else Modifier.selectable(on, role = Role.RadioButton) { draft = draft.pick(questions, step, j) }
            Row(select.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                if (q.multiSelect) Checkbox(on, onCheckedChange = null) else RadioButton(on, onClick = null)
                Spacer(Modifier.width(12.dp))
                Column(Modifier.weight(1f)) {
                    Text(o.label, style = MaterialTheme.typography.bodyLarge)
                    if (o.description.isNotEmpty()) {
                        Text(o.description, style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant)
                    }
                }
            }
        }
        OutlinedTextField(
            draft.text[step], { draft = draft.write(step, it) },
            label = { Text(stringResource(R.string.question_other)) },
            placeholder = { Text(stringResource(R.string.question_other_hint)) },
            modifier = Modifier.fillMaxWidth().padding(top = 4.dp),
        )
        Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
            if (step > 0) TextButton(onClick = { step-- }) { Text(stringResource(R.string.back)) }
            val last = step == questions.lastIndex
            Button(
                onClick = { if (last) onSubmit(draft.answers()) else step++ },
                enabled = draft.answered(step),
            ) { Text(stringResource(if (last) R.string.answer else R.string.next)) }
        }
    }
}
