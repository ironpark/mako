package policy

import (
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// shellLangs are the shells a command line is read as. Agents run their
// commands with bash or zsh, the user's own shell among them, and a line
// is only as simple as the less simple reading of it.
var shellLangs = []syntax.LangVariant{syntax.LangBash, syntax.LangZsh}

// maxShellDepth is how many times over eval and sh -c are read into.
const maxShellDepth = 4

// shellCommands are the commands a command line runs, as bash and zsh read
// it: each command of a list, a pipeline, a subshell, a function or a
// control structure, of a substitution in double quotes or out of them,
// and those eval and sh -c are given to run. Each is there as the line
// spells it, as its words read without their quotes, and without a wrapper
// such as env, sudo or nohup in front of it.
//
// simple is false for a line that is more than plain commands joined by
// newlines, ;, &, &&, || or |, each of plain words: text, or text in single
// or double quotes. A substitution, an expansion ($'...' among them), an
// assignment, a redirection, a comment, a control structure or a line
// either shell cannot read makes it more. An allow pattern vouches only
// for a simple line; a deny pattern reads the commands found in any line,
// though it cannot read what an expansion leaves until it runs.
func shellCommands(line string) (cmds []string, simple bool) {
	r := shellReader{seen: map[string]bool{}}
	simple = r.read(line, 0)
	return r.cmds, simple
}

type shellReader struct {
	cmds []string
	seen map[string]bool
}

// add adds c, its whitespace collapsed, unless it is blank or there.
func (r *shellReader) add(c string) {
	c = strings.Join(strings.Fields(c), " ")
	if c != "" && !r.seen[c] {
		r.seen[c] = true
		r.cmds = append(r.cmds, c)
	}
}

// read adds the commands line runs, read depth times into eval and sh -c
// already, and says whether it is simple, those it runs that way too.
func (r *shellReader) read(line string, depth int) (simple bool) {
	simple = true
	for _, lang := range shellLangs {
		p := syntax.NewParser(syntax.Variant(lang), syntax.KeepComments(true))
		// A shell runs each line it read before one it cannot read, so
		// the statements read before an error are read as well.
		for st, err := range p.StmtsSeq(strings.NewReader(line)) {
			if err != nil {
				simple = false
				break
			}
			if !plainStmt(st) {
				simple = false
			}
			syntax.Walk(st, func(n syntax.Node) bool {
				if call, ok := n.(*syntax.CallExpr); ok && len(call.Args) > 0 && !r.call(line, call, depth) {
					simple = false
				}
				return true
			})
		}
	}
	return simple
}

// call adds the command call runs, and those it has eval or a shell run;
// false when those are not simple, or too deep to read.
func (r *shellReader) call(line string, call *syntax.CallExpr, depth int) bool {
	r.add(source(line, call))
	argv := make([]string, len(call.Args))
	for i, w := range call.Args {
		argv[i] = wordText(line, w)
	}
	r.add(strings.Join(argv, " "))
	argv = unwrap(argv)
	r.add(strings.Join(argv, " "))
	script, ok := nestedScript(argv)
	if !ok {
		return true
	}
	if depth >= maxShellDepth {
		return false
	}
	return r.read(script, depth+1)
}

// plainStmt says st is a plain command, or plain commands joined by &&,
// || or |, with no redirection or comment.
func plainStmt(st *syntax.Stmt) bool {
	if st.Negated || st.Coprocess || st.Disown || len(st.Redirs) > 0 || len(st.Comments) > 0 {
		return false
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		return len(c.Assigns) == 0 && len(c.Args) > 0 && !slices.ContainsFunc(c.Args, func(w *syntax.Word) bool { return !plainWord(w) })
	case *syntax.BinaryCmd:
		return (c.Op == syntax.AndStmt || c.Op == syntax.OrStmt || c.Op == syntax.Pipe) && plainStmt(c.X) && plainStmt(c.Y)
	}
	return false
}

// plainWord says w is text, or text in single or double quotes, which no
// shell expands into anything it runs.
func plainWord(w *syntax.Word) bool {
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if p.Dollar || slices.ContainsFunc(p.Parts, func(q syntax.WordPart) bool { _, lit := q.(*syntax.Lit); return !lit }) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// source is the text of line that n spans.
func source(line string, n syntax.Node) string {
	from, to := int(n.Pos().Offset()), int(n.End().Offset())
	if from < 0 || to > len(line) || from > to {
		return ""
	}
	return line[from:to]
}

// wordText is w as the shell reads it, its quotes gone; of what it
// expands, the text that spells it.
func wordText(line string, w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		partText(&b, line, part, false)
	}
	return b.String()
}

func partText(b *strings.Builder, line string, part syntax.WordPart, quoted bool) {
	switch p := part.(type) {
	case *syntax.Lit:
		b.WriteString(unescape(p.Value, quoted))
	case *syntax.SglQuoted:
		if p.Dollar {
			b.WriteString(ansiC(p.Value))
		} else {
			b.WriteString(p.Value)
		}
	case *syntax.DblQuoted:
		for _, q := range p.Parts {
			partText(b, line, q, true)
		}
	default:
		b.WriteString(source(line, p))
	}
}

// unescape is s with the backslashes the shell removes removed: before any
// rune out of quotes, and in double quotes before $, `, " and \.
func unescape(s string, quoted bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) && (!quoted || strings.ContainsRune("$`\"\\", rs[i+1])) {
			i++
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// ansiEscapes are the escapes of $'...' that stand for one rune.
var ansiEscapes = map[byte]string{
	'a': "\a", 'b': "\b", 'e': "\x1b", 'E': "\x1b", 'f': "\f", 'n': "\n",
	'r': "\r", 't': "\t", 'v': "\v", '\\': `\`, '\'': "'", '"': `"`, '?': "?",
}

// ansiC is the text of $'s', its escapes read as bash reads them.
func ansiC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		if r, ok := ansiEscapes[s[i]]; ok {
			b.WriteString(r)
			continue
		}
		// \xHH, \uHHHH, \UHHHHHHHH, and \NNN in octal.
		base, digits, from := 16, 0, i+1
		switch s[i] {
		case 'x':
			digits = 2
		case 'u':
			digits = 4
		case 'U':
			digits = 8
		default:
			base, digits, from = 8, 3, i
		}
		to := from
		for to < len(s) && to-from < digits && isDigit(s[to], base) {
			to++
		}
		if to == from {
			b.WriteByte('\\')
			b.WriteByte(s[i])
			continue
		}
		n, _ := strconv.ParseUint(s[from:to], base, 32)
		if s[i] == 'u' || s[i] == 'U' {
			b.WriteRune(rune(n))
		} else {
			b.WriteByte(byte(n))
		}
		i = to - 1
	}
	return b.String()
}

