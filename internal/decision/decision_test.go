package decision

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/keychain"
)

func TestVaultKeychainFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	// Without a keychain, secrets go in a 0600 file.
	fileVault := NewVault(dir, nil, "no keychain")
	if store, note := fileVault.SecretStore(); store != "file" || note != "no keychain" {
		t.Fatalf("store=%q note=%q", store, note)
	}
	if err := fileVault.Set("typesafe.ai", "ts-secret"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "provider-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode %o, want 0600", mode)
	}

	// Opening with a keychain migrates file secrets into the keychain.
	kc := &keychain.Fake{}
	kcVault := NewVault(dir, kc, "")
	if store, _ := kcVault.SecretStore(); store != kc.Name() {
		t.Fatalf("store=%q", store)
	}
	got, ok, err := kcVault.Get(ProviderTypeSafe)
	if err != nil || !ok || got != "ts-secret" {
		t.Fatalf("migrated key = %q %v %v", got, ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-keys.json")); !os.IsNotExist(err) {
		t.Fatal("provider-keys.json remained after migration")
	}

	// Env fallback works when vault lacks a key, and stored keys take priority.
	t.Setenv("OPENAI_API_KEY", "env-openai")
	if k, src, err := kcVault.ResolveKey(ProviderOpenAI); err != nil || k != "env-openai" || src != "env" {
		t.Fatalf("env resolve = %q %q %v", k, src, err)
	}
	if err := kcVault.Set(ProviderOpenAI, "stored-openai"); err != nil {
		t.Fatal(err)
	}
	if k, src, err := kcVault.ResolveKey(ProviderOpenAI); err != nil || k != "stored-openai" || src != "vault" {
		t.Fatalf("vault resolve = %q %q %v", k, src, err)
	}
	if err := kcVault.Delete(ProviderOpenAI); err != nil {
		t.Fatal(err)
	}
	if kcVault.Has(ProviderOpenAI) {
		t.Fatal("deleted key still marked set")
	}
}

