package detect

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Dialogs as Claude Code and Codex draw them are read as the options they
// offer, the one the cursor is on, and what they ask.
func TestParseChoice(t *testing.T) {
	for _, c := range []struct {
		file     string
		options  []string
		selected int
		text     string // its last line
	}{
		{"claude_trust", []string{"No, exit", "Yes, I trust this folder"}, 0, "Security guide"},
		{"claude_edit_perm", []string{"Yes", "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", "No"}, 0, "Do you want to make this edit to a.txt?"},
		{"claude_bash_perm", []string{"Yes", "Yes, and don’t ask again for: rtk wc *", "Yes, and switch to auto mode · auto mode handles these prompts for you", "No"}, 0, "Do you want to proceed?"},
		{"claude_plan", []string{"Yes, and use auto mode", "Yes, manually approve edits", "Tell Claude what to change"}, 0, "Claude has written up a plan and is ready to execute. Would you like to proceed?"},
		{"codex_trust", []string{"Trust and continue", "Back to Agent Command Center"}, 0, ""},
		{"codex_exec_perm", []string{"Yes, proceed (y)", "Yes, and don't ask again for commands that start with `touch b.txt` (p)", "No, and tell Codex what to do differently (esc)"}, 0, "$ touch b.txt"},
	} {
		screen, err := os.ReadFile("testdata/choice_" + c.file + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ParseChoice(string(screen), 140)
		if !ok {
			t.Errorf("%s: no choice read", c.file)
			continue
		}
		if !reflect.DeepEqual(got.Options, c.options) || got.Selected != c.selected {
			t.Errorf("%s: options %q, selected %d", c.file, got.Options, got.Selected)
		}
		lines := strings.Split(got.Text, "\n")
		if c.text != "" && lines[len(lines)-1] != c.text {
			t.Errorf("%s: text %q", c.file, got.Text)
		}
	}
}

// The cursor moved down, a list of checkboxes, an agent at its prompt.
func TestParseChoiceOthers(t *testing.T) {
	moved := " Do you want to proceed?\n   1. Yes\n ❯ 2. No\n Esc to cancel"
	if c, ok := ParseChoice(moved, 80); !ok || c.Selected != 1 || c.Text != "Do you want to proceed?" {
		t.Errorf("moved: %+v %v", c, ok)
	}
	boxes := " Which fruits?\n ❯ 1. [ ] Apple\n   2. [ ] Pear\n Enter to select"
	if c, ok := ParseChoice(boxes, 80); ok {
		t.Errorf("checkboxes read as %+v", c)
	}
	prompt := "⏺ Done.\n─────\n❯ \n─────\n  ? for shortcuts"
	if c, ok := ParseChoice(prompt, 80); ok {
		t.Errorf("prompt read as %+v", c)
	}
	typed := "─────\n❯ fix the bug\n─────"
	if c, ok := ParseChoice(typed, 80); ok {
		t.Errorf("a prompt typed read as %+v", c)
	}
}

func TestParseChoiceWrappedColumnZeroKoreanOptions(t *testing.T) {
	rules := MustCompile(nil).WithCursors(">")
	screen := strings.Join([]string{
		"Question",
		"─────────────────────────────────────────────────────────────────────────────────────────────",
		"",
		"Question 1/4: Docker Compose 태스크의 이름과 기본 모드를 어떻게 할까요?",
		"",
		"> 1. (Recommended) docker:* 네임스페이스, 기본은 Tailscale 오버레이 포함 (LOCAL=1이면 로컬",
		"포트)",
		"  2. stack:* 네임스페이스, 기본은 Tailscale 오버레이 포함 (LOCAL=1이면 로컬 포트)",
		"  3. docker:* 네임스페이스, 기본은 로컬 compose.yaml만 (TAILSCALE=1이면 오버레이 포함)",
		"  4. Write-in...",
		"",
		"  ↑/↓ Navigate · → Next · enter Select · esc Skip · ctrl+r Review",
		"esc to cancel                                                auto · Gemini 4 Neon · xhigh",
	}, "\n")

	c, ok := rules.ParseChoice(screen, 95)
	if !ok {
		t.Fatal("expected wrapped Korean choice dialog to parse")
	}
	want := []string{
		"(Recommended) docker:* 네임스페이스, 기본은 Tailscale 오버레이 포함 (LOCAL=1이면 로컬 포트)",
		"stack:* 네임스페이스, 기본은 Tailscale 오버레이 포함 (LOCAL=1이면 로컬 포트)",
		"docker:* 네임스페이스, 기본은 로컬 compose.yaml만 (TAILSCALE=1이면 오버레이 포함)",
		"Write-in...",
	}
	if !reflect.DeepEqual(c.Options, want) || c.Selected != 0 {
		t.Fatalf("got options=%q selected=%d, want=%q selected=0", c.Options, c.Selected, want)
	}

	// Trailing option wrapped onto column 0 at full display width must also join
	// without swallowing the footer below.
	lastWrapped := strings.Join([]string{
		"Question 4/4: 이름은 어떻게 처리할까요?",
		"",
		"> 1. (Recommended) 하위 호환 alias(ci:fetch:packs, server:release:dev, release)를",
		"숨김(internal/alias)으로 잠시 유지",
		"  2. 옛 이름은 깔끔하게 제거하고 새 이름만 남김",
		"",
		"                          enter Submit All",
	}, "\n")
	c2, ok := rules.ParseChoice(lastWrapped, 101)
	if !ok || len(c2.Options) != 2 {
		t.Fatalf("lastWrapped: got %+v %v, want 2 options", c2, ok)
	}
	if c2.Options[0] != "(Recommended) 하위 호환 alias(ci:fetch:packs, server:release:dev, release)를 숨김(internal/alias)으로 잠시 유지" {
		t.Fatalf("wrapped option 1 = %q", c2.Options[0])
	}
}
