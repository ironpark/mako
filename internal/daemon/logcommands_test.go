package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

// listing is a driver whose log lines each name the command they hold.
type listing struct{ lines *int }

func (l listing) LogCommands(line []byte) []agent.Command {
	*l.lines++
	if name, ok := strings.CutPrefix(string(line), "cmd "); ok {
		return []agent.Command{{Name: name}}
	}
	return nil
}

// A log's commands are read from where it was last read, whole lines
// alone; a session whose log names none has the latest its agent's did.
func TestLoggedCommandsReadOnFromTheLast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	write := func(s string) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString(s)
		f.Close()
	}
	names := func(cmds []agent.Command) string {
		var out []string
		for _, c := range cmds {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	lines := 0
	driver := listing{&lines}
	var l loggedCommands
	write("cmd a\nother\ncmd b")
	if got := names(l.of(driver, "x", path)); got != "a" || lines != 2 {
		t.Fatalf("first read %q of %d lines", got, lines)
	}
	write("\ncmd a\ncmd c\n")
	if got := names(l.of(driver, "x", path)); got != "a,b,c" || lines != 5 {
		t.Fatalf("read on %q, %d lines in all", got, lines)
	}
	if got := names(l.of(driver, "x", "")); got != "a,b,c" {
		t.Fatalf("a session with no log has %q", got)
	}
	if got := names(withLogged([]agent.Command{{Name: "b"}, {Name: "z"}}, l.of(driver, "x", path))); got != "b,z,a,c" {
		t.Fatalf("merged %q", got)
	}
}
