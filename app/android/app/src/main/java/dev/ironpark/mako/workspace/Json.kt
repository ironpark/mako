package dev.ironpark.mako.workspace

/**
 * A reader of JSON, enough for what the app reads of a computer's
 * workspace, its window layout documents, and the Go tests' fixtures:
 * objects as maps, in their order, arrays as lists, strings, numbers as
 * doubles, booleans and null. Android's org.json is a stub where the unit
 * tests run, on the JVM. A text that is not one JSON value throws
 * [IllegalArgumentException], or one nested past [MAX_DEPTH].
 */
internal class Json private constructor(private val s: String) {
    private var i = 0
    private var depth = 0

    private fun value(): Any? {
        space()
        require(i < s.length) { "JSON ends early" }
        return when (s[i]) {
            '{' -> nested { obj() }
            '[' -> nested { arr() }
            '"' -> str()
            't' -> word("true", true)
            'f' -> word("false", false)
            'n' -> word("null", null)
            else -> num()
        }
    }

    private inline fun <T> nested(read: () -> T): T {
        require(++depth <= MAX_DEPTH) { "JSON nested deeper than $MAX_DEPTH" }
        return read().also { depth-- }
    }

    private fun space() {
        while (i < s.length && s[i] in " \t\r\n") i++
    }

    private fun expect(c: Char) {
        require(i < s.length && s[i] == c) { "bad JSON at $i: want $c" }
        i++
    }

    private fun word(w: String, v: Any?): Any? {
        require(s.startsWith(w, i)) { "bad JSON at $i" }
        i += w.length
        return v
    }

    private fun num(): Double {
        val start = i
        while (i < s.length && (s[i].isDigit() || s[i] in "+-.eE")) i++
        return s.substring(start, i).toDoubleOrNull() ?: throw IllegalArgumentException("bad JSON number at $start")
    }

    private fun str(): String {
        expect('"')
        val out = StringBuilder()
        while (true) {
            require(i < s.length) { "JSON ends in a string" }
            val c = s[i++]
            if (c == '"') break
            if (c != '\\') {
                out.append(c)
                continue
            }
            require(i < s.length) { "JSON ends in a string" }
            when (val e = s[i++]) {
                'u' -> {
                    require(i + 4 <= s.length) { "JSON ends in a string" }
                    out.append(s.substring(i, i + 4).toIntOrNull(16)?.toChar() ?: throw IllegalArgumentException("bad JSON escape at $i"))
                    i += 4
                }
                'n' -> out.append('\n')
                't' -> out.append('\t')
                'r' -> out.append('\r')
                'b' -> out.append('\b')
                'f' -> out.append('\u000c')
                '"', '\\', '/' -> out.append(e)
                else -> throw IllegalArgumentException("bad JSON escape at $i")
            }
        }
        return out.toString()
    }

    private fun obj(): Map<String, Any?> {
        expect('{')
        val out = linkedMapOf<String, Any?>()
        space()
        if (i < s.length && s[i] == '}') return out.also { i++ }
        while (true) {
            space()
            val k = str()
            space()
            expect(':')
            out[k] = value()
            space()
            require(i < s.length) { "JSON ends in an object" }
            when (s[i++]) {
                '}' -> return out
                ',' -> {}
                else -> throw IllegalArgumentException("bad JSON at ${i - 1}")
            }
        }
    }

    private fun arr(): List<Any?> {
        expect('[')
        val out = mutableListOf<Any?>()
        space()
        if (i < s.length && s[i] == ']') return out.also { i++ }
        while (true) {
            out += value()
            space()
            require(i < s.length) { "JSON ends in an array" }
            when (s[i++]) {
                ']' -> return out
                ',' -> {}
                else -> throw IllegalArgumentException("bad JSON at ${i - 1}")
            }
        }
    }

    companion object {
        /** How deep objects and arrays nest, at most. */
        const val MAX_DEPTH = 64

        /** The one JSON value text holds. */
        fun parse(text: String): Any? {
            val j = Json(text)
            val v = j.value()
            j.space()
            require(j.i == text.length) { "more than one JSON value" }
            return v
        }
    }
}
