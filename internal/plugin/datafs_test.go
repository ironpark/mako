package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestDatafsCRUDAndScopeIsolation(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("vault", map[string]string{
		ManifestName: `id = "vault"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["fs:data"]

[[contributes.block]]
id = "ui"
title = "Vault"
scope = "project"
`,
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("ui", {
      state: { summary: "" },
      async load(s, ctx) {
        await ctx.datafs.mkdir("concepts");
        await ctx.datafs.write("index.md", "# Index for " + ctx.scopeKey);
        await ctx.datafs.write("concepts/auth.md", "# Auth " + ctx.scopeKey);
        await ctx.datafs.move("concepts/auth.md", "concepts/security.md");
        const moved = await ctx.datafs.read("concepts/security.md");
        const missing = await ctx.datafs.read("concepts/auth.md");
        const st = await ctx.datafs.stat("concepts/security.md");
        const list = await ctx.datafs.list("", { recursive: true });
        const stats = await ctx.datafs.stats();
        const del = await ctx.datafs.delete("concepts", { recursive: true });
        const after = await ctx.datafs.list("", { recursive: true });
        s.summary = JSON.stringify({
          movedFound: moved.found,
          movedText: moved.text,
          missingFound: missing.found,
          statFound: st.found,
          statDir: st.dir,
          paths: (list.entries || []).map((e) => e.path + (e.dir ? "/" : "")),
          fileCountBeforeDelete: stats.fileCount,
          deleted: del.deleted,
          afterPaths: (after.entries || []).map((e) => e.path),
        });
      },
      render: (s, ui) => ui.text(s.summary),
    });
  },
};`,
	})
	r.state("vault", protocol.PluginWaiting)
	r.grant("vault")

	wA := r.watchScoped("plugin:vault/ui", "project:repo:github.com/ironpark/alpha")
	treeA := wA.until("alpha loaded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, `"movedFound":true`)
	})
	txtA := strings.Join(texts(treeA), "\n")
	for _, want := range []string{
		`"movedText":"# Auth project:repo:github.com/ironpark/alpha"`,
		`"missingFound":false`,
		`"statFound":true`,
		`"statDir":false`,
		`"concepts/"`,
		`"concepts/security.md"`,
		`"index.md"`,
		`"deleted":true`,
		`"afterPaths":["index.md"]`,
	} {
		if !strings.Contains(txtA, want) {
			t.Fatalf("scope A missing %s in %s", want, txtA)
		}
	}

	wB := r.watchScoped("plugin:vault/ui", "project:repo:github.com/ironpark/beta")
	treeB := wB.until("beta loaded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, `"movedText":"# Auth project:repo:github.com/ironpark/beta"`)
	})
	if strings.Contains(strings.Join(texts(treeB), "\n"), "alpha") {
		t.Fatalf("scope B leaked scope A content: %v", texts(treeB))
	}
}

func TestDatafsSandboxQuotaAndSymlinkRejection(t *testing.T) {
	base := t.TempDir()
	store := newDatafsStore(filepath.Join(base, "files"), 128) // tiny 128-byte quota

	// Path traversal / absolute / backslash / reserved tmp / root delete refused.
	for _, bad := range []string{"../escape.md", "/etc/passwd", `a\b.md`, "a:b.md", ".mako-tmp-x.tmp"} {
		if _, err := store.write("", bad, "x", nil, true); err == nil {
			t.Fatalf("write(%q) unexpectedly succeeded", bad)
		}
		if _, err := store.read("", bad, 0, 0); err == nil {
			t.Fatalf("read(%q) unexpectedly succeeded", bad)
		}
	}
	if _, err := store.remove("", "", true); err == nil {
		t.Fatal("delete scope root unexpectedly succeeded")
	}
	if _, err := store.remove("", ".", true); err == nil {
		t.Fatal("delete '.' unexpectedly succeeded")
	}

	// Quota enforced cleanly, overwrite within quota allowed.
	if _, err := store.write("", "a.md", strings.Repeat("a", 100), nil, true); err != nil {
		t.Fatalf("write within quota failed: %v", err)
	}
	if _, err := store.write("", "b.md", strings.Repeat("b", 50), nil, true); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("expected quota error, got %v", err)
	}
	if _, err := store.write("", "a.md", strings.Repeat("c", 120), nil, true); err != nil {
		t.Fatalf("overwrite within quota failed: %v", err)
	}

	// Moving a folder into itself is refused.
	if err := store.mkdir("", "dir/sub"); err != nil {
		t.Fatal(err)
	}
	if err := store.move("", "dir", "dir/sub/dir", true); err == nil {
		t.Fatal("moving folder inside itself unexpectedly succeeded")
	}

	// External symlink planted inside scope dir is hidden and refused on read/write/delete.
	outside := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(base, "files", "global", "link.txt")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("", "link.txt", 0, 0); err == nil {
		t.Fatal("reading symlink unexpectedly succeeded")
	}
	if _, err := store.write("", "link.txt", "overwrite", nil, false); err == nil {
		t.Fatal("writing symlink unexpectedly succeeded")
	}
	if _, err := store.remove("", "link.txt", false); err == nil {
		t.Fatal("deleting symlink unexpectedly succeeded")
	}
	listed, err := store.list("", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listed.GetEntries() {
		if e.GetName() == "link.txt" {
			t.Fatal("symlink leaked into ListData")
		}
	}
}
