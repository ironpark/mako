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
			name: "rendered_markdown_table_preserved_with_prose",
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
				"┌──────┬───────┐",
				"│ Mode │ Speed │",
				"├──────┼───────┤",
				"│ Fast │ High  │",
				"└──────┴───────┘",
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
