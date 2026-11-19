package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/keychain"
)

const defaultHTTPTimeout = 45 * time.Second

// Request describes a decision evaluation call from a prompt template or caller.
type Request struct {
	Provider  string              `json:"provider,omitempty"`
	Model     string              `json:"model,omitempty"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions,omitempty"`

	// Single-question top-level shorthand fields:
	Type         string   `json:"type,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	Options      []string `json:"options,omitempty"`
	Criteria     any      `json:"criteria,omitempty"`
	Levels       []string `json:"levels,omitempty"`
}

// Question defines one typed decision question.
type Question struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions,omitempty"`
	Options      []string `json:"options,omitempty"`
	Criteria     any      `json:"criteria,omitempty"`
	Levels       []string `json:"levels,omitempty"`
}

// Answer is the normalized result for a single question.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Level         *int               `json:"level,omitempty"`
	Label         string             `json:"label,omitempty"`
	Probability   *float64           `json:"probability,omitempty"`
	Yes           *bool              `json:"yes,omitempty"`
	Value         any                `json:"value,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Refusal       string             `json:"refusal,omitempty"`
}

// Response is the normalized decision evaluation response.
type Response struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Answers  map[string]Answer `json:"answers"`

	// Convenience fields copied from the sole answer when a single question is evaluated.
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Level         *int               `json:"level,omitempty"`
	Label         string             `json:"label,omitempty"`
	Probability   *float64           `json:"probability,omitempty"`
	Yes           *bool              `json:"yes,omitempty"`
	Value         any                `json:"value,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Endpoints holds base URLs for each provider, overridable in tests.
type Endpoints struct {
	TypeSafe   string
	OpenRouter string
	OpenAI     string
	Google     string
}

func defaultEndpoints() Endpoints {
	return Endpoints{
		TypeSafe:   "https://api.typesafe.ai",
		OpenRouter: "https://openrouter.ai",
		OpenAI:     "https://api.openai.com",
		Google:     "https://generativelanguage.googleapis.com",
	}
}

// Store holds the API key vault and runs decision evaluations.
type Store struct {
	Vault     *Vault
	Client    *http.Client
	Endpoints Endpoints
}

// NewStore creates a Store backed by dir and keychain kc.
func NewStore(dir string, kc keychain.Keychain, note string) *Store {
	return &Store{
		Vault:     NewVault(dir, kc, note),
		Client:    &http.Client{Timeout: defaultHTTPTimeout},
		Endpoints: defaultEndpoints(),
	}
}

type normQuestion struct {
	Key          string
	Kind         string // "choice", "noul", "score"
	Instructions string
	Options      []string
	CriteriaMap  map[string]string
	Levels       []string
}

