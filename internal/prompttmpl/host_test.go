package prompttmpl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeHost struct {
	catalog AgentCatalog
	spawns  []SpawnRequest
	asks    []AskRequest
	decides [][]byte

	spawnFn  func(SpawnRequest) (SpawnResult, error)
	askFn    func(AskRequest) (AskResult, error)
	askCtxFn func(context.Context, AskRequest) (AskResult, error)
	decideFn func([]byte) ([]byte, error)
}

func (f *fakeHost) Agents(_ context.Context, agentName string, _ bool) (AgentCatalog, error) {
	if agentName == "" {
		return f.catalog, nil
	}
	var filtered []AgentEntry
	for _, a := range f.catalog.Agents {
		if a.Name == agentName {
			filtered = append(filtered, a)
		}
	}
	return AgentCatalog{NodeID: f.catalog.NodeID, Agents: filtered}, nil
}

func (f *fakeHost) Spawn(_ context.Context, req SpawnRequest) (SpawnResult, error) {
	f.spawns = append(f.spawns, req)
	if f.spawnFn != nil {
		return f.spawnFn(req)
	}
	a, p := SplitTarget(req.Target, req.Agent, req.Profile)
	return SpawnResult{SessionID: "sess-1", Agent: a, Profile: p, Name: req.Name, Project: req.Project}, nil
}

func (f *fakeHost) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	f.asks = append(f.asks, req)
	if f.askCtxFn != nil {
		return f.askCtxFn(ctx, req)
	}
	if f.askFn != nil {
		return f.askFn(req)
	}
	if req.Format == "json" {
		return AskResult{Format: "json", Output: `{"ok":true,"count":2}`}, nil
	}
	return AskResult{Format: "text", Output: "plain answer"}, nil
}

func (f *fakeHost) Decide(_ context.Context, rawJSON []byte) ([]byte, error) {
	f.decides = append(f.decides, append([]byte(nil), rawJSON...))
	if f.decideFn != nil {
		return f.decideFn(rawJSON)
	}
	var req map[string]any
	_ = json.Unmarshal(rawJSON, &req)
	switch req["type"] {
	case "noul":
		return []byte(`{"provider":"typesafe","model":"jev-latest","yes":true}`), nil
	case "score":
		return []byte(`{"provider":"typesafe","model":"jev-latest","score":0.8,"level":3,"label":"High"}`), nil
	default:
		return []byte(`{"provider":"typesafe","model":"jev-latest","choice":"codex"}`), nil
	}
}

func TestSelectTypeParseBindAndMatch(t *testing.T) {
	src := `---
args:
  - name: env
    type: select
    options: [dev, staging, prod]
    default: dev
---
{#match env}
{:dev}local
{:staging | prod}remote:{{env}}
{/match}`
	tm, err := Parse("deploy", []byte(src))
	if err != nil {
		t.Fatalf("Parse select: %v", err)
	}
	if tm.Args[0].Type != Select {
		t.Fatalf("arg type = %q, want select", tm.Args[0].Type)
	}
	got, err := tm.Expand(t.Context(), "PROD", nil)
	if err != nil {
		t.Fatalf("Expand select: %v", err)
	}
	if got != "remote:prod" {
		t.Fatalf("got %q, want remote:prod", got)
	}
	if _, err := tm.Bind("qa"); err == nil {
		t.Fatal("expected BadValue error for unknown select option")
	}
	// Missing options for select should still fail Parse.
	if _, err := Parse("bad", []byte("---\nargs: [{name: env, type: select}]\n---\n{{env}}\n")); err == nil {
		t.Fatal("expected error for select without options")
	}
}

func TestMakoReservedArgumentRejected(t *testing.T) {
	_, err := Parse("bad", []byte("---\nargs: [{name: mako}]\n---\n{{mako}}\n"))
	if err == nil {
		t.Fatal("expected error when argument is named mako")
	}
}

