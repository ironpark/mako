//go:build unix

package session

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A turn ends where work goes idle or done: by the hooks, an approval
// answered, or the screen.
func TestTurnsAreCounted(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.SetStatus(protocol.AgentIdle)
	s.SetStatus(protocol.AgentWorking)
	s.SetStatus(protocol.AgentWaiting)
	if s.Turns() != 0 {
		t.Fatal("a turn ended on asking the user")
	}
	s.SetStatus(protocol.AgentWorking)
	s.SetStatus(protocol.AgentDone)
	s.SetStatus(protocol.AgentDone)
	if s.Turns() != 1 {
		t.Fatalf("turns %d after one", s.Turns())
	}
	s.SetStatus(protocol.AgentCompacting)
	s.mu.Lock()
	s.endWorkLocked(time.Now())
	s.recomputeLocked(time.Now())
	s.mu.Unlock()
	if s.Turns() != 2 {
		t.Fatalf("turns %d after a turn ended unreported", s.Turns())
	}
}

// Changed closes as the status, an approval or the state changes, and not
// for a status said again.
func TestChangesAreAnnounced(t *testing.T) {
	pokes := 0
	s := &Session{state: protocol.StateRunning, spec: Spec{OnChange: func() { pokes++ }}}
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	ch := s.Changed()
	s.SetStatus(protocol.AgentWorking)
	if !closed(ch) || pokes != 1 {
		t.Fatal("a status that changed was not announced")
	}
	ch = s.Changed()
	s.SetStatus(protocol.AgentWorking)
	if closed(ch) || pokes != 1 {
		t.Fatal("a status said again was announced")
	}
	s.mu.Lock()
	seq := s.statusSeq
	s.mu.Unlock()
	if _, ok := s.BeginApproval(seq, &protocol.Approval{Id: "a"}); !ok || !closed(ch) {
		t.Fatal("an approval was not announced")
	}
	ch = s.Changed()
	s.FinishApproval(seq, "a", protocol.AgentWorking)
	if !closed(ch) {
		t.Fatal("an approval answered was not announced")
	}
}

