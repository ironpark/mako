package decision

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

const maxResponseBytes = 2 << 20

// buildSystemOneQuestions converts normalized questions into the payload shape
// accepted by TypeSafe AI /v1/systemone and OpenRouter /api/alpha/decisions.
func buildSystemOneQuestions(questions []normQuestion) map[string]any {
	out := make(map[string]any, len(questions))
	for _, q := range questions {
		entry := map[string]any{
			"type":         q.Kind,
			"instructions": q.Instructions,
		}
		switch q.Kind {
		case "choice":
			entry["criteria"] = q.CriteriaMap
		case "score":
			entry["criteria"] = q.Levels
		case "noul":
			if len(q.CriteriaMap) > 0 {
				entry["criteria"] = q.CriteriaMap
			}
		}
		out[q.Key] = entry
	}
	return out
}

func (s *Store) callTypeSafe(ctx context.Context, apiKey, model string, state any, questions []normQuestion) (map[string]Answer, error) {
	payload := map[string]any{
		"model":     model,
		"state":     state,
		"questions": buildSystemOneQuestions(questions),
	}
	body, err := s.postJSON(ctx, strings.TrimRight(s.Endpoints.TypeSafe, "/")+"/v1/systemone", map[string]string{
		"Authorization": "Bearer " + apiKey,
	}, payload)
	if err != nil {
		return nil, fmt.Errorf("typesafe: %w", err)
	}
	return parseSystemOneAnswers(body, questions)
}

func isOpenRouterDecisionModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "typesafe/jev-router" {
		return false
	}
	return strings.HasPrefix(m, "typesafe/") || strings.HasPrefix(m, "cloudflare/clef")
}

func (s *Store) callOpenRouter(ctx context.Context, apiKey, model string, state any, questions []normQuestion) (map[string]Answer, error) {
	base := strings.TrimRight(s.Endpoints.OpenRouter, "/")
	if isOpenRouterDecisionModel(model) {
		payload := map[string]any{
			"model":     model,
			"state":     state,
			"questions": buildSystemOneQuestions(questions),
		}
		body, err := s.postJSON(ctx, base+"/api/alpha/decisions", map[string]string{
			"Authorization": "Bearer " + apiKey,
		}, payload)
		if err != nil {
			return nil, fmt.Errorf("openrouter: %w", err)
		}
		return parseSystemOneAnswers(body, questions)
	}
	return s.callOpenAICompat(ctx, base+"/api/v1/chat/completions", apiKey, model, state, questions)
}

func parseSystemOneAnswers(body []byte, questions []normQuestion) (map[string]Answer, error) {
	var env struct {
		Answers map[string]json.RawMessage `json:"answers"`
		Error   any                        `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("invalid decision response JSON: %w", err)
	}
	if len(env.Answers) == 0 {
		return nil, fmt.Errorf("decision API returned no answers: %s", agent.Clip(string(body), 200))
	}
	out := make(map[string]Answer, len(questions))
	for _, q := range questions {
		raw, ok := env.Answers[q.Key]
		if !ok {
			return nil, fmt.Errorf("decision API omitted answer for %q", q.Key)
		}
		var item struct {
			Type          string          `json:"type"`
			Choice        string          `json:"choice"`
			Score         *float64        `json:"score"`
			Legend        string          `json:"legend"`
			Noul          *float64        `json:"noul"`
			Probability   *float64        `json:"probability"`
			Confidence    *float64        `json:"confidence"`
			Probabilities json.RawMessage `json:"probabilities"`
			Message       string          `json:"message"`
			Reason        string          `json:"reason"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("answer %q: %w", q.Key, err)
		}
		if item.Type == "refusal" {
			out[q.Key] = Answer{
				Type:    "refusal",
				Refusal: cmp.Or(item.Message, item.Reason, "refused"),
			}
			continue
		}
		ans := Answer{
			Type:       q.Kind,
			Confidence: item.Confidence,
		}
		switch q.Kind {
		case "choice":
			ans.Choice = item.Choice
			ans.Value = item.Choice
			ans.Probabilities = parseChoiceProbabilities(item.Probabilities)
		case "score":
			ans.Score = item.Score
			if item.Score != nil {
				lvl := clampLevel(int(math.Round(*item.Score)), len(q.Levels))
				ans.Level = &lvl
				ans.Label = cmp.Or(strings.TrimSpace(item.Legend), q.Levels[lvl])
				ans.Value = *item.Score
			} else {
				ans.Label = strings.TrimSpace(item.Legend)
			}
			ans.Probabilities = parseScoreProbabilities(item.Probabilities)
		case "noul":
			p := item.Probability
			if p == nil {
				p = item.Noul
			}
			if p != nil {
				clamped := clamp01(*p)
				yes := clamped >= 0.5
				ans.Probability = &clamped
				ans.Yes = &yes
				ans.Value = yes
			}
		}
		out[q.Key] = ans
	}
	return out, nil
}

