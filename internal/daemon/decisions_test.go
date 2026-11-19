//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/protocol"
)

func TestProvidersAndRunDecisionOverSocket(t *testing.T) {
	d := meshDaemon(t, "dec")
	cfgPath := filepath.Join(d.dir, "config.json")
	d.FollowAgents(cfgPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ts-secret-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-latest",
			"answers": map[string]any{
				"decision": map[string]any{
					"type":       "choice",
					"choice":     "deploy",
					"confidence": 0.91,
				},
			},
		})
	}))
	defer srv.Close()

	store := d.decisionStore()
	store.Endpoints.TypeSafe = srv.URL

	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	list, err := c.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetProviders()) != 4 {
		t.Fatalf("providers len = %d", len(list.GetProviders()))
	}

	updated, err := c.ActProvider(ctx, &protocol.ProviderAction{
		Op:       "set_key",
		Provider: "typesafe.ai",
		Value:    "ts-secret-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range updated.GetProviders() {
		if p.GetId() == decision.ProviderTypeSafe {
			found = p.GetSet()
		}
	}
	if !found {
		t.Fatalf("typesafe key not marked set: %+v", updated.GetProviders())
	}

	updated, err = c.ActProvider(ctx, &protocol.ProviderAction{
		Op:              "set_defaults",
		DefaultProvider: "typesafe",
		DefaultModel:    "jev-latest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetDefaultProvider() != "typesafe" || updated.GetDefaultModel() != "jev-latest" {
		t.Fatalf("defaults = %q / %q", updated.GetDefaultProvider(), updated.GetDefaultModel())
	}

	res, err := c.RunDecision(ctx, &protocol.RunDecision{
		RequestJson: `{"type":"choice","state":"CI is green","options":["deploy","hold"]}`,
	})
	if err != nil {
		t.Fatalf("RunDecision: %v", err)
	}
	var out decision.Response
	if err := json.Unmarshal([]byte(res.GetResponseJson()), &out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "typesafe" || out.Choice != "deploy" {
		t.Fatalf("decision response = %+v", out)
	}

	cleared, err := c.ActProvider(ctx, &protocol.ProviderAction{
		Op:       "clear_key",
		Provider: "typesafe",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cleared.GetProviders() {
		if p.GetId() == decision.ProviderTypeSafe && p.GetSet() {
			t.Fatalf("expected typesafe key cleared: %+v", p)
		}
	}
}

func TestDecisionHandlersRejectPeers(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)

	// Calling handlers directly with fromPeer=true enforces the local-frontend boundary.
	if err := a.handleListProviders(t.Context(), nil, &protocol.Envelope{Id: "1"}, true); err == nil || !strings.Contains(err.Error(), "front-ends") {
		t.Fatalf("ListProviders fromPeer err = %v", err)
	}
	if err := a.handleProviderAction(t.Context(), nil, &protocol.Envelope{Id: "1"}, true); err == nil || !strings.Contains(err.Error(), "front-ends") {
		t.Fatalf("ProviderAction fromPeer err = %v", err)
	}
	if err := a.handleRunDecision(t.Context(), nil, &protocol.Envelope{Id: "1"}, true); err == nil || !strings.Contains(err.Error(), "front-ends") {
		t.Fatalf("RunDecision fromPeer err = %v", err)
	}
}
