package prompttmpl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/qngg"
)

const hostWallTimeout = 5 * time.Minute

var (
	errScriptCPU  = errors.New("its JavaScript ran too long")
	errScriptWall = errors.New("its prompt script ran too long")
)

// AgentCatalog lists agents, profiles, and models available on the node.
type AgentCatalog struct {
	NodeID string       `json:"nodeId"`
	Agents []AgentEntry `json:"agents"`
}

// AgentEntry describes an agent available to prompt template scripts.
type AgentEntry struct {
	Name            string         `json:"name"`
	Command         string         `json:"command,omitempty"`
	Profiles        []ProfileEntry `json:"profiles"`
	Models          []ModelEntry   `json:"models"`
	Yolo            bool           `json:"yolo"`
	SupportsYolo    bool           `json:"supportsYolo"`
	SupportsModel   bool           `json:"supportsModel"`
	SupportsEffort  bool           `json:"supportsEffort"`
	SupportsOneShot bool           `json:"supportsOneShot"`
	SupportsSpawn   bool           `json:"supportsSpawn"`
}

// ProfileEntry describes one configured profile of an agent.
type ProfileEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Yolo        bool   `json:"yolo"`
	Spawnable   bool   `json:"spawnable"`
}

// ModelEntry describes a model offered by an agent and its supported efforts.
type ModelEntry struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Efforts []string `json:"efforts,omitempty"`
}

// SpawnRequest configures a Mako sub-agent session launched from a template.
type SpawnRequest struct {
	Target  string `json:"target,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Yolo    *bool  `json:"yolo,omitempty"`
	Name    string `json:"name,omitempty"`
	Project string `json:"project,omitempty"`
	Prompt  string `json:"prompt"`
}

// SpawnResult describes the child Mako session started by Spawn.
type SpawnResult struct {
	SessionID string `json:"sessionId"`
	Agent     string `json:"agent"`
	Profile   string `json:"profile,omitempty"`
	Name      string `json:"name,omitempty"`
	Project   string `json:"project,omitempty"`
}

// AskRequest configures a single non-interactive agent prompt execution.
type AskRequest struct {
	Target  string `json:"target,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Yolo    *bool  `json:"yolo,omitempty"`
	Project string `json:"project,omitempty"`
	Prompt  string `json:"prompt"`
	Format  string `json:"format,omitempty"` // "text" or "json"
	Schema  any    `json:"schema,omitempty"`
}

// AskResult returns the output from a one-off prompt call.
type AskResult struct {
	Output string `json:"output"`
	Format string `json:"format"`
}

// Host connects prompt template JavaScript (`mako.*`) to a live Mako node.
type Host interface {
	Agents(ctx context.Context, agentName string, refresh bool) (AgentCatalog, error)
	Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error)
	Ask(ctx context.Context, req AskRequest) (AskResult, error)
	Decide(ctx context.Context, rawJSON []byte) ([]byte, error)
}

// SplitTarget resolves "agent:profile" shorthand across target, agentName, and profile.
func SplitTarget(target, agentName, profile string) (string, string) {
	if target != "" {
		a, p, hasColon := strings.Cut(target, ":")
		if agentName == "" {
			agentName = a
		}
		if hasColon && profile == "" {
			profile = p
		}
	}
	if profile == "" && strings.Contains(agentName, ":") {
		a, p, _ := strings.Cut(agentName, ":")
		agentName, profile = a, p
	}
	return strings.TrimSpace(agentName), strings.TrimSpace(profile)
}

// SchemaJSON normalizes an AskRequest.Schema value (string or JSON object) to a JSON string.
func SchemaJSON(schema any) (string, error) {
	if schema == nil {
		return "", nil
	}
	switch s := schema.(type) {
	case string:
		return strings.TrimSpace(s), nil
	default:
		b, err := json.Marshal(s)
		if err != nil {
			return "", fmt.Errorf("schema: %w", err)
		}
		return string(b), nil
	}
}

