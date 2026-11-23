package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// The kinds of event that tell a plugin how an asynchronous decision
// evaluation went; it need not subscribe to them.
const (
	EventDecisionAnswered = "decisionAnswered"
	EventDecisionFailed   = "decisionFailed"

	maxDecisionRequestBytes = 256 << 10
	maxDecisionQuestions    = 32
	maxDecisionTagBytes     = 512
	decisionsPerMin         = 60
	decisionEvalDefault     = 15 * time.Second
	decisionEvalMax         = 20 * time.Second
	decisionSubmitDefault   = 30 * time.Second
	decisionSubmitMax       = 45 * time.Second
	maxDecisionInflight     = 2
)

// ErrDecisionUnavailable is returned by DecisionHost.Evaluate when no
// decision provider has an API key configured.
var ErrDecisionUnavailable = errors.New("decision unavailable")

// DecisionStatus is what DecisionHost.Status tells a plugin.
type DecisionStatus struct {
	Available       bool
	Provider, Model string
}

// DecisionHost is the daemon's side of the decision permission.
type DecisionHost interface {
	Status() DecisionStatus
	// Evaluate evaluates requestJSON (provider and model already stripped by
	// the host) and returns the normalized response JSON. An error that
	// errors.Is ErrDecisionUnavailable means no provider has a key.
	Evaluate(ctx context.Context, plugin string, requestJSON []byte) (response []byte, provider, model string, err error)
}

func (h *host) DecisionStatus(_ context.Context, _ *hostapi.StatusRequest) (*hostapi.StatusResponse, error) {
	dec := h.p.m.cfg.Decisions
	if dec == nil {
		return &hostapi.StatusResponse{}, nil
	}
	st := dec.Status()
	if !st.Available {
		return &hostapi.StatusResponse{}, nil
	}
	return &hostapi.StatusResponse{
		Available: true,
		Provider:  st.Provider,
		Model:     st.Model,
	}, nil
}

func (h *host) DecisionEvaluate(ctx context.Context, req *hostapi.EvaluateRequest) (*hostapi.EvaluateResponse, error) {
	p := h.p
	dec := p.m.cfg.Decisions
	if dec == nil {
		return nil, fmt.Errorf("%w: this node has no decision model for plugins", ErrDecisionUnavailable)
	}
	if !p.decisionRate.take() {
		return nil, fmt.Errorf("the plugin called decision more than %d times a minute", decisionsPerMin)
	}
	clean, err := sanitizeDecisionRequest(req.GetRequestJson())
	if err != nil {
		return nil, err
	}
	timeout := clampDecisionTimeout(req.GetTimeoutMs(), decisionEvalDefault, decisionEvalMax)
	evalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	resp, prov, model, err := dec.Evaluate(evalCtx, p.id, clean)
	if err != nil {
		if errors.Is(err, ErrDecisionUnavailable) && !strings.Contains(strings.ToLower(err.Error()), "decision unavailable") {
			return nil, fmt.Errorf("%w: %v", ErrDecisionUnavailable, err)
		}
		return nil, err
	}
	return &hostapi.EvaluateResponse{
		ResponseJson: string(resp),
		Provider:     prov,
		Model:        model,
		ElapsedMs:    elapsedMillis(start),
	}, nil
}

