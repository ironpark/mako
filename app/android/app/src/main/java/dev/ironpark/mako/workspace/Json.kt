package dev.ironpark.mako.workspace

/**
 * A reader of JSON as Go's encoding/json reads it, enough for what the
 * app reads of a computer's window layout documents, and the Go tests'
 * fixtures. Android's org.json is a stub where the unit tests run, on the
 * JVM.
 *
 * A text is checked whole first, as Go's decoder scans a value before it
 * decodes any of it: the same grammar, strict about numbers (no `.5`, no
 * `01`, no `+1`) and strings (no control characters), and the same bound
 * on nesting, [MAX_DEPTH], all without recursion. A text that fails throws
 * [IllegalArgumentException]. A value is then read only as far as its
 * reader asks ([Raw], as Go's json.RawMessage), so that what a reader does
 * not need is skipped, however deep, as Go skips it.
 */
internal object Json {
    /** How deep objects and arrays nest, at most: encoding/json's maxNestingDepth. */
    const val MAX_DEPTH = 10_000

    /** The one JSON value text holds, unread. */
    fun raw(text: String): Raw {
        val start = space(text, 0)
        val end = check(text, start)
        require(space(text, end) == text.length) { "more than one JSON value" }
        return Raw(text, start, end)
    }

    /**
     * The one JSON value text holds, read whole: objects as maps, in their
     * order, arrays as lists, strings, numbers as doubles, booleans and
     * null. It recurses as deep as the value nests; it is for the tests'
     * fixtures, which nest a few levels.
     */
    fun parse(text: String): Any? = raw(text).decode()

    /**
     * Raw is a value of a checked text, unread. A reader asks for what it
     * needs, a level at a time: a member or an item is found by skipping
     * what is before it, which is a count of brackets, not a reading.
     */
    class Raw internal constructor(private val s: String, private val start: Int, private val end: Int) {
        val isNull: Boolean get() = s[start] == 'n'

        /** The value's text as it is written. */
        val text: String get() = s.substring(start, end)

        /** The string, null for a value of another type. */
        fun string(): String? = if (s[start] == '"') unquote(s, start) else null

        fun bool(): Boolean? = when (s[start]) {
            't' -> true
            'f' -> false
            else -> null
        }

        /** The number's literal, null for a value of another type. */
        fun number(): String? = if (s[start] == '-' || s[start] in '0'..'9') text else null

        /** An object's members, null for a value of another type; of a key twice, the last, as Go reads it into a map. */
        fun members(): Map<String, Raw>? {
            if (s[start] != '{') return null
            val out = linkedMapOf<String, Raw>()
            var i = space(s, start + 1)
            if (s[i] == '}') return out
            while (true) {
                val key = unquote(s, i)
                i = space(s, skip(s, i))
                i = space(s, i + 1) // past ':'
                val e = skip(s, i)
                out[key] = Raw(s, i, e)
                i = space(s, e)
                if (s[i++] == '}') return out
                i = space(s, i)
            }
        }

        /** An array's items, null for a value of another type. */
        fun items(): List<Raw>? {
            if (s[start] != '[') return null
            val out = mutableListOf<Raw>()
            var i = space(s, start + 1)
            if (s[i] == ']') return out
            while (true) {
                val e = skip(s, i)
                out += Raw(s, i, e)
                i = space(s, e)
                if (s[i++] == ']') return out
                i = space(s, i)
            }
        }

        /**
         * Whether a number anywhere in the value is beyond a double, which Go
         * fails to decode into one, as it decodes a block's props: a scan of
         * the text, however deep it nests.
         */
        fun overflows(): Boolean {
            var i = start
            while (i < end) {
                val c = s[i]
                when {
                    c == '"' -> i = skip(s, i)
                    c == '-' || c in '0'..'9' -> {
                        val e = skip(s, i)
                        if (s.substring(i, e).toDouble().isInfinite()) return true
                        i = e
                    }
                    else -> i++
                }
            }
            return false
        }

        internal fun decode(): Any? = when (s[start]) {
            '{' -> members()!!.mapValues { it.value.decode() }
            '[' -> items()!!.map { it.decode() }
            '"' -> string()
            't' -> true
            'f' -> false
            'n' -> null
            else -> text.toDouble()
        }
    }

    private fun space(s: String, from: Int): Int {
        var i = from
        while (i < s.length && s[i] in " \t\r\n") i++
        return i
    }