// Evaluate parses rawJSON, chooses the provider/model, resolves the API key,
// evaluates every question, and returns normalized JSON.
func (s *Store) Evaluate(ctx context.Context, rawJSON []byte, cfg config.Decision) ([]byte, error) {
	var req Request
	if err := json.Unmarshal(rawJSON, &req); err != nil {
		return nil, fmt.Errorf("invalid decision request: %w", err)
	}
	res, err := s.EvaluateRequest(ctx, req, cfg)
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

// EvaluateRequest evaluates a structured Request against cfg and the Store's keys.
func (s *Store) EvaluateRequest(ctx context.Context, req Request, cfg config.Decision) (Response, error) {
	questions, err := normalizeQuestions(req)
	if err != nil {
		return Response{}, err
	}
	provider, model, apiKey, err := s.resolveProvider(req, cfg)
	if err != nil {
		return Response{}, err
	}
	stateVal := req.State
	if stateVal == nil {
		stateVal = ""
	}

	var answers map[string]Answer
	switch provider {
	case ProviderTypeSafe:
		answers, err = s.callTypeSafe(ctx, apiKey, model, stateVal, questions)
	case ProviderOpenRouter:
		answers, err = s.callOpenRouter(ctx, apiKey, model, stateVal, questions)
	case ProviderOpenAI:
		answers, err = s.callOpenAICompat(ctx, s.Endpoints.OpenAI+"/v1/chat/completions", apiKey, model, stateVal, questions)
	case ProviderGoogle:
		answers, err = s.callGoogle(ctx, apiKey, model, stateVal, questions)
	default:
		return Response{}, fmt.Errorf("unsupported provider %q", provider)
	}
	if err != nil {
		return Response{}, err
	}

	resp := Response{
		Provider: provider,
		Model:    model,
		Answers:  answers,
	}
	if len(questions) == 1 {
		only := answers[questions[0].Key]
		resp.Choice = only.Choice
		resp.Score = only.Score
		resp.Level = only.Level
		resp.Label = only.Label
		resp.Probability = only.Probability
		resp.Yes = only.Yes
		resp.Value = only.Value
		resp.Confidence = only.Confidence
		resp.Probabilities = only.Probabilities
	}
	return resp, nil
}

func (s *Store) resolveProvider(req Request, cfg config.Decision) (provider, model, apiKey string, err error) {
	if strings.TrimSpace(req.Provider) != "" {
		p := NormalizeProvider(req.Provider)
		if p == "" {
			return "", "", "", fmt.Errorf("unknown decision provider %q (expected openrouter, typesafe, openai, or google)", req.Provider)
		}
		key, _, err := s.Vault.ResolveKey(p)
		if err != nil {
			return "", "", "", err
		}
		if key == "" {
			return "", "", "", fmt.Errorf("no API key configured for %s; add one in Settings ▸ API keys", p)
		}
		return p, pickModel(p, req.Model, cfg), key, nil
	}

	var order []string
	if hint := inferProviderFromModel(req.Model); hint != "" {
		order = append(order, hint)
	}
	if def := NormalizeProvider(cfg.Provider); def != "" && !slices.Contains(order, def) {
		order = append(order, def)
	}
	for _, p := range []string{ProviderTypeSafe, ProviderOpenRouter, ProviderOpenAI, ProviderGoogle} {
		if !slices.Contains(order, p) {
			order = append(order, p)
		}
	}
	for _, p := range order {
		key, _, kerr := s.Vault.ResolveKey(p)
		if kerr != nil {
			return "", "", "", kerr
		}
		if key != "" {
			return p, pickModel(p, req.Model, cfg), key, nil
		}
	}
	return "", "", "", errors.New("no decision model API key is configured; add one in Settings ▸ API keys")
}

func inferProviderFromModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "/"):
		return ProviderOpenRouter
	case strings.HasPrefix(m, "jev"):
		return ProviderTypeSafe
	case strings.HasPrefix(m, "gemini"):
		return ProviderGoogle
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return ProviderOpenAI
	default:
		return ""
	}
}

func pickModel(provider, reqModel string, cfg config.Decision) string {
	if m := strings.TrimSpace(reqModel); m != "" {
		return m
	}
	if NormalizeProvider(cfg.Provider) == provider && strings.TrimSpace(cfg.Model) != "" {
		return strings.TrimSpace(cfg.Model)
	}
	if m := strings.TrimSpace(cfg.Models[provider]); m != "" {
		return m
	}
	return DefaultModels[provider]
}

func normalizeQuestions(req Request) ([]normQuestion, error) {
	raw := req.Questions
	if len(raw) == 0 {
		if req.Type == "" && req.Instructions == "" && len(req.Options) == 0 && req.Criteria == nil && len(req.Levels) == 0 {
			return nil, errors.New("decision request must include questions or instructions")
		}
		raw = map[string]Question{
			"decision": {
				Type:         req.Type,
				Instructions: req.Instructions,
				Options:      req.Options,
				Criteria:     req.Criteria,
				Levels:       req.Levels,
			},
		}
	}
	keys := slices.Sorted(maps.Keys(raw))
	out := make([]normQuestion, 0, len(keys))
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return nil, errors.New("question key must not be empty")
		}
		nq, err := normalizeOneQuestion(k, raw[k])
		if err != nil {
			return nil, fmt.Errorf("question %q: %w", k, err)
		}
		out = append(out, nq)
	}
	return out, nil
}

