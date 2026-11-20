package agy

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

type quotaBucket struct {
	RemainingFraction   *float64 `json:"remaining_fraction"`
	RemainingPercentage *float64 `json:"remaining_percentage"`
	UsedPercentage      *float64 `json:"used_percentage"`
	ResetTime           string   `json:"reset_time"`
	ResetsAt            int64    `json:"resets_at"`
	ResetInSeconds      int64    `json:"reset_in_seconds"`
	Disabled            bool     `json:"disabled"`
}

// Status reads agy's statusLine JSON payload for context window size, current
// usage, model/effort, and account quota limits (5h / weekly remaining quota).
func (*Agent) Status(input []byte) *protocol.Usage {
	var state struct {
		Model struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Effort      string `json:"effort"`
		} `json:"model"`
		Context struct {
			TotalInput          int64    `json:"total_input_tokens"`
			TotalOutput         int64    `json:"total_output_tokens"`
			Size                int64    `json:"context_window_size"`
			UsedPercentage      *float64 `json:"used_percentage"`
			RemainingPercentage *float64 `json:"remaining_percentage"`
			CurrentUsage        *struct {
				Input       int64 `json:"input_tokens"`
				CacheCreate int64 `json:"cache_creation_input_tokens"`
				CacheRead   int64 `json:"cache_read_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
		Quota      map[string]quotaBucket `json:"quota"`
		Quotas     map[string]quotaBucket `json:"quotas"`
		RateLimits struct {
			FiveHour *quotaBucket `json:"five_hour"`
			SevenDay *quotaBucket `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(input, &state) != nil {
		return &protocol.Usage{}
	}
	u := &protocol.Usage{
		Input:  state.Context.TotalInput,
		Output: state.Context.TotalOutput,
		Window: state.Context.Size,
		Model:  state.Model.DisplayName,
		Effort: state.Model.Effort,
	}
	if u.Model == "" {
		u.Model = state.Model.ID
	}
	if cu := state.Context.CurrentUsage; cu != nil && (cu.Input > 0 || cu.CacheRead > 0 || cu.CacheCreate > 0) {
		u.Context = cu.Input + cu.CacheRead + cu.CacheCreate
	} else if state.Context.Size > 0 {
		if state.Context.UsedPercentage != nil && *state.Context.UsedPercentage > 0 {
			u.Context = int64(math.Round(float64(state.Context.Size) * (*state.Context.UsedPercentage) / 100))
		} else if state.Context.RemainingPercentage != nil && *state.Context.RemainingPercentage < 100 {
			u.Context = int64(math.Round(float64(state.Context.Size) * (100 - *state.Context.RemainingPercentage) / 100))
		}
	}

	now := time.Now()
	if state.RateLimits.FiveHour != nil {
		if rl := quotaToRateLimit(protocol.FiveHours, *state.RateLimits.FiveHour, now); rl != nil {
			u.SetLimit(rl)
		}
	}
	if state.RateLimits.SevenDay != nil {
		if rl := quotaToRateLimit(protocol.Week, *state.RateLimits.SevenDay, now); rl != nil {
			u.SetLimit(rl)
		}
	}

	buckets := state.Quota
	if len(buckets) == 0 {
		buckets = state.Quotas
	}
	if len(buckets) > 0 {
		family := modelQuotaFamily(u.Model)
		applyQuotaBuckets(u, buckets, family, now)
	}
	return u
}

func modelQuotaFamily(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.Contains(m, "gpt"):
		return "gpt"
	case strings.Contains(m, "gemini") || strings.Contains(m, "orchestrator"):
		return "gemini"
	}
	return ""
}

func applyQuotaBuckets(u *protocol.Usage, buckets map[string]quotaBucket, family string, now time.Time) {
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	best := map[int64]struct {
		score  int
		bucket quotaBucket
	}{}
	for _, k := range keys {
		b := buckets[k]
		if b.Disabled {
			continue
		}
		mins := bucketMinutes(k)
		if mins <= 0 {
			continue
		}
		score := 1
		kl := strings.ToLower(k)
		if family != "" && strings.Contains(kl, family) {
			score = 3
		} else if family == "" && strings.Contains(kl, "gemini") {
			score = 2
		}
		if cur, ok := best[mins]; !ok || score > cur.score {
			best[mins] = struct {
				score  int
				bucket quotaBucket
			}{score: score, bucket: b}
		}
	}
	for mins, chosen := range best {
		if rl := quotaToRateLimit(mins, chosen.bucket, now); rl != nil {
			u.SetLimit(rl)
		}
	}
}

func bucketMinutes(name string) int64 {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "5h") || strings.Contains(n, "five_hour") || strings.Contains(n, "sprint"):
		return protocol.FiveHours
	case strings.Contains(n, "week") || strings.Contains(n, "7d") || strings.Contains(n, "seven_day"):
		return protocol.Week
	case strings.Contains(n, "24h") || strings.Contains(n, "daily") || strings.Contains(n, "1d"):
		return 24 * 60
	}
	return 0
}

func quotaToRateLimit(minutes int64, b quotaBucket, now time.Time) *protocol.RateLimit {
	if b.Disabled {
		return nil
	}
	var used float64
	switch {
	case b.UsedPercentage != nil:
		used = *b.UsedPercentage
	case b.RemainingFraction != nil:
		used = (1 - *b.RemainingFraction) * 100
	case b.RemainingPercentage != nil:
		used = 100 - *b.RemainingPercentage
	default:
		return nil
	}
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	resets := b.ResetsAt
	if resets == 0 && b.ResetTime != "" {
		if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
			resets = t.Unix()
		}
	}
	if resets == 0 && b.ResetInSeconds > 0 {
		resets = now.Add(time.Duration(b.ResetInSeconds) * time.Second).Unix()
	}
	return &protocol.RateLimit{Minutes: minutes, Used: used, Resets: resets}
}

// UserStatusLine returns the custom statusLine command from agy's settings.json,
// if enabled, so mako's statusLine wrapper can forward its output.
func (*Agent) UserStatusLine(_ []byte) string {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		return ""
	}
	var settings struct {
		StatusLine *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Enabled *bool  `json:"enabled"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(body, &settings) != nil || settings.StatusLine == nil {
		return ""
	}
	if settings.StatusLine.Enabled != nil && !*settings.StatusLine.Enabled {
		return ""
	}
	if settings.StatusLine.Type != "" && settings.StatusLine.Type != "command" {
		return ""
	}
	return settings.StatusLine.Command
}
