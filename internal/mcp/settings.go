package mcp

import (
	"encoding/json"
	"strings"
)

// ParseSettings reads a server's settings as written on the settings page:
// JSON, or the JSON with comments and trailing commas Zed writes default
// settings in. Blank is no settings.
func ParseSettings(text string) (json.RawMessage, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	plain := stripJSONC(text)
	var v any
	if err := json.Unmarshal([]byte(plain), &v); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	return json.RawMessage(b), err
}

// stripJSONC drops // and /* */ comments outside strings, then the commas
// left before a closing bracket or brace.
func stripJSONC(s string) string {
	var b strings.Builder
	scan(s, func(i int, inString bool) int {
		if !inString && strings.HasPrefix(s[i:], "//") {
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				return len(s)
			}
			return i + end
		}
		if !inString && strings.HasPrefix(s[i:], "/*") {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return len(s)
			}
			return i + 2 + end + 2
		}
		b.WriteByte(s[i])
		return i + 1
	})
	s = b.String()
	b.Reset()
	scan(s, func(i int, inString bool) int {
		if !inString && s[i] == ',' {
			rest := strings.TrimLeft(s[i+1:], " \t\r\n")
			if rest == "" || rest[0] == '}' || rest[0] == ']' {
				return i + 1
			}
		}
		b.WriteByte(s[i])
		return i + 1
	})
	return b.String()
}

// scan walks s, telling step at each byte whether it is inside a string;
// step returns where to go on.
func scan(s string, step func(i int, inString bool) int) {
	inString, escaped := false, false
	for i := 0; i < len(s); {
		c := s[i]
		in := inString
		switch {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
			in = true
		}
		i = step(i, in)
	}
}
