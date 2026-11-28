package repotrust

import "testing"

// The command a hint prints pastes as it is: a path with a space or a
// quote in it is one word, quoted as the system's shell takes it.
func TestAllowCommandQuotesWhatTheShellWouldSplit(t *testing.T) {
	for _, tc := range []struct{ goos, node, path, want string }{
		{"darwin", "", "/src/app/.mako/x.toml", "mako repo-trust allow /src/app/.mako/x.toml"},
		{"linux", "12D3KooW", "/src/app/x.toml", "mako -node 12D3KooW repo-trust allow /src/app/x.toml"},
		{"darwin", "", "/Users/me/My Projects/app/x.toml", "mako repo-trust allow '/Users/me/My Projects/app/x.toml'"},
		{"linux", "", "/src/it's here/x.toml", `mako repo-trust allow '/src/it'\''s here/x.toml'`},
		{"linux", "", "/src/$(rm -rf ~)/x", "mako repo-trust allow '/src/$(rm -rf ~)/x'"},
		{"linux", "", `/src/a\b`, `mako repo-trust allow '/src/a\b'`},
		{"windows", "", `C:\src\app\x.toml`, `mako repo-trust allow C:\src\app\x.toml`},
		{"windows", "", `C:\My Projects\app\x.toml`, `mako repo-trust allow "C:\My Projects\app\x.toml"`},
		{"linux", "", "", "mako repo-trust allow ''"},
	} {
		if got := allowCommand(tc.goos, tc.node, tc.path); got != tc.want {
			t.Errorf("%s: %q = %s, want %s", tc.goos, tc.path, got, tc.want)
		}
	}
}
