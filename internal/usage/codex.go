package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

const defaultCodexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

type codexAuthFile struct {
	Tokens *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type codexUsageWindow struct {
	UsedPercent        *flexFloat `json:"used_percent"`
	ResetAt            *flexFloat `json:"reset_at"`
	LimitWindowSeconds *flexFloat `json:"limit_window_seconds"`
}

type codexRateLimit struct {
	PrimaryWindow   *codexUsageWindow `json:"primary_window"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window"`
}

type codexUsageResponse struct {
	PlanType             string          `json:"plan_type"`
	Email                string          `json:"email"`
	RateLimit            *codexRateLimit `json:"rate_limit"`
	AdditionalRateLimits []struct {
		LimitName      string          `json:"limit_name"`
		MeteredFeature string          `json:"metered_feature"`
		RateLimit      *codexRateLimit `json:"rate_limit"`
	} `json:"additional_rate_limits"`
	CodeReviewRateLimit *codexRateLimit `json:"code_review_rate_limit"`
	Credits             *struct {
		Balance *flexFloat `json:"balance"`
	} `json:"credits"`
}

func (c *Collector) collectCodex(ctx context.Context, spec AgentSpec) *protocol.AgentAccountUsage {
	nowUnix := c.now().Unix()

	if envLookup(spec.Env, "OPENAI_BASE_URL") != "" {
		return &protocol.AgentAccountUsage{
			Agent:     "codex",
			Status:    "unavailable",
			Detail:    "Using custom OpenAI endpoint",
			FetchedAt: nowUnix,
		}
	}

	token, accountID := loadCodexAuth(spec.Env)
	if token == "" {
		return &protocol.AgentAccountUsage{
			Agent:     "codex",
			Status:    "unavailable",
			Detail:    "Not signed in to Codex",
			FetchedAt: nowUnix,
		}
	}

	endpoint := c.CodexEndpoint
	if endpoint == "" {
		endpoint = defaultCodexUsageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &protocol.AgentAccountUsage{Agent: "codex", Status: "error", Detail: err.Error(), FetchedAt: nowUnix}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return &protocol.AgentAccountUsage{
			Agent:     "codex",
			Status:    "error",
			Detail:    fmt.Sprintf("Codex usage request failed: %v", err),
			FetchedAt: nowUnix,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &protocol.AgentAccountUsage{
			Agent:     "codex",
			Status:    "unavailable",
			Detail:    fmt.Sprintf("Codex session rejected (HTTP %d); sign in again", resp.StatusCode),
			FetchedAt: nowUnix,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &protocol.AgentAccountUsage{
			Agent:     "codex",
			Status:    "error",
			Detail:    fmt.Sprintf("Codex usage API returned HTTP %d", resp.StatusCode),
			FetchedAt: nowUnix,
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &protocol.AgentAccountUsage{Agent: "codex", Status: "error", Detail: err.Error(), FetchedAt: nowUnix}
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "<") {
		return &protocol.AgentAccountUsage{Agent: "codex", Status: "error", Detail: "Codex usage API returned HTML", FetchedAt: nowUnix}
	}

	var parsed codexUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return &protocol.AgentAccountUsage{Agent: "codex", Status: "error", Detail: "Invalid Codex usage JSON", FetchedAt: nowUnix}
	}

	var limits []*protocol.AccountLimit
	limits = append(limits, codexLimitsFromRateLimit(parsed.RateLimit, "", "")...)
	for _, add := range parsed.AdditionalRateLimits {
		id := strings.TrimSpace(add.MeteredFeature)
		if id == "" {
			id = strings.TrimSpace(add.LimitName)
		}
		if id == "" {
			continue
		}
		group := strings.TrimSpace(add.LimitName)
		if group == "" {
			group = id
		}
		limits = append(limits, codexLimitsFromRateLimit(add.RateLimit, "limit:"+url.QueryEscape(id), group)...)
	}
	limits = append(limits, codexLimitsFromRateLimit(parsed.CodeReviewRateLimit, "code_review", "Code review")...)

	var credits *float64
	if parsed.Credits != nil && parsed.Credits.Balance != nil {
		v := float64(*parsed.Credits.Balance)
		credits = &v
	}

	plan := formatCodexPlan(parsed.PlanType)
	if len(limits) == 0 && credits == nil {
		return &protocol.AgentAccountUsage{
			Agent:     "codex",
			Status:    "unavailable",
			Plan:      plan,
			Detail:    "No usage windows reported",
			FetchedAt: nowUnix,
		}
	}
	return &protocol.AgentAccountUsage{
		Agent:     "codex",
		Status:    "available",
		Plan:      plan,
		Limits:    limits,
		Credits:   credits,
		FetchedAt: nowUnix,
	}
}

func loadCodexAuth(env map[string]string) (token, accountID string) {
	var paths []string
	if dir := envLookup(env, "CODEX_HOME"); dir != "" {
		paths = append(paths, filepath.Join(dir, "auth.json"))
	}
	if home := userHome(env); home != "" {
		p := filepath.Join(home, ".codex", "auth.json")
		if len(paths) == 0 || paths[0] != p {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f codexAuthFile
		if json.Unmarshal(data, &f) == nil && f.Tokens != nil && strings.TrimSpace(f.Tokens.AccessToken) != "" {
			return strings.TrimSpace(f.Tokens.AccessToken), strings.TrimSpace(f.Tokens.AccountID)
		}
	}
	return "", ""
}

func codexLimitsFromRateLimit(rl *codexRateLimit, scopeID, group string) []*protocol.AccountLimit {
	if rl == nil {
		return nil
	}
	slots := []struct {
		win     *codexUsageWindow
		primary bool
	}{
		{rl.PrimaryWindow, true},
		{rl.SecondaryWindow, false},
	}
	var out []*protocol.AccountLimit
	for _, s := range slots {
		if s.win == nil || s.win.UsedPercent == nil {
			continue
		}
		var mins int64
		if s.win.LimitWindowSeconds != nil && *s.win.LimitWindowSeconds > 0 {
			mins = int64(*s.win.LimitWindowSeconds) / 60
		}
		var resets int64
		if s.win.ResetAt != nil && *s.win.ResetAt > 0 {
			resets = int64(*s.win.ResetAt)
		}
		id, label := codexWindowIDLabel(mins, s.primary)
		if scopeID != "" {
			id = scopeID + ":" + id
		}
		out = append(out, &protocol.AccountLimit{
			Id:      id,
			Label:   label,
			Minutes: mins,
			Used:    clampPct(float64(*s.win.UsedPercent)),
			Resets:  resets,
			Group:   group,
		})
	}
	return out
}

func codexWindowIDLabel(mins int64, primary bool) (string, string) {
	switch {
	case mins >= 6*24*60 && mins <= 8*24*60:
		return "weekly", "Weekly"
	case mins > 0 && mins%60 == 0 && mins < 24*60:
		h := mins / 60
		return fmt.Sprintf("%dh", h), fmt.Sprintf("%dh", h)
	case mins > 0 && mins%(24*60) == 0:
		d := mins / (24 * 60)
		return fmt.Sprintf("%dd", d), fmt.Sprintf("%dd", d)
	case mins > 0:
		return fmt.Sprintf("%dm", mins), fmt.Sprintf("%dm", mins)
	case primary:
		return "unknown_primary", "Primary limit"
	default:
		return "unknown_secondary", "Secondary limit"
	}
}

func formatCodexPlan(plan string) string {
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return ""
	}
	return strings.ToUpper(plan[:1]) + plan[1:]
}
