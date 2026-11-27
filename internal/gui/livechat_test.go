package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A reply streaming shows as the agent's text and the calls it began,
// after the conversation, keyed apart from what the log holds.
func TestAReplyStreamingShowsAsTurns(t *testing.T) {
	turns := liveTurns([]*protocol.LiveBlock{
		{Index: 0, Kind: "text", Text: "Looking"},
		{Index: 1, Kind: "tool", Tool: "Bash", Text: `{"description":"List","command":"ls -la \"my`},
		{Index: 2, Kind: "text", Text: "  "},
	})
	var msgs []*protocol.ChatMessage
	for _, turn := range turns {
		if len(turn.Key) < 5 || turn.Key[:5] != "live:" {
			t.Fatalf("turn keyed %q", turn.Key)
		}
		msgs = append(msgs, turn.Messages...)
	}
	if len(msgs) != 2 || msgs[0].GetText() != "Looking" || msgs[1].GetTool() != "Bash" || !msgs[1].GetPending() || msgs[1].GetDetail() != `ls -la "my` {
		t.Fatalf("messages %v", msgs)
	}
	thinking := liveTurns([]*protocol.LiveBlock{{Index: 0, Kind: "text", Text: "a"}, {Index: 1, Kind: "thinking", Text: "Reading file…"}})
	if n := len(thinking); n != 2 || !thinking[n-1].Thinking || thinking[n-1].Status != "Reading file…" {
		t.Fatalf("no line says it thinks with status: %+v", thinking)
	}
	if got := liveStatusLabel("Reading file..."); got == "" {
		t.Fatal("empty localized status label")
	}
	if got := liveStatusLabel("Searching web…"); got != "Searching web…" {
		t.Fatalf("custom status label = %q, want %q", got, "Searching web…")
	}
	if got := liveStatusLabel("1/3 steps · Editing files... 1m 56s"); got != Tf("%s steps", "1/3")+" · "+T("Editing files…")+" 1m 56s" {
		t.Fatalf("stepped status label = %q", got)
	}
	if got := liveStatusLabel("0/6 steps · Running command... 10s"); got != Tf("%s steps", "0/6")+" · "+T("Running command…")+" 10s" {
		t.Fatalf("stepped command label = %q", got)
	}
	if got := liveStatusLabel("Calling MCP tool…"); got != T("Calling MCP tool…") {
		t.Fatalf("mcp status label = %q", got)
	}
	if done := liveTurns([]*protocol.LiveBlock{{Index: 0, Kind: "thinking"}, {Index: 1, Kind: "text", Text: "a"}}); len(done) != 1 || done[0].Thinking {
		t.Fatalf("a thought done still shows: %+v", done)
	}
	if liveTurns(nil) != nil {
		t.Fatal("turns of no reply")
	}
}

// Once the log grows, what streams waits for the read of it, so a block
// leaves the stream as the logged one shows.
func TestTheStreamWaitsForTheLog(t *testing.T) {
	state := newChatState()
	var landed []func()
	read := func(then func()) { landed = append(landed, then) }
	tookLive(state, &protocol.ChatLive{Blocks: []*protocol.LiveBlock{{Kind: "text", Text: "a"}}}, read)
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "a" {
		t.Fatalf("the stream shows %v", got)
	}
	tookLive(state, &protocol.ChatLive{Logged: true}, read)
	tookLive(state, &protocol.ChatLive{Logged: true, Blocks: []*protocol.LiveBlock{{Kind: "text", Text: "b"}}}, read)
	// The log grew again while it was read: read again, as the read out
	// may have gone before it grew.
	if got := ggui.Peek(state.live); len(landed) != 2 || len(got) != 1 || got[0].GetText() != "a" {
		t.Fatalf("while the log is read: %v, %d reads", got, len(landed))
	}
	landed[0]()
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "a" {
		t.Fatalf("with a read still out: %v", got)
	}
	landed[1]()
	if got := ggui.Peek(state.live); len(got) != 1 || got[0].GetText() != "b" {
		t.Fatalf("once the read landed: %v", got)
	}
}

// A read asked for while one is out comes after it: then waits for it.
func TestAReadAskedForWhileOneIsOutComesAfter(t *testing.T) {
	state := newChatState()
	reads, ran := 0, 0
	state.loading = true
	readThen(state, func() { ran++ }, func() { reads++ })
	if !state.reread || ran != 0 {
		t.Fatal("a read out did not ask for another")
	}
}