func parseChoiceProbabilities(raw json.RawMessage) map[string]float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]float64
	if json.Unmarshal(raw, &m) == nil && len(m) > 0 {
		return m
	}
	var list []struct {
		Value       string  `json:"value"`
		Probability float64 `json:"probability"`
	}
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		out := make(map[string]float64, len(list))
		for _, item := range list {
			out[item.Value] = item.Probability
		}
		return out
	}
	return nil
}

func parseScoreProbabilities(raw json.RawMessage) map[string]float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var nums []float64
	if json.Unmarshal(raw, &nums) == nil && len(nums) > 0 {
		out := make(map[string]float64, len(nums))
		for i, p := range nums {
			out[strconv.Itoa(i)] = p
		}
		return out
	}
	var list []struct {
		Level       int     `json:"level"`
		Probability float64 `json:"probability"`
	}
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		out := make(map[string]float64, len(list))
		for _, item := range list {
			out[strconv.Itoa(item.Level)] = item.Probability
		}
		return out
	}
	return nil
}

func (s *Store) callOpenAICompat(ctx context.Context, endpoint, apiKey, model string, state any, questions []normQuestion) (map[string]Answer, error) {
	sysPrompt, userPrompt := buildGenerativePrompts(state, questions)
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": userPrompt},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "decision_answers",
				"strict": true,
				"schema": buildJSONSchema(questions, true),
			},
		},
	}
	if !isReasoningModel(model) {
		payload["temperature"] = 0
	}
	body, err := s.postJSON(ctx, endpoint, map[string]string{
		"Authorization": "Bearer " + apiKey,
	}, payload)
	if err != nil {
		return nil, err
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("invalid chat completion JSON: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, errors.New("provider returned no choices")
	}
	msg := chatResp.Choices[0].Message
	if msg.Refusal != "" {
		return nil, fmt.Errorf("model refused decision: %s", msg.Refusal)
	}
	rawJSON, err := agent.ExtractJSON(msg.Content)
	if err != nil {
		return nil, err
	}
	return parseGenerativeAnswers([]byte(rawJSON), questions)
}

func (s *Store) callGoogle(ctx context.Context, apiKey, model string, state any, questions []normQuestion) (map[string]Answer, error) {
	base := strings.TrimRight(s.Endpoints.Google, "/")
	endpoint := base + "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	sysPrompt, userPrompt := buildGenerativePrompts(state, questions)
	payload := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": sysPrompt}},
		},
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]string{{"text": userPrompt}},
			},
		},
		"generationConfig": map[string]any{
			"temperature":      0,
			"responseMimeType": "application/json",
			"responseSchema":   buildJSONSchema(questions, false),
		},
	}
	body, err := s.postJSON(ctx, endpoint, map[string]string{
		"x-goog-api-key": apiKey,
	}, payload)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	var gemResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &gemResp); err != nil {
		return nil, fmt.Errorf("google: invalid response JSON: %w", err)
	}
	if len(gemResp.Candidates) == 0 {
		return nil, errors.New("google: response had no candidates")
	}
	var text strings.Builder
	for _, p := range gemResp.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	rawJSON, err := agent.ExtractJSON(text.String())
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	return parseGenerativeAnswers([]byte(rawJSON), questions)
}

func buildGenerativePrompts(state any, questions []normQuestion) (string, string) {
	const sys = "You are a calibrated decision classifier. Evaluate the provided state against each question strictly according to its options or rubric and output only JSON matching the schema."
	var b strings.Builder
	b.WriteString("STATE:\n")
	switch v := state.(type) {
	case string:
		b.WriteString(v)
	default:
		enc, _ := json.MarshalIndent(v, "", "  ")
		b.Write(enc)
	}
	b.WriteString("\n\nQUESTIONS:\n")
	for _, q := range questions {
		fmt.Fprintf(&b, "- %s (%s): %s\n", q.Key, q.Kind, q.Instructions)
		switch q.Kind {
		case "choice":
			for _, opt := range q.Options {
				fmt.Fprintf(&b, "  * %s: %s\n", opt, q.CriteriaMap[opt])
			}
		case "score":
			for i, lvl := range q.Levels {
				fmt.Fprintf(&b, "  * level %d: %s\n", i, lvl)
			}
		case "noul":
			if t := q.CriteriaMap["true"]; t != "" {
				fmt.Fprintf(&b, "  * true: %s\n", t)
			}
			if f := q.CriteriaMap["false"]; f != "" {
				fmt.Fprintf(&b, "  * false: %s\n", f)
			}
		}
	}
	return sys, b.String()
}

