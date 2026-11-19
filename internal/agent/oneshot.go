package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// AppendFlagOnce appends flag to cmd unless it is already present.
func AppendFlagOnce(cmd []string, flag string) []string {
	if slices.Contains(cmd, flag) {
		return cmd
	}
	return append(cmd, flag)
}

// JSONPromptSuffix appends instructions asking the agent to emit raw JSON
// when the CLI does not enforce a schema natively.
func JSONPromptSuffix(prompt, format, schema string) string {
	if format != "json" {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nRespond ONLY with valid JSON (no markdown fences or commentary).")
	if strings.TrimSpace(schema) != "" {
		b.WriteString("\nConform strictly to this JSON Schema:\n")
		b.WriteString(strings.TrimSpace(schema))
	}
	return b.String()
}

// ExtractJSON extracts and validates JSON from a model's raw text output,
// stripping optional markdown code fences or surrounding prose if needed.
func ExtractJSON(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("agent returned empty output instead of JSON")
	}
	if json.Valid([]byte(s)) {
		return s, nil
	}
	if stripped := stripJSONFence(s); stripped != "" && json.Valid([]byte(stripped)) {
		return stripped, nil
	}
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		start := strings.IndexByte(s, pair[0])
		end := strings.LastIndexByte(s, pair[1])
		if start >= 0 && end > start {
			sub := strings.TrimSpace(s[start : end+1])
			if json.Valid([]byte(sub)) {
				return sub, nil
			}
		}
	}
	return "", fmt.Errorf("agent output is not valid JSON: %s", Clip(s, 160))
}

func stripJSONFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return ""
	}
	_, after, ok := strings.Cut(s, "\n")
	if !ok {
		return ""
	}
	rest := after
	end := strings.LastIndex(rest, "```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// CompactJSON compacts valid JSON bytes or returns trimmed text.
func CompactJSON(b []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, b) == nil {
		return buf.String()
	}
	return strings.TrimSpace(string(b))
}
