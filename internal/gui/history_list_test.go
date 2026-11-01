package gui

import (
	"fmt"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A long history builds the rows in view, not every commit's.
func TestAHistoryBuildsTheRowsInView(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	h := &protocol.GitHistory{More: true}
	for i := range 500 {
		h.Commits = append(h.Commits, &protocol.GitCommit{Hash: fmt.Sprintf("%040d", i), Subject: fmt.Sprintf("commit %d", i), Author: "a"})
	}
	m.GitHistory.Set(historyView{History: h})
	p := ggui.ProbeBuilder(func() ggui.Widget { return historyBody(m) }, ggui.Sz(400, 600))
	defer p.Close()
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleButton, "commit 0"); !ok {
		t.Fatalf("the first commit is not shown:\n%s", p.Semantics().String())
	}
	built := 0
	for i := range 500 {
		if _, ok := p.Semantics().Find(ggui.RoleButton, fmt.Sprintf("commit %d", i)); ok {
			built++
		}
	}
	if built > 60 {
		t.Fatalf("%d of 500 rows built in a 600 high view", built)
	}
}
