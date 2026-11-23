package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func loadOKFFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js", "lib/okf.js", "lib/i18n.js", "lib/retrieval.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/okf", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	return files
}

func seedOKFDocsForAsk(t *testing.T, r *rig, ctx ScopeContext) {
	t.Helper()
	docs := []string{
		`{
			"path": "decisions/001-storage.md",
			"title": "Use Sandboxed Datafs for OKF",
			"type": "Decision",
			"description": "Store OKF bundles as real Markdown files under sandboxed plugin-data.",
			"tags": "storage, okf, sandbox",
			"status": "active",
			"body": "# Storage Decision\n\nWe store OKF documents in sandboxed datafs so every project vault is isolated. Builds on [[concepts/overview]] and [[architecture/mesh]]."
		}`,
		`{
			"path": "concepts/auth.md",
			"title": "Token Authentication",
			"type": "Concept",
			"description": "Short-lived session tokens authenticate CLI tools and peers.",
			"tags": "auth, security",
			"status": "needs_review",
			"body": "# Token Authentication\n\nEach session receives a scoped bearer token. See [[concepts/overview]]."
		}`,
		`{
			"path": "procedures/sync.md",
			"title": "Vault Sync Runbook",
			"type": "Procedure",
			"description": "How to verify CRDT and file vault synchronization across nodes.",
			"tags": "sync, ops",
			"status": "active",
			"body": "# Vault Sync Runbook\n\nCheck peer status and confirm [[decisions/001-storage]] files replicate cleanly."
		}`,
		`{
			"path": "architecture/mesh.md",
			"title": "Peer Mesh Topology",
			"type": "Architecture",
			"description": "Encrypted libp2p mesh connecting paired Mako nodes.",
			"tags": "mesh, networking",
			"status": "active",
			"body": "# Peer Mesh Topology\n\nNodes connect over libp2p and exchange signed updates. Related to [[concepts/auth]]."
		}`,
	}
	for _, payload := range docs {
		res, err := r.m.CallToolScoped(t.Context(), "okf", "upsert_doc", []byte(payload), ctx)
		if err != nil || !strings.Contains(res, `"saved":true`) {
			t.Fatalf("seed upsert_doc failed: %s, err=%v", res, err)
		}
	}
}

func findNode(root *protocol.UiNode, pred func(*protocol.UiNode) bool) *protocol.UiNode {
	if root == nil {
		return nil
	}
	if pred(root) {
		return root
	}
	for _, ch := range root.GetChildren() {
		if got := findNode(ch, pred); got != nil {
			return got
		}
	}
	return nil
}