    /**
     * Checks the value at start, and where it ends: a loop over a stack of
     * the objects and arrays open, as Go's scanner is, so that nesting
     * costs no recursion.
     */
    private fun check(s: String, start: Int): Int {
        val open = StringBuilder()
        var i = start
        while (true) {
            // A value at i.
            require(i < s.length) { "JSON ends early" }
            when (val c = s[i]) {
                '{', '[' -> {
                    require(open.length < MAX_DEPTH) { "JSON nested deeper than $MAX_DEPTH" }
                    open.append(c)
                    i = space(s, i + 1)
                    if (i < s.length && s[i] == (if (c == '{') '}' else ']')) {
                        open.setLength(open.length - 1)
                        i++
                    } else {
                        if (c == '{') i = key(s, i)
                        continue
                    }
                }
                '"' -> i = string(s, i)
                't' -> i = word(s, i, "true")
                'f' -> i = word(s, i, "false")
                'n' -> i = word(s, i, "null")
                else -> i = number(s, i)
            }
            // After a value: the next of its object's or array's, or their ends.
            while (true) {
                if (open.isEmpty()) return i
                i = space(s, i)
                require(i < s.length) { "JSON ends early" }
                val inObject = open.last() == '{'
                val c = s[i++]
                if (c == ',') {
                    i = space(s, i)
                    if (inObject) i = key(s, i)
                    break
                }
                require(c == (if (inObject) '}' else ']')) { "bad JSON at ${i - 1}" }
                open.setLength(open.length - 1)
            }
        }
    }

    /** Checks an object's key and its colon, and where its value starts. */
    private fun key(s: String, at: Int): Int {
        require(at < s.length && s[at] == '"') { "bad JSON at $at: want a key" }
        val i = space(s, string(s, at))
        require(i < s.length && s[i] == ':') { "bad JSON at $i: want :" }
        return space(s, i + 1)
    }

    private fun word(s: String, at: Int, w: String): Int {
        require(s.startsWith(w, at)) { "bad JSON at $at" }
        return at + w.length
    }

    /** Checks a number as JSON writes one, -?(0|[1-9][0-9]*)(.[0-9]+)?([eE][+-]?[0-9]+)?, and where it ends. */
    private fun number(s: String, at: Int): Int {
        var i = at
        fun digits(): Int {
            val from = i
            while (i < s.length && s[i] in '0'..'9') i++
            return i - from
        }
        if (i < s.length && s[i] == '-') i++
        when {
            i < s.length && s[i] == '0' -> i++
            i < s.length && s[i] in '1'..'9' -> digits()
            else -> throw IllegalArgumentException("bad JSON number at $at")
        }
        if (i < s.length && s[i] == '.') {
            i++
            require(digits() > 0) { "bad JSON number at $at" }
        }
        if (i < s.length && (s[i] == 'e' || s[i] == 'E')) {
            i++
            if (i < s.length && (s[i] == '+' || s[i] == '-')) i++
            require(digits() > 0) { "bad JSON number at $at" }
        }
        return i
    }

    /** Checks a string, and where it ends. */
    private fun string(s: String, at: Int): Int {
        var i = at + 1
        while (true) {
            require(i < s.length) { "JSON ends in a string" }
            val c = s[i]
            when {
                c == '"' -> return i + 1
                c < ' ' -> throw IllegalArgumentException("a control character in a JSON string at $i")
                c != '\\' -> i++
                else -> {
                    require(i + 1 < s.length) { "JSON ends in a string" }
                    when (s[i + 1]) {
                        '"', '\\', '/', 'b', 'f', 'n', 'r', 't' -> i += 2
                        'u' -> {
                            require(i + 6 <= s.length && (i + 2 until i + 6).all { hex(s[it]) >= 0 }) { "bad JSON escape at $i" }
                            i += 6
                        }
                        else -> throw IllegalArgumentException("bad JSON escape at $i")
                    }
                }
            }
        }
    }

    private fun hex(c: Char): Int = when (c) {
        in '0'..'9' -> c - '0'
        in 'a'..'f' -> c - 'a' + 10
        in 'A'..'F' -> c - 'A' + 10
        else -> -1
    }

    /** Where the value at i, of a checked text, ends. */
    private fun skip(s: String, at: Int): Int {
        var i = at
        when (s[i]) {
            '"' -> {
                i++
                while (s[i] != '"') i += if (s[i] == '\\') 2 else 1
                return i + 1
            }
            '{', '[' -> {
                var depth = 0
                while (true) {
                    when (s[i]) {
                        '"' -> {
                            i = skip(s, i)
                            continue
                        }
                        '{', '[' -> depth++
                        '}', ']' -> if (--depth == 0) return i + 1
                    }
                    i++
                }
            }
            else -> {
                while (i < s.length && s[i] !in ",:]} \t\r\n") i++
                return i
            }
        }
    }

    /** The string at i, of a checked text, its escapes read. */
    private fun unquote(s: String, at: Int): String {
        val out = StringBuilder()
        var i = at + 1
        while (true) {
            val c = s[i++]
            if (c == '"') return out.toString()
            if (c != '\\') {
                out.append(c)
                continue
            }
            when (val e = s[i++]) {
                'u' -> {
                    out.append(((hex(s[i]) shl 12) or (hex(s[i + 1]) shl 8) or (hex(s[i + 2]) shl 4) or hex(s[i + 3])).toChar())
                    i += 4
                }
                'n' -> out.append('\n')
                't' -> out.append('\t')
                'r' -> out.append('\r')
                'b' -> out.append('\b')
                'f' -> out.append('\u000c')
                else -> out.append(e) // '"', '\\' or '/'
            }
        }
    }
}
