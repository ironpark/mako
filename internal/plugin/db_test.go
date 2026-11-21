package plugin

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

func TestDBSQLSandboxGuardrails(t *testing.T) {
	// Read-only Query must reject DML/DDL, multi-statement, PRAGMA, ATTACH.
	badQueries := []string{
		"",
		"INSERT INTO t VALUES (1)",
		"DELETE FROM t",
		"DROP TABLE t",
		"PRAGMA user_version",
		"ATTACH DATABASE '/tmp/evil.db' AS evil",
		"SELECT 1; DROP TABLE t",
		"WITH x AS (SELECT 1) DELETE FROM t",
		"SELECT load_extension('evil.so')",
		"SELECT 1 /* unterminated",
	}
	for _, sql := range badQueries {
		if err := checkSQL(sql, true); err == nil {
			t.Errorf("checkSQL(%q, readOnly=true) unexpectedly succeeded", sql)
		}
	}

	// Exec/Batch must reject ATTACH, PRAGMA, VACUUM, transactions, multi-stmt, and internal _mako_sync mutation.
	badExecs := []string{
		"",
		"ATTACH DATABASE '/tmp/evil.db' AS evil",
		"DETACH DATABASE evil",
		"PRAGMA trusted_schema = ON",
		"VACUUM",
		"REINDEX",
		"BEGIN TRANSACTION",
		"COMMIT",
		"CREATE TABLE a (id INT); CREATE TABLE b (id INT)",
		"DELETE FROM _mako_sync",
		"INSERT INTO _mako_sync_meta (k, v) VALUES ('a', 'b')",
		`UPDATE "_mako_sync_tables" SET name = 'x'`,
		"SELECT load_extension('x')",
	}
	for _, sql := range badExecs {
		if err := checkSQL(sql, false); err == nil {
			t.Errorf("checkSQL(%q, readOnly=false) unexpectedly succeeded", sql)
		}
	}

	// Valid queries & statements must pass, including semicolons inside string literals.
	goodQueries := []string{
		"SELECT 1",
		"SELECT 'a; DROP TABLE t' AS s",
		"WITH cte AS (SELECT id FROM items) SELECT * FROM cte -- comment",
		"SELECT id, json_extract(doc, '$.title') FROM cards WHERE deleted = 0;",
	}
	for _, sql := range goodQueries {
		if err := checkSQL(sql, true); err != nil {
			t.Errorf("checkSQL(%q, readOnly=true) failed: %v", sql, err)
		}
	}
	goodExecs := []string{
		"CREATE TABLE IF NOT EXISTS notes (id TEXT PRIMARY KEY, body TEXT)",
		"CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(title, body)",
		"INSERT INTO notes VALUES (?, ?)",
	}
	for _, sql := range goodExecs {
		if err := checkSQL(sql, false); err != nil {
			t.Errorf("checkSQL(%q, readOnly=false) failed: %v", sql, err)
		}
	}
}