func buildJSONSchema(questions []normQuestion, includeAdditionalProps bool) map[string]any {
	props := make(map[string]any, len(questions))
	req := make([]string, 0, len(questions))
	for _, q := range questions {
		req = append(req, q.Key)
		var qProps map[string]any
		var qReq []string
		switch q.Kind {
		case "choice":
			qProps = map[string]any{
				"choice":     map[string]any{"type": "string", "enum": q.Options},
				"confidence": map[string]any{"type": "number"},
			}
			qReq = []string{"choice", "confidence"}
		case "noul":
			qProps = map[string]any{
				"yes":         map[string]any{"type": "boolean"},
				"probability": map[string]any{"type": "number"},
			}
			qReq = []string{"yes", "probability"}
		case "score":
			qProps = map[string]any{
				"level":      map[string]any{"type": "integer"},
				"confidence": map[string]any{"type": "number"},
			}
			qReq = []string{"level", "confidence"}
		}
		qObj := map[string]any{
			"type":       "object",
			"properties": qProps,
			"required":   qReq,
		}
		if includeAdditionalProps {
			qObj["additionalProperties"] = false
		}
		props[q.Key] = qObj
	}
	root := map[string]any{
		"type":       "object",
		"properties": props,
		"required":   req,
	}
	if includeAdditionalProps {
		root["additionalProperties"] = false
	}
	return root
}

func parseGenerativeAnswers(rawJSON []byte, questions []normQuestion) (map[string]Answer, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &rawMap); err != nil {
		return nil, fmt.Errorf("invalid structured answer JSON: %w", err)
	}
	out := make(map[string]Answer, len(questions))
	for _, q := range questions {
		raw, ok := rawMap[q.Key]
		if !ok {
			return nil, fmt.Errorf("missing answer for %q", q.Key)
		}
		var item struct {
			Choice      string   `json:"choice"`
			Yes         *bool    `json:"yes"`
			Probability *float64 `json:"probability"`
			Level       *int     `json:"level"`
			Score       *float64 `json:"score"`
			Confidence  *float64 `json:"confidence"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("answer %q: %w", q.Key, err)
		}
		ans := Answer{Type: q.Kind}
		if item.Confidence != nil {
			c := clamp01(*item.Confidence)
			ans.Confidence = &c
		}
		switch q.Kind {
		case "choice":
			ans.Choice = item.Choice
			ans.Value = item.Choice
		case "noul":
			var p float64
			switch {
			case item.Probability != nil:
				p = clamp01(*item.Probability)
			case item.Yes != nil && *item.Yes:
				p = 1
			default:
				p = 0
			}
			yes := p >= 0.5
			if item.Yes != nil && item.Probability == nil {
				yes = *item.Yes
			}
			ans.Probability = &p
			ans.Yes = &yes
			ans.Value = yes
		case "score":
			lvl := 0
			if item.Level != nil {
				lvl = clampLevel(*item.Level, len(q.Levels))
			} else if item.Score != nil {
				lvl = clampLevel(int(math.Round(*item.Score)), len(q.Levels))
			}
			sc := float64(lvl)
			ans.Level = &lvl
			ans.Score = &sc
			ans.Label = q.Levels[lvl]
			ans.Value = lvl
		}
		out[q.Key] = ans
	}
	return out, nil
}

func (s *Store) postJSON(ctx context.Context, endpoint string, headers map[string]string, payload any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("response exceeded 2 MiB limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, apiErrorMessage(body))
	}
	return body, nil
}

func apiErrorMessage(body []byte) string {
	var errObj struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
	}
	if json.Unmarshal(body, &errObj) == nil {
		switch e := errObj.Error.(type) {
		case string:
			if strings.TrimSpace(e) != "" {
				return agent.Clip(e, 240)
			}
		case map[string]any:
			if msg, ok := e["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return agent.Clip(msg, 240)
			}
		}
		if strings.TrimSpace(errObj.Msg) != "" {
			return agent.Clip(errObj.Msg, 240)
		}
	}
	return agent.Clip(string(body), 240)
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampLevel(v, n int) int {
	if v < 0 {
		return 0
	}
	if v >= n {
		return n - 1
	}
	return v
}

func isReasoningModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "gpt-5")
}