func TestHostSpawnAskAgentsAndDecide(t *testing.T) {
	src := `---
args:
  - name: task
    type: string
    required: true
---
{@const catalog = await mako.agents("claude")}
{@const summary = await mako.ask({ agent: "claude", model: "sonnet", effort: "low", prompt: "summarize " + task, format: "text" })}
{@const data = await mako.ask({ target: "codex:fast", prompt: "extract", schema: { type: "object" } })}
{@const pick = await mako.decide.choice(task, catalog.agents[0].profiles.map(p => p.name))}
{@const ok = await mako.decide.yes("proceed?")}
{@const child = await mako.spawn({ agent: catalog.agents[0].name, profile: pick, model: catalog.agents[0].models[0].id, effort: "high", name: "Worker", prompt: summary })}
Agent={{child.agent}}:{{child.profile}} Model={{child.sessionId}} JSON={{data.count}} Direct={{await mako.ask("ping")}}`
	tm, err := Parse("workflow", []byte(src))
	if err != nil {
		t.Fatalf("Parse host workflow: %v", err)
	}
	host := &fakeHost{
		catalog: AgentCatalog{
			NodeID: "n1",
			Agents: []AgentEntry{
				{
					Name: "claude",
					Profiles: []ProfileEntry{
						{Name: "reviewer", Description: "Code review", Spawnable: true},
						{Name: "builder", Description: "Feature builder", Spawnable: true},
					},
					Models: []ModelEntry{
						{ID: "claude-opus-4-6", Efforts: []string{"low", "medium", "high"}},
					},
					SupportsModel:  true,
					SupportsEffort: true,
				},
			},
		},
	}
	host.decideFn = func(raw []byte) ([]byte, error) {
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		if req["type"] == "noul" {
			return []byte(`{"yes":true}`), nil
		}
		return []byte(`{"choice":"reviewer"}`), nil
	}

	out, err := tm.ExpandWith(t.Context(), `"fix bug"`, map[string]string{"agent": "claude"}, host)
	if err != nil {
		t.Fatalf("ExpandWith: %v", err)
	}
	want := "Agent=claude:reviewer Model=sess-1 JSON=2 Direct=plain answer"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if len(host.spawns) != 1 {
		t.Fatalf("spawns len = %d", len(host.spawns))
	}
	sp := host.spawns[0]
	if sp.Agent != "claude" || sp.Profile != "reviewer" || sp.Model != "claude-opus-4-6" || sp.Effort != "high" || sp.Prompt != "plain answer" {
		t.Fatalf("spawn req = %+v", sp)
	}
	if len(host.asks) != 3 {
		t.Fatalf("asks len = %d", len(host.asks))
	}
	if host.asks[1].Format != "json" || host.asks[1].Target != "codex:fast" || host.asks[1].Schema == nil {
		t.Fatalf("second ask req = %+v", host.asks[1])
	}
}

func TestHostNilFailsClearly(t *testing.T) {
	tm, err := Parse("liveonly", []byte(`{{await mako.ask("hi")}}`))
	if err != nil {
		t.Fatalf("Parse liveonly: %v", err)
	}
	_, err = tm.Expand(t.Context(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "active Mako session") {
		t.Fatalf("expected clear error without host, got %v", err)
	}
	if _, err := Parse("badconst", []byte(`{@const x = }
ok`)); err == nil {
		t.Fatal("expected syntax error for malformed @const")
	}
}

func TestHostInIfEachAndScriptBlocks(t *testing.T) {
	src := `{#if await mako.decide.yes("gate")}
yes:{#each await mako.ask({ prompt: "list", format: "json" }) as item}[{{item}}]{/each}
{/if}
` + "```js\nconst s = await mako.decide.score(\"diff\", [\"Low\", \"High\"]);\nreturn s.label;\n```"
	tm, err := Parse("blocks", []byte(src))
	if err != nil {
		t.Fatalf("Parse blocks: %v", err)
	}
	host := &fakeHost{
		askFn: func(req AskRequest) (AskResult, error) {
			return AskResult{Format: "json", Output: `["a","b"]`}, nil
		},
	}
	got, err := tm.ExpandWith(t.Context(), "", nil, host)
	if err != nil {
		t.Fatalf("ExpandWith blocks: %v", err)
	}
	want := "yes:[a][b]\nHigh"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHostCallRespectsHostWallTimeout(t *testing.T) {
	tm, err := Parse("wall", []byte(`{{await mako.ask("hang")}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, comp, err := tm.prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	var sawDeadline bool
	host := &fakeHost{
		askCtxFn: func(ctx context.Context, _ AskRequest) (AskResult, error) {
			_, sawDeadline = ctx.Deadline()
			<-ctx.Done()
			return AskResult{}, ctx.Err()
		},
	}

	b := newBudget(context.Background(), 50*time.Millisecond, 90*time.Millisecond)
	defer b.release()
	_, err = evalPromptJS(b, tm, comp.program, nil, nil, host)
	if !sawDeadline {
		t.Fatal("host.Ask ctx did not carry a deadline")
	}
	if !errors.Is(err, errScriptWall) {
		t.Fatalf("expected errScriptWall, got %v", err)
	}
}

func TestHostCallRespectsParentCancelAndPauseGuard(t *testing.T) {
	tm, err := Parse("cancel", []byte(`{{await mako.ask("wait")}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, comp, err := tm.prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	parent, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	host := &fakeHost{
		askCtxFn: func(ctx context.Context, _ AskRequest) (AskResult, error) {
			close(started)
			<-ctx.Done()
			return AskResult{}, ctx.Err()
		},
	}

	b := newBudget(parent, time.Second, 5*time.Second)
	defer b.release()
	go func() {
		<-started
		cancel()
	}()
	if _, err := evalPromptJS(b, tm, comp.program, nil, nil, host); err == nil {
		t.Fatal("expected error when parent ctx is canceled")
	}

	// endCPU must be a no-op while paused.
	b2 := newBudget(context.Background(), time.Hour, time.Hour)
	defer b2.release()
	b2.pause()
	b2.endCPU()
	if b2.Err() != nil {
		t.Fatalf("endCPU fired while paused: %v", b2.Err())
	}
}
