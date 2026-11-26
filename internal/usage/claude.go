package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

const (
	defaultClaudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	claudeOAuthBetaHeader   = "oauth-2025-04-20"
	claudeKeychainService   = "Claude Code-credentials"
	claudeFallbackAccount   = "claude-code-user"
	claudeMinRetryAfter     = 5 * time.Second
	claudeDefaultRetryAfter = 60 * time.Second
	claudeMaxRetryAfter     = 30 * time.Minute
)

var (
	claudeKeychainAccountRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	claudeNonAlnumRe        = regexp.MustCompile(`[^a-z0-9]+`)
)

type claudeCredentialsFile struct {
	ClaudeAiOauth *claudeOAuth `json:"claudeAiOauth"`
}

type claudeOAuth struct {
	AccessToken      string  `json:"accessToken"`
	ExpiresAt        float64 `json:"expiresAt"`
	RefreshToken     string  `json:"refreshToken"`
	SubscriptionType string  `json:"subscriptionType"`
	RateLimitTier    string  `json:"rateLimitTier"`
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexFloat(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	return fmt.Errorf("expected number or numeric string, got %s", string(data))
}

type claudeUsageWindow struct {
	Utilization flexFloat `json:"utilization"`
	ResetsAt    string    `json:"resets_at"`
}

type claudeScopeLabel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type claudeLimitEntry struct {
	Kind     string     `json:"kind"`
	Percent  *flexFloat `json:"percent"`
	ResetsAt string     `json:"resets_at"`
	Scope    *struct {
		Model   *claudeScopeLabel `json:"model"`
		Surface *claudeScopeLabel `json:"surface"`
	} `json:"scope"`
}

type claudeUsageResponse struct {
	FiveHour         *claudeUsageWindow `json:"five_hour"`
	SevenDay         *claudeUsageWindow `json:"seven_day"`
	SevenDayOpus     *claudeUsageWindow `json:"seven_day_opus"`
	SevenDayOmelette *claudeUsageWindow `json:"seven_day_omelette"`
	Limits           []json.RawMessage  `json:"limits"`
}

type claudeScopedLimit struct {
	dimension string // "model" or "surface"
	id        string
	name      string
	usedPct   *float64
	resetsAt  int64
}

func (c *Collector) collectClaude(ctx context.Context, spec AgentSpec) *protocol.AgentAccountUsage {
	now := c.now()
	nowUnix := now.Unix()

	if base := strings.TrimRight(envLookup(spec.Env, "ANTHROPIC_BASE_URL"), "/"); base != "" && base != "https://api.anthropic.com" {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "unavailable",
			Detail:    "Using custom Anthropic endpoint",
			FetchedAt: nowUnix,
		}
	}
	if envEnabled(envLookup(spec.Env, "CLAUDE_CODE_USE_BEDROCK")) || envEnabled(envLookup(spec.Env, "CLAUDE_CODE_USE_VERTEX")) {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "unavailable",
			Detail:    "Using Bedrock/Vertex backend",
			FetchedAt: nowUnix,
		}
	}

	creds := c.loadClaudeCredentials(ctx, spec.Env)
	if creds == nil || strings.TrimSpace(creds.AccessToken) == "" {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "unavailable",
			Detail:    "Not signed in to Claude",
			FetchedAt: nowUnix,
		}
	}

	plan := formatClaudePlan(creds.SubscriptionType, creds.RateLimitTier)
	if creds.ExpiresAt > 0 {
		exp := creds.ExpiresAt
		if exp > 1e12 {
			exp /= 1000.0
		}
		if int64(exp) <= nowUnix {
			return &protocol.AgentAccountUsage{
				Agent:     "claude",
				Status:    "unavailable",
				Plan:      plan,
				Detail:    "Claude OAuth token expired; sign in again",
				FetchedAt: nowUnix,
			}
		}
	}

	tokenKey := sha256Hex(creds.AccessToken)
	c.mu.Lock()
	for k, until := range c.claudeLimitedUntil {
		if !now.Before(until) {
			delete(c.claudeLimitedUntil, k)
		}
	}
	limitedUntil := c.claudeLimitedUntil[tokenKey]
	c.mu.Unlock()
	if now.Before(limitedUntil) {
		wait := limitedUntil.Sub(now).Round(time.Second)
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "error",
			Plan:      plan,
			Detail:    fmt.Sprintf("Rate limited; retry in %s", wait),
			FetchedAt: nowUnix,
		}
	}

	endpoint := c.ClaudeEndpoint
	if endpoint == "" {
		endpoint = defaultClaudeUsageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &protocol.AgentAccountUsage{Agent: "claude", Status: "error", Plan: plan, Detail: err.Error(), FetchedAt: nowUnix}
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", claudeOAuthBetaHeader)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "error",
			Plan:      plan,
			Detail:    fmt.Sprintf("Claude usage request failed: %v", err),
			FetchedAt: nowUnix,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "unavailable",
			Plan:      plan,
			Detail:    fmt.Sprintf("Claude session rejected (HTTP %d); sign in again", resp.StatusCode),
			FetchedAt: nowUnix,
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), now)
		c.mu.Lock()
		if c.claudeLimitedUntil == nil {
			c.claudeLimitedUntil = map[string]time.Time{}
		}
		c.claudeLimitedUntil[tokenKey] = now.Add(retryAfter)
		c.mu.Unlock()
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "error",
			Plan:      plan,
			Detail:    fmt.Sprintf("Rate limited (HTTP 429); retry in %s", retryAfter.Round(time.Second)),
			FetchedAt: nowUnix,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "error",
			Plan:      plan,
			Detail:    fmt.Sprintf("Claude usage API returned HTTP %d", resp.StatusCode),
			FetchedAt: nowUnix,
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &protocol.AgentAccountUsage{Agent: "claude", Status: "error", Plan: plan, Detail: err.Error(), FetchedAt: nowUnix}
	}
	var parsed claudeUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return &protocol.AgentAccountUsage{Agent: "claude", Status: "error", Plan: plan, Detail: "Invalid Claude usage JSON", FetchedAt: nowUnix}
	}

	limits := buildClaudeLimits(&parsed)
	if len(limits) == 0 {
		return &protocol.AgentAccountUsage{
			Agent:     "claude",
			Status:    "unavailable",
			Plan:      plan,
			Detail:    "No usage windows reported",
			FetchedAt: nowUnix,
		}
	}
	return &protocol.AgentAccountUsage{
		Agent:     "claude",
		Status:    "available",
		Plan:      plan,
		Limits:    limits,
		FetchedAt: nowUnix,
	}
}

