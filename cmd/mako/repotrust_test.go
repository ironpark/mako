package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// fakeTrust answers repo-trust's requests as a daemon would, keeping what
// it was asked.
type fakeTrust struct {
	asked []*protocol.RepoTrust
	file  *protocol.TrustedFile
}

func (f *fakeTrust) ask(_ context.Context, r *protocol.RepoTrust) (*protocol.RepoTrustInfo, error) {
	f.asked = append(f.asked, r)
	return &protocol.RepoTrustInfo{Files: []*protocol.TrustedFile{f.file}}, nil
}

// allow shows the file whole, its control characters made visible, and
// trusts it, by the hash shown, only when the user says yes.
func TestRepoTrustAllowShowsTheFileAndAsks(t *testing.T) {
	file := &protocol.TrustedFile{Root: "/repo", Path: ".mako/worktree.toml", Sha256: "abc123",
		Content: []byte("setup_cmd = \"make\"\x1b[2K\rsetup_cmd = \"evil\"\n")}
	for _, tc := range []struct {
		answer string
		trust  bool
	}{{"y\n", true}, {"yes\n", true}, {"\n", false}, {"no\n", false}, {"", false}} {
		f := &fakeTrust{file: file}
		var out bytes.Buffer
		if err := repoTrust(t.Context(), f.ask, "node1", []string{"allow", "/repo/.mako/worktree.toml"}, strings.NewReader(tc.answer), &out); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		if strings.ContainsAny(text, "\x1b\r") || !strings.Contains(text, `\x1b[2K\x0dsetup_cmd = "evil"`) {
			t.Fatalf("control characters printed as they are:\n%q", text)
		}
		if !strings.Contains(text, "abc123") {
			t.Fatalf("hash not shown:\n%s", text)
		}
		last := f.asked[len(f.asked)-1]
		if tc.trust != (last.GetOp() == protocol.RepoTrustAllow) {
			t.Fatalf("answer %q: asked %v", tc.answer, f.asked)
		}
		if tc.trust && (last.GetSha256() != "abc123" || last.GetPath() != "/repo/.mako/worktree.toml" || last.GetNodeId() != "node1") {
			t.Fatalf("trusted as %+v", last)
		}
	}
}

// A relative path is taken from the folder mako runs in, and a file
// already trusted is not asked about again.
func TestRepoTrustPathsAndTrustedFiles(t *testing.T) {
	f := &fakeTrust{file: &protocol.TrustedFile{Root: "/repo", Path: "f", Sha256: "h", Trusted: true}}
	var out bytes.Buffer
	if err := repoTrust(t.Context(), f.ask, "", []string{"allow", "f"}, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 1 || !filepath.IsAbs(f.asked[0].GetPath()) {
		t.Fatalf("asked %v", f.asked)
	}
	if !strings.Contains(out.String(), "already trusted") {
		t.Fatalf("out = %s", out.String())
	}
	if err := repoTrust(t.Context(), f.ask, "", []string{"revoke"}, nil, &out); err == nil {
		t.Fatal("revoke without a path")
	}
	if err := repoTrust(t.Context(), f.ask, "", []string{"bogus"}, nil, &out); err == nil {
		t.Fatal("an unknown command")
	}
	f.asked = nil
	if err := repoTrust(t.Context(), f.ask, "", nil, nil, &out); err != nil || f.asked[0].GetOp() != protocol.RepoTrustList {
		t.Fatalf("no command lists: %v, %v", f.asked, err)
	}
}