// TestOKFExampleAsk exercises the full 2-stage Ask workflow end-to-end:
// agent picker filtering & persistence (.okf/assistant.json), Stage 1 decision
// batch submission (single call with d01.. + entry + needs_more) + spinner UI,
// Essential solid chip + why collapsible, Stage 2 agent approval & prompt
// structure, okf: citation navigation into Reader & Back to Ask, .okf/ hiding
// from Tree/list_docs/lint, and Korean locale 5th tab "AI 질의".
func TestOKFExampleAsk(t *testing.T) {
	decBlock := make(chan struct{})
	dec := &fakeDecisions{
		status: DecisionStatus{Available: true, Provider: "typesafe", Model: "jev"},
		block:  decBlock,
		respond: func(req []byte) ([]byte, error) {
			var parsed struct {
				State struct {
					Candidates map[string]struct {
						Path string `json:"path"`
					} `json:"candidates"`
				} `json:"state"`
				Questions map[string]json.RawMessage `json:"questions"`
			}
			if err := json.Unmarshal(req, &parsed); err != nil {
				return nil, err
			}
			answers := map[string]any{}
			entryID := "none"
			for cid, c := range parsed.State.Candidates {
				switch c.Path {
				case "decisions/001-storage.md":
					answers[cid] = map[string]any{"type": "score", "level": 3, "label": "Essential", "score": 1.0}
					entryID = cid
				case "architecture/mesh.md":
					answers[cid] = map[string]any{"type": "score", "level": 2, "label": "Relevant", "score": 0.67}
				default:
					answers[cid] = map[string]any{"type": "score", "level": 0, "label": "Unrelated", "score": 0.0}
				}
			}
			answers["entry"] = map[string]any{"type": "choice", "choice": entryID}
			answers["needs_more"] = map[string]any{"type": "noul", "yes": false, "probability": 0.1}
			out := map[string]any{
				"provider": "typesafe",
				"model":    "jev",
				"answers":  answers,
			}
			return json.Marshal(out)
		},
	}

	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "explorer", Description: "Fast read-only explorer", Model: "sonnet", Installed: true, Askable: true},
			{Agent: "claude", Profile: "", Description: "Default claude", Installed: true, Askable: true},
			{Agent: "codex", Profile: "off", Description: "Disabled profile", Installed: true, Askable: false, Reason: "allow_template is off"},
		},
		answer: func(q AgentQuestion) (string, error) {
			return `{"answer":"OKF stores documents in sandboxed datafs per [[decisions/001-storage.md]] and connects nodes via [[architecture/mesh.md]].","citations":["decisions/001-storage.md","architecture/mesh.md"],"confidence":"high","follow_ups":["How does mesh replication work?"],"missing":[]}`, nil
		},
	}

	scopeA := "project:p-alpha"
	ctxA := ScopeContext{
		Scope:       protocol.ScopeProject,
		ScopeKey:    scopeA,
		ProjectID:   "p-alpha",
		ProjectName: "Alpha",
		Roots:       []string{"/tmp/alpha"},
	}

	r := startRig(t, Config{
		Limits:    testLimits,
		Decisions: dec,
		Asks:      asksHost,
		Projects: func() []ProjectRoots {
			return []ProjectRoots{{ID: "p-alpha", Name: "Alpha", Roots: []string{"/tmp/alpha"}}}
		},
		ResolveScope: func(scopeKey string) ScopeContext {
			if scopeKey == scopeA {
				return ctxA
			}
			return ScopeContext{}
		},
	})
	r.write("okf", loadOKFFiles(t))
	r.state("okf", protocol.PluginWaiting)
	r.grant("okf")

	w := r.watchScoped("plugin:okf/vault", scopeA)
	defer w.cancel()

	w.until("starter vault", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "2 docs")
	})

	// Seed 4 more docs (total 6 > default ask_max_docs 5) so Stage 1 runs.
	seedOKFDocsForAsk(t, r, ctxA)

	var tabsID string
	w.until("6 docs loaded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		tn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "tabs" && strings.HasSuffix(x.GetId(), "/main-tabs")
		})
		if tn != nil {
			tabsID = tn.GetId()
		}
		return tabsID != "" && hasText(n, "6 docs")
	})

	// (1) Switch to Ask tab -> ask-assign card with ONLY askable options.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Ask",
	}); err != nil {
		t.Fatalf("switch to Ask tab: %v", err)
	}

	var agentSelectID string
	w.until("ask-assign card rendered with askable agents only", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		sel := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "select" && strings.HasSuffix(x.GetId(), "/ask-agent")
		})
		if sel == nil {
			return false
		}
		opts := sel.GetOptions()
		if len(opts) != 2 || opts[0] != "claude · explorer" || opts[1] != "claude" {
			return false
		}
		for _, o := range opts {
			if strings.Contains(o, "codex") {
				return false
			}
		}
		agentSelectID = sel.GetId()
		return hasText(n, "Assign an exploration agent") && hasText(n, "Decision model · jev")
	})

	// (2) Assign exploration agent -> writes .okf/assistant.json and shows ask-agent-chip.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  agentSelectID,
		Kind:  protocol.UiChange,
		Value: "claude · explorer",
	}); err != nil {
		t.Fatalf("assign agent: %v", err)
	}

	var askInputID string
	w.until("agent assigned chip and active composer", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		chip := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "chip" && strings.HasSuffix(x.GetId(), "/ask-agent-chip") && x.GetText() == "claude · explorer"
		})
		inp := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "input" && strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled()
		})
		if inp != nil {
			askInputID = inp.GetId()
		}
		return chip != nil && askInputID != ""
	})

	// Verify .okf/assistant.json was persisted and hidden from list_docs and lint.
	listed, err := r.m.CallToolScoped(t.Context(), "okf", "list_docs", nil, ctxA)
	if err != nil || strings.Contains(listed, ".okf") || !strings.Contains(listed, `"count":6`) {
		t.Fatalf("list_docs leaked .okf or wrong count: %s, err=%v", listed, err)
	}
	linted, err := r.m.CallToolScoped(t.Context(), "okf", "lint", nil, ctxA)
	if err != nil || strings.Contains(linted, ".okf") {
		t.Fatalf("lint leaked .okf: %s, err=%v", linted, err)
	}

	// (3) Submit question -> observe Stage 1 spinner, then release decision model.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  askInputID,
		Kind:  protocol.UiSubmit,
		Value: "How does OKF store sandboxed documents and connect the peer mesh?",
	}); err != nil {
		t.Fatalf("submitAsk: %v", err)
	}

	w.until("Stage 1 spinner rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		prog := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "Stage 1")
		})
		return prog != nil
	})

	// Release Stage 1 decision call.
	close(decBlock)

	// Verify Stage 1 resulted in ref chips (solid for Essential, soft for Relevant), why collapsible, and consent bar.
	w.until("Stage 1 selection chips, why collapsible, and consent spinner", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		solidChip := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "chip" && strings.Contains(x.GetId(), "/ref-") && x.GetVariant() == "solid" && x.GetTone() == "accent"
		})
		softChip := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "chip" && strings.Contains(x.GetId(), "/ref-") && x.GetVariant() == "soft"
		})
		why := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "collapsible" && strings.HasSuffix(x.GetId(), "-why")
		})
		cons := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "approval")
		})
		return solidChip != nil && softChip != nil && why != nil && cons != nil
	})

	// Verify fakeDecisions received a single batch call with d01.., entry, needs_more.
	decCalls := dec.recordedCalls()
	if len(decCalls) != 1 {
		t.Fatalf("expected 1 batch decision call, got %d", len(decCalls))
	}
	var decReq struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(decCalls[0], &decReq); err != nil {
		t.Fatalf("unmarshal decision req: %v", err)
	}
	if _, ok := decReq.Questions["d01"]; !ok {
		t.Fatalf("missing d01 question in %v", decReq.Questions)
	}
	if _, ok := decReq.Questions["entry"]; !ok {
		t.Fatalf("missing entry question in %v", decReq.Questions)
	}
	if _, ok := decReq.Questions["needs_more"]; !ok {
		t.Fatalf("missing needs_more question in %v", decReq.Questions)
	}

	// Approve the agent ask once -> Stage 2 runs fakeAsks and delivers agentAnswered.
	ask := r.ask("okf")
	if ask.GetKind() != "ask" || ask.GetAgent() != "claude" || ask.GetProfile() != "explorer" {
		t.Fatalf("unexpected ask approval request: %+v", ask)
	}
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "okf",
		Op:       protocol.PluginAllowAgent,
		Key:      ask.GetId(),
		Value:    protocol.AgentAlways,
	}); err != nil {
		t.Fatalf("approve ask: %v", err)
	}

	// (4) Verify markdown answer with (okf:...) link, follow-up chip, and confidence pill.
	var ansMdID string
	w.until("agentAnswered rendered with okf: citation links and follow-ups", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		md := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "markdown" && strings.HasSuffix(x.GetId(), "-md") &&
				x.GetChange() && strings.Contains(x.GetText(), "(okf:decisions/001-storage.md)")
		})
		fu := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "chip" && strings.Contains(x.GetId(), "/fu-") && x.GetText() == "How does mesh replication work?"
		})
		if md != nil {
			ansMdID = md.GetId()
		}
		return md != nil && fu != nil && hasText(n, "High confidence")
	})

	// Verify prompt sent to fakeAsks contains full doc text, [[path]], and ## QUESTION.
	recordedAsks := asksHost.recordedAsks()
	if len(recordedAsks) != 1 {
		t.Fatalf("expected 1 agent ask, got %d", len(recordedAsks))
	}
	prompt := recordedAsks[0].Prompt
	for _, mustContain := range []string{
		"[[decisions/001-storage.md]]",
		"We store OKF documents in sandboxed datafs",
		"## QUESTION",
		"How does OKF store sandboxed documents and connect the peer mesh?",
	} {
		if !strings.Contains(prompt, mustContain) {
			t.Fatalf("agent prompt missing %q:\n%s", mustContain, prompt)
		}
	}

	// (5) Click okf: citation link -> opens Reader overlay; click Back -> returns to Ask tab!
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  ansMdID,
		Kind:  protocol.UiChange,
		Value: "okf:decisions/001-storage.md",
	}); err != nil {
		t.Fatalf("click okf citation link: %v", err)
	}

	var backBtnID string
	w.until("Reader overlay opened from Ask citation", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		bc := findNode(n, func(x *protocol.UiNode) bool {
			if x.GetKind() != "breadcrumb" {
				return false
			}
			opts := x.GetOptions()
			return len(opts) == 3 && opts[0] == "Vault" && opts[1] == "decisions" && opts[2] == "001-storage.md"
		})
		back := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.HasSuffix(x.GetId(), "/reader-back")
		})
		if back != nil {
			backBtnID = back.GetId()
		}
		return bc != nil && back != nil
	})

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  backBtnID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("click Reader Back: %v", err)
	}

	w.until("returned to Ask tab after Reader Back", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		sc := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "scroll" && strings.HasSuffix(x.GetId(), "/main-scroll") && x.GetVariant() == "follow"
		})
		return sc != nil && hasText(n, "High confidence")
	})

	// Switch to Tree tab and verify .okf/ directory is hidden (4 folders: architecture, concepts, decisions, procedures).
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Tree",
	}); err != nil {
		t.Fatalf("switch to Tree tab: %v", err)
	}
	w.until("Tree tab excludes .okf hidden directory", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "4 folders · 6 docs") && !hasText(n, ".okf")
	})

	// (8) Korean locale: verify 5th tab is "AI 질의".
	wKo := r.watchLocale("plugin:okf/vault", scopeA, "ko-KR")
	defer wKo.cancel()
	wKo.until("Korean Ask tab label", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		tn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "tabs" && strings.HasSuffix(x.GetId(), "/main-tabs")
		})
		if tn == nil {
			return false
		}
		opts := tn.GetOptions()
		return len(opts) == 5 && opts[4] == "AI 질의"
	})
}