// Keys sent after a paste come a moment after it, not in its burst.
func TestKeysWaitOnAPaste(t *testing.T) {
	s, err := Start(Spec{ID: "send", Command: []string{"sh", "-c", "stty raw -echo; od -c"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := s.Send("text", []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < pasteGap(len("text")) {
		t.Fatal("the keys went with the paste")
	}
}

func TestFastModeControlsStateAndExitCleanup(t *testing.T) {
	pokes := 0
	s := &Session{
		state: protocol.StateRunning,
		spec:  Spec{ID: "codex-fast", Agent: "codex", OnChange: func() { pokes++ }},
	}
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}

	// Initially unsupported: Controls is nil for codex and FastMode is false.
	info := s.Info()
	if info.GetControls() != nil || info.GetFastMode() {
		t.Fatalf("initial codex session controls=%v fastMode=%v, want nil / false", info.GetControls(), info.GetFastMode())
	}

	// Enabling fast mode support allocates AgentControls{FastMode: true} and notifies once.
	ch := s.Changed()
	s.SetFastModeSupported(true)
	if !closed(ch) || pokes != 1 {
		t.Fatalf("SetFastModeSupported(true) did not announce change: closed=%v pokes=%d", closed(ch), pokes)
	}
	info = s.Info()
	if info.GetControls() == nil || !info.GetControls().GetFastMode() || info.GetFastMode() {
		t.Fatalf("after SetFastModeSupported(true): controls=%v fastMode=%v", info.GetControls(), info.GetFastMode())
	}

	// Duplicate SetFastModeSupported(true) is a no-op.
	ch = s.Changed()
	s.SetFastModeSupported(true)
	if closed(ch) || pokes != 1 {
		t.Fatalf("duplicate SetFastModeSupported(true) announced spuriously: closed=%v pokes=%d", closed(ch), pokes)
	}

	// ReportFastMode toggles state and announces only on actual changes.
	ch = s.Changed()
	s.ReportFastMode(true)
	if !closed(ch) || pokes != 2 || !s.Info().GetFastMode() {
		t.Fatalf("ReportFastMode(true): closed=%v pokes=%d fast=%v", closed(ch), pokes, s.Info().GetFastMode())
	}
	ch = s.Changed()
	s.ReportFastMode(true)
	if closed(ch) || pokes != 2 {
		t.Fatalf("duplicate ReportFastMode(true) announced spuriously: closed=%v pokes=%d", closed(ch), pokes)
	}
	ch = s.Changed()
	s.ReportFastMode(false)
	if !closed(ch) || pokes != 3 || s.Info().GetFastMode() {
		t.Fatalf("ReportFastMode(false): closed=%v pokes=%d fast=%v", closed(ch), pokes, s.Info().GetFastMode())
	}

	// Live process exit clears fastMode and Controls.FastMode.
	live, err := Start(Spec{ID: "codex-exit-fast", Agent: "codex", Command: []string{"sh", "-c", "sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	live.SetFastModeSupported(true)
	live.ReportFastMode(true)
	if info := live.Info(); !info.GetFastMode() || !info.GetControls().GetFastMode() {
		live.Close()
		t.Fatalf("live codex session missing fast mode before exit: fast=%v controls=%v", info.GetFastMode(), info.GetControls())
	}
	if err := live.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	exited := live.Info()
	if exited.GetState() != protocol.StateExited || exited.GetFastMode() || exited.GetControls().GetFastMode() {
		t.Fatalf("exited session kept fast mode: state=%s fast=%v controls=%v", exited.GetState(), exited.GetFastMode(), exited.GetControls())
	}
	// Late reports after exit are ignored.
	live.SetFastModeSupported(true)
	live.ReportFastMode(true)
	if info := live.Info(); info.GetFastMode() || info.GetControls().GetFastMode() {
		t.Fatalf("late fast mode reports resurrected exited state: fast=%v controls=%v", info.GetFastMode(), info.GetControls())
	}
}

func TestDynamicModelsControlsConfigureAndExit(t *testing.T) {
	pokes := 0
	s := &Session{
		state: protocol.StateRunning,
		spec:  Spec{ID: "agy-dyn", Agent: "agy", OnChange: func() { pokes++ }},
	}
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}

	// agy has empty static Models() initially.
	if got := s.Info().GetControls().GetModels(); len(got) != 0 {
		t.Fatalf("initial agy models = %v, want empty", got)
	}

	// SetDynamicModels populates Controls.Models and announces once.
	ch := s.Changed()
	s.SetDynamicModels([]string{"gemini-4-neon", "gpt-oss-120b-medium"})
	if !closed(ch) || pokes != 1 {
		t.Fatalf("SetDynamicModels did not announce change: closed=%v pokes=%d", closed(ch), pokes)
	}
	if got := s.Info().GetControls().GetModels(); len(got) != 2 || got[0] != "gemini-4-neon" || got[1] != "gpt-oss-120b-medium" {
		t.Fatalf("Controls.Models = %v, want [gemini-4-neon gpt-oss-120b-medium]", got)
	}

	// Duplicate SetDynamicModels is a no-op.
	ch = s.Changed()
	s.SetDynamicModels([]string{"gemini-4-neon", "gpt-oss-120b-medium"})
	if closed(ch) || pokes != 1 {
		t.Fatalf("duplicate SetDynamicModels announced spuriously: closed=%v pokes=%d", closed(ch), pokes)
	}

	// Live PTY session accepts base model ID and registered variant ID, rejects unknown.
	live, err := Start(Spec{
		ID:      "agy-live-models",
		Agent:   "agy",
		Project: t.TempDir(),
		Command: []string{"sh", "-c", "stty raw -echo; cat >/dev/null"},
	})
	if err != nil {
		t.Fatal(err)
	}
	live.SetDynamicModelVariants([]string{"gemini-4-neon-high"})
	live.SetDynamicModels([]string{"gemini-4-neon"})
	if err := live.Configure("", "gemini-4-neon", ""); err != nil {
		live.Close()
		t.Fatalf("Configure base dynamic model failed: %v", err)
	}
	if err := live.Configure("", "gemini-4-neon-high", ""); err != nil {
		live.Close()
		t.Fatalf("Configure variant dynamic model failed: %v", err)
	}
	if err := live.Configure("", "gemini-4-neon-bogus", ""); err == nil {
		live.Close()
		t.Fatal("Configure accepted unknown dynamic model")
	}

	// Exiting clears dynamic models and ignores late updates.
	if err := live.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := live.Info().GetControls().GetModels(); len(got) != 0 {
		t.Fatalf("exited session retained dynamic models: %v", got)
	}
	live.SetDynamicModels([]string{"gemini-4-neon"})
	if got := live.Info().GetControls().GetModels(); len(got) != 0 {
		t.Fatalf("late SetDynamicModels resurrected exited state: %v", got)
	}
}

func TestConfigureRejectsNonModelAgentWithoutPanic(t *testing.T) {
	for _, agentName := range []string{"codex", "shell"} {
		t.Run(agentName, func(t *testing.T) {
			s := &Session{
				state: protocol.StateRunning,
				spec:  Spec{ID: agentName + "-nomodel", Agent: agentName},
			}
			if err := s.Configure("", "gpt-5.4", ""); err == nil {
				t.Fatalf("%s: Configure(model) succeeded, want error", agentName)
			}
			if err := s.Configure("", "", "high"); err == nil {
				t.Fatalf("%s: Configure(effort) succeeded, want error", agentName)
			}
			if err := s.Configure("auto", "", ""); err == nil {
				t.Fatalf("%s: Configure(mode) succeeded, want error", agentName)
			}
		})
	}
}