func TestTypeSafeAndOpenRouterProviders(t *testing.T) {
	tsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer ts-key" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{
			"model": "jev-latest",
			"answers": {
				"route": {"type": "choice", "choice": "codex", "probabilities": {"claude": 0.1, "codex": 0.9}, "confidence": 0.88},
				"risk": {"type": "score", "score": 2, "legend": "High", "probabilities": [0.05, 0.15, 0.8], "confidence": 0.81},
				"needs_review": {"type": "noul", "noul": 0.92}
			}
		}`))
	}))
	defer tsSrv.Close()

	var orDecisionCalls, orChatCalls int
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer or-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/alpha/decisions":
			orDecisionCalls++
			_, _ = w.Write([]byte(`{
				"model": "typesafe/jev-1.14",
				"answers": {
					"decision": {
						"type": "choice",
						"choice": "fast",
						"probabilities": [{"value": "fast", "probability": 0.91}, {"value": "deep", "probability": 0.09}],
						"confidence": 0.85
					}
				}
			}`))
		case "/api/v1/chat/completions":
			orChatCalls++
			_, _ = w.Write([]byte(`{
				"choices": [{"message": {"content": "{\"decision\":{\"choice\":\"deep\",\"confidence\":0.77}}"}}]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer orSrv.Close()

	store := NewStore(t.TempDir(), &keychain.Fake{}, "")
	store.Endpoints.TypeSafe = tsSrv.URL
	store.Endpoints.OpenRouter = orSrv.URL
	_ = store.Vault.Set(ProviderTypeSafe, "ts-key")
	_ = store.Vault.Set(ProviderOpenRouter, "or-key")

	// Multi-question TypeSafe call.
	res, err := store.EvaluateRequest(t.Context(), Request{
		Provider: "typesafe.ai",
		State:    "Changed auth middleware and SQL queries.",
		Questions: map[string]Question{
			"route":        {Type: "choice", Instructions: "Pick agent", Options: []string{"claude", "codex"}},
			"risk":         {Type: "score", Instructions: "Risk level", Levels: []string{"Low", "Medium", "High"}},
			"needs_review": {Type: "noul", Instructions: "Needs careful review?"},
		},
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answers["route"].Choice != "codex" || *res.Answers["risk"].Level != 2 || res.Answers["risk"].Label != "High" || !*res.Answers["needs_review"].Yes {
		t.Fatalf("unexpected typesafe answers: %+v", res)
	}

	// OpenRouter decision model routes to /api/alpha/decisions and populates top-level fields.
	res, err = store.EvaluateRequest(t.Context(), Request{
		Provider:     "openrouter",
		Model:        "typesafe/jev-1.14",
		State:        "Rename typo in README",
		Type:         "choice",
		Instructions: "Complexity",
		Options:      []string{"fast", "deep"},
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if orDecisionCalls != 1 || res.Choice != "fast" || res.Probabilities["fast"] != 0.91 {
		t.Fatalf("openrouter decision = %+v (calls=%d)", res, orDecisionCalls)
	}

	// OpenRouter routes Google & OpenAI native decision models to /api/alpha/decisions too.
	for _, model := range []string{"google/gemini-decision-1", "openai/gpt-6-luna"} {
		res, err = store.EvaluateRequest(t.Context(), Request{
			Provider:     "openrouter",
			Model:        model,
			State:        "Rename typo in README",
			Type:         "choice",
			Instructions: "Complexity",
			Options:      []string{"fast", "deep"},
		}, config.Decision{})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if res.Choice != "fast" {
			t.Fatalf("%s choice = %q, want fast", model, res.Choice)
		}
	}
	if orDecisionCalls != 3 {
		t.Fatalf("openrouter decision calls = %d, want 3", orDecisionCalls)
	}

	// OpenRouter generative model routes to /api/v1/chat/completions.
	res, err = store.EvaluateRequest(t.Context(), Request{
		Provider:     "openrouter",
		Model:        "openai/gpt-4.1-mini",
		State:        "Rewrite consensus engine",
		Type:         "choice",
		Instructions: "Complexity",
		Options:      []string{"fast", "deep"},
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if orChatCalls != 1 || res.Choice != "deep" {
		t.Fatalf("openrouter chat = %+v (calls=%d)", res, orChatCalls)
	}
}

func TestOpenAIAndGoogleProviders(t *testing.T) {
	var oaDecisionCalls, oaChatCalls int
	oaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oa-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/decisions":
			oaDecisionCalls++
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["model"] != "gpt-6-luna" {
				http.Error(w, "bad decision request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{
				"id": "dec_1",
				"model": "gpt-6-luna",
				"answers": [
					{"name": "decision", "type": "predicate", "probability": 0.94, "confidence": 0.94}
				]
			}`))
		case "/v1/chat/completions":
			oaChatCalls++
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"decision\":{\"yes\":true,\"probability\":0.87}}"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oaSrv.Close()

	var gemDecisionCalls, gemGenCalls int
	gemSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != "gem-key" {
			http.Error(w, "bad google headers/query: "+r.URL.String(), http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !json.Valid(body) {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/v1beta/decisions":
			gemDecisionCalls++
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil || req["model"] != "gemini-decision-1" {
				http.Error(w, "bad google decision request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{
				"answers": {
					"decision": {"type": "score", "score": 1.1, "probabilities": [0.1, 0.8, 0.1], "confidence": 0.88}
				}
			}`))
		case "/v1beta/models/gemini-2.5-flash:generateContent":
			gemGenCalls++
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"decision\":{\"level\":1,\"confidence\":0.9}}"}]}}]}`))
		default:
			http.Error(w, "unexpected google path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer gemSrv.Close()

	store := NewStore(t.TempDir(), &keychain.Fake{}, "")
	store.Endpoints.OpenAI = oaSrv.URL
	store.Endpoints.Google = gemSrv.URL
	_ = store.Vault.Set(ProviderOpenAI, "oa-key")
	_ = store.Vault.Set(ProviderGoogle, "gem-key")

	// OpenAI default uses dedicated decision model gpt-6-luna via /v1/decisions.
	res, err := store.EvaluateRequest(t.Context(), Request{
		Provider:     "openai",
		State:        "Added panic recovery",
		Type:         "noul",
		Instructions: "Is this defensive?",
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if oaDecisionCalls != 1 || res.Model != "gpt-6-luna" || res.Yes == nil || !*res.Yes || *res.Probability != 0.94 {
		t.Fatalf("openai decision = %+v (calls=%d)", res, oaDecisionCalls)
	}

	// OpenAI generative fallback still works when a chat model is explicitly chosen.
	res, err = store.EvaluateRequest(t.Context(), Request{
		Provider:     "openai",
		Model:        "gpt-4.1-mini",
		State:        "Added panic recovery",
		Type:         "noul",
		Instructions: "Is this defensive?",
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if oaChatCalls != 1 || res.Yes == nil || !*res.Yes || *res.Probability != 0.87 {
		t.Fatalf("openai chat fallback = %+v (calls=%d)", res, oaChatCalls)
	}

	// Google default uses dedicated decision model gemini-decision-1 via /v1beta/decisions.
	res, err = store.EvaluateRequest(t.Context(), Request{
		Provider:     "google-ai-studio",
		State:        "Medium patch",
		Type:         "score",
		Instructions: "Effort needed",
		Levels:       []string{"trivial", "moderate", "major"},
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if gemDecisionCalls != 1 || res.Model != "gemini-decision-1" || res.Level == nil || *res.Level != 1 || res.Label != "moderate" {
		t.Fatalf("google decision = %+v (calls=%d)", res, gemDecisionCalls)
	}

	// Google generative fallback still works when a generative model is explicitly chosen.
	res, err = store.EvaluateRequest(t.Context(), Request{
		Provider:     "google-ai-studio",
		Model:        "gemini-2.5-flash",
		State:        "Medium patch",
		Type:         "score",
		Instructions: "Effort needed",
		Levels:       []string{"trivial", "moderate", "major"},
	}, config.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if gemGenCalls != 1 || res.Level == nil || *res.Level != 1 || res.Label != "moderate" {
		t.Fatalf("google generative fallback = %+v (calls=%d)", res, gemGenCalls)
	}
}
