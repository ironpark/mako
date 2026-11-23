package plugin

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// fakeDecisions stands for the daemon's decision model host in tests.
// Both assist_test.go and example_okf_ask_test.go reuse this struct.
type fakeDecisions struct {
	mu      sync.Mutex
	status  DecisionStatus
	respond func(req []byte) ([]byte, error)
	calls   [][]byte
	block   chan struct{}
}

func (f *fakeDecisions) Status() DecisionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeDecisions) Evaluate(ctx context.Context, _ string, requestJSON []byte) ([]byte, string, string, error) {
	f.mu.Lock()
	cp := append([]byte(nil), requestJSON...)
	f.calls = append(f.calls, cp)
	st := f.status
	fn := f.respond
	blk := f.block
	f.mu.Unlock()

	if blk != nil {
		select {
		case <-blk:
		case <-ctx.Done():
			return nil, "", "", ctx.Err()
		}
	}
	if fn != nil {
		resp, err := fn(cp)
		if err != nil {
			return nil, "", "", err
		}
		return resp, st.Provider, st.Model, nil
	}
	if !st.Available {
		return nil, "", "", fmt.Errorf("%w: no key configured", ErrDecisionUnavailable)
	}
	return []byte(`{"answers":{}}`), st.Provider, st.Model, nil
}

func (f *fakeDecisions) recordedCalls() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.calls))
	for i, c := range f.calls {
		out[i] = append([]byte(nil), c...)
	}
	return out
}

// fakeAsks stands for the daemon's one-shot agent ask host in tests.
// Both assist_test.go and example_okf_ask_test.go reuse this struct.
type fakeAsks struct {
	mu      sync.Mutex
	choices []AgentChoice
	answer  func(q AgentQuestion) (string, error)
	asked   []AgentQuestion
	block   chan struct{}
}

func (f *fakeAsks) Choices() []AgentChoice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.choices)
}

func (f *fakeAsks) Ask(ctx context.Context, _ string, q AgentQuestion) (string, error) {
	f.mu.Lock()
	f.asked = append(f.asked, q)
	fn := f.answer
	blk := f.block
	f.mu.Unlock()

	if blk != nil {
		select {
		case <-blk:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if fn != nil {
		return fn(q)
	}
	return "ok", nil
}

func (f *fakeAsks) recordedAsks() []AgentQuestion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}
