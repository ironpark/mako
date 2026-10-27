package shellenv

import (
	"maps"
	"testing"
)

func TestParseKeepsOnlyTheFencedEnvironment(t *testing.T) {
	out := []byte("Last login: today\n" + marker + "PATH=/opt/bin:/usr/bin\x00_=/usr/bin/env\x00SHLVL=2\x00MULTI=a\nb\x00EMPTY=\x00" + marker + "bye\n")
	want := map[string]string{"PATH": "/opt/bin:/usr/bin", "MULTI": "a\nb", "EMPTY": ""}
	if got := parse(out); !maps.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if parse([]byte("no markers")) != nil || parse([]byte(marker+"PATH=/x")) != nil {
		t.Fatal("read an environment without both markers")
	}
}