type scriptBudget struct {
	parent context.Context
	done   chan struct{}

	mu      sync.Mutex
	err     error
	left    time.Duration
	started time.Time
	paused  int
	timer   *time.Timer
	wall    *time.Timer
	stop    func() bool
}

func newBudget(parent context.Context, cpu, wall time.Duration) *scriptBudget {
	b := &scriptBudget{parent: parent, done: make(chan struct{}), left: cpu, started: time.Now()}
	b.timer = time.AfterFunc(cpu, func() { b.end(errScriptCPU) })
	b.wall = time.AfterFunc(wall, func() { b.end(errScriptWall) })
	b.stop = context.AfterFunc(parent, func() { b.end(parent.Err()) })
	return b
}

func (b *scriptBudget) end(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return
	}
	b.err = err
	close(b.done)
}

func (b *scriptBudget) pause() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused++
	if b.paused == 1 && b.err == nil {
		b.timer.Stop()
		b.left -= time.Since(b.started)
	}
}

func (b *scriptBudget) resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused--
	if b.paused > 0 || b.err != nil {
		return
	}
	b.started = time.Now()
	if b.left <= 0 {
		b.err = errScriptCPU
		close(b.done)
		return
	}
	b.timer.Reset(b.left)
}

func (b *scriptBudget) release() {
	b.timer.Stop()
	b.wall.Stop()
	b.stop()
}

func (b *scriptBudget) exceeded() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if errors.Is(b.err, errScriptCPU) || errors.Is(b.err, errScriptWall) {
		return b.err
	}
	return nil
}

func (b *scriptBudget) Deadline() (time.Time, bool) { return time.Time{}, false }
func (b *scriptBudget) Done() <-chan struct{}       { return b.done }
func (b *scriptBudget) Value(key any) any           { return b.parent.Value(key) }

func (b *scriptBudget) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		return nil
	}
	if errors.Is(b.err, errScriptCPU) || errors.Is(b.err, errScriptWall) {
		return context.DeadlineExceeded
	}
	return b.err
}

const makoPreludeJS = `const mako = (() => {
  const __call = globalThis.__makoCall;
  delete globalThis.__makoCall;

  function normalizeSpawnOrAsk(arg0, arg1, extra) {
    let opts;
    if (typeof arg0 === "string") {
      if (typeof arg1 === "string") {
        opts = Object.assign({}, extra || {}, { target: arg0, prompt: arg1 });
      } else if (arg1 && typeof arg1 === "object") {
        opts = Object.assign({ target: arg0 }, arg1, extra || {});
      } else {
        opts = { prompt: arg0 };
      }
    } else if (arg0 && typeof arg0 === "object") {
      opts = Object.assign({}, arg0);
    } else {
      throw new TypeError("expected prompt string or options object");
    }
    return opts;
  }

  async function agents(arg) {
    let opts = {};
    if (typeof arg === "string") {
      opts = { agent: arg };
    } else if (arg && typeof arg === "object") {
      opts = arg;
    }
    return JSON.parse(__call("agents", JSON.stringify(opts)));
  }

  async function spawn(arg0, arg1, extra) {
    const opts = normalizeSpawnOrAsk(arg0, arg1, extra);
    return JSON.parse(__call("spawn", JSON.stringify(opts)));
  }

  async function ask(arg0, arg1, extra) {
    const opts = normalizeSpawnOrAsk(arg0, arg1, extra);
    if (!opts.format && opts.schema !== undefined && opts.schema !== null) {
      opts.format = "json";
    }
    const res = JSON.parse(__call("ask", JSON.stringify(opts)));
    if (res.format === "json") {
      return JSON.parse(res.output);
    }
    return res.output;
  }

  async function decide(opts) {
    if (!opts || typeof opts !== "object") {
      throw new TypeError("mako.decide expects an options object");
    }
    return JSON.parse(__call("decide", JSON.stringify(opts)));
  }

  decide.choice = async (state, optionsOrCriteria, extra) => {
    const req = Object.assign({}, extra || {}, { type: "choice", state });
    if (Array.isArray(optionsOrCriteria)) {
      req.options = optionsOrCriteria;
    } else {
      req.criteria = optionsOrCriteria;
    }
    const res = await decide(req);
    return res.choice;
  };

  decide.yes = async (state, instructionsOrExtra, extra) => {
    let req;
    if (instructionsOrExtra && typeof instructionsOrExtra === "object") {
      req = Object.assign({}, instructionsOrExtra, { type: "noul", state });
    } else {
      req = Object.assign({}, extra || {}, { type: "noul", state, instructions: instructionsOrExtra });
    }
    const res = await decide(req);
    return Boolean(res.yes);
  };

  decide.noul = decide.yes;

  decide.score = async (state, levelsOrCriteria, extra) => {
    const req = Object.assign({}, extra || {}, { type: "score", state });
    if (Array.isArray(levelsOrCriteria)) {
      req.levels = levelsOrCriteria;
    } else {
      req.criteria = levelsOrCriteria;
    }
    return decide(req);
  };

  return Object.freeze({
    agents,
    spawn,
    ask,
    prompt: ask,
    decide: Object.freeze(decide),
  });
})();
`

