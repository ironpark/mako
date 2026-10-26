package gui

import (
	"testing"
)

func TestShellToolsReadTheirCommands(t *testing.T) {
	for _, tc := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"command": "go test ./..."}, "go test ./..."},
		{map[string]any{"cmd": "ls -la"}, "ls -la"},
		{map[string]any{"command": []any{"bash", "-lc", "git status"}}, "git status"},
		{map[string]any{"command": []any{"rg", "-n", "x"}}, "rg -n x"},
	} {
		if got := shellCommand(tc.in); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.in, got, tc.want)
		}
	}
	code, out := shellOutput("Chunk ID: 1\nWall time: 0.1 seconds\nProcess exited with code 2\nOutput:\nboom\n")
	if code != 2 || out != "boom\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if code, out := shellOutput("plain"); code != -1 || out != "plain" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestReadOutputLosesItsLineNumbers(t *testing.T) {
	text, first, ok := numberedLines("    12→package gui\n    13→\n    14→func x() {}\n\n<system-reminder>\nnote\n</system-reminder>\n")
	if !ok || first != 12 || text != "package gui\n\nfunc x() {}" {
		t.Fatalf("ok %v first %d text %q", ok, first, text)
	}
	if _, _, ok := numberedLines("no numbers here"); ok {
		t.Fatal("plain text read as numbered")
	}
}

func TestPatchesSplitByFile(t *testing.T) {
	files := parsePatch("*** Begin Patch\n*** Update File: a.go\n@@ func a\n-old\n+new\n ctx\n*** Add File: b.go\n+package b\n*** Delete File: c.go\n*** End Patch\n")
	if len(files) != 3 || files[0].Path != "a.go" || files[1].Op != "add" || files[2].Op != "delete" {
		t.Fatalf("files %+v", files)
	}
	ops := ""
	for _, l := range files[0].Lines {
		ops += l.GetOp()
	}
	if ops != "@-+ " || files[0].Lines[2].GetText() != "new" {
		t.Fatalf("lines %v", files[0].Lines)
	}
}
