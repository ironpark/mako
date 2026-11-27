package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestActivityKeepsChildrenOutsideTheChatWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	var body strings.Builder
	body.WriteString(`{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"go"}}` + "\n")
	body.WriteString(`{"type":"assistant","uuid":"task","message":{"role":"assistant","content":[{"type":"tool_use","id":"bg","name":"Agent","input":{"prompt":"Review it."}}]}}` + "\n")
	body.WriteString(`{"type":"user","uuid":"started","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"bg","content":"launched"}]}}` + "\n")
	for i := range 300 {
		fmt.Fprintf(&body, `{"type":"user","uuid":"m%d","message":{"role":"user","content":"m%d"}}`+"\n", i, i)
	}
	writeLog(t, path, body.String())
	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
	var activity Activity
	read := func() ActivityState {
		t.Helper()
		got, err := activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if len(read().Agents) != 0 {
		t.Fatal("child exists before its log")
	}
	// The parent log does not change when the child starts or finishes.
	child := filepath.Join(dir, "c", "subagents", "agent-b.meta.json")
	writeLog(t, strings.TrimSuffix(child, ".meta.json")+".jsonl", `{"type":"user","timestamp":"`+stamp+`","message":{"role":"user","content":"Review it."}}`+"\n")
	writeLog(t, child, `{"toolUseId":"bg","requestShape":"background","finished":false}`)
	if got := read().Agents; len(got) != 1 || !got[0].GetRunning() {
		t.Fatalf("running child outside window: %v", got)
	}
	writeLog(t, child, `{"toolUseId":"bg","requestShape":"background","finished":true}`)
	if got := read().Agents; len(got) != 1 || got[0].GetRunning() {
		t.Fatalf("completed child: %v", got)
	}
}

func TestActivityReadsCodexCancellationAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	info := &protocol.SessionInfo{Agent: "codex", ConversationId: "c"}
	now := time.Now().UTC()
	head := `{"type":"session_meta","payload":{"id":"c","cwd":"/p"}}` + "\n"
	record := func(kind string, at time.Time) string {
		return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":%q}}`+"\n", at.Format(time.RFC3339Nano), kind)
	}
	writeLog(t, path, head+record("task_started", now))
	var activity Activity
	for i, step := range []struct{ kind, want string }{
		{"", protocol.AgentWorking},
		{"turn_aborted", protocol.AgentIdle},
		{"task_started", protocol.AgentWorking},
		{"task_complete", protocol.AgentDone},
	} {
		if step.kind != "" {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(record(step.kind, now.Add(time.Duration(i)*time.Second)))
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := activity.Read(info, path)
		if err != nil || got.Status != step.want {
			t.Fatalf("%s: %+v, %v", step.kind, got, err)
		}
	}
	// A replacement with the same size must reset the lifecycle scan.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), `"task_complete"`, `"turn_aborted" `))
	if err := os.WriteFile(path+".new", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	got, err := activity.Read(info, path)
	if err != nil || got.Status != protocol.AgentIdle {
		t.Fatalf("replaced log: %+v, %v", got, err)
	}
}

func TestActivityDatesTheLastMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	said, later := time.Now().UTC().Add(-time.Hour), time.Now().UTC()
	at := func(t time.Time) string { return t.Format(time.RFC3339Nano) }
	writeLog(t, path,
		`{"type":"user","sessionId":"c","cwd":"/p","timestamp":"`+at(said.Add(-time.Minute))+`","message":{"role":"user","content":"go"}}`+"\n"+
			`{"type":"assistant","timestamp":"`+at(said)+`","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n"+
			// A tool's call is no message of the conversation's.
			`{"type":"assistant","timestamp":"`+at(later)+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls"}}]}}`+"\n")
	var activity Activity
	got, err := activity.Read(&protocol.SessionInfo{Agent: "claude", ConversationId: "c"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Said.Equal(said) {
		t.Fatalf("last message at %v, want %v", got.Said, said)
	}
}

// A log that lists the agent's skills says the commands it takes, read on
// as the log grows; a later listing renews those it names again.
func TestActivityReadsTheCommandsALogSays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	listing := func(desc string, names ...string) string {
		var content []string
		for _, n := range names {
			content = append(content, "- "+n+": "+desc)
		}
		return `{"type":"attachment","attachment":{"type":"skill_listing","content":"` + strings.Join(content, `\n`) + `","names":["` + strings.Join(names, `","`) + `"]}}` + "\n"
	}
	writeLog(t, path, `{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"go"}}`+"\n"+listing("old", "simplify", "loop"))
	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
	var activity Activity
	names := func() string {
		t.Helper()
		got, err := activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range got.Commands {
			out = append(out, c.Name+"="+c.Description)
		}
		return strings.Join(out, ",")
	}
	if got := names(); got != "simplify=old,loop=old" {
		t.Fatalf("commands %q", got)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(listing("new", "loop", "review"))
	f.Close()
	if got := names(); got != "simplify=old,loop=new,review=new" {
		t.Fatalf("read on: %q", got)
	}
}

func TestActivityTracksAndClearsAuthErrors(t *testing.T) {
	t.Run("claude transcript auth error and recovery", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "c.jsonl")
		info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
		base := time.Now().UTC().Truncate(time.Second)
		at := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }

		// Quoted user text must never set AuthError.
		writeLog(t, path, `{"type":"user","sessionId":"c","cwd":"/p","timestamp":"`+at(0)+`","message":{"role":"user","content":"Failed to authenticate: OAuth session expired and could not be refreshed"}}`+"\n")
		var activity Activity
		got, err := activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuthError != nil {
			t.Fatalf("quoted user text triggered auth error: %+v", got.AuthError)
		}

		// Top-level assistant authentication_failed sets AuthError.
		appendLine := func(line string) {
			t.Helper()
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(line + "\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		appendLine(`{"type":"assistant","timestamp":"` + at(time.Second) + `","error":"authentication_failed","isApiErrorMessage":true,"message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Failed to authenticate: OAuth session expired and could not be refreshed."}]}}`)
		got, err = activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuthError == nil || got.AuthError.GetKind() != protocol.AuthErrorOAuthExpired || !got.AuthErrorAt.Equal(base.Add(time.Second)) {
			t.Fatalf("expected oauth_expired at +1s, got %+v", got)
		}

		// Synthetic assistant message must not clear AuthError.
		appendLine(`{"type":"assistant","timestamp":"` + at(2*time.Second) + `","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Still waiting"}]}}`)
		got, err = activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuthError == nil {
			t.Fatal("synthetic assistant message cleared auth error")
		}

		// Normal model reply clears AuthError and records AuthClearedAt.
		appendLine(`{"type":"assistant","timestamp":"` + at(3*time.Second) + `","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Recovered!"}]}}`)
		got, err = activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuthError != nil || !got.AuthClearedAt.Equal(base.Add(3*time.Second)) {
			t.Fatalf("expected auth error cleared at +3s, got %+v", got)
		}
	})

	t.Run("codex rollout auth error and recovery", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rollout.jsonl")
		info := &protocol.SessionInfo{Agent: "codex", ConversationId: "c"}
		base := time.Now().UTC().Truncate(time.Second)
		at := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }

		writeLog(t, path,
			`{"type":"session_meta","payload":{"id":"c","cwd":"/p"}}`+"\n"+
				`{"type":"event_msg","timestamp":"`+at(time.Second)+`","payload":{"type":"error","codex_error_info":"unauthorized","message":"Your access token could not be refreshed because your refresh token was expired."}}`+"\n")
		var activity Activity
		got, err := activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuthError == nil || got.AuthError.GetKind() != protocol.AuthErrorOAuthExpired {
			t.Fatalf("expected codex oauth_expired, got %+v", got)
		}

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(`{"type":"event_msg","timestamp":"` + at(2*time.Second) + `","payload":{"type":"task_started"}}` + "\n")
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, err = activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		if got.AuthError != nil || !got.AuthClearedAt.Equal(base.Add(2*time.Second)) {
			t.Fatalf("expected codex auth error cleared on task_started, got %+v", got)
		}
	})
}