// TestOKFExampleAskFallbackRefusalAndCancel verifies graceful degradation when
// no decision model is configured (local ranking), agent refusal -> danger card
// + Retry, and cancelling an in-flight Stage 2 ask.
func TestOKFExampleAskFallbackRefusalAndCancel(t *testing.T) {
	askBlk := make(chan struct{})
	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "explorer", Installed: true, Askable: true},
		},
		block: askBlk,
		answer: func(q AgentQuestion) (string, error) {
			return `{"answer":"Local fallback worked for [[decisions/001-storage.md]].","citations":["decisions/001-storage.md"],"confidence":"medium","follow_ups":[],"missing":[]}`, nil
		},
	}

	scopeA := "project:p-local"
	ctxA := ScopeContext{
		Scope:       protocol.ScopeProject,
		ScopeKey:    scopeA,
		ProjectID:   "p-local",
		ProjectName: "LocalProj",
		Roots:       []string{"/tmp/local"},
	}

	// Decisions is nil -> decision unavailable -> local ranking fallback.
	r := startRig(t, Config{
		Limits: testLimits,
		Asks:   asksHost,
		Projects: func() []ProjectRoots {
			return []ProjectRoots{{ID: "p-local", Name: "LocalProj", Roots: []string{"/tmp/local"}}}
		},
		ResolveScope: func(scopeKey string) ScopeContext {
			if scopeKey == scopeA {
				return ctxA
			}
			return ScopeContext{}
		},
	})
	r.write("okf", loadOKFFiles(t))
	r.state("okf", protocol.PluginWaiting)
	r.grant("okf")

	w := r.watchScoped("plugin:okf/vault", scopeA)
	defer w.cancel()
	w.until("starter", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "2 docs") })
	seedOKFDocsForAsk(t, r, ctxA)

	var tabsID string
	w.until("6 docs", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if tn := findNode(n, func(x *protocol.UiNode) bool { return x.GetKind() == "tabs" }); tn != nil {
			tabsID = tn.GetId()
		}
		return tabsID != "" && hasText(n, "6 docs")
	})

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Ask",
	}); err != nil {
		t.Fatal(err)
	}
	var selID string
	w.until("ask-agent with Local ranking pill", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if sel := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/ask-agent") }); sel != nil {
			selID = sel.GetId()
		}
		return selID != "" && hasText(n, "Local ranking")
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  selID,
		Kind:  protocol.UiChange,
		Value: "claude · explorer",
	}); err != nil {
		t.Fatal(err)
	}
	var inpID string
	w.until("composer ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled() }); inp != nil {
			inpID = inp.GetId()
		}
		return inpID != ""
	})

	// Submit question -> skips Stage 1 (local mode) and asks approval.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  inpID,
		Kind:  protocol.UiSubmit,
		Value: "Explain sandboxed storage",
	}); err != nil {
		t.Fatal(err)
	}
	ask := r.ask("okf")
	// Deny approval -> danger error card + Retry button.
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "okf",
		Op:       protocol.PluginDenyAgent,
		Key:      ask.GetId(),
	}); err != nil {
		t.Fatal(err)
	}

	var retryID string
	w.until("refused danger card and retry button", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		dangerCard := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "card" && x.GetVariant() == "danger" && strings.HasSuffix(x.GetId(), "-ans")
		})
		retry := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.Contains(x.GetId(), "/retry-")
		})
		if retry != nil {
			retryID = retry.GetId()
		}
		return dangerCard != nil && retry != nil && hasText(n, "declined") && hasText(n, "local")
	})

	// Tap Retry -> approve once -> cancel while blocked in Stage 2!
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  retryID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	ask2 := r.ask("okf")
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "okf",
		Op:       protocol.PluginAllowAgent,
		Key:      ask2.GetId(),
		Value:    protocol.AgentAlways,
	}); err != nil {
		t.Fatal(err)
	}

	// Cancel running ask via the Cancel button on the turn card.
	var cancelID string
	w.until("cancel button visible during ask", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		btn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.Contains(x.GetId(), "/cancel-")
		})
		if btn != nil {
			cancelID = btn.GetId()
		}
		return btn != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  cancelID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("canceled state and retry", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		retry := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.Contains(x.GetId(), "/retry-")
		})
		if retry != nil {
			retryID = retry.GetId()
		}
		return retry != nil && hasText(n, "Canceled.")
	})

	// Unblock fakeAsks and retry -> completes cleanly in local mode.
	close(askBlk)
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  retryID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("local fallback answer completed", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Local fallback worked") && hasText(n, "Medium confidence")
	})
}

