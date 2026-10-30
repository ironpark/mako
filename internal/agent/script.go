package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/protocol"
)

// WriteOnce writes source in dir as name-<its hash><ext>, unless it is
// there already, and returns its path: a file mako makes for an agent, the
// same for every launch that makes it alike, so none need clean it up.
func WriteOnce(dir, name, ext, source string) (string, error) {
	sum := sha256.Sum256([]byte(source))
	path := filepath.Join(dir, name+"-"+hex.EncodeToString(sum[:6])+ext)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(source), 0o600)
}

// HookScript is JavaScript, for the extensions and plugins mako writes for
// agents that load them: runHook(hook, input) runs the shell command hook
// with input as JSON on its standard input, as Claude Code runs its hooks,
// and nothing waits on it.
const HookScript = `import { spawn } from "node:child_process";

let hookSequence = 0n;

function runHook(hook, input) {
	try {
		const now = BigInt(Date.now()) * 1000000n;
		hookSequence = now > hookSequence ? now : hookSequence + 1n;
		const child = spawn(hook, { shell: true, stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
		child.on("error", () => {});
		child.stdin.on("error", () => {});
		child.stdin.end(JSON.stringify({ ...input, mako_hook_seq: hookSequence.toString() }));
		child.unref();
	} catch {}
}
`

// Tokens are a model call's tokens, as pi logs them and mako's OpenCode
// plugin writes them down.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// Add counts t into total; a reply's tokens are also what its context
// holds.
func (t Tokens) Add(total *protocol.Usage, reply bool) {
	total.Input += t.Input + t.CacheWrite
	total.CachedInput += t.CacheRead
	total.Output += t.Output
	if reply {
		total.Context = t.Input + t.CacheRead + t.CacheWrite
	}
}