func (c *Collector) loadClaudeCredentials(ctx context.Context, env map[string]string) *claudeOAuth {
	configDir := envLookup(env, "CLAUDE_CONFIG_DIR")
	service := claudeKeychainService
	if configDir != "" {
		service = claudeKeychainService + "-" + sha256Hex(configDir)[:8]
	}
	account := claudeKeychainAccount(env)

	readKeychain := c.ReadClaudeKeychain
	if readKeychain == nil {
		readKeychain = readClaudeKeychainPlatform
	}
	if raw, err := readKeychain(ctx, service, account); err == nil && raw != "" {
		var f claudeCredentialsFile
		if json.Unmarshal([]byte(raw), &f) == nil && f.ClaudeAiOauth != nil && f.ClaudeAiOauth.AccessToken != "" {
			return f.ClaudeAiOauth
		}
	}

	dir := configDir
	if dir == "" {
		if home := userHome(env); home != "" {
			dir = filepath.Join(home, ".claude")
		}
	}
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return nil
	}
	var f claudeCredentialsFile
	if json.Unmarshal(data, &f) != nil || f.ClaudeAiOauth == nil || f.ClaudeAiOauth.AccessToken == "" {
		return nil
	}
	return f.ClaudeAiOauth
}

func claudeKeychainAccount(env map[string]string) string {
	u := envLookup(env, "USER")
	if u == "" {
		if cur, err := user.Current(); err == nil {
			u = cur.Username
		}
	}
	if claudeKeychainAccountRe.MatchString(u) {
		return u
	}
	return claudeFallbackAccount
}

func formatClaudePlan(subType, tier string) string {
	subType = strings.TrimSpace(subType)
	if subType == "" {
		return ""
	}
	label := strings.ToUpper(subType[:1]) + subType[1:]
	if tier = strings.TrimSpace(tier); tier != "" {
		parts := strings.Split(tier, "_")
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" {
			return label + " " + last
		}
	}
	return label
}

func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return claudeDefaultRetryAfter
	}
	var d time.Duration
	if secs, err := strconv.Atoi(header); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(header); err == nil {
		d = t.Sub(now)
	} else {
		return claudeDefaultRetryAfter
	}
	if d < claudeMinRetryAfter {
		return claudeMinRetryAfter
	}
	if d > claudeMaxRetryAfter {
		return claudeMaxRetryAfter
	}
	return d
}

