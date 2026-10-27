package gui

import (
	"testing"
)

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
