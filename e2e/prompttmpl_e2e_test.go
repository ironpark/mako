package e2e_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/prompttmpl"
)

func TestPromptTmplTier1(t *testing.T) {
	t.Run("loaddir_loadall_find_override", func(t *testing.T) {
		dir := t.TempDir()
		validSrc := `---
description: Custom review template
agents: [claude, codex:fast]
args:
  - name: file
    type: string
    required: true
  - name: depth
    type: integer
    default: "2"
---
Review {{file}} depth={{depth}} on {{agent}}.`
		if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(validSrc), 0o644); err != nil {
			t.Fatal(err)
		}
		// Override built-in "template" to verify user override precedence.
		overrideSrc := `---
description: User override for template
---
Custom template maker.`
		if err := os.WriteFile(filepath.Join(dir, prompttmpl.MakerName+".md"), []byte(overrideSrc), 0o644); err != nil {
			t.Fatal(err)
		}
		// Broken template with invalid frontmatter should appear in broken slice.
		brokenSrc := `---
description: [unclosed
---
bad`
		if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte(brokenSrc), 0o644); err != nil {
			t.Fatal(err)
		}

		loaded, broken := prompttmpl.LoadDir(dir)
		if len(loaded) != 2 || len(broken) != 1 || broken[0].Name != "broken" {
			t.Fatalf("LoadDir: loaded=%d broken=%+v", len(loaded), broken)
		}

		all, brokenAll := prompttmpl.LoadAll(dir)
		if len(brokenAll) != 1 {
			t.Fatalf("LoadAll broken=%+v", brokenAll)
		}
		var foundMaker, foundPlugin, foundReview *prompttmpl.Template
		for _, tm := range all {
			switch tm.Name {
			case prompttmpl.MakerName:
				foundMaker = tm
			case prompttmpl.PluginMakerName:
				foundPlugin = tm
			case "review":
				foundReview = tm
			}
		}
		if foundMaker == nil || foundMaker.Builtin || foundMaker.Description != "User override for template" {
			t.Fatalf("expected user override for %q, got %+v", prompttmpl.MakerName, foundMaker)
		}
		if foundPlugin == nil || !foundPlugin.Builtin {
			t.Fatalf("expected builtin %q in LoadAll, got %+v", prompttmpl.PluginMakerName, foundPlugin)
		}
		if foundReview == nil {
			t.Fatal("missing review template in LoadAll")
		}

		byFind, err := prompttmpl.Find(dir, prompttmpl.MakerName)
		if err != nil || byFind.Builtin || byFind.Description != "User override for template" {
			t.Fatalf("Find override: tm=%+v err=%v", byFind, err)
		}
		if _, err := prompttmpl.Find(dir, "does-not-exist"); err == nil {
			t.Fatal("expected Find on missing template to return error")
		}
	})

	t.Run("format_roundtrip_and_for_matching", func(t *testing.T) {
		src := `---
description: Roundtrip check
agents: [claude, codex:fast]
args:
  - name: target
    type: string
    required: true
  - name: mode
    type: select
    options: [quick, deep]
    default: quick
---
Inspect {{target}} ({{mode}}).`
		tm, err := prompttmpl.Parse("inspect", []byte(src))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		formatted := prompttmpl.Format(tm)
		rt, err := prompttmpl.Parse("inspect", formatted)
		if err != nil {
			t.Fatalf("Parse(Format): %v\n%s", err, formatted)
		}
		if rt.Description != tm.Description || len(rt.Args) != 2 || strings.TrimSpace(rt.Body) != strings.TrimSpace(tm.Body) {
			t.Fatalf("roundtrip mismatch: got %+v want %+v", rt, tm)
		}
		if !rt.For("claude", "") || !rt.For("claude", "sonnet") {
			t.Fatal("expected For(claude, ...) to match")
		}
		if !rt.For("codex", "fast") || rt.For("codex", "slow") {
			t.Fatal("expected codex:fast to match only profile fast")
		}
		if rt.For("gemini", "") {
			t.Fatal("unexpected match for gemini")
		}
	})

	t.Run("bind_positional_named_rest_and_argerrors", func(t *testing.T) {
		src := `---
args:
  - name: count
    type: integer
    required: true
  - name: kind
    type: choice
    options: [bug, feat]
    default: bug
  - name: note
    type: text
    rest: true
---
{{count}} {{kind}}: {{note}}`
		tm, err := prompttmpl.Parse("note", []byte(src))
		if err != nil {
			t.Fatal(err)
		}

		vals, err := tm.Bind(`3 kind=feat hello "world" & more`)
		if err != nil {
			t.Fatalf("Bind valid: %v", err)
		}
		if vals["count"] != "3" || vals["kind"] != "feat" || vals["note"] != `hello "world" & more` {
			t.Fatalf("unexpected bound values: %+v", vals)
		}

		cases := []struct {
			name string
			args string
			want prompttmpl.ErrorKind
		}{
			{"missing", "", prompttmpl.Missing},
			{"bad_int", "abc", prompttmpl.BadValue},
			{"bad_choice", "1 kind=invalid", prompttmpl.BadValue},
			{"twice", "1 count=2", prompttmpl.Twice},
			{"unquoted", `"unclosed`, prompttmpl.Unquoted},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := tm.Bind(tc.args)
				var ae *prompttmpl.ArgError
				if !errors.As(err, &ae) || ae.Kind != tc.want {
					t.Fatalf("Bind(%q) err = %v, want ArgError kind %v", tc.args, err, tc.want)
				}
			})
		}

		noRestTm, err := prompttmpl.Parse("norest", []byte("---\nargs:\n  - name: a\n    type: string\n---\n{{a}}"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = noRestTm.Bind("one two")
		var ae *prompttmpl.ArgError
		if !errors.As(err, &ae) || ae.Kind != prompttmpl.Extra {
			t.Fatalf("Bind extra err = %v, want Extra", err)
		}
	})

	t.Run("expand_svelte_control_flow_and_js_block", func(t *testing.T) {
		src := `---
args:
  - name: env
    type: select
    options: [prod, dev]
    required: true
  - name: items
    type: string
    default: "a,b"
---
{#if env == "prod"}STRICT{:else}RELAXED{/if}
{#match env}{:prod}P{:dev}D{:else}?{/match}
{#each items.split(",") as it, idx}[{{idx}}:{{it}}]{/each}
` + "```js\nreturn \"sum=\" + (2 + 3);\n```"
		tm, err := prompttmpl.Parse("render", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		out, err := tm.Expand(context.Background(), "prod", map[string]string{"agent": "claude"})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}
		for _, want := range []string{"STRICT", "P", "[0:a][1:b]", "sum=5"} {
			if !strings.Contains(out, want) {
				t.Fatalf("Expand output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("load_metadata_parsedraft_and_expandwith", func(t *testing.T) {
		dir := t.TempDir()
		src := "---\ndescription: Fast metadata check\nargs:\n  - name: topic\n    type: string\n    required: true\n---\nTopic={{topic}}"
		if err := os.WriteFile(filepath.Join(dir, "meta.md"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		metaOnly, broken := prompttmpl.LoadDirMetadata(dir)
		if len(metaOnly) != 1 || len(broken) != 0 || metaOnly[0].Name != "meta" || metaOnly[0].Description != "Fast metadata check" {
			t.Fatalf("LoadDirMetadata: meta=%+v broken=%+v", metaOnly, broken)
		}
		allMeta, brokenAll := prompttmpl.LoadAllMetadata(dir)
		if len(brokenAll) != 0 || len(allMeta) < 2 {
			t.Fatalf("LoadAllMetadata: len=%d broken=%+v", len(allMeta), brokenAll)
		}
		d, ok := prompttmpl.ParseDraft("//")
		if !ok || d.Name != "" {
			t.Fatalf("ParseDraft('//') = (%+v, %v), want empty Name ok=true", d, ok)
		}
		tm, err := prompttmpl.Find(dir, "meta")
		if err != nil {
			t.Fatal(err)
		}
		out, err := tm.ExpandWith(context.Background(), "concurrency", nil, &blockingAskHost{})
		if err != nil || strings.TrimSpace(out) != "Topic=concurrency" {
			t.Fatalf("ExpandWith: out=%q err=%v", out, err)
		}
	})
}

type blockingAskHost struct {
	called      chan struct{}
	ctxErr      chan error
	hasDeadline bool
	until       time.Duration
	immediate   error
}

func (h *blockingAskHost) Agents(context.Context, string, bool) (prompttmpl.AgentCatalog, error) {
	return prompttmpl.AgentCatalog{}, nil
}

func (h *blockingAskHost) Spawn(context.Context, prompttmpl.SpawnRequest) (prompttmpl.SpawnResult, error) {
	return prompttmpl.SpawnResult{}, nil
}

func (h *blockingAskHost) Ask(ctx context.Context, _ prompttmpl.AskRequest) (prompttmpl.AskResult, error) {
	dl, ok := ctx.Deadline()
	h.hasDeadline = ok
	if ok {
		h.until = time.Until(dl)
	}
	select {
	case h.called <- struct{}{}:
	default:
	}
	if h.immediate != nil {
		return prompttmpl.AskResult{}, h.immediate
	}
	<-ctx.Done()
	select {
	case h.ctxErr <- ctx.Err():
	default:
	}
	return prompttmpl.AskResult{}, ctx.Err()
}

func (h *blockingAskHost) Decide(context.Context, []byte) ([]byte, error) {
	return []byte("null"), nil
}

func TestPromptTmplTier2Boundaries(t *testing.T) {
	// B20: ParseCall empty-name rejection.
	t.Run("B20_parse_call", func(t *testing.T) {
		c, ok := prompttmpl.ParseCall("//rev a b")
		if !ok || c.Name != "rev" || c.Args != "a b" {
			t.Fatalf("ParseCall('//rev a b') = (%+v, %v), want Name=rev Args='a b' ok=true", c, ok)
		}
		if _, ok := prompttmpl.ParseCall("// rev"); ok {
			t.Fatal("ParseCall('// rev') returned ok=true, want false")
		}
		if c, ok := prompttmpl.ParseCall("//"); ok {
			t.Fatalf("ParseCall('//') returned (%+v, true), want ok=false (B20)", c)
		}
	})

	// B11: Bounded context cancellation propagates to Host.Ask, nil host fails cleanly, infinite JS aborts.
	t.Run("B11_host_ask_bounded_timeout_cancellation", func(t *testing.T) {
		tm, err := prompttmpl.Parse("hanghost", []byte(`{{await mako.ask("hang")}}`))
		if err != nil {
			t.Fatal(err)
		}
		host := &blockingAskHost{
			called: make(chan struct{}, 1),
			ctxErr: make(chan error, 1),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err = tm.ExpandWith(ctx, "", nil, host)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected ExpandWith to fail on context timeout")
		}
		if elapsed > 2*time.Second {
			t.Fatalf("ExpandWith took %v to cancel (expected < 2s)", elapsed)
		}
		if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "ran too long") {
			t.Fatalf("expected deadline exceeded or 'ran too long' error, got %v", err)
		}
		select {
		case <-host.called:
		default:
			t.Fatal("Host.Ask was never invoked")
		}
		select {
		case askErr := <-host.ctxErr:
			if !errors.Is(askErr, context.DeadlineExceeded) {
				t.Fatalf("Host.Ask ctx.Err() = %v, want DeadlineExceeded", askErr)
			}
		default:
			t.Fatal("Host.Ask did not observe ctx cancellation")
		}
	})

	t.Run("B11_host_ask_wall_deadline_with_background_ctx", func(t *testing.T) {
		tm, err := prompttmpl.Parse("bgwall", []byte(`{{await mako.ask("probe")}}`))
		if err != nil {
			t.Fatal(err)
		}
		probeErr := errors.New("probe complete")
		host := &blockingAskHost{
			called:    make(chan struct{}, 1),
			immediate: probeErr,
		}
		// Pass context.Background() (no parent deadline): dispatchHostCall must pass the
		// bounded scriptBudget (hostWallTimeout = 5m), NOT b.parent, so Host.Ask sees a deadline.
		start := time.Now()
		_, err = tm.ExpandWith(context.Background(), "", nil, host)
		elapsed := time.Since(start)
		if err == nil || !strings.Contains(err.Error(), "probe complete") {
			t.Fatalf("expected ExpandWith to return immediate probe error, got %v", err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("ExpandWith took %v (expected < 2s)", elapsed)
		}
		select {
		case <-host.called:
		default:
			t.Fatal("Host.Ask was never invoked")
		}
		if !host.hasDeadline {
			t.Fatal("Host.Ask received context without deadline when parent was context.Background(); expected bounded hostWallTimeout (B11)")
		}
		if host.until <= 0 || host.until > 5*time.Minute+time.Second {
			t.Fatalf("Host.Ask deadline until = %v, want in (0, 5m+1s] (B11)", host.until)
		}
	})

	t.Run("B11_nil_host_fails_cleanly", func(t *testing.T) {
		tm, err := prompttmpl.Parse("nilhost", []byte(`{{await mako.ask("hi")}}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tm.Expand(context.Background(), "", nil)
		if err == nil || !strings.Contains(err.Error(), "active Mako session") {
			t.Fatalf("expected clear error for nil host, got %v", err)
		}
	})

	t.Run("B11_infinite_js_loop_aborts_promptly", func(t *testing.T) {
		tm, err := prompttmpl.Parse("spin", []byte("```js\nfor(;;){}\n```"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err = tm.Expand(ctx, "", nil)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected infinite JS loop to fail")
		}
		if elapsed > 2*time.Second {
			t.Fatalf("infinite JS loop took %v to abort (expected < 2s)", elapsed)
		}
	})
}
