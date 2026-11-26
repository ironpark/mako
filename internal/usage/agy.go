package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/agy"
	"github.com/ironpark/mako/internal/protocol"
)

const agyUsageTimeout = 18 * time.Second

var (
	agyTrailingModelsRe    = regexp.MustCompile(`(?i)(?:^|\s+)models$`)
	agyTrailingRemainingRe = regexp.MustCompile(`(?i)(?:\s+limit)?\s+remaining$`)
)

type agyUsageResponse struct {
	Status   string `json:"status"`
	Error    string `json:"error"`
	Response string `json:"response"`
	Command  *struct {
		Name string `json:"name"`
		Data struct {
			Groups []struct {
				Name    string `json:"name"`
				Buckets []struct {
					ID                string   `json:"id"`
					Name              string   `json:"name"`
					Window            string   `json:"window"`
					RemainingFraction *float64 `json:"remaining_fraction"`
					ResetTime         string   `json:"reset_time"`
					Disabled          bool     `json:"disabled"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

func (c *Collector) collectAgy(ctx context.Context, spec AgentSpec) *protocol.AgentAccountUsage {
	now := c.now().Unix()
	diag := agy.SignInDiagnostic(spec.Env)

	bin := spec.Program
	if bin == "" && c.RunCommand == nil {
		if found, err := agent.Find("agy"); err == nil {
			bin = found
		}
	}
	if bin == "" {
		if c.RunCommand == nil {
			return &protocol.AgentAccountUsage{
				Agent:     "agy",
				Status:    "unavailable",
				Detail:    "agy CLI not installed",
				FetchedAt: now,
			}
		}
		bin = "agy"
	}

	tctx, cancel := context.WithTimeout(ctx, agyUsageTimeout)
	defer cancel()
	out, err := c.runCommand(tctx, bin, []string{"-p", "/usage", "--output-format", "json"}, spec.Env)
	if err != nil {
		return &protocol.AgentAccountUsage{
			Agent:     "agy",
			Status:    "error",
			Detail:    fmt.Sprintf("agy usage failed (check sign-in). %s", diag),
			FetchedAt: now,
		}
	}

	var resp agyUsageResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return &protocol.AgentAccountUsage{
			Agent:     "agy",
			Status:    "error",
			Detail:    fmt.Sprintf("Invalid agy usage JSON. %s", diag),
			FetchedAt: now,
		}
	}
	if resp.Status != "SUCCESS" {
		msg := strings.TrimSpace(resp.Error)
		if msg == "" {
			msg = strings.TrimSpace(resp.Response)
		}
		if msg == "" {
			msg = fmt.Sprintf("agy usage returned %s", resp.Status)
		}
		return &protocol.AgentAccountUsage{
			Agent:     "agy",
			Status:    "error",
			Detail:    fmt.Sprintf("%s (%s)", msg, diag),
			FetchedAt: now,
		}
	}
	if resp.Command == nil || resp.Command.Name != "usage" {
		return &protocol.AgentAccountUsage{
			Agent:     "agy",
			Status:    "unavailable",
			Detail:    "No usage quota reported by agy",
			FetchedAt: now,
		}
	}

	var limits []*protocol.AccountLimit
	for _, group := range resp.Command.Data.Groups {
		groupLabel := strings.TrimSpace(agyTrailingModelsRe.ReplaceAllString(strings.TrimSpace(group.Name), ""))
		for _, bucket := range group.Buckets {
			if bucket.ID == "" {
				continue
			}
			if !bucket.Disabled && bucket.RemainingFraction == nil {
				continue
			}
			var used float64
			if !bucket.Disabled && bucket.RemainingFraction != nil {
				used = clampPct((1.0 - *bucket.RemainingFraction) * 100.0)
			}
			label := formatAgyWindowLabel(bucket.Name, bucket.Window)
			var resets int64
			if !bucket.Disabled {
				resets = parseISO8601Unix(bucket.ResetTime)
			}
			limits = append(limits, &protocol.AccountLimit{
				Id:       bucket.ID,
				Label:    label,
				Minutes:  agyWindowMinutes(bucket.Window),
				Used:     used,
				Resets:   resets,
				Disabled: bucket.Disabled,
				Group:    groupLabel,
			})
		}
	}
	if len(limits) == 0 {
		return &protocol.AgentAccountUsage{
			Agent:     "agy",
			Status:    "unavailable",
			Detail:    "No usage quota reported by agy",
			FetchedAt: now,
		}
	}
	return &protocol.AgentAccountUsage{
		Agent:     "agy",
		Status:    "available",
		Detail:    diag,
		Limits:    limits,
		FetchedAt: now,
	}
}

func formatAgyWindowLabel(name, window string) string {
	w := strings.ToLower(strings.TrimSpace(window))
	if w == "5h" {
		return "5h"
	}
	cleaned := strings.TrimSpace(agyTrailingRemainingRe.ReplaceAllString(strings.TrimSpace(name), ""))
	if cleaned != "" {
		return cleaned
	}
	switch w {
	case "weekly":
		return "Weekly"
	case "monthly":
		return "Monthly"
	default:
		return strings.TrimSpace(window)
	}
}

func agyWindowMinutes(window string) int64 {
	w := strings.ToLower(strings.TrimSpace(window))
	switch w {
	case "5h":
		return 5 * 60
	case "weekly":
		return 7 * 24 * 60
	case "monthly":
		return 30 * 24 * 60
	}
	if len(w) >= 2 {
		n, err := strconv.ParseInt(w[:len(w)-1], 10, 64)
		if err == nil && n > 0 {
			switch w[len(w)-1] {
			case 'm':
				return n
			case 'h':
				return n * 60
			case 'd':
				return n * 24 * 60
			}
		}
	}
	return 0
}
