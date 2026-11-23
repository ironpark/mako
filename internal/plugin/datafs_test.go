package plugin

import (
	"bytes"
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

func TestDatafsBinaryWriteRoundTripAndTypeErrors(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("binfs", map[string]string{
		ManifestName: manifest("binfs", []string{"fs:data"}, ""),
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("ui", {
      state: { out: "" },
      async load(s, ctx) {
        const bytes = new Uint8Array([0x00, 0x01, 0xfe, 0xff, 0x80]);
        await ctx.datafs.write("u8.bin", { data: bytes });
        await ctx.datafs.write("ab.bin", { data: bytes.buffer });
        await ctx.datafs.write("sub.bin", { data: bytes.subarray(1, 4) });
        await ctx.datafs.write("direct.bin", bytes);
        await ctx.datafs.write("b64.bin", { data: "AAEC/w==" });

        const r1 = await ctx.datafs.read("u8.bin");
        const r2 = await ctx.datafs.read("ab.bin");
        const r3 = await ctx.datafs.read("sub.bin");
        const r4 = await ctx.datafs.read("direct.bin");
        const r5 = await ctx.datafs.read("b64.bin");

        const errs = [];
        for (const bad of [123, [1, 2, 3], null]) {
          try {
            await ctx.datafs.write("bad.bin", { data: bad });
            errs.push("unexpected-ok");
          } catch (e) {
            errs.push(e.name);
          }
        }
        s.out = JSON.stringify({
          b1: r1.binary, s1: r1.size,
          b2: r2.binary, s2: r2.size,
          b3: r3.binary, s3: r3.size,
          b4: r4.binary, s4: r4.size,
          b5: r5.binary, s5: r5.size,
          errs,
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`,
	})
	r.state("binfs", protocol.PluginWaiting)
	r.grant("binfs")

	tree := r.watch("plugin:binfs/ui").until("binary roundtrip", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, `"s1":5`)
	})
	txt := strings.Join(texts(tree), "\n")
	for _, want := range []string{
		`"b1":true,"s1":5`,
		`"b2":true,"s2":5`,
		`"b3":true,"s3":3`,
		`"b4":true,"s4":5`,
		`"b5":true,"s5":4`,
		`"errs":["TypeError","TypeError","TypeError"]`,
	} {
		if !strings.Contains(txt, want) {
			t.Fatalf("missing %s in %s", want, txt)
		}
	}

	scopeDir := filepath.Join(r.data, "binfs", "files", "global")
	wantFull := []byte{0x00, 0x01, 0xfe, 0xff, 0x80}
	for _, name := range []string{"u8.bin", "ab.bin", "direct.bin"} {
		got, err := os.ReadFile(filepath.Join(scopeDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, wantFull) {
			t.Fatalf("%s bytes = %x, want %x", name, got, wantFull)
		}
	}
	subGot, err := os.ReadFile(filepath.Join(scopeDir, "sub.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(subGot, []byte{0x01, 0xfe, 0xff}) {
		t.Fatalf("sub.bin bytes = %x, want 01feff", subGot)
	}
	b64Got, err := os.ReadFile(filepath.Join(scopeDir, "b64.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b64Got, []byte{0x00, 0x01, 0x02, 0xff}) {
		t.Fatalf("b64.bin bytes = %x, want 000102ff", b64Got)
	}
}

func TestDatafsIncrementalQuotaAccuracy(t *testing.T) {
	base := t.TempDir()
	store := newDatafsStore(filepath.Join(base, "files"), 1<<20)

	assertExact := func(step string) {
		t.Helper()
		st, err := store.stats("")
		if err != nil {
			t.Fatalf("%s: stats: %v", step, err)
		}
		store.mu.Lock()
		wantBytes, wantFiles := store.walkUsageLocked()
		store.mu.Unlock()
		if int64(st.GetUsedBytes()) != wantBytes || int(st.GetFileCount()) != wantFiles {
			t.Fatalf("%s: cached=(%d,%d), walk=(%d,%d)", step, st.GetUsedBytes(), st.GetFileCount(), wantBytes, wantFiles)
		}
	}

	// Perform 60+ operations across multiple scopes without triggering extra full walks.
	for i := range 20 {
		name := filepath.Join("dir", string(rune('a'+i))+".txt")
		if _, err := store.write("", name, strings.Repeat("x", 10+i), nil, true); err != nil {
			t.Fatal(err)
		}
		if _, err := store.write("project:alpha", name, strings.Repeat("y", 5+i), nil, true); err != nil {
			t.Fatal(err)
		}
	}
	// Grow and shrink existing file.
	if _, err := store.write("", "dir/a.txt", strings.Repeat("z", 100), nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write("", "dir/a.txt", "hi", nil, true); err != nil {
		t.Fatal(err)
	}
	// Rename and overwrite existing regular file.
	if err := store.move("", "dir/b.txt", "dir/c.txt", true); err != nil {
		t.Fatal(err)
	}
	if err := store.move("", "dir/d.txt", "dir/renamed.txt", true); err != nil {
		t.Fatal(err)
	}
	// Same-path move and case-only rename must preserve exact quota counts.
	if err := store.move("", "dir/renamed.txt", "dir/renamed.txt", true); err != nil {
		t.Fatal(err)
	}
	if err := store.move("", "dir/renamed.txt", "dir/Renamed.txt", true); err != nil {
		t.Fatal(err)
	}
	// Remove single file, empty dir, and recursive dir.
	if ok, err := store.remove("", "dir/a.txt", false); err != nil || !ok {
		t.Fatalf("remove file: %v %v", ok, err)
	}
	if err := store.mkdir("", "empty"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.remove("", "empty", false); err != nil || !ok {
		t.Fatalf("remove empty dir: %v %v", ok, err)
	}
	if ok, err := store.remove("project:alpha", "dir", true); err != nil || !ok {
		t.Fatalf("remove recursive dir: %v %v", ok, err)
	}

	// Before assertExact calls walkUsageLocked, verify only 1 full WalkDir occurred!
	if got := store.walkCount.Load(); got != 1 {
		t.Fatalf("walkCount = %d across 50+ ops; want 1", got)
	}
	assertExact("after mutations")

	// Reset clears cached state cleanly.
	if err := os.RemoveAll(store.base); err != nil {
		t.Fatal(err)
	}
	store.reset()
	st, err := store.stats("")
	if err != nil {
		t.Fatal(err)
	}
	if st.GetUsedBytes() != 0 || st.GetFileCount() != 0 {
		t.Fatalf("after reset stats = (%d,%d), want (0,0)", st.GetUsedBytes(), st.GetFileCount())
	}
}
