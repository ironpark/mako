package syntax

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
)

func TestLinkRollbackOnFailure(t *testing.T) {
	r := newRuntime(t)
	orig := readTestdata(t, "html", "html.wasm")

	// Failing at missing tree_sitter_<name> happens AFTER memBase, r.table, stack, and Instantiate.
	r.mu.Lock()
	tableBefore := r.table
	r.mu.Unlock()
	for i := range 20 {
		_, err := r.LoadLanguage("nonexistent_grammar_symbol", orig)
		if err == nil {
			t.Fatalf("iter %d: expected LoadLanguage to fail for missing symbol", i)
		}
		r.mu.Lock()
		tableAfter := r.table
		r.mu.Unlock()
		if tableAfter != tableBefore {
			t.Fatalf("iter %d: r.table leaked from %d to %d", i, tableBefore, tableAfter)
		}
	}

	// Failing at unsupported import must reject before allocating table slots or heap.
	badImport := append([]byte(nil), orig...)
	idx := bytes.Index(badImport, []byte("env"))
	if idx < 0 {
		t.Fatal("html.wasm missing env import string")
	}
	badImport[idx] = 'x'
	if _, err := r.LoadLanguage("html", badImport); err == nil {
		t.Fatal("expected unsupported import error")
	}
	r.mu.Lock()
	if r.table != tableBefore {
		t.Fatalf("r.table changed on invalid import: %d -> %d", tableBefore, r.table)
	}
	r.mu.Unlock()

	// Loading the valid grammar must still succeed after repeated failed links.
	l := load(t, r, "html")
	tree := parse(t, l, htmlSrc)
	if !bytes.Contains([]byte(tree.RootSExpr()), []byte("(document")) {
		t.Fatalf("unexpected parse tree: %s", tree.RootSExpr())
	}
}

func TestGrammarCacheBoundsAndConcurrentAcquire(t *testing.T) {
	// Ensure engine is initialized.
	r := newRuntime(t)
	htmlWasm := readTestdata(t, "html", "html.wasm")
	secs, err := readSections(htmlWasm)
	if err != nil {
		t.Fatal(err)
	}
	validBytes := writeSections(secs)

	cache := newGrammarModuleCache(2)
	var compileCount atomic.Int32
	compile := func() (*wasmtime.Module, error) {
		compileCount.Add(1)
		return wasmtime.NewModule(engine, validBytes)
	}

	// Race multiple goroutines on the same key: losing modules must close cleanly without panic.
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			entry, err := cache.acquire([]byte("key-same"), compile)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			cache.release(entry)
		}()
	}
	wg.Wait()

	// Acquire key1 (hold ref), key2, key3 -> key1 is evicted while still held!
	e1, err := cache.acquire([]byte("key-1"), compile)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := cache.acquire([]byte("key-2"), compile)
	if err != nil {
		t.Fatal(err)
	}
	cache.release(e2)
	e3, err := cache.acquire([]byte("key-3"), compile)
	if err != nil {
		t.Fatal(err)
	}
	cache.release(e3)

	cache.mu.Lock()
	cacheLen := len(cache.entries)
	evicted1 := e1.evicted
	refs1 := e1.refs
	cache.mu.Unlock()
	if cacheLen != 2 {
		t.Fatalf("cache len = %d, want bounded cap 2", cacheLen)
	}
	if !evicted1 || refs1 != 1 {
		t.Fatalf("expected key-1 evicted with 1 caller ref, got evicted=%v refs=%d", evicted1, refs1)
	}

	// While e1 is still held (not yet released), its module must still be valid for Instantiate.
	if err := r.usable(); err != nil {
		t.Fatal(err)
	}
	cache.release(e1)

	// Compile failure must propagate cleanly without inserting into cache.
	wantErr := errors.New("boom")
	if _, err := cache.acquire([]byte("key-err"), func() (*wasmtime.Module, error) { return nil, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}

	// Concurrent LoadLanguage across multiple Runtimes using the global grammarCache.
	var loadWG sync.WaitGroup
	loadWG.Add(4)
	for range 4 {
		go func() {
			defer loadWG.Done()
			rt, err := NewRuntime(t.Context())
			if err != nil {
				t.Errorf("NewRuntime: %v", err)
				return
			}
			defer rt.Close()
			if _, err := rt.LoadLanguage("html", htmlWasm); err != nil {
				t.Errorf("LoadLanguage: %v", err)
			}
		}()
	}
	loadWG.Wait()
}
