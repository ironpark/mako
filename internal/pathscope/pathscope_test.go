package pathscope

import "testing"

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		loc, dir, repo string
		want           bool
	}{
		{"/w/web", "/w/web/src", "", true},
		{"/w/web", "/w/webapp", "", false},
		{"/home/me", "/home/me/src/mako", "/home/me/src/mako", false}, // a repository of its own
		{"/w/server", "/tmp/server-fix", "/w/server", true},           // its worktree
		{"/w/mono/web", "/w/mono/web/x", "/w/mono", true},             // a monorepo's folder
		{`C:\w\web`, `C:\w\web\src`, "", true},
		{"", "/w", "", false},
	} {
		if got := Covers(c.loc, c.dir, c.repo); got != c.want {
			t.Errorf("Covers(%q, %q, %q) = %v", c.loc, c.dir, c.repo, got)
		}
	}
}