func evalPromptJS(b *scriptBudget, t *Template, program string, values, builtins map[string]string, host Host) (_ string, err error) {
	defer trappedScript(&err)

	b.pause()
	resumed := false
	resume := func() {
		if !resumed {
			resumed = true
			b.resume()
		}
	}
	defer resume()

	rt, err := qngg.New(qngg.Config{MemoryLimit: scriptMemory})
	if err != nil {
		return "", fmt.Errorf("JavaScript: %w", err)
	}
	defer closeQuietly(rt)

	if values == nil {
		values = map[string]string{}
	}
	if builtins == nil {
		builtins = map[string]string{}
	}
	for name, v := range map[string]any{"args": t.typedArgs(values), "vars": builtins, "__raw": values} {
		if err := rt.Set(name, v); err != nil {
			return "", err
		}
	}
	if err := rt.SetFunc("__makoCall", func(call *qngg.FunctionCall) (*qngg.Value, error) {
		op := call.Arg(0).String()
		raw := call.Arg(1).String()
		if host == nil {
			return nil, fmt.Errorf("mako.%s requires an active Mako session", op)
		}
		b.pause()
		defer b.resume()

		out, callErr := dispatchHostCall(b.parent, host, op, []byte(raw))
		if callErr != nil {
			return nil, callErr
		}
		return call.Runtime().NewString(string(out)), nil
	}); err != nil {
		return "", err
	}

	if initV, err := rt.Eval(b, "mako-prelude.js", makoPreludeJS); err != nil {
		return "", scriptErrorWithBudget(b, err)
	} else {
		initV.Free()
	}

	resume()
	v, err := rt.Eval(b, t.Name+".md", program, qngg.TopLevelAwait())
	if err != nil {
		return "", scriptErrorWithBudget(b, err)
	}
	defer v.Free()
	return strings.TrimRight(v.String(), "\n"), nil
}

func dispatchHostCall(ctx context.Context, host Host, op string, raw []byte) ([]byte, error) {
	switch op {
	case "agents":
		var in struct {
			Agent   string `json:"agent"`
			Refresh bool   `json:"refresh"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("mako.agents: %w", err)
			}
		}
		res, err := host.Agents(ctx, strings.TrimSpace(in.Agent), in.Refresh)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "spawn":
		var req SpawnRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("mako.spawn: %w", err)
		}
		res, err := host.Spawn(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "ask":
		var req AskRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("mako.ask: %w", err)
		}
		res, err := host.Ask(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "decide":
		return host.Decide(ctx, raw)
	default:
		return nil, fmt.Errorf("unknown mako call %q", op)
	}
}

func scriptErrorWithBudget(b *scriptBudget, err error) error {
	if over := b.exceeded(); over != nil {
		return over
	}
	return scriptError(err)
}

func trappedScript(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("JavaScript runtime trapped: %v", r)
	}
}

func closeQuietly(rt *qngg.Runtime) {
	defer func() { _ = recover() }()
	rt.Close()
}