func normalizeOneQuestion(key string, q Question) (normQuestion, error) {
	kind := strings.ToLower(strings.TrimSpace(q.Type))
	switch kind {
	case "":
		switch {
		case len(q.Options) > 0:
			kind = "choice"
		case len(q.Levels) > 0:
			kind = "score"
		default:
			switch q.Criteria.(type) {
			case map[string]any, map[string]string:
				kind = "choice"
			case []any, []string:
				kind = "score"
			default:
				kind = "noul"
			}
		}
	case "choice", "select":
		kind = "choice"
	case "noul", "boolean", "bool", "yesno", "yes_no":
		kind = "noul"
	case "score":
		kind = "score"
	default:
		return normQuestion{}, fmt.Errorf("unsupported question type %q (expected choice, score, or noul)", q.Type)
	}

	instr := strings.TrimSpace(q.Instructions)
	nq := normQuestion{Key: key, Kind: kind, Instructions: instr}

	switch kind {
	case "choice":
		cmap := map[string]string{}
		var opts []string
		for _, o := range q.Options {
			o = strings.TrimSpace(o)
			if o != "" && !slices.Contains(opts, o) {
				opts = append(opts, o)
				cmap[o] = o
			}
		}
		if q.Criteria != nil {
			m, err := stringMap(q.Criteria)
			if err != nil {
				return normQuestion{}, fmt.Errorf("choice criteria must be an object or options list: %w", err)
			}
			for _, o := range slices.Sorted(maps.Keys(m)) {
				if !slices.Contains(opts, o) {
					opts = append(opts, o)
				}
				if desc := strings.TrimSpace(m[o]); desc != "" {
					cmap[o] = desc
				} else if cmap[o] == "" {
					cmap[o] = o
				}
			}
		}
		if len(opts) == 0 {
			return normQuestion{}, errors.New("choice question requires options or criteria")
		}
		if nq.Instructions == "" {
			nq.Instructions = fmt.Sprintf("Choose the best option for %s.", key)
		}
		nq.Options = opts
		nq.CriteriaMap = cmap

	case "score":
		var levels []string
		for _, l := range q.Levels {
			if l = strings.TrimSpace(l); l != "" {
				levels = append(levels, l)
			}
		}
		if len(levels) == 0 && q.Criteria != nil {
			ls, err := stringSlice(q.Criteria)
			if err != nil {
				return normQuestion{}, fmt.Errorf("score criteria must be an array of level descriptions: %w", err)
			}
			for _, l := range ls {
				if l = strings.TrimSpace(l); l != "" {
					levels = append(levels, l)
				}
			}
		}
		if len(levels) < 2 {
			return normQuestion{}, errors.New("score question requires at least 2 levels")
		}
		if len(levels) > 10 {
			return normQuestion{}, errors.New("score question supports at most 10 levels")
		}
		if nq.Instructions == "" {
			nq.Instructions = fmt.Sprintf("Score %s on the rubric.", key)
		}
		nq.Levels = levels

	case "noul":
		if q.Criteria != nil {
			m, err := stringMap(q.Criteria)
			if err == nil && len(m) > 0 {
				nq.CriteriaMap = m
			}
		}
		if nq.Instructions == "" {
			nq.Instructions = fmt.Sprintf("Evaluate whether %s is true.", key)
		}
	}
	return nq, nil
}

func stringMap(v any) (map[string]string, error) {
	switch m := v.(type) {
	case map[string]string:
		return mapsClone(m), nil
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = fmt.Sprint(val)
		}
		return out, nil
	}
	return nil, errors.New("not a map")
}

func stringSlice(v any) ([]string, error) {
	switch s := v.(type) {
	case []string:
		return slices.Clone(s), nil
	case []any:
		out := make([]string, len(s))
		for i, item := range s {
			out[i] = fmt.Sprint(item)
		}
		return out, nil
	}
	return nil, errors.New("not a list")
}