// TestOKFExploreToolAndTreeNavigation verifies the synchronous explore tool
// (with and without decision model), Tree folder selection + newDocInFolder +
// cancelEdit return to Tree, and Reader -> Edit -> cancelEdit return to Reader.
func TestOKFExploreToolAndTreeNavigation(t *testing.T) {
	dec := &fakeDecisions{
		status: DecisionStatus{Available: true, Provider: "typesafe", Model: "jev"},
		respond: func(req []byte) ([]byte, error) {
			var parsed struct {
				State struct {
					Candidates map[string]struct {
						Path string `json:"path"`
					} `json:"candidates"`
				} `json:"state"`
			}
			_ = json.Unmarshal(req, &parsed)
			answers := map[string]any{}
			entry := "none"
			for cid, c := range parsed.State.Candidates {
				if c.Path == "decisions/001-storage.md" {
					answers[cid] = map[string]any{"type": "score", "level": 3, "label": "Essential", "score": 1.0}
					entry = cid
				} else {
					answers[cid] = map[string]any{"type": "score", "level": 1, "label": "Tangential", "score": 0.33}
				}
			}
			answers["entry"] = map[string]any{"type": "choice", "choice": entry}
			answers["needs_more"] = map[string]any{"type": "noul", "yes": false, "probability": 0.1}
			return json.Marshal(map[string]any{"provider": "typesafe", "model": "jev", "answers": answers})
		},
	}

	scopeA := "project:p-explore"
	ctxA := ScopeContext{
		Scope:       protocol.ScopeProject,
		ScopeKey:    scopeA,
		ProjectID:   "p-explore",
		ProjectName: "Explore",
		Roots:       []string{"/tmp/explore"},
	}

	r := startRig(t, Config{
		Limits:    testLimits,
		Decisions: dec,
		ResolveScope: func(scopeKey string) ScopeContext {
			if scopeKey == scopeA {
				return ctxA
			}
			return ScopeContext{}
		},
	})
	r.write("okf", loadOKFFiles(t))
	r.state("okf", protocol.PluginWaiting)
	r.grant("okf")

	w := r.watchScoped("plugin:okf/vault", scopeA)
	defer w.cancel()
	w.until("starter", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "2 docs") })
	seedOKFDocsForAsk(t, r, ctxA)

	// 1. explore tool with decision model enabled.
	rawDec, err := r.m.CallToolScoped(t.Context(), "okf", "explore", []byte(`{"query":"sandboxed storage"}`), ctxA)
	if err != nil {
		t.Fatalf("explore with decision: %v", err)
	}
	var expDec struct {
		Mode     string `json:"mode"`
		Entry    string `json:"entry"`
		Decision *struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"decision"`
		Results []struct {
			Path  string `json:"path"`
			Level string `json:"level"`
		} `json:"results"`
		Subgraph string `json:"subgraph"`
	}
	if err := json.Unmarshal([]byte(rawDec), &expDec); err != nil {
		t.Fatalf("unmarshal explore decision: %v (%s)", err, rawDec)
	}
	if expDec.Mode != "decision" || expDec.Entry != "decisions/001-storage.md" || expDec.Decision == nil || expDec.Decision.Model != "jev" {
		t.Fatalf("unexpected explore(decision) result: %+v", expDec)
	}
	if len(expDec.Results) == 0 || expDec.Results[0].Path != "decisions/001-storage.md" || expDec.Results[0].Level != "Essential" {
		t.Fatalf("expected decisions/001-storage.md Essential first, got %+v", expDec.Results)
	}
	if expDec.Subgraph == "" {
		t.Fatalf("expected non-empty ASCII subgraph")
	}

	// 2. explore tool with use_decision="false" -> local mode, still ranks decisions/001-storage.md #1.
	rawLoc, err := r.m.CallToolScoped(t.Context(), "okf", "explore", []byte(`{"query":"sandboxed","use_decision":"false"}`), ctxA)
	if err != nil {
		t.Fatalf("explore local: %v", err)
	}
	var expLoc struct {
		Mode    string `json:"mode"`
		Results []struct {
			Path string `json:"path"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(rawLoc), &expLoc); err != nil {
		t.Fatal(err)
	}
	if expLoc.Mode != "local" || len(expLoc.Results) == 0 || expLoc.Results[0].Path != "decisions/001-storage.md" {
		t.Fatalf("unexpected explore(local) result: %+v", expLoc)
	}

	// 2b. Korean bigram & multi-token retrieval via search + explore.
	if _, err := r.m.CallToolScoped(t.Context(), "okf", "upsert_doc", []byte(`{
		"path": "concepts/korean-sync.md",
		"title": "분산 동기화 프로토콜",
		"type": "Concept",
		"description": "노드 간 지식 동기화와 충돌 해결 규칙을 정의합니다.",
		"tags": "동기화, 분산",
		"status": "active",
		"body": "# 분산 동기화\n\nHLC 타임스탬프 기반으로 오프라인 편집 충돌을 자동으로 병합합니다."
	}`), ctxA); err != nil {
		t.Fatalf("upsert Korean doc: %v", err)
	}
	koSearch, err := r.m.CallToolScoped(t.Context(), "okf", "search", []byte(`{"query":"동기화 충돌"}`), ctxA)
	if err != nil || !strings.Contains(koSearch, `"concepts/korean-sync.md"`) {
		t.Fatalf("Korean search failed: %s, err=%v", koSearch, err)
	}
	if _, err := r.m.CallToolScoped(t.Context(), "okf", "delete_doc", []byte(`{"path":"concepts/korean-sync.md"}`), ctxA); err != nil {
		t.Fatalf("cleanup Korean doc: %v", err)
	}

	// 3. Tree folder selection -> newDocInFolder -> Editor prefilled -> Cancel returns to Tree.
	var tabsID string
	w.until("tabs ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if tn := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/main-tabs") }); tn != nil {
			tabsID = tn.GetId()
		}
		return tabsID != "" && hasText(n, "6 docs")
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Tree",
	}); err != nil {
		t.Fatal(err)
	}
	var dirConceptsID string
	w.until("Tree concepts dir with badge variant", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		d := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "tree_item" && strings.HasSuffix(x.GetId(), "/dir-concepts") && x.GetVariant() == "badge"
		})
		if d != nil {
			dirConceptsID = d.GetId()
		}
		return d != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  dirConceptsID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	var newDocHereID string
	w.until("tree-selection card with newDocInFolder button", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		btn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.HasSuffix(x.GetId(), "/tree-sel-newdoc")
		})
		if btn != nil {
			newDocHereID = btn.GetId()
		}
		return btn != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  newDocHereID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	var edCancelID string
	w.until("Editor overlay prefilled with concepts/ and tabs unchanged (5 tabs)", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		pathInput := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "input" && strings.HasSuffix(x.GetId(), "/ed-path") && x.GetValue() == "concepts/"
		})
		cancelBtn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.HasSuffix(x.GetId(), "/ed-cancel")
		})
		tn := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/main-tabs") })
		if cancelBtn != nil {
			edCancelID = cancelBtn.GetId()
		}
		return pathInput != nil && cancelBtn != nil && tn != nil && len(tn.GetOptions()) == 5
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  edCancelID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("cancelEdit returned to Tree tab", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findNode(n, func(x *protocol.UiNode) bool { return x.GetKind() == "tree" }) != nil
	})

	// 4. Open doc from Tree -> Reader -> Edit -> Cancel returns to Reader!
	var docItemID string
	w.until("doc tree_item ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		d := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "tree_item" && x.GetDrag() == "decisions/001-storage.md"
		})
		if d != nil {
			docItemID = d.GetId()
		}
		return d != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  docItemID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	var editBtnID string
	w.until("Reader opened from Tree", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		btn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.HasSuffix(x.GetId(), "/reader-edit")
		})
		if btn != nil {
			editBtnID = btn.GetId()
		}
		return btn != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  editBtnID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("Editor opened from Reader", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		cancelBtn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.HasSuffix(x.GetId(), "/ed-cancel")
		})
		if cancelBtn != nil {
			edCancelID = cancelBtn.GetId()
		}
		return cancelBtn != nil && hasText(n, "Edit decisions/001-storage.md")
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  edCancelID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("cancelEdit returned to Reader", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/reader-meta") }) != nil
	})
}

