package agent

import "testing"

func TestExtractJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"ok":true}`, `{"ok":true}`},
		{"fenced", "```json\n{\"a\": 1}\n```", `{"a": 1}`},
		{"prose", "Here is the result:\n[1, 2, 3]\nDone.", `[1, 2, 3]`},
	} {
		got, err := ExtractJSON(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q (%v), want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := ExtractJSON("not json at all"); err == nil {
		t.Fatal("accepted non-json")
	}
}