func (h *host) DecisionSubmit(_ context.Context, req *hostapi.SubmitRequest) (*hostapi.SubmitResponse, error) {
	p := h.p
	dec := p.m.cfg.Decisions
	if dec == nil {
		return nil, fmt.Errorf("%w: this node has no decision model for plugins", ErrDecisionUnavailable)
	}
	tag := req.GetTag()
	if len(tag) > maxDecisionTagBytes || !utf8.ValidString(tag) {
		return nil, fmt.Errorf("a tag is UTF-8 of at most %d bytes", maxDecisionTagBytes)
	}
	if !p.decisionRate.take() {
		return nil, fmt.Errorf("the plugin called decision more than %d times a minute", decisionsPerMin)
	}
	clean, err := sanitizeDecisionRequest(req.GetRequestJson())
	if err != nil {
		return nil, err
	}
	timeout := clampDecisionTimeout(req.GetTimeoutMs(), decisionSubmitDefault, decisionSubmitMax)
	id := "d" + nonce()

	p.pendingMu.Lock()
	if len(p.decJobs) >= maxDecisionInflight {
		p.pendingMu.Unlock()
		return nil, fmt.Errorf("at most %d decision requests may run at once", maxDecisionInflight)
	}
	jobCtx, cancel := context.WithTimeout(p.m.ctx, timeout)
	p.decJobs[id] = cancel
	p.pendingMu.Unlock()

	go p.runDecision(id, clean, tag, jobCtx, cancel)
	return &hostapi.SubmitResponse{RequestId: id}, nil
}

func (p *plugin) delDecJob(id string) {
	p.pendingMu.Lock()
	delete(p.decJobs, id)
	p.pendingMu.Unlock()
}

func (p *plugin) runDecision(id string, reqJSON []byte, tag string, ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		cancel()
		p.delDecJob(id)
	}()
	if state, _ := p.state(); (state != protocol.PluginReady && state != protocol.PluginRunning) || !p.hasGrant(hostapi.PermDecision) {
		p.tellDecisionFailed(id, tag, "canceled", "the plugin may not run now", 0)
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	start := time.Now()
	resp, _, _, err := p.m.cfg.Decisions.Evaluate(ctx, p.id, reqJSON)
	elapsed := elapsedMillis(start)
	if err != nil {
		code := decisionErrorCode(ctx, err)
		p.m.cfg.Log.Warn("plugin's decision failed", "plugin", p.id, "code", code, "err", err)
		p.tellDecisionFailed(id, tag, code, err.Error(), elapsed)
		return
	}
	ev := &hostapi.SessionEvent{
		Kind:         EventDecisionAnswered,
		RequestId:    id,
		Tag:          tag,
		ResponseJson: string(resp),
		ElapsedMs:    elapsed,
	}
	p.post(func() { p.deliver(ev, hostapi.PermDecision) })
}

func (p *plugin) tellDecisionFailed(id, tag, code, why string, elapsed uint32) {
	ev := &hostapi.SessionEvent{
		Kind:      EventDecisionFailed,
		RequestId: id,
		Tag:       tag,
		Code:      code,
		Error:     why,
		ElapsedMs: elapsed,
	}
	p.post(func() { p.deliver(ev, hostapi.PermDecision) })
}

func decisionErrorCode(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, ErrDecisionUnavailable):
		return "unavailable"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}

// sanitizeDecisionRequest validates size, JSON structure, and question count,
// and removes any provider/model keys so the node's own defaults always decide.
func sanitizeDecisionRequest(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > maxDecisionRequestBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("a decision request is JSON of 1 to %d KiB", maxDecisionRequestBytes>>10)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("invalid decision request JSON: %w", err)
	}
	if qRaw, ok := obj["questions"]; ok && len(qRaw) > 0 && string(qRaw) != "null" {
		var qs map[string]json.RawMessage
		if err := json.Unmarshal(qRaw, &qs); err != nil {
			return nil, fmt.Errorf("invalid decision questions: %w", err)
		}
		if len(qs) > maxDecisionQuestions {
			return nil, fmt.Errorf("a decision request holds at most %d questions", maxDecisionQuestions)
		}
	}
	for k := range obj {
		if strings.EqualFold(k, "provider") || strings.EqualFold(k, "model") {
			delete(obj, k)
		}
	}
	return json.Marshal(obj)
}

func clampDecisionTimeout(ms uint32, def, maxDur time.Duration) time.Duration {
	if ms == 0 {
		return def
	}
	d := time.Duration(ms) * time.Millisecond
	if d > maxDur {
		return maxDur
	}
	return d
}

func elapsedMillis(start time.Time) uint32 {
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	if ms > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(ms)
}
