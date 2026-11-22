package agy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractLiveExistingFixtures(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		file       string
		lastLogged string
		want       string
	}{
		{"screen_idle.txt", "", ""},
		{"screen_trust.txt", "", ""},
		{"screen_working.txt", "", ""},
		{"screen_permission.txt", "", ""},
		{"screen_done.txt", "", ""},
		{"screen_interrupted.txt", "", ""},
		{"screen_slash_menu.txt", "", ""},
		// In screen_running_command.txt, agy already rendered the reply after
		// `● Ran (date)` while the spinner/footer are still in working state.
		{"screen_running_command.txt", "", "Thu Nov 19 13:42:33 KST 2026"},
		// Once that reply is recorded in transcript_full.jsonl, it is suppressed.
		{"screen_running_command.txt", "Thu Nov 19 13:42:33 KST 2026", ""},
	} {
		t.Run(tc.file+"_logged="+tc.lastLogged, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got := a.ExtractLive(string(raw), tc.lastLogged)
			if got != tc.want {
				t.Fatalf("ExtractLive(%s, %q) = %q, want %q", tc.file, tc.lastLogged, got, tc.want)
			}
		})
	}
}

func TestExtractLiveStreamingCases(t *testing.T) {
	a := New()
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)

	splash := `
      ▄▀▀▄
     ▀▀▀▀▀▀
    ▀▀▀▀▀▀▀▀
   ▄▀▀    ▀▀▄
  ▄▀▀      ▀▀▄

  Antigravity CLI 1.5.14
  user@example.com (Google AI Pro)
  Gemini 4 Hydrogen (Medium)
  /home/user/project
`

	cases := []struct {
		name       string
		screen     string
		lastLogged string
		want       string
	}{
		{
			name: "working_with_streaming_response",
			screen: splash + "\n" + divider + "\n" +
				"> Explain what liveHub does.\n\n" +
				"  liveHub fans out streaming reply blocks to WatchChat subscribers.\n" +
				"⣾  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          accept-edits · Gemini 4 Hydrogen · medium\n",
			want: "liveHub fans out streaming reply blocks to WatchChat subscribers.",
		},
		{
			name: "working_with_spinner_only_and_wrapped_long_prompt",
			screen: splash + "\n" + divider + "\n" +
				"> Please inspect internal/daemon/live.go and explain how the lookOut goroutine\n" +
				"  tracks transcript file growth and notifies followers.\n" +
				"⣾  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          accept-edits · Gemini 4 Hydrogen · medium\n",
			want: "",
		},
		{
			name: "working_with_stacked_custom_statusline_below_footer",
			screen: divider + "\n" +
				"> Hello\n\n" +
				"  Hi there, streaming live!\n" +
				"⠋  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n" +
				"main · 12% ctx · 5h 98%\n",
			want: "Hi there, streaming live!",
		},
		{
			name: "idle_with_stacked_custom_statusline_must_not_emit",
			screen: divider + "\n" +
				"> Hello\n\n" +
				"  Hi there, already done.\n\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"? for shortcuts                        auto · Gemini 4 Hydrogen · medium\n" +
				"main · 12% ctx · 5h 98%\n",
			want: "",
		},
		{
			name: "permission_dialog_with_stacked_statusline_must_not_emit",
			screen: divider + "\n" +
				"> Run tests\n\n" +
				"  I will run the tests now.\n\n" +
				"● Ran (go test ./...) (ctrl+o to expand)\n\n" +
				"Command\n" + rule80 + "\n\n" +
				"Requesting permission for:\n   go test ./...\n\n" +
				"Run this command?\n> 1. Yes, run command\n  2. No, cancel\n\n" +
				"  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command\n" +
				"esc to cancel                          accept-edits · Gemini 4 Hydrogen · medium\n" +
				"main · 15% ctx\n",
			want: "",
		},
		{
			name: "question_dialog_must_not_emit",
			screen: divider + "\n" +
				"> Refactor auth\n\n" +
				"Which approach do you prefer?\n" +
				"> 1. Option A\n" +
				"  2. Option B\n" +
				"  ↑/↓ Navigate · enter Select · esc Skip\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			want: "",
		},
		{
			name: "multi_turn_emits_only_current_turn",
			screen: splash + "\n" + divider + "\n" +
				"> First question\n\n" +
				"  First answer that is already logged.\n\n" +
				divider + "\n" +
				"> Second question\n\n" +
				"  Second answer currently streaming...\n" +
				"⠙  Generating...\n" +
				rule80 + "\n> draft follow-up\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			lastLogged: "First answer that is already logged.",
			want:       "Second answer currently streaming...",
		},
		{
			name: "tool_output_and_box_followed_by_streaming_text",
			screen: divider + "\n" +
				"> Inspect live.go and update it\n\n" +
				"  Let me read live.go first.\n\n" +
				"● Read (internal/daemon/live.go) (ctrl+o to expand)\n" +
				"  ⎿  Read 120 lines (ctrl+o to expand)\n" +
				"     package daemon\n\n" +
				"● Edited (internal/daemon/live.go)\n" +
				"  ╭────────────────────────────────────────╮\n" +
				"  │ - old line                             │\n" +
				"  │ + new line                             │\n" +
				"  ╰────────────────────────────────────────╯\n\n" +
				"  I updated `live.go` to wire screen extraction.\n" +
				"  Next I will run the test suite.\n" +
				"⠹  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          accept-edits · Gemini 4 Hydrogen · medium\n",
			want: "I updated `live.go` to wire screen extraction.\nNext I will run the test suite.",
		},
		{
			name: "pre_tool_text_not_reemitted_while_waiting_after_tool",
			screen: divider + "\n" +
				"> Inspect live.go\n\n" +
				"  Let me read live.go first.\n\n" +
				"● Read (internal/daemon/live.go) (ctrl+o to expand)\n" +
				"  ⎿  Read 120 lines\n" +
				"⠸  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          accept-edits · Gemini 4 Hydrogen · medium\n",
			want: "",
		},
		{
			name: "multi_paragraph_lists_code_fence_and_blockquote",
			screen: divider + "\n" +
				"> Show a Go example with notes\n\n" +
				"  Here is an overview of the design:\n\n" +
				"  - Clean extraction in `agy`\n" +
				"  - Zero-duplicate clearing in `liveHub`\n\n" +
				"  > Note: code blocks keep markers like > and ● verbatim.\n\n" +
				"  ```go\n" +
				"  > not a prompt\n" +
				"  ● not a tool\n" +
				"  func main() {\n" +
				"      println(\"hello\")\n" +
				"  }\n" +
				"  ```\n" +
				"⠼  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			want: strings.Join([]string{
				"Here is an overview of the design:",
				"",
				"- Clean extraction in `agy`",
				"- Zero-duplicate clearing in `liveHub`",
				"",
				"> Note: code blocks keep markers like > and ● verbatim.",
				"",
				"```go",
				"> not a prompt",
				"● not a tool",
				"func main() {",
				"    println(\"hello\")",
				"}",
				"```",
			}, "\n"),
		},
		{
			name: "soft_wrapped_prose_rejoined_and_short_lines_preserved",
			screen: divider + "\n" +
				"> Explain wrapping\n\n" +
				"  The daemon polls the terminal screen buffer whenever a client watches the\n" +
				"  chat stream and extracts only the newly streamed assistant response text\n" +
				"  from the current turn.\n\n" +
				"  Status: OK\n" +
				"  Port: 8080\n" +
				"⠴  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			want: "The daemon polls the terminal screen buffer whenever a client watches the chat stream and extracts only the newly streamed assistant response text from the current turn.\n\nStatus: OK\nPort: 8080",
		},
		{
			name: "last_logged_suppresses_matching_markdown_rendered_text",
			screen: divider + "\n" +
				"> Check date\n\n" +
				"  The command date returned Thu Nov 19 13:42:33 KST 2026.\n" +
				"⠦  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			lastLogged: "The command `date` returned **Thu Nov 19 13:42:33 KST 2026**.",
			want:       "",
		},
		{
			name: "last_logged_suppresses_rendered_headings_and_bullets",
			screen: divider + "\n" +
				"> Summarize changes\n\n" +
				"  Summary\n\n" +
				"  • Fixed liveHub race\n" +
				"  • Added unit tests\n" +
				"⠦  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			lastLogged: "## Summary\n\n- Fixed `liveHub` race\n+ Added **unit tests**",
			want:       "",
		},
		{
			name: "rendered_markdown_table_converted_to_gfm_with_prose",
			screen: divider + "\n" +
				"> Compare options\n\n" +
				"  Here is a summary table:\n\n" +
				"  ┌──────┬───────┐\n" +
				"  │ Mode │ Speed │\n" +
				"  ├──────┼───────┤\n" +
				"  │ Fast │ High  │\n" +
				"  └──────┴───────┘\n" +
				"⠧  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			want: strings.Join([]string{
				"Here is a summary table:",
				"",
				"| Mode | Speed |",
				"| --- | --- |",
				"| Fast | High |",
			}, "\n"),
		},
		{
			name: "splash_only_with_working_footer_must_not_emit",
			screen: splash + "\n" +
				"⣾  Starting...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Hydrogen · medium\n",
			want: "",
		},
		{
			name: "swarm_bar_and_artifact_tip_suppressed_when_no_prose",
			screen: divider + "\n" +
				"> 지금 플러그인에서 쓸수 있는 스토리지는 어떤식이지?\n\n" +
				"● ReadFile(internal/plugin/storage.go)\n" +
				"  ⎿ Read 215 lines\n" +
				"  └ Tip: Press ctrl+r to review pending artifacts.\n" +
				rule80 + "\n" +
				strings.Repeat("─", 56) + "\n" +
				rule80 + "\n" +
				strings.Repeat("─", 56) + " ● Swarm(Plugin scope system (rev 42))\n" +
				"Done · 67 posts · alt+s board · alt+x dismiss\n" +
				"⠧  Thinking...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Neon · high\n",
			want: "",
		},
		{
			name: "streaming_prose_preserved_above_swarm_bar_and_tip",
			screen: divider + "\n" +
				"> 지금 플러그인에서 쓸수 있는 스토리지는 어떤식이지?\n\n" +
				"  현재 플러그인이 쓸 수 있는 스토리지는 4가지입니다.\n" +
				"  └ Tip: Press ctrl+r to review pending artifacts.\n" +
				rule80 + "\n" +
				strings.Repeat("─", 56) + " ● Swarm(Plugin scope system (rev 42))\n" +
				"Done · 67 posts · alt+s board · alt+x dismiss\n" +
				"⠧  Generating...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Neon · high\n",
			want: "현재 플러그인이 쓸 수 있는 스토리지는 4가지입니다.",
		},
		{
			name: "last_logged_suppresses_markdown_links_and_rules_and_tables",
			screen: divider + "\n" +
				"> 커밋해줘\n\n" +
				"  2. Plugin SQLite (ctx.db) 구현\n\n" +
				"  • go.mod에 modernc.org/sqlite 추가 완료\n\n" +
				"  ┌──────┬───────┐\n" +
				"  │ Mode │ Speed │\n" +
				"  ├──────┼───────┤\n" +
				"  │ Fast │ High  │\n" +
				"  └──────┴───────┘\n" +
				"⣾  Thinking...\n" +
				rule80 + "\n>\n" + rule80 + "\n" +
				"esc to cancel                          auto · Gemini 4 Neon · high\n",
			lastLogged: "### 2. Plugin SQLite (`ctx.db`) 구현\n\n- [go.mod](file:///Users/ironpark/Projects/Personal/open-source/mako/go.mod)에 `modernc.org/sqlite` 추가 완료\n\n---\n\n| Mode | Speed |\n| --- | --- |\n| Fast | High |",
			want:       "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := a.ExtractLive(tc.screen, tc.lastLogged)
			if got != tc.want {
				t.Fatalf("ExtractLive() =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestExtractBaseline(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		file string
		want string
	}{
		{"screen_idle.txt", ""},
		{"screen_trust.txt", ""},
		{"screen_permission.txt", ""},
		{"screen_slash_menu.txt", ""},
		{"screen_interrupted.txt", ""},
		{"screen_done.txt", "Thu Nov 19 13:42:33 KST 2026"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if got := a.ExtractBaseline(string(raw)); got != tc.want {
				t.Fatalf("ExtractBaseline(%s) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}

	// Stacked custom statusLine below idle "? for shortcuts" footer must also be stripped.
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)
	idleStacked := divider + "\n" +
		"> Hello\n\n" +
		"  Previous completed reply.\n\n" +
		rule80 + "\n> draft next message\n" + rule80 + "\n" +
		"? for shortcuts                        auto · Gemini 4 Neon · medium\n" +
		"main · 12% ctx · 5h 98%\n"
	if got := a.ExtractBaseline(idleStacked); got != "Previous completed reply." {
		t.Fatalf("ExtractBaseline(idleStacked) = %q, want %q", got, "Previous completed reply.")
	}
}

func TestExtractStatus(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		file string
		want string
	}{
		{"screen_idle.txt", ""},
		{"screen_trust.txt", ""},
		{"screen_permission.txt", ""},
		{"screen_done.txt", ""},
		{"screen_interrupted.txt", ""},
		{"screen_slash_menu.txt", ""},
		{"screen_working.txt", "Generating…"},
		{"screen_running_command.txt", "Running command…"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if got := a.ExtractStatus(string(raw)); got != tc.want {
				t.Fatalf("ExtractStatus(%s) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}

	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)
	reading := divider + "\n" +
		"> Check live.go\n\n" +
		"⢸  Reading file...\n" +
		rule80 + "\n>\n" + rule80 + "\n" +
		"esc to cancel                          auto · Gemini 4 Neon · high\n" +
		"main · 12% ctx · 5h 98%\n"
	if got := a.ExtractStatus(reading); got != "Reading file…" {
		t.Fatalf("ExtractStatus(reading) = %q, want %q", got, "Reading file…")
	}
}

func TestExtractSwarmAndAgentActivity(t *testing.T) {
	a := New()
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)

	// Idle coordinator at ">" with active Swarm and two background Agent rows
	// below the composer box (matching Screenshot 1).
	idleSwarm := divider + "\n" +
		"> 여기에 더해 플러그인에서 i18n 지원\n\n" +
		"● Used PostMessage (Posted #69 to board)\n\n" +
		"  1단계 완료 알림이 오면 바로 2단계 병렬 작업으로 넘어가겠습니다.\n\n" +
		rule80 + "\n> \n" + rule80 + "\n" +
		"● Swarm(Plugin UI rich components & OKF upg...)  coordinator: @u... · 69 posts · alt+s board · alt+x dismiss\n" +
		"● Agent(worker: Swarm Worker (ui-schema))  For split elements, the code now enforces exactly ... · 6m44s\n" +
		"● Agent(worker: Swarm Worker (monitor))  Working · 27h53m4s\n"

	wantActivity := strings.Join([]string{
		"Swarm(Plugin UI rich components & OKF upg...) · coordinator: @u... · 69 posts",
		"Agent(worker: Swarm Worker (ui-schema)) · For split elements, the code now enforces exactly ... · 6m44s",
		"Agent(worker: Swarm Worker (monitor)) · Working · 27h53m4s",
	}, "\n")

	if got := a.ExtractActivity(idleSwarm); got != wantActivity {
		t.Fatalf("ExtractActivity(idleSwarm) =\n%q\nwant:\n%q", got, wantActivity)
	}
	if got := a.ExtractStatus(idleSwarm); got != wantActivity {
		t.Fatalf("ExtractStatus(idleSwarm) =\n%q\nwant:\n%q", got, wantActivity)
	}
	if strings.Contains(wantActivity, "alt+s") || strings.Contains(wantActivity, "alt+x") {
		t.Fatalf("shortcut chrome leaked: %q", wantActivity)
	}
	// ExtractLive must not emit idle screen text or Swarm/Agent chrome.
	if got := a.ExtractLive(idleSwarm, ""); got != "" {
		t.Fatalf("ExtractLive(idleSwarm) = %q, want empty", got)
	}
	// ExtractBaseline strips Swarm/Agent rows and composer box cleanly.
	if got := a.ExtractBaseline(idleSwarm); got != "1단계 완료 알림이 오면 바로 2단계 병렬 작업으로 넘어가겠습니다." {
		t.Fatalf("ExtractBaseline(idleSwarm) = %q", got)
	}

	// Working coordinator with BOTH braille spinner and active Swarm/Agent rows.
	workingSwarm := divider + "\n" +
		"> Check workers\n\n" +
		"  Streaming coordinator prose.\n" +
		"⠧  Thinking...\n" +
		rule80 + "\n>\n" + rule80 + "\n" +
		"esc to cancel                          auto · Gemini 4 Neon · high\n" +
		"● Swarm(Plugin UI)  coordinator: active · 12 posts · alt+s board · alt+x dismiss\n" +
		"● Agent(worker: Swarm Worker (ui-schema))  Working · 1m05s\n"

	wantWorkingAct := "Swarm(Plugin UI) · coordinator: active · 12 posts\nAgent(worker: Swarm Worker (ui-schema)) · Working · 1m05s"
	if got := a.ExtractActivity(workingSwarm); got != wantWorkingAct {
		t.Fatalf("ExtractActivity(workingSwarm) = %q, want %q", got, wantWorkingAct)
	}
	if got := a.ExtractStatus(workingSwarm); got != "Thinking…\n"+wantWorkingAct {
		t.Fatalf("ExtractStatus(workingSwarm) = %q, want %q", got, "Thinking…\n"+wantWorkingAct)
	}
	if got := a.ExtractLive(workingSwarm, ""); got != "Streaming coordinator prose." {
		t.Fatalf("ExtractLive(workingSwarm) = %q, want prose only", got)
	}

	// Finished Swarm ("Done · 67 posts · alt+s board · alt+x dismiss") must not
	// report active work when idle or add to working spinner status.
	doneSwarm := divider + "\n" +
		"> Summary\n\n" +
		"  All done.\n" +
		strings.Repeat("─", 56) + " ● Swarm(Plugin scope system (rev 42))\n" +
		"Done · 67 posts · alt+s board · alt+x dismiss\n" +
		rule80 + "\n>\n" + rule80 + "\n" +
		"? for shortcuts                        auto · Gemini 4 Neon · high\n"
	if got := a.ExtractActivity(doneSwarm); got != "" {
		t.Fatalf("ExtractActivity(doneSwarm) = %q, want empty", got)
	}
	if got := a.ExtractStatus(doneSwarm); got != "" {
		t.Fatalf("ExtractStatus(doneSwarm) = %q, want empty", got)
	}

	// Historical "● Agent(...)" in the conversation body above prose must NOT
	// be mistaken for active bottom chrome.
	bodyAgent := divider + "\n" +
		"> Ask helper\n\n" +
		"● Agent(research: Explorer)\n" +
		"  ⎿ Done\n\n" +
		"  Final answer from assistant.\n\n" +
		rule80 + "\n>\n" + rule80 + "\n" +
		"? for shortcuts                        auto · Gemini 4 Neon · high\n"
	if got := a.ExtractActivity(bodyAgent); got != "" {
		t.Fatalf("ExtractActivity(bodyAgent) = %q, want empty", got)
	}

	// Permission dialog suppresses Swarm/Agent activity.
	permWithSwarm := divider + "\n" +
		"> Run\n\n" +
		"Requesting permission for:\n   go test ./...\n> 1. Yes\n  2. No\n" +
		"  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command\n" +
		"● Swarm(Plugin UI)  coordinator: active · 12 posts · alt+s board · alt+x dismiss\n"
	if got := a.ExtractActivity(permWithSwarm); got != "" {
		t.Fatalf("ExtractActivity(permWithSwarm) = %q, want empty", got)
	}
}

func TestExtractLiveNestedUnicodeBulletsTokenJoinsAndHTMLPlaceholders(t *testing.T) {
	a := New()
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)

	// Reproduces agy's rendered PTY screen for nested bullet lists containing
	// Korean text, hard-wrapped file paths, dotted filenames, CLI flags, routes,
	// and backtick-stripped angle-bracket placeholders (<hash>, <id>, <task-notification>).
	screen := divider + "\n" +
		"> docs 작성해줘\n\n" +
		"  작성된 문서\n\n" +
		"  • **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/agy/README.m\n" +
		"    d** — Antigravity CLI (agy) 통합 상세\n" +
		"    • mako-bridge 사이드카(sidecar.json + config.json 자동 등록, agent-cli.\n" +
		"      sock 스트림) → agentapi send-message → PTY 3단계 메시지 전달\n" +
		"    • 전역 hooks.json(mako-<hash>) 병합과 SubAgentHooks.IsSubAgent 3중 방어\n" +
		"    • ScreenLive PTY 추출, brain/<id> 트랜스크립트 스캔, mcp_config.\n" +
		"      json 환경변수 자격증명 분리\n" +
		"  • **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/codex/README\n" +
		"    .md** — Codex CLI (codex) 통합 상세\n" +
		"    • codex app-\n" +
		"      server + codex --remote 브리지, mako-msg /\n" +
		"      mako-plugin CLI 지원\n" +
		"  • **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/claude/READM\n" +
		"    E.md** — Claude Code (claude) 통합 상세\n" +
		"    • 내장 mako-live Mod(embed.FS 추출, --\n" +
		"      plugin-dir, MAKO_LIVE_SOCKET, /\n" +
		"      live 60ms 배치 스트리밍)\n" +
		"    ◦ BackgroundTasks(Bash background / <task-notification>)\n" +
		"  • **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/other/README\n" +
		"    .md** — pi, omp, omo, opencode 통합 상세\n" +
		"    • pi.\n" +
		"      registerMcpServer stdio MCP 확장\n" +
		"⣾  Generating...\n" +
		rule80 + "\n>\n" + rule80 + "\n" +
		"esc to cancel                          auto · Gemini 4 Neon · high\n"

	got := a.ExtractLive(screen, "")
	want := strings.Join([]string{
		"작성된 문서",
		"",
		"- **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/agy/README.md** — Antigravity CLI (agy) 통합 상세",
		"  - mako-bridge 사이드카(sidecar.json + config.json 자동 등록, agent-cli.sock 스트림) → agentapi send-message → PTY 3단계 메시지 전달",
		"  - 전역 hooks.json(mako-\\<hash>) 병합과 SubAgentHooks.IsSubAgent 3중 방어",
		"  - ScreenLive PTY 추출, brain/\\<id> 트랜스크립트 스캔, mcp_config.json 환경변수 자격증명 분리",
		"- **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/codex/README.md** — Codex CLI (codex) 통합 상세",
		"  - codex app-server + codex --remote 브리지, mako-msg / mako-plugin CLI 지원",
		"- **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/claude/README.md** — Claude Code (claude) 통합 상세",
		"  - 내장 mako-live Mod(embed.FS 추출, --plugin-dir, MAKO_LIVE_SOCKET, /live 60ms 배치 스트리밍)",
		"  - BackgroundTasks(Bash background / \\<task-notification>)",
		"- **/Users/ironpark/Projects/Personal/open-source/mako/docs/agents/other/README.md** — pi, omp, omo, opencode 통합 상세",
		"  - pi.registerMcpServer stdio MCP 확장",
	}, "\n")
	if got != want {
		t.Fatalf("ExtractLive() mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}

	// Once transcript_full.jsonl records the completed markdown (with file://
	// links and backtick-wrapped placeholders), matchesLogged must suppress it.
	logged := strings.Join([]string{
		"### 작성된 문서",
		"",
		"- **[docs/agents/agy/README.md](file:///Users/ironpark/Projects/Personal/open-source/mako/docs/agents/agy/README.md)** — Antigravity CLI (`agy`) 통합 상세",
		"  - `mako-bridge` 사이드카(`sidecar.json` + `config.json` 자동 등록, `agent-cli.sock` 스트림) → `agentapi send-message` → PTY 3단계 메시지 전달",
		"  - 전역 `hooks.json`(`mako-<hash>`) 병합과 `SubAgentHooks.IsSubAgent` 3중 방어",
		"  - `ScreenLive` PTY 추출, `brain/<id>` 트랜스크립트 스캔, `mcp_config.json` 환경변수 자격증명 분리",
		"- **[docs/agents/codex/README.md](file:///Users/ironpark/Projects/Personal/open-source/mako/docs/agents/codex/README.md)** — Codex CLI (`codex`) 통합 상세",
		"  - `codex app-server` + `codex --remote` 브리지, `mako-msg` / `mako-plugin` CLI 지원",
		"- **[docs/agents/claude/README.md](file:///Users/ironpark/Projects/Personal/open-source/mako/docs/agents/claude/README.md)** — Claude Code (`claude`) 통합 상세",
		"  - 내장 `mako-live` Mod(`embed.FS` 추출, `--plugin-dir`, `MAKO_LIVE_SOCKET`, `/live` 60ms 배치 스트리밍)",
		"  - `BackgroundTasks`(`Bash` background / `<task-notification>`)",
		"- **[docs/agents/other/README.md](file:///Users/ironpark/Projects/Personal/open-source/mako/docs/agents/other/README.md)** — `pi`, `omp`, `omo`, `opencode` 통합 상세",
		"  - `pi.registerMcpServer` stdio MCP 확장",
	}, "\n")
	if suppressed := a.ExtractLive(screen, logged); suppressed != "" {
		t.Fatalf("ExtractLive(screen, logged) = %q, want empty (suppressed)", suppressed)
	}
}

func TestExtractLiveKoreanWideWrapAndCodeFencePreservation(t *testing.T) {
	a := New()
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)

	// Korean characters are 2 display columns wide: 35 Korean chars = 70 cols on an
	// 80-col screen, which must rejoin across wrapped lines while preserving
	// sentence-ending spaces and untouched fenced code blocks.
	screen := divider + "\n" +
		"> 설명해줘\n\n" +
		"  공통 인터페이스 설계 문서와 에이전트별 통합 문서를 실제 드라이버 코드와 데몬\n" +
		"  파이프라인을 기준으로 정리했습니다. 다음 단계로 검증을 진행합니다.\n\n" +
		"  ```html\n" +
		"  <task-notification>keep verbatim</task-notification>\n" +
		"  ```\n" +
		"  • Indented code `<id>` stays intact, bare <hash> is escaped.\n" +
		"⠙  Generating...\n" +
		rule80 + "\n>\n" + rule80 + "\n" +
		"esc to cancel                          auto · Gemini 4 Neon · high\n"

	got := a.ExtractLive(screen, "")
	want := strings.Join([]string{
		"공통 인터페이스 설계 문서와 에이전트별 통합 문서를 실제 드라이버 코드와 데몬 파이프라인을 기준으로 정리했습니다. 다음 단계로 검증을 진행합니다.",
		"",
		"```html",
		"<task-notification>keep verbatim</task-notification>",
		"```",
		"- Indented code `<id>` stays intact, bare \\<hash> is escaped.",
	}, "\n")
	if got != want {
		t.Fatalf("ExtractLive() =\n%q\nwant:\n%q", got, want)
	}
}

func TestJoinSeparatorKeepsNormalSentenceSpaces(t *testing.T) {
	if sep := joinSeparator("  The command finished.", "  Next step runs tests.", 78); sep != " " {
		t.Fatalf("sentence break sep = %q, want space", sep)
	}
	if sep := joinSeparator("  설명을 완료했습니다.", "  다음 단계로 이동합니다.", 78); sep != " " {
		t.Fatalf("korean sentence break sep = %q, want space", sep)
	}
	if sep := joinSeparator("  Check docs/agents/README.md", "  explains the design.", 78); sep != " " {
		t.Fatalf("complete path followed by word sep = %q, want space", sep)
	}
	if sep := joinSeparator("  Use mako-msg /", "  mako-plugin CLI", 78); sep != " " {
		t.Fatalf("binary slash separator sep = %q, want space", sep)
	}
	if sep := joinSeparator("  Check SubAgentHooks.", "  IsSubAgent(parent, child)", 78); sep != "" {
		t.Fatalf("qualified CamelCase symbol sep = %q, want empty", sep)
	}
}

func TestExtractLiveBoxTableConversions(t *testing.T) {
	a := New()
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)

	t.Run("multiline_cells_with_row_dividers_and_placeholders", func(t *testing.T) {
		screen := divider + "\n" +
			"> 표로 정리해줘\n\n" +
			"  비교 요약:\n" +
			"  ┌─────────────┬────────────────────────────┐\n" +
			"  │ 항목        │ 설명                       │\n" +
			"  ├─────────────┼────────────────────────────┤\n" +
			"  │ hooks.json  │ mako-<hash> 키로           │\n" +
			"  │             │ agent-cli.                 │\n" +
			"  │             │ sock에 연결                │\n" +
			"  ├─────────────┼────────────────────────────┤\n" +
			"  │ ScreenLive  │ brain/<id> 로그와          │\n" +
			"  │             │ 비교 (a | b)               │\n" +
			"  └─────────────┴────────────────────────────┘\n" +
			"  후속 설명 문장입니다.\n" +
			"⣾  Generating...\n" +
			rule80 + "\n>\n" + rule80 + "\n" +
			"esc to cancel                          auto · Gemini 4 Neon · high\n"

		got := a.ExtractLive(screen, "")
		want := strings.Join([]string{
			"비교 요약:",
			"",
			"| 항목 | 설명 |",
			"| --- | --- |",
			"| hooks.json | mako-\\<hash> 키로 agent-cli.sock에 연결 |",
			"| ScreenLive | brain/\\<id> 로그와 비교 (a \\| b) |",
			"",
			"후속 설명 문장입니다.",
		}, "\n")
		if got != want {
			t.Fatalf("ExtractLive() =\n%s\n\nwant:\n%s", got, want)
		}

		// Completed markdown with <br> in cells and backticks must suppress cleanly.
		logged := "비교 요약:\n\n| 항목 | 설명 |\n| --- | --- |\n| `hooks.json` | `mako-<hash>` 키로<br>`agent-cli.sock`에 연결 |\n| `ScreenLive` | `brain/<id>` 로그와 비교 (a | b) |\n\n후속 설명 문장입니다."
		if suppressed := a.ExtractLive(screen, logged); suppressed != "" {
			t.Fatalf("expected logged table with <br> to suppress, got %q", suppressed)
		}
	})

	t.Run("compact_table_without_body_dividers_and_rounded_streaming_table", func(t *testing.T) {
		screen := divider + "\n" +
			"> Compare\n\n" +
			"  ╭──────┬─────────────╮\n" +
			"  │ Key  │ Description │\n" +
			"  ├──────┼─────────────┤\n" +
			"  │ Fast │ Low latency │\n" +
			"  │      │ mode        │\n" +
			"  │ Safe │ Full checks │\n" +
			"⠙  Generating...\n" +
			rule80 + "\n>\n" + rule80 + "\n" +
			"esc to cancel                          auto · Gemini 4 Neon · high\n"

		got := a.ExtractLive(screen, "")
		want := strings.Join([]string{
			"| Key | Description |",
			"| --- | --- |",
			"| Fast | Low latency mode |",
			"| Safe | Full checks |",
		}, "\n")
		if got != want {
			t.Fatalf("ExtractLive(streaming table) =\n%s\n\nwant:\n%s", got, want)
		}
	})
}

func TestExtractTurn(t *testing.T) {
	a := New()
	rule80 := strings.Repeat("─", 80)
	divider := strings.Repeat("─", 60)

	screen := divider + "\n" +
		"> First prompt\n\n" +
		"  First reply.\n\n" +
		divider + "\n" +
		"> 두 번째 질문\n\n" +
		"  두 번째 응답.\n\n" +
		rule80 + "\n> draft text\n" + rule80 + "\n" +
		"? for shortcuts                        auto · Gemini 4 Neon · high\n"

	turns, prompt := a.ExtractTurn(screen)
	if turns != 2 || prompt != "두 번째 질문" {
		t.Fatalf("ExtractTurn() = (%d, %q), want (2, %q)", turns, prompt, "두 번째 질문")
	}
}