func buildClaudeLimits(resp *claudeUsageResponse) []*protocol.AccountLimit {
	var out []*protocol.AccountLimit
	if resp.FiveHour != nil {
		out = append(out, &protocol.AccountLimit{
			Id:      "five_hour",
			Label:   "5h",
			Minutes: 5 * 60,
			Used:    clampPct(float64(resp.FiveHour.Utilization)),
			Resets:  parseISO8601Unix(resp.FiveHour.ResetsAt),
		})
	}
	if resp.SevenDay != nil {
		out = append(out, &protocol.AccountLimit{
			Id:      "weekly",
			Label:   "Weekly",
			Minutes: 7 * 24 * 60,
			Used:    clampPct(float64(resp.SevenDay.Utilization)),
			Resets:  parseISO8601Unix(resp.SevenDay.ResetsAt),
		})
	}

	var legacy []claudeScopedLimit
	if resp.SevenDayOpus != nil {
		u := clampPct(float64(resp.SevenDayOpus.Utilization))
		legacy = append(legacy, claudeScopedLimit{
			dimension: "model",
			name:      "Opus",
			usedPct:   &u,
			resetsAt:  parseISO8601Unix(resp.SevenDayOpus.ResetsAt),
		})
	}
	if resp.SevenDayOmelette != nil {
		u := clampPct(float64(resp.SevenDayOmelette.Utilization))
		legacy = append(legacy, claudeScopedLimit{
			dimension: "model",
			name:      "Omelette",
			usedPct:   &u,
			resetsAt:  parseISO8601Unix(resp.SevenDayOmelette.ResetsAt),
		})
	}

	var fromArray []claudeScopedLimit
	for _, raw := range resp.Limits {
		var entry claudeLimitEntry
		if json.Unmarshal(raw, &entry) != nil || entry.Kind != "weekly_scoped" || entry.Scope == nil {
			continue
		}
		var dim string
		var scope *claudeScopeLabel
		if entry.Scope.Model != nil {
			dim, scope = "model", entry.Scope.Model
		} else if entry.Scope.Surface != nil {
			dim, scope = "surface", entry.Scope.Surface
		}
		if scope == nil {
			continue
		}
		id := strings.TrimSpace(scope.ID)
		name := strings.TrimSpace(scope.DisplayName)
		if name == "" {
			name = id
		}
		if name == "" {
			continue
		}
		var pct *float64
		if entry.Percent != nil {
			v := clampPct(float64(*entry.Percent))
			pct = &v
		}
		fromArray = append(fromArray, claudeScopedLimit{
			dimension: dim,
			id:        id,
			name:      name,
			usedPct:   pct,
			resetsAt:  parseISO8601Unix(entry.ResetsAt),
		})
	}

	reconciled := reconcileClaudeScopedLimits(legacy, fromArray)
	taken := map[string]bool{}
	for _, lim := range reconciled {
		if lim.usedPct == nil {
			continue
		}
		key := lim.id
		if key == "" {
			key = normalizeClaudeScopeName(lim.name)
		}
		baseID := fmt.Sprintf("weekly_%s_%s", lim.dimension, key)
		id := baseID
		for suffix := 2; taken[id]; suffix++ {
			id = fmt.Sprintf("%s_%d", baseID, suffix)
		}
		taken[id] = true
		out = append(out, &protocol.AccountLimit{
			Id:      id,
			Label:   "Weekly",
			Group:   lim.name,
			Minutes: 7 * 24 * 60,
			Used:    *lim.usedPct,
			Resets:  lim.resetsAt,
		})
	}
	return out
}

func reconcileClaudeScopedLimits(legacy, fromArray []claudeScopedLimit) []claudeScopedLimit {
	out := append([]claudeScopedLimit(nil), legacy...)
	for _, item := range fromArray {
		idx := -1
		for i, cand := range out {
			if sameClaudeScopedLimit(cand, item) {
				idx = i
				break
			}
		}
		if idx == -1 {
			out = append(out, item)
			continue
		}
		merged := item
		if merged.usedPct == nil {
			merged.usedPct = out[idx].usedPct
		}
		if merged.resetsAt == 0 {
			merged.resetsAt = out[idx].resetsAt
		}
		out[idx] = merged
	}
	return out
}

func sameClaudeScopedLimit(a, b claudeScopedLimit) bool {
	if a.dimension != b.dimension {
		return false
	}
	if a.id != "" && b.id != "" {
		return a.id == b.id
	}
	return normalizeClaudeScopeName(a.name) == normalizeClaudeScopeName(b.name)
}

func normalizeClaudeScopeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = claudeNonAlnumRe.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}
