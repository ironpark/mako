// Package usage collects session-independent account quota and rate-limit
// snapshots for installed agents (claude, codex, agy).
package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
)

const (
	defaultCacheTTL      = 2 * time.Minute
	defaultErrorCacheTTL = 30 * time.Second
	defaultHTTPTimeout   = 15 * time.Second
)

// AgentSpec describes one installed agent whose account quota should be queried.
type AgentSpec struct {
	Name    string            // "claude", "codex", "agy"
	Program string            // resolved binary path (empty if not found)
	Env     map[string]string // effective environment overrides for the agent
}

type cacheEntry struct {
	usage     *protocol.AgentAccountUsage
	expiresAt time.Time
}

// Collector queries and caches account-level usage across agents.
type Collector struct {
	HTTPClient         *http.Client
	CacheTTL           time.Duration
	Now                func() time.Time
	RunCommand         func(ctx context.Context, bin string, args []string, env map[string]string) ([]byte, error)
	ReadClaudeKeychain func(ctx context.Context, service, account string) (string, error)
	ClaudeEndpoint     string
	CodexEndpoint      string

	mu                 sync.Mutex
	cache              map[string]cacheEntry
	claudeLimitedUntil map[string]time.Time
}

// NewCollector returns a Collector with default HTTP timeouts and cache TTL.
func NewCollector() *Collector {
	return &Collector{
		HTTPClient: &http.Client{Timeout: defaultHTTPTimeout},
		CacheTTL:   defaultCacheTTL,
		Now:        time.Now,
	}
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func (c *Collector) cacheTTL() time.Duration {
	if c.CacheTTL > 0 {
		return c.CacheTTL
	}
	return defaultCacheTTL
}

func (c *Collector) runCommand(ctx context.Context, bin string, args []string, env map[string]string) ([]byte, error) {
	if c.RunCommand != nil {
		return c.RunCommand(ctx, bin, args, env)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if len(env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), env)
	}
	return safeexec.Output(cmd)
}

// Collect queries all specified agents concurrently and returns their snapshots
// in the same order as specs.
func (c *Collector) Collect(ctx context.Context, specs []AgentSpec, refresh bool) *protocol.AccountUsage {
	results := make([]*protocol.AgentAccountUsage, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		wg.Go(func() {
			results[i] = c.collectOne(ctx, spec, refresh)
		})
	}
	wg.Wait()

	out := &protocol.AccountUsage{Accounts: make([]*protocol.AgentAccountUsage, 0, len(results))}
	for _, r := range results {
		if r != nil {
			out.Accounts = append(out.Accounts, r)
		}
	}
	return out
}

func (c *Collector) collectOne(ctx context.Context, spec AgentSpec, refresh bool) *protocol.AgentAccountUsage {
	key := cacheKeyForSpec(spec)
	now := c.now()
	if !refresh {
		c.mu.Lock()
		entry, ok := c.cache[key]
		c.mu.Unlock()
		if ok && now.Before(entry.expiresAt) {
			return proto.CloneOf(entry.usage)
		}
	}

	var res *protocol.AgentAccountUsage
	switch spec.Name {
	case "claude":
		res = c.collectClaude(ctx, spec)
	case "codex":
		res = c.collectCodex(ctx, spec)
	case "agy":
		res = c.collectAgy(ctx, spec)
	default:
		return nil
	}
	if res == nil {
		return nil
	}
	if res.FetchedAt == 0 {
		res.FetchedAt = c.now().Unix()
	}

	ttl := c.cacheTTL()
	if res.Status == "error" && ttl > defaultErrorCacheTTL {
		ttl = defaultErrorCacheTTL
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cacheEntry{}
	}
	c.cache[key] = cacheEntry{
		usage:     proto.CloneOf(res),
		expiresAt: c.now().Add(ttl),
	}
	c.mu.Unlock()
	return res
}

func cacheKeyForSpec(spec AgentSpec) string {
	var b strings.Builder
	b.WriteString(spec.Name)
	b.WriteByte('\x00')
	b.WriteString(spec.Program)
	keys := []string{
		"HOME", "USERPROFILE", "USER",
		"CLAUDE_CONFIG_DIR", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
		"CODEX_HOME", "OPENAI_BASE_URL",
		"ANTIGRAVITY_HOME", "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY",
	}
	for _, k := range keys {
		b.WriteByte('\x00')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(envLookup(spec.Env, k))
	}
	return b.String()
}

func envLookup(env map[string]string, key string) string {
	if env != nil {
		if v, ok := env[key]; ok {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(os.Getenv(key))
}

func userHome(env map[string]string) string {
	if h := envLookup(env, "HOME"); h != "" {
		return h
	}
	if h := envLookup(env, "USERPROFILE"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func mergeEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := overrides[k]; replaced {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func parseISO8601Unix(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return 0
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func envEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