// A tool call's arguments, cut short as they stream, say what it does.
func TestArgumentsStreamingSayWhatACallDoes(t *testing.T) {
	for args, want := range map[string]string{
		`{"description":"Run","command":"go test ./...`: "go test ./...",
		`{"file_path":"/a/b.go","old_string":"x`:        "/a/b.go",
		`{"command":"echo \"hi\" \`:                     `echo "hi"`,
		`{"pattern":"TODO`:                              "TODO",
		`{"other":"x"}`:                                 "x",
	} {
		if got := argsPreview(args); got != want {
			t.Errorf("argsPreview(%s) = %q, want %q", args, got, want)
		}
	}
}

func TestLiveActivitySplittingAndCard(t *testing.T) {
	raw := strings.Join([]string{
		"Swarm(Plugin UI rich components) · coordinator: @ui-schema · 69 posts",
		"Agent(worker: Swarm Worker (ui-schema)) · For split elements... · 6m44s",
		"Agent(worker: Swarm Worker (monitor)) · Working · 27h53m4s",
		"Task(go test -race ./internal/daemon/...) · running · 06:08:51",
	}, "\n")

	// When only Swarm/Agent/Task activity is present (coordinator idle at ">"),
	// liveTurns must NOT emit a spurious "Thinking…" turn in the feed.
	turns := liveTurns([]*protocol.LiveBlock{{Index: 0, Kind: "thinking", Text: raw}})
	if len(turns) != 0 {
		t.Fatalf("activity-only live block emitted feed turns: %+v", turns)
	}

	// When a foreground spinner status accompanies Swarm/Agent/Task activity,
	// liveTurns emits the foreground status while splitLiveStatus extracts both.
	fg, acts := splitLiveStatus("Thinking…\n" + raw)
	if fg != "Thinking…" || len(acts) != 4 {
		t.Fatalf("splitLiveStatus = (%q, %+v)", fg, acts)
	}
	if acts[0].Kind != "swarm" || acts[0].Title != "Plugin UI rich components" || acts[0].Detail != "coordinator: @ui-schema · 69 posts" {
		t.Fatalf("swarm item = %+v", acts[0])
	}
	if acts[1].Kind != "agent" || acts[1].Title != "Swarm Worker (ui-schema)" || acts[1].Detail != "For split elements..." || acts[1].Duration != "6m44s" {
		t.Fatalf("agent 1 = %+v", acts[1])
	}
	if acts[2].Kind != "agent" || acts[2].Title != "Swarm Worker (monitor)" || acts[2].Detail != T("Working") || acts[2].Duration != "27h53m4s" {
		t.Fatalf("agent 2 = %+v", acts[2])
	}
	if acts[3].Kind != "task" || acts[3].Title != "go test -race ./internal/daemon/..." || acts[3].Detail != T("Running") || acts[3].Duration != "06:08:51" {
		t.Fatalf("task item = %+v", acts[3])
	}

	// Deduplicates Agent rows already shown in runningAgents while keeping Swarm and Task rows.
	deduped := liveActivityItems(
		[]*protocol.LiveBlock{{Index: 0, Kind: "thinking", Text: raw}},
		[]*protocol.SubAgent{{Id: "a1", Running: true, Description: "Swarm Worker (ui-schema)"}},
	)
	if len(deduped) != 3 || deduped[0].Kind != "swarm" || deduped[1].Title != "Swarm Worker (monitor)" || deduped[2].Kind != "task" {
		t.Fatalf("deduped = %+v", deduped)
	}

	// Render liveActivityCard over the composer and confirm Swarm, Agent, and Task rows appear.
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "agy", State: protocol.StateRunning, Status: protocol.AgentDone}})
	state := m.chatState("s")
	state.live.Set([]*protocol.LiveBlock{{Index: 0, Kind: "thinking", Text: raw}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return liveActivityCard(m, "s", state) }, ggui.Sz(800, 200))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	for _, want := range []string{T("Swarm"), T("Command"), "Plugin UI rich components", "Swarm Worker (ui-schema)", "6m44s", "Swarm Worker (monitor)", "27h53m4s", "go test -race ./internal/daemon/...", "06:08:51"} {
		if !strings.Contains(page, want) {
			t.Fatalf("liveActivityCard missing %q:\n%s", want, page)
		}
	}
}