// isDigit says c is a digit of base 8 or 16.
func isDigit(c byte, base int) bool {
	if base == 8 {
		return '0' <= c && c <= '7'
	}
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// wrappers are commands that run the command their words name after their
// own options.
var wrappers = map[string]bool{
	"builtin": true, "command": true, "exec": true, "nohup": true, "time": true,
	"env": true, "nice": true, "timeout": true, "stdbuf": true, "xargs": true,
	"sudo": true, "doas": true,
}

// unwrap is argv without the wrappers in front of the command it runs, nor
// their options.
func unwrap(argv []string) []string {
	for len(argv) > 0 && wrappers[path.Base(argv[0])] {
		w := path.Base(argv[0])
		argv = argv[1:]
		for len(argv) > 0 {
			a := argv[0]
			if a == "--" {
				argv = argv[1:]
				break
			}
			if !strings.HasPrefix(a, "-") && !isAssign(a) && (a == "" || !unicode.IsDigit(rune(a[0]))) {
				break
			}
			argv = argv[1:]
			// sudo's and doas's options that take a value.
			if (w == "sudo" || w == "doas") && len(argv) > 0 && slices.Contains([]string{"-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U"}, a) {
				argv = argv[1:]
			}
		}
	}
	return argv
}

// isAssign says a is NAME=value.
func isAssign(a string) bool {
	name, _, ok := strings.Cut(a, "=")
	return ok && name != "" && !strings.ContainsFunc(name, func(r rune) bool {
		return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) && !unicode.IsDigit(rune(name[0]))
}

// shells are the shells whose -c runs the script after it.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true}

// nestedScript is the line argv has run as a script of its own: eval's
// words, or the script a shell's -c names.
func nestedScript(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	if argv[0] == "eval" {
		return strings.Join(argv[1:], " "), len(argv) > 1
	}
	if !shells[path.Base(argv[0])] {
		return "", false
	}
	c := false
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-o" || a == "+o" || a == "-O" || a == "+O":
			i++ // takes an option's name
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			c = c || strings.ContainsRune(a[1:], 'c')
		default:
			return a, c
		}
	}
	return "", false
}
