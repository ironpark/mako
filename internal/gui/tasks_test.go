package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// Over the composer a line says how many shells run; it opens to the
// commands, those running first, and one opens to its details.
func TestShellsOpenToTheirDetails(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	started := time.Now().Add(-5 * time.Minute).Unix()
	exit := int32(1)
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "a", State: protocol.StateRunning, Tasks: []*protocol.BackgroundTask{
		{Id: "b1", Command: "make check\n  && echo done", Running: false, Status: protocol.TaskFailed, ExitCode: &exit, Started: started, Ended: started + 60},
		{Id: "b2", Command: "zig build check", Description: "Run the check suite", Running: true, Started: started},
		{Id: "b3", Command: "sleep 100", Running: true, Started: started + 30},
	}}})
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Stack(ggui.Column(runningTasks(m, "a")).Stretch(), shellDialog(m)).Expand()
	}, ggui.Sz(700, 600))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, Tf("%d shells", 2)); !ok {
		t.Fatal("no line says two shells run")
	}
	if _, ok := sem.Find(ggui.RoleText, "Run the check suite"); ok {
		t.Fatal("the list shows before it is opened")
	}
	clickText(t, p, Tf("%d shells", 2))
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "make check …"); !ok {
		t.Fatal("an ended command is not listed by its first line")
	}
	clickText(t, p, "Run the check suite")
	p.Frame()
	if !ggui.Peek(m.Shell.Open) {
		t.Fatal("the command's details did not open")
	}
	sem = p.Semantics()
	for _, want := range []string{"zig build check", T("Running")} {
		if _, ok := sem.Find(ggui.RoleText, want); !ok {
			t.Fatalf("the details show no %q", want)
		}
	}
}

// clickText clicks where text is painted.
func clickText(t *testing.T, p *ggui.Probe, text string) {
	t.Helper()
	n, ok := p.Semantics().Find(ggui.RoleText, text)
	if !ok {
		t.Fatalf("no %q to click", text)
	}
	p.Click(n.Rect.Center())
}

func TestRuntimeIsToTheSecond(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5*time.Minute + 23*time.Second + 400*time.Millisecond: Tf("%dm %ds", 5, 23),
		42 * time.Second: Tf("%ds", 42),
		2*time.Hour + 3*time.Minute + time.Second: Tf("%dh %dm %ds", 2, 3, 1),
	} {
		if got := runtimeText(d); got != want {
			t.Errorf("runtimeText(%v) = %q, want %q", d, got, want)
		}
	}
}
