package dev.ironpark.mako.workspace

/**
 * A reader of the JSON the Go fixtures hold, enough for them: objects as
 * maps, arrays as lists, strings, numbers as doubles, booleans and null.
 * The unit tests run on the JVM, where Android's org.json is a stub.
 */
internal class Json private constructor(private val s: String) {
    private var i = 0

    private fun value(): Any? {
        space()
        return when (s[i]) {
            '{' -> obj()
            '[' -> arr()
            '"' -> str()
            't' -> word("true", true)
            'f' -> word("false", false)
            'n' -> word("null", null)
            else -> num()
        }
    }

    private fun space() {
        while (i < s.length && s[i].isWhitespace()) i++
    }

    private fun word(w: String, v: Any?): Any? {
        check(s.startsWith(w, i)) { "bad JSON at $i" }
        i += w.length
        return v
    }

    private fun num(): Double {
        val start = i
        while (i < s.length && (s[i].isDigit() || s[i] in "+-.eE")) i++
        return s.substring(start, i).toDouble()
    }

    private fun str(): String {
        i++ // "
        val out = StringBuilder()
        while (s[i] != '"') {
            val c = s[i++]
            if (c != '\\') {
                out.append(c)
                continue
            }
            when (val e = s[i++]) {
                'u' -> {
                    out.append(s.substring(i, i + 4).toInt(16).toChar())
                    i += 4
                }
                'n' -> out.append('\n')
                't' -> out.append('\t')
                'r' -> out.append('\r')
                'b' -> out.append('\b')
                'f' -> out.append('\u000c')
                else -> out.append(e)
            }
        }
        i++
        return out.toString()
    }

    private fun obj(): Map<String, Any?> {
        i++
        val out = linkedMapOf<String, Any?>()
        space()
        if (s[i] == '}') return out.also { i++ }
        while (true) {
            space()
            val k = str()
            space()
            check(s[i++] == ':')
            out[k] = value()
            space()
            if (s[i++] == '}') return out
        }
    }

    private fun arr(): List<Any?> {
        i++
        val out = mutableListOf<Any?>()
        space()
        if (s[i] == ']') return out.also { i++ }
        while (true) {
            out += value()
            space()
            if (s[i++] == ']') return out
        }
    }

    companion object {
        fun parse(text: String): Any? = Json(text).value()
    }
}