func assertAllNodeKeySegmentsWithinLimit(t *testing.T, root *protocol.UiNode, maxBytes int) {
	t.Helper()
	var walk func(*protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n == nil {
			return
		}
		for _, seg := range strings.Split(n.GetId(), "/") {
			if len(seg) > maxBytes {
				t.Fatalf("node id %q has segment %q with %d UTF-8 bytes (> %d)", n.GetId(), seg, len(seg), maxBytes)
			}
		}
		for _, ch := range n.GetChildren() {
			walk(ch)
		}
	}
	walk(root)
}

// TestOKFAskStartedStage1CancelAndRaceGuard verifies:
//  1. Unassigned agent picker shows selection hint rather than askable[0].description.
//  2. Stage 1 (selecting) progress card includes a working cancelAsk button.
//  3. Approving a pending ask immediately delivers agentAskStarted -> consent transitions to answering.
//  4. Canceling an in-flight Stage 2 ask and immediately starting a follow-up question is immune to a
//     delayed agentAskFailed from the canceled turn overwriting ask.busy.
func TestOKFAskStartedStage1CancelAndRaceGuard(t *testing.T) {
	decBlock1 := make(chan struct{})
	var decCalls atomic.Int32
	dec := &fakeDecisions{
		status: DecisionStatus{Available: true, Provider: "typesafe", Model: "jev"},
		respond: func(req []byte) ([]byte, error) {
			if decCalls.Add(1) == 1 {
				<-decBlock1
			}
			var parsed struct {
				State struct {
					Candidates map[string]struct {
						Path string `json:"path"`
					} `json:"candidates"`
				} `json:"state"`
			}
			_ = json.Unmarshal(req, &parsed)
			answers := map[string]any{}
			entry := "none"
			for cid, c := range parsed.State.Candidates {
				if c.Path == "decisions/001-storage.md" {
					answers[cid] = map[string]any{"type": "score", "level": 3, "label": "Essential", "score": 1.0}
					entry = cid
				} else {
					answers[cid] = map[string]any{"type": "score", "level": 1, "label": "Tangential", "score": 0.33}
				}
			}
			answers["entry"] = map[string]any{"type": "choice", "choice": entry}
			answers["needs_more"] = map[string]any{"type": "noul", "yes": false, "probability": 0.1}
			return json.Marshal(map[string]any{"provider": "typesafe", "model": "jev", "answers": answers})
		},
	}

	ask1Started := make(chan struct{})
	ask1Release := make(chan struct{})
	ask2Started := make(chan struct{})
	ask2Release := make(chan struct{})
	var askCallNum atomic.Int32

	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "explorer", Description: "Fast read-only explorer", Installed: true, Askable: true},
			{Agent: "claude", Profile: "", Description: "Default claude", Installed: true, Askable: true},
		},
		answer: func(q AgentQuestion) (string, error) {
			switch askCallNum.Add(1) {
			case 1:
				close(ask1Started)
				<-ask1Release
				return "", errors.New("delayed cancel failure from turn 1")
			default:
				close(ask2Started)
				<-ask2Release
				return `{"answer":"Second turn completed cleanly after [[decisions/001-storage.md]].","citations":["decisions/001-storage.md"],"confidence":"high","follow_ups":[],"missing":[]}`, nil
			}
		},
	}

	scopeA := "project:p-race"
	ctxA := ScopeContext{
		Scope:       protocol.ScopeProject,
		ScopeKey:    scopeA,
		ProjectID:   "p-race",
		ProjectName: "RaceProj",
		Roots:       []string{"/tmp/race"},
	}

	r := startRig(t, Config{
		Limits:    testLimits,
		Decisions: dec,
		Asks:      asksHost,
		Projects: func() []ProjectRoots {
			return []ProjectRoots{{ID: "p-race", Name: "RaceProj", Roots: []string{"/tmp/race"}}}
		},
		ResolveScope: func(scopeKey string) ScopeContext {
			if scopeKey == scopeA {
				return ctxA
			}
			return ScopeContext{}
		},
	})
	r.write("okf", loadOKFFiles(t))
	r.state("okf", protocol.PluginWaiting)
	r.grant("okf")

	w := r.watchScoped("plugin:okf/vault", scopeA)
	defer w.cancel()
	w.until("starter", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "2 docs") })
	seedOKFDocsForAsk(t, r, ctxA)

	var tabsID string
	w.until("6 docs", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if tn := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/main-tabs") }); tn != nil {
			tabsID = tn.GetId()
		}
		return tabsID != "" && hasText(n, "6 docs")
	})

	// Switch to Ask tab and verify unassigned hint (must NOT show askable[0].description).
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Ask",
	}); err != nil {
		t.Fatal(err)
	}

	var selID string
	w.until("unassigned picker shows hint without first agent description", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		sel := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/ask-agent") })
		if sel != nil {
			selID = sel.GetId()
		}
		return sel != nil && hasText(n, "Select an agent profile above") && !hasText(n, "Fast read-only explorer")
	})

	// Assign claude · explorer.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  selID,
		Kind:  protocol.UiChange,
		Value: "claude · explorer",
	}); err != nil {
		t.Fatal(err)
	}

	var inpID string
	w.until("composer ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled() })
		if inp != nil {
			inpID = inp.GetId()
		}
		return inp != nil
	})

	// 1. Submit question -> Stage 1 (selecting) must expose a Cancel button.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  inpID,
		Kind:  protocol.UiSubmit,
		Value: "Stage 1 cancel test question?",
	}); err != nil {
		t.Fatal(err)
	}

	var stage1CancelID string
	w.until("Stage 1 selecting spinner with cancel button", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		prog := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "Stage 1")
		})
		btn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.Contains(x.GetId(), "/cancel-")
		})
		if btn != nil {
			stage1CancelID = btn.GetId()
		}
		return prog != nil && btn != nil
	})

	// Cancel during Stage 1 -> composer immediately re-enables; releasing decBlock1 later must be ignored.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  stage1CancelID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("Stage 1 canceled and composer re-enabled", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled() })
		if inp != nil {
			inpID = inp.GetId()
		}
		return inp != nil && hasText(n, "Canceled.")
	})
	close(decBlock1)

	// 2. Submit Turn A -> enters consent -> approve -> agentAskStarted transitions consent -> answering!
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  inpID,
		Kind:  protocol.UiSubmit,
		Value: "First Stage 2 question?",
	}); err != nil {
		t.Fatal(err)
	}
	w.until("Turn A waiting for consent approval", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		prog := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "approval")
		})
		return prog != nil
	})

	askA := r.ask("okf")
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "okf",
		Op:       protocol.PluginAllowAgent,
		Key:      askA.GetId(),
		Value:    protocol.AgentAlways,
	}); err != nil {
		t.Fatalf("approve askA: %v", err)
	}
	<-ask1Started

	var turnACancelID string
	w.until("agentAskStarted transitioned Turn A from consent to answering", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		prog := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "Stage 2") && strings.Contains(x.GetText(), "answering")
		})
		btn := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "button" && strings.Contains(x.GetId(), "/cancel-")
		})
		if btn != nil {
			turnACancelID = btn.GetId()
		}
		return prog != nil && btn != nil
	})

	// 3. Cancel Turn A (its ask goroutine is still blocked on ask1Release), then immediately launch Turn B.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  turnACancelID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("Turn A canceled and composer enabled for Turn B", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled() })
		if inp != nil {
			inpID = inp.GetId()
		}
		return inp != nil
	})

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  inpID,
		Kind:  protocol.UiSubmit,
		Value: "Second Stage 2 follow-up question?",
	}); err != nil {
		t.Fatal(err)
	}
	<-ask2Started

	w.until("Turn B is answering and composer is disabled", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		prog := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "Stage 2")
		})
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && x.GetDisabled() })
		return prog != nil && inp != nil
	})

	// Now release Turn A so its delayed agentAskFailed arrives while Turn B is still running!
	close(ask1Release)

	// Trigger a benign UI interaction (toggle why on Turn B? or toggleHistory) to force a render after Turn A's event drains,
	// and verify Turn B is STILL answering with composer disabled!
	// Wait: releasing ask2Release after checking ensures Turn B finishes cleanly.
	// Let's toggle history twice to ensure any queued Turn A agentAskFailed was processed on the actor loop.
	var histBtnID string
	w.until("history button found", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		b := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/ask-history-btn") })
		if b != nil {
			histBtnID = b.GetId()
		}
		return b != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  histBtnID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("delayed Turn A failure did not clear busy for Turn B", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		hist := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/ask-history") })
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") })
		prog := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "progress" && x.GetVariant() == "spinner" && strings.Contains(x.GetText(), "Stage 2")
		})
		if hist == nil {
			return false
		}
		if inp == nil || !inp.GetDisabled() || prog == nil {
			t.Fatalf("expected Turn B to remain busy/answering after delayed Turn A failure; inpDisabled=%v prog=%v", inp != nil && inp.GetDisabled(), prog != nil)
		}
		return true
	})

	// Release Turn B -> completes cleanly.
	close(ask2Release)
	w.until("Turn B completed cleanly", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled() })
		return inp != nil && hasText(n, "Second turn completed cleanly")
	})
}

