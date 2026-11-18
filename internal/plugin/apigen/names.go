package apigen

import (
	"strings"
	"unicode"
)

// kebab spells a CamelCase name as WIT does: GetItemRequest is
// get-item-request.
func kebab(name string) string {
	var b strings.Builder
	rs := []rune(name)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]))
			nextLower := i > 0 && i+1 < len(rs) && unicode.IsUpper(rs[i-1]) && unicode.IsLower(rs[i+1])
			if prevLower || nextLower {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// snakeToKebab spells a field's snake_case name as WIT does.
func snakeToKebab(name string) string { return strings.ReplaceAll(name, "_", "-") }

// snakeToCamel is node_id as NodeId.
func snakeToCamel(name string) string {
	var b strings.Builder
	for _, w := range strings.Split(name, "_") {
		if w == "" {
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// lowerCamel is ListSessions as listSessions.
func lowerCamel(name string) string {
	if name == "" {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}

// validKebab says name is a WIT identifier: words of lowercase letters
// and digits, each starting with a letter, joined by hyphens.
func validKebab(name string) bool {
	if name == "" {
		return false
	}
	for _, w := range strings.Split(name, "-") {
		if w == "" || w[0] < 'a' || w[0] > 'z' {
			return false
		}
		for _, r := range w {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return false
			}
		}
	}
	return true
}

// witKeywords are WIT's keywords, which an identifier spelled as one
// escapes with %: those wasm-tools 1.263 refuses bare as a field's name,
// map among them (WIT's map type), and float32 and float64, which it
// takes now, escaped all the same, as an escape is good for any name.
var witKeywords = map[string]bool{
	"map": true,
	"as":  true, "async": true, "bool": true, "borrow": true, "char": true, "constructor": true,
	"enum": true, "export": true, "f32": true, "f64": true, "flags": true, "float32": true,
	"float64": true, "from": true, "func": true, "future": true, "import": true, "include": true,
	"interface": true, "list": true, "option": true, "own": true, "package": true, "record": true,
	"resource": true, "result": true, "s16": true, "s32": true, "s64": true, "s8": true,
	"static": true, "stream": true, "string": true, "tuple": true, "type": true, "u16": true,
	"u32": true, "u64": true, "u8": true, "use": true, "variant": true, "with": true, "world": true,
	"error-context": true,
}

// witIdent is name as a WIT identifier, escaped if it is a keyword.
func witIdent(name string) string {
	if witKeywords[name] {
		return "%" + name
	}
	return name
}

// goPermName is the Go constant of a permission: sessions:read is
// PermSessionsRead.
func goPermName(p string) string {
	var b strings.Builder
	b.WriteString("Perm")
	for _, w := range strings.Split(p, ":") {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// docLines are a comment's lines, the space after the comment marker
// taken off each.
func docLines(doc string) []string {
	if doc == "" {
		return nil
	}
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.TrimPrefix(l, " "), " ")
	}
	return lines
}

// prefixed writes doc's lines, each after prefix, at indent.
func prefixed(b *strings.Builder, indent, prefix, doc string) {
	for _, l := range docLines(doc) {
		if l == "" {
			b.WriteString(indent + strings.TrimRight(prefix, " ") + "\n")
			continue
		}
		b.WriteString(indent + prefix + l + "\n")
	}
}
