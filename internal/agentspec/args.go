package agentspec

import (
	"fmt"
	"strings"
)

// SplitArgs splits a command line as a POSIX shell would split words:
// spaces separate them, single quotes keep everything literal, double
// quotes keep spaces and let a backslash escape ", \ and $, and a
// backslash outside quotes escapes the next character. Nothing is expanded.
func SplitArgs(s string) ([]string, error) {
	var (
		out   []string
		word  strings.Builder
		inArg bool
		quote rune
	)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs) && strings.ContainsRune(`"\$`, rs[i+1]):
				i++
				word.WriteRune(rs[i])
			default:
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == '\\':
			if i+1 == len(rs) {
				return nil, fmt.Errorf("a trailing backslash escapes nothing")
			}
			i++
			word.WriteRune(rs[i])
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				out = append(out, word.String())
				word.Reset()
				inArg = false
			}
		default:
			word.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if inArg {
		out = append(out, word.String())
	}
	return out, nil
}

// JoinArgs writes args as a command line SplitArgs reads back, quoting
// only the words that need it.
func JoinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`;&|<>()*?[]#~") {
			quoted[i] = a
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