func TestDBQueryExecBatchFTS5AndScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store := newDBStore(filepath.Join(t.TempDir(), "db"), defaultDBQuota, newSyncClock("node-a"), nil)
	defer store.closeAll()

	scopeA := "project:repo:github.com/ironpark/mako"
	scopeB := "project:repo:github.com/ironpark/ggui"

	// Create table + FTS5 index in scope A and insert via atomic Batch.
	if _, err := store.batch(ctx, scopeA, []*hostapi.DbStatement{
		{Sql: `CREATE TABLE notes (id TEXT PRIMARY KEY, title TEXT NOT NULL, score INTEGER NOT NULL)`},
		{Sql: `CREATE VIRTUAL TABLE notes_fts USING fts5(title)`},
		{Sql: `INSERT INTO notes VALUES (?, ?, ?)`, ParamsJson: []string{`"n1"`, `"SQLite P2P Sync"`, `10`}},
		{Sql: `INSERT INTO notes_fts VALUES (?)`, ParamsJson: []string{`"SQLite P2P Sync"`}},
		{Sql: `INSERT INTO notes VALUES (?, ?, ?)`, ParamsJson: []string{`"n2"`, `"Kanban Drag Drop"`, `20`}},
		{Sql: `INSERT INTO notes_fts VALUES (?)`, ParamsJson: []string{`"Kanban Drag Drop"`}},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify atomic batch rollback when a later statement fails.
	if _, err := store.batch(ctx, scopeA, []*hostapi.DbStatement{
		{Sql: `INSERT INTO notes VALUES (?, ?, ?)`, ParamsJson: []string{`"n3"`, `"Should Rollback"`, `30`}},
		{Sql: `INSERT INTO notes VALUES (?, ?, ?)`, ParamsJson: []string{`"n1"`, `"Duplicate PK"`, `40`}},
	}); err == nil {
		t.Fatal("expected duplicate PK batch to fail")
	}

	// FTS5 query returns matching row; rolled-back n3 is absent.
	res, err := store.query(ctx, scopeA, `SELECT title FROM notes_fts WHERE notes_fts MATCH ?`, []string{`"SQLite"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RowsJson) != 1 || !strings.Contains(res.RowsJson[0], "SQLite P2P Sync") {
		t.Fatalf("fts query = %+v", res)
	}
	cnt, err := store.query(ctx, scopeA, `SELECT COUNT(*) AS c FROM notes`, nil)
	if err != nil || len(cnt.RowsJson) != 1 || cnt.RowsJson[0] != "[2]" {
		t.Fatalf("count = %+v, err = %v", cnt, err)
	}

	// Scope B is an independent SQLite file (notes table doesn't exist there).
	if _, err := store.query(ctx, scopeB, `SELECT * FROM notes`, nil); err == nil {
		t.Fatal("expected scope B to be isolated from scope A")
	}
	if store.used() <= 0 {
		t.Fatalf("expected positive db usage, got %d", store.used())
	}
}

func TestDBSyncCRDTMergeAndLiveReload(t *testing.T) {
	pluginFiles := map[string]string{
		ManifestName: `id = "notes"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["db", "db:sync"]

[[contributes.block]]
id = "list"
title = "Notes"
scope = "project"
`,
		"main.js": `let mako;
async function readAll(ctx) {
  await ctx.db.syncEnable("items");
  const q = await ctx.db.query("SELECT id, doc FROM items WHERE deleted = 0 ORDER BY id");
  return q.rows.map((r) => ({ id: r.id, ...JSON.parse(r.doc) }));
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("list", {
      state: { items: [], syncEvents: 0 },
      async load(s, ctx) {
        s.items = await readAll(ctx);
      },
      render: (s, ui) => ui.column([
        ui.text("syncs=" + s.syncEvents, { key: "syncs" }),
        ...s.items.map((it) => ui.text(it.id + ":" + it.title, { key: it.id })),
        ui.button("Add", { key: "add", onTap: ["add"] }),
        ui.button("Del", { key: "del", onTap: ["del"] }),
      ]),
      on: {
        async add(s) {
          const ctx = mako.blocks.context("list");
          await ctx.db.syncEnable("items");
          const id = ctx.projectId + "-1";
          await ctx.db.syncPut("items", id, { title: "From " + ctx.nodeId + " " + s.items.length });
          s.items = await readAll(ctx);
        },
        async del(s) {
          const ctx = mako.blocks.context("list");
          await ctx.db.syncEnable("items");
          await ctx.db.syncDelete("items", "shared");
          s.items = await readAll(ctx);
        },
      },
    });
  },
  on: {
    dbSync(ev) {
      const s = mako.blocks.state("list", ev.scopeKey);
      s.syncEvents = (s.syncEvents || 0) + ev.rows;
    },
  },
};`,
	}

	makeNode := func(nodeID string) *rig {
		r := newRig(t, testLimits, nil)
		r.m.syncClock = newSyncClock(nodeID)
		r.write("notes", pluginFiles)
		r.state("notes", protocol.PluginWaiting)
		r.grant("notes")
		return r
	}

	ra := makeNode("node-a")
	rb := makeNode("node-b")
	scope := "project:repo:github.com/ironpark/mako"

	// Watch block on both nodes.
	wa := ra.watchScoped("plugin:notes/list", scope)
	wb := rb.watchScoped("plugin:notes/list", scope)
	wa.until("a ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "syncs=0") })
	wb.until("b ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "syncs=0") })

	// Put a shared row directly on node A via its dbStore (simulating offline edit).
	pa, ok := ra.m.plugin("notes")
	if !ok {
		t.Fatal("notes plugin missing on node A")
	}
	pb, ok := rb.m.plugin("notes")
	if !ok {
		t.Fatal("notes plugin missing on node B")
	}
	if err := pa.db.syncEnable(context.Background(), scope, "items"); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.db.syncPut(context.Background(), scope, "items", "shared", `{"title":"Hello from A"}`); err != nil {
		t.Fatal(err)
	}
	if err := pb.db.syncEnable(context.Background(), scope, "items"); err != nil {
		t.Fatal(err)
	}
	if _, err := pb.db.syncPut(context.Background(), scope, "items", "b-only", `{"title":"Hello from B"}`); err != nil {
		t.Fatal(err)
	}

	// Exchange bundles bidirectionally.
	bundleA := ra.m.ExportSyncBundle()
	if len(bundleA.GetBuckets()) != 1 || len(bundleA.GetDigest()) != 32 {
		t.Fatalf("unexpected bundleA: %+v", bundleA)
	}
	if !rb.m.MergeSyncBundle(bundleA) {
		t.Fatal("expected node B to merge node A's row")
	}
	// Node B's watched block should reload live with A's row and on.dbSync fired!
	wb.until("b saw A's row", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "shared:Hello from A") && hasText(n, "b-only:Hello from B") && hasText(n, "syncs=1")
	})

	// Merge B back into A -> digests converge!
	if !ra.m.MergeSyncBundle(rb.m.ExportSyncBundle()) {
		t.Fatal("expected node A to merge node B's row")
	}
	wa.until("a saw B's row", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "shared:Hello from A") && hasText(n, "b-only:Hello from B")
	})
	if !bytes.Equal(ra.m.SyncDigest(), rb.m.SyncDigest()) {
		t.Fatalf("digests diverged after bidirectional merge: %x vs %x", ra.m.SyncDigest(), rb.m.SyncDigest())
	}

	// Delete "shared" on B and replicate tombstone to A.
	if _, err := rb.m.Event(context.Background(), &protocol.BlockEvent{
		Block: "plugin:notes/list",
		Scope: scope,
		Node:  "/del",
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	wb.until("b deleted shared", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return !hasText(n, "shared:Hello from A") && hasText(n, "b-only:Hello from B")
	})
	if !ra.m.MergeSyncBundle(rb.m.ExportSyncBundle()) {
		t.Fatal("expected node A to merge tombstone from B")
	}
	wa.until("a saw tombstone", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return !hasText(n, "shared:Hello from A") && hasText(n, "b-only:Hello from B")
	})
	if !bytes.Equal(ra.m.SyncDigest(), rb.m.SyncDigest()) {
		t.Fatalf("digests diverged after tombstone merge: %x vs %x", ra.m.SyncDigest(), rb.m.SyncDigest())
	}

	// Quarantined future HLC timestamp (> MaxClockDrift ahead) must be ignored.
	farFuture := &protocol.PluginSyncBundle{
		Buckets: []*protocol.PluginSyncBucket{{
			PluginId: "notes",
			Scope:    scope,
			Rows: []*protocol.PluginSyncRow{{
				Table: "items",
				Id:    "spoof",
				Doc:   `{"title":"From the year 2099"}`,
				Clock: &protocol.HLC{WallTime: time.Now().Add(time.Hour).UnixMilli(), NodeId: "evil"},
			}},
		}},
	}
	if ra.m.MergeSyncBundle(farFuture) {
		t.Fatal("quarantined future HLC row was accepted")
	}
}
