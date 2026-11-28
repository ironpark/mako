package policy

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The lines the review found past the rules: a $'...' that hides a second
// command from a reader that takes it for '...', and commands in a
// substitution in double quotes. bash runs both commands of each.
func TestShellRulesReadAsBashDoes(t *testing.T) {
	ansi := `git status $'\'' ; rm -rf ~ # '`
	allow := []Rule{{ID: "a", Enabled: true, Action: ActionAllow, Tool: "Bash", Pattern: "git status*"}}
	if d := EvaluateRules(allow, bash("claude", ansi)); d.Matched {
		t.Errorf("allow git status* allowed %q: %+v", ansi, d)
	}
	rm := []Rule{{ID: "rm", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "rm -rf*"}}
	if d := EvaluateRules(rm, bash("claude", ansi)); !d.Matched || d.Action != ActionDeny {
		t.Errorf("deny rm -rf* missed %q: %+v", ansi, d)
	}
	push := []Rule{{ID: "p", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "git push*"}}
	exact := []Rule{{ID: "x", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "git push"}}
	for _, c := range []struct {
		rules []Rule
		line  string
	}{
		{push, `echo "$(echo ok; git push)"`},
		{exact, `echo "$(git push)"`},
		{exact, "echo \"`git push`\""},
		{push, `echo "${x:-$(git push)}"`},
		{push, `x=$(git push) true`},
		{push, "cat <<EOF\n$(git push)\nEOF"},
		{exact, `$'git\x20push'`},
		{exact, `"git" push`},
		{exact, `g\it push`},
		{push, `eval "git push origin"`},
		{push, `bash -lc 'echo ok; git push'`},
		{push, `env GIT_TRACE=1 git push`},
		{push, `sudo -u me git push`},
		{push, `f() { git push; }; f`},
		{push, "if true; then git push; fi"},
		{push, "git push\necho \"unclosed"},
	} {
		if d := EvaluateRules(c.rules, bash("claude", c.line)); !d.Matched || d.Action != ActionDeny {
			t.Errorf("deny %s missed %q: %+v", c.rules[0].Pattern, c.line, d)
		}
		if d := EvaluateRules(allow, bash("claude", c.line)); d.Matched {
			t.Errorf("allow git status* allowed %q: %+v", c.line, d)
		}
	}
	for _, line := range []string{
		"git status # ; rm -rf ~",
		"GIT_PAGER='sh -c id' git status",
		"git status $HOME",
		"git status ${x}",
		"git status \"$x\"",
		"git status $\"x\"",
		"git status |& cat",
		"! git status",
		"ls *(e:'rm -rf ~':)",
		"cat =(rm -rf ~)",
		"bash -c 'git status $(rm -rf ~)'",
	} {
		if d := EvaluateRules(allow, bash("claude", line)); d.Matched {
			t.Errorf("allow git status* allowed %q: %+v", line, d)
		}
	}
	for _, line := range []string{"git status", "git status -s && git status --short", "git status 'a b' \"c d\"", "git status | git status -s"} {
		if d := EvaluateRules(allow, bash("claude", line)); !d.Matched {
			t.Errorf("allow git status* did not allow %q: %+v", line, d)
		}
	}
}

// What a shell runs of a line is among the commands shellCommands finds in
// it, and a line that runs more than its plain commands is not simple. Each
// line echoes markers; each marker a shell prints must come from a command
// found.
func TestShellCommandsFindWhatTheShellRuns(t *testing.T) {
	cases := []struct {
		line   string
		simple bool
	}{
		{`echo M1 $'\'' ; echo M2 # '`, false},
		{`echo "$(echo M1; echo M2)"`, false},
		{`echo "$(echo M1)"`, false},
		{"echo \"`echo M1`\"", false},
		{`echo M1 "${x:-$(echo M2)}"`, false},
		{`x=$(echo M1) echo M2`, false},
		{`eval "echo M1; echo M2"`, true},
		{`bash -c 'echo M1; echo M2'`, true},
		{`env FOO=1 echo M1`, true},
		{`cat <(echo M1)`, false},
		{`f() { echo M1; }; f`, false},
		{`if true; then echo M1; fi`, false},
		{"echo M1\necho M2", true},
		{`echo M1 && echo M2 || echo M3`, true},
		{`echo M1 | cat`, true},
		{`$'\x65cho' M1`, false},
		{`"echo" M1`, true},
		{`e\cho M1`, true},
		{`command echo M1`, true},
		{`echo M1 # ; echo M2`, false},
		{"cat <<EOF\n$(echo M1)\nEOF", false},
		{`(echo M1)`, false},
		{`{ echo M1; }`, false},
		{"echo M1\necho \"unclosed", false},
	}
	marker := regexp.MustCompile(`M[0-9]+`)
	ran := 0
	for _, sh := range []string{"/bin/bash", "/bin/zsh"} {
		if _, err := os.Stat(sh); err != nil {
			continue
		}
		ran++
		for _, c := range cases {
			cmd := exec.Command(sh, "-c", c.line)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"HOME=" + cmd.Dir, "PATH=/usr/bin:/bin"}
			out, _ := cmd.Output()
			cmds, simple := shellCommands(c.line)
			if simple != c.simple {
				t.Errorf("%q: simple = %v, want %v", c.line, simple, c.simple)
			}
			for _, m := range marker.FindAllString(string(out), -1) {
				if !slices.ContainsFunc(cmds, func(c string) bool { return c == "echo "+m || strings.HasPrefix(c, "echo "+m+" ") }) {
					t.Errorf("%s ran echo %s of %q, not among %q", sh, m, c.line, cmds)
				}
			}
			if !strings.Contains(c.line, "unclosed") && len(marker.FindAllString(string(out), -1)) == 0 {
				t.Errorf("%s ran nothing of %q: %q", sh, c.line, out)
			}
		}
	}
	if ran == 0 {
		t.Skip("no bash or zsh here")
	}
}