// TestOKFLongKoreanSafeKeyLargeAnswerAndHiddenPathGuard verifies:
//  1. Hidden paths (.okf/secret.md) are rejected by upsert_doc, move_doc, and delete_doc.
//  2. 50+ char Korean doc paths, tags, colliding ASCII tags (a/b vs a_b vs a#b), and 50+ char Korean
//     broken wiki-links render cleanly across Browse, Tree, Graph, Health, and Reader with every key <= 128 UTF-8 bytes.
//  3. Browse snippets strip raw Markdown syntax (**, `, [[path|label]]).
//  4. A 40 KiB Korean agent answer renders cleanly within uiblock's 32 KiB markdown cap, preserves newlines,
//     and displays the Truncated badge.
//  5. Editing a >7.5 KiB Korean document opens the Editor safely without exceeding uiblock's 8 KiB textarea cap
//     and shows the size warning banner.
func TestOKFLongKoreanSafeKeyLargeAnswerAndHiddenPathGuard(t *testing.T) {
	// Build a ~40 KiB Korean Markdown answer (each paragraph ~105 bytes * 400 ≈ 42 KiB).
	var hugeAnswer strings.Builder
	hugeAnswer.WriteString("# 대규모 한국어 분석 결과\n\n[[concepts/overview.md]] 문서를 기반으로 상세 분석을 제공합니다.\n\n")
	for i := 0; i < 400; i++ {
		hugeAnswer.WriteString("한글 마크다운 본문 단락입니다. 줄바꿈이 보존되어야 하며 바이트 한도를 안전하게 지켜야 합니다.\n\n")
	}
	hugeAnswerJSON, err := json.Marshal(map[string]any{
		"answer":     hugeAnswer.String(),
		"citations":  []string{"concepts/overview.md"},
		"confidence": "high",
		"follow_ups": []string{"요약을 더 짧게 볼까요?"},
		"missing":    []string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "explorer", Installed: true, Askable: true},
		},
		answer: func(q AgentQuestion) (string, error) {
			return string(hugeAnswerJSON), nil
		},
	}

	scopeA := "project:p-utf8"
	ctxA := ScopeContext{
		Scope:       protocol.ScopeProject,
		ScopeKey:    scopeA,
		ProjectID:   "p-utf8",
		ProjectName: "Utf8Proj",
		Roots:       []string{"/tmp/utf8"},
	}

	r := startRig(t, Config{
		Limits: testLimits,
		Asks:   asksHost,
		Projects: func() []ProjectRoots {
			return []ProjectRoots{{ID: "p-utf8", Name: "Utf8Proj", Roots: []string{"/tmp/utf8"}}}
		},
		ResolveScope: func(scopeKey string) ScopeContext {
			if scopeKey == scopeA {
				return ctxA
			}
			return ScopeContext{}
		},
	})
	r.write("okf", loadOKFFiles(t))
	r.state("okf", protocol.PluginWaiting)
	r.grant("okf")

	w := r.watchScoped("plugin:okf/vault", scopeA)
	defer w.cancel()
	w.until("starter", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "2 docs") })

	// 1. Hidden path (.okf/secret.md) write / move / delete must be refused.
	if _, err := r.m.CallToolScoped(t.Context(), "okf", "upsert_doc", []byte(`{
		"path": ".okf/secret.md",
		"title": "Secret",
		"body": "should fail"
	}`), ctxA); err == nil {
		t.Fatal("expected upsert_doc on .okf/secret.md to fail")
	}
	if _, err := r.m.CallToolScoped(t.Context(), "okf", "move_doc", []byte(`{
		"from_path": "concepts/overview.md",
		"to_path": ".okf/secret.md"
	}`), ctxA); err == nil {
		t.Fatal("expected move_doc to .okf/secret.md to fail")
	}
	// Verify vault remained clean after refused writes (also resets consecutive tool-error counter).
	if listed, err := r.m.CallToolScoped(t.Context(), "okf", "list_docs", nil, ctxA); err != nil || strings.Contains(listed, ".okf") || !strings.Contains(listed, `"count":2`) {
		t.Fatalf("expected clean vault after refused hidden-path calls: %s, err=%v", listed, err)
	}
	if _, err := r.m.CallToolScoped(t.Context(), "okf", "delete_doc", []byte(`{
		"path": ".okf/secret.md"
	}`), ctxA); err == nil {
		t.Fatal("expected delete_doc on .okf/secret.md to fail")
	}

	// 2. Long Korean path (>50 Korean chars = >150 UTF-8 bytes), long Korean tag, colliding tags, long Korean broken link.
	longKoStem := strings.Repeat("가나다라마바사아자차", 6) // 60 Korean chars = 180 UTF-8 bytes
	longKoPath := "concepts/" + longKoStem + ".md"
	longKoTag := strings.Repeat("태그한글검증", 10)                   // 60 Korean chars = 180 UTF-8 bytes
	longKoBroken := "concepts/" + strings.Repeat("깨진링크대상한글", 8) // 64 Korean chars = 192 UTF-8 bytes

	longDocPayload, err := json.Marshal(map[string]any{
		"path":        longKoPath,
		"title":       "긴 한글 경로 테스트 문서",
		"type":        "Concept",
		"description": "",
		"tags":        []string{longKoTag, "a/b", "a_b", "a#b"},
		"status":      "active",
		"body":        "# 제목\n\n이 문서는 **굵은글씨**와 `인라인코드` 그리고 [[concepts/overview|개요문서]] 및 [[" + longKoBroken + "]] 링크를 포함합니다.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := r.m.CallToolScoped(t.Context(), "okf", "upsert_doc", longDocPayload, ctxA); err != nil || !strings.Contains(res, `"saved":true`) {
		t.Fatalf("upsert long Korean doc failed: %s, err=%v", res, err)
	}

	var tabsID, longCardID string
	browseTree := w.until("Browse renders long Korean doc with clean snippet and valid keys", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if tn := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/main-tabs") }); tn != nil {
			tabsID = tn.GetId()
		}
		card := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "card" && strings.Contains(x.GetId(), "/doc-") && hasText(x, "긴 한글 경로 테스트 문서")
		})
		if card != nil {
			longCardID = card.GetId()
		}
		return tabsID != "" && card != nil && hasText(n, "3 docs") && hasText(n, "굵은글씨") && hasText(n, "개요문서")
	})
	assertAllNodeKeySegmentsWithinLimit(t, browseTree, 128)
	// Verify Browse card snippet stripped raw markdown markers (** and ` and [[).
	if hasText(browseTree, "**굵은글씨**") || hasText(browseTree, "`인라인코드`") || hasText(browseTree, "[[concepts/overview") {
		t.Fatalf("expected Browse snippet to strip raw markdown markers")
	}

	// Switch across Tree, Graph, Health tabs and verify all render cleanly with <= 128B keys.
	for _, tab := range []string{"Tree", "Graph", "Health (1)"} {
		if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
			Block: "plugin:okf/vault",
			Scope: scopeA,
			Node:  tabsID,
			Kind:  protocol.UiChange,
			Value: tab,
		}); err != nil {
			t.Fatalf("switch to %s: %v", tab, err)
		}
		tree := w.until("tab "+tab+" rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
			switch tab {
			case "Tree":
				return findNode(n, func(x *protocol.UiNode) bool { return x.GetKind() == "tree" }) != nil
			case "Graph":
				return findNode(n, func(x *protocol.UiNode) bool { return x.GetKind() == "graph" }) != nil
			default:
				return findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/health-broken") }) != nil
			}
		})
		assertAllNodeKeySegmentsWithinLimit(t, tree, 128)
	}

	// Back to Browse -> tap long Korean doc -> verify Reader renders cleanly with <= 128B keys.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Browse",
	}); err != nil {
		t.Fatal(err)
	}
	w.until("back to Browse", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		card := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "card" && strings.Contains(x.GetId(), "/doc-") && hasText(x, "긴 한글 경로 테스트 문서")
		})
		if card != nil {
			longCardID = card.GetId()
		}
		return card != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  longCardID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	readerTree := w.until("Reader renders long Korean doc", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/reader-body") }) != nil
	})
	assertAllNodeKeySegmentsWithinLimit(t, readerTree, 128)

	// 3. Large >7.5 KiB Korean doc in Editor must clamp textarea safely and show warning banner.
	bigDocBody := strings.Repeat("편집기 바이트 한도 테스트용 한국어 문장입니다.\n", 200) // ~13 KiB UTF-8
	bigDocPayload, err := json.Marshal(map[string]any{
		"path":   "concepts/big-doc.md",
		"title":  "Big Editor Doc",
		"type":   "Concept",
		"status": "active",
		"body":   bigDocBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := r.m.CallToolScoped(t.Context(), "okf", "upsert_doc", bigDocPayload, ctxA); err != nil || !strings.Contains(res, `"saved":true`) {
		t.Fatalf("upsert big doc failed: %s, err=%v", res, err)
	}
	// Close Reader and open Big Editor Doc -> Edit.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Browse",
	}); err != nil {
		t.Fatal(err)
	}
	var bigCardID string
	w.until("Big Editor Doc card visible", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		c := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "card" && strings.HasSuffix(x.GetId(), "/doc-concepts_big-doc.md")
		})
		if c != nil {
			bigCardID = c.GetId()
		}
		return c != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  bigCardID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	var editBtnID string
	w.until("Reader for Big Editor Doc", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		b := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/reader-edit") })
		if b != nil {
			editBtnID = b.GetId()
		}
		return b != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  editBtnID,
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	w.until("Editor renders >7.5 KiB doc safely with warning banner", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		warn := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/ed-body-limit-warn") })
		ta := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "textarea" && strings.HasSuffix(x.GetId(), "/ed-body")
		})
		if warn == nil || ta == nil {
			return false
		}
		if len(ta.GetValue()) > 8*1024 {
			t.Fatalf("textarea value exceeded 8 KiB: %d", len(ta.GetValue()))
		}
		return true
	})

	// 4. Ask tab -> 40 KiB Korean answer renders cleanly within 32 KiB limit, preserves newlines, and shows Truncated pill.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  tabsID,
		Kind:  protocol.UiChange,
		Value: "Ask",
	}); err != nil {
		t.Fatal(err)
	}
	var selID string
	w.until("Ask picker ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		sel := findNode(n, func(x *protocol.UiNode) bool { return strings.HasSuffix(x.GetId(), "/ask-agent") })
		if sel != nil {
			selID = sel.GetId()
		}
		return sel != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  selID,
		Kind:  protocol.UiChange,
		Value: "claude · explorer",
	}); err != nil {
		t.Fatal(err)
	}
	var inpID string
	w.until("Ask composer ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		inp := findNode(n, func(x *protocol.UiNode) bool { return strings.Contains(x.GetId(), "/ask-input-") && !x.GetDisabled() })
		if inp != nil {
			inpID = inp.GetId()
		}
		return inp != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:okf/vault",
		Scope: scopeA,
		Node:  inpID,
		Kind:  protocol.UiSubmit,
		Value: "긴 한국어 답변 테스트 질문",
	}); err != nil {
		t.Fatal(err)
	}
	ask := r.ask("okf")
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "okf",
		Op:       protocol.PluginAllowAgent,
		Key:      ask.GetId(),
		Value:    protocol.AgentAlways,
	}); err != nil {
		t.Fatal(err)
	}

	w.until("40 KiB Korean answer clamped safely with newlines and Truncated pill", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		md := findNode(n, func(x *protocol.UiNode) bool {
			return x.GetKind() == "markdown" && strings.HasSuffix(x.GetId(), "-md")
		})
		if md == nil {
			return false
		}
		text := md.GetText()
		if len(text) > 32*1024 {
			t.Fatalf("markdown node exceeded 32 KiB: %d bytes", len(text))
		}
		return strings.Contains(text, "대규모 한국어 분석 결과") &&
			strings.Contains(text, "\n") &&
			hasText(n, "Truncated")
	})
}
