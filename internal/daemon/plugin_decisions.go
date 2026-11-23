package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/plugin"
)

const maxPluginDecisionQuestions = 32

// pluginDecisions carries out a plugin's decision permission (plugin.DecisionHost)
// using the daemon's decision store and the user's configured defaults/keys.
type pluginDecisions struct{ d *Daemon }

func (p pluginDecisions) Status() plugin.DecisionStatus {
	prov, model, err := p.d.decisionStore().Resolve(p.d.decisionConfig())
	if err != nil {
		return plugin.DecisionStatus{}
	}
	return plugin.DecisionStatus{
		Available: true,
		Provider:  prov,
		Model:     model,
	}
}

func (p pluginDecisions) Evaluate(ctx context.Context, _ string, raw []byte) ([]byte, string, string, error) {
	var req decision.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, "", "", fmt.Errorf("invalid decision request: %w", err)
	}
	// The user's configured defaults always decide provider and model.
	req.Provider, req.Model = "", ""
	if len(req.Questions) > maxPluginDecisionQuestions {
		return nil, "", "", fmt.Errorf("a decision request holds at most %d questions", maxPluginDecisionQuestions)
	}
	res, err := p.d.decisionStore().EvaluateRequest(ctx, req, p.d.decisionConfig())
	if err != nil {
		if errors.Is(err, decision.ErrNoKey) {
			return nil, "", "", fmt.Errorf("%w: %v", plugin.ErrDecisionUnavailable, err)
		}
		return nil, "", "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, "", "", err
	}
	return b, res.Provider, res.Model, nil
}
