package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/ironpark/mako/internal/uiblock"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// invoke calls the prelude's method in the runtime, under a budget, and
// deals with what went wrong: a limit gone over stops the plugin; its
// own errors, enough of them in a row, do too. gesture says the call
// answers the user, so that it may open a link.
func (p *plugin) invoke(method string, gesture bool, args ...any) (string, error) {
	if p.rt == nil {
		return "", errors.New("the plugin is not running")
	}
	b := newBudget(p.m.ctx, p.m.lim.CPU, p.m.lim.Wall)
	defer b.release()
	p.gesture = gesture
	out, err := p.rt.invoke(b, method, args...)
	p.gesture = false
	p.lastUsed = time.Now()
	err = classify(b, err)
	var over *limitError
	switch {
	case errors.As(err, &over):
		p.kill(over.msg)
	case method == "load" || method == "event" || method == "command" || method == "sessionEvent" || method == "callTool" || method == "gitDone":
		p.count(&p.failures, "calls", err)
	}
	return out, err
}

// count counts a call's failure in a row of them, stopping the plugin at
// Limits.Failures, or ends the row on a success. Its handlers' calls and
// its renders are counted apart, so that one kind working does not hide
// the other failing every time.
func (p *plugin) count(n *int, what string, err error) {
	if err == nil {
		*n = 0
		return
	}
	*n++
	if *n >= p.m.lim.Failures {
		p.kill(fmt.Sprintf("its %s failed %d times in a row, the last with: %v", what, *n, err))
	}
}

// evictInstances removes LRU-evicted (block, scope) states from both saved
// state and the live JS runtime.
func (p *plugin) evictInstances(keys []string) {
	for _, key := range keys {
		p.stopGitWatchesForInstance(key)
		delete(p.saved, key)
		if p.rt != nil {
			blockID, scopeKey := splitInstanceKey(key)
			_, _ = p.invoke("evict", false, blockID, scopeKey)
		}
	}
}

// render draws each block instance again, keeping the last tree that read when
// a render fails.
func (p *plugin) renderAll() {
	p.mu.Lock()
	keys := make([]string, 0, len(p.inst))
	for key := range p.inst {
		keys = append(keys, key)
	}
	p.mu.Unlock()
	slices.Sort(keys)
	for _, key := range keys {
		if p.rt == nil {
			return
		}
		p.render(key)
	}
}

// renderMatching re-renders watched instances matching scopeKey (plus global
// instances and any explicit extraKeys). When scopeKey == "", every instance is
// re-rendered (preserving global command/event behavior).
func (p *plugin) renderMatching(scopeKey string, extraKeys ...string) {
	if scopeKey == "" {
		p.renderAll()
		return
	}
	p.mu.Lock()
	keys := make([]string, 0, len(p.inst))
	for key, in := range p.inst {
		if slices.Contains(extraKeys, key) || (in.subs > 0 && (in.scopeKey == scopeKey || in.scopeKey == "")) {
			keys = append(keys, key)
		}
	}
	p.mu.Unlock()
	slices.Sort(keys)
	for _, key := range keys {
		if p.rt == nil {
			return
		}
		p.render(key)
	}
}

func (p *plugin) render(key string) {
	p.mu.Lock()
	in, ok := p.inst[key]
	if !ok {
		p.mu.Unlock()
		return
	}
	blockID, scopeKey, locale, standalone := in.id, in.scopeKey, in.locale, in.standaloneLocked()
	p.mu.Unlock()
	sc := p.m.resolveScope(scopeKey)
	sc.Locale = normalizeLocale(locale)
	sc.View = View{Standalone: standalone}
	ctxJSON := marshalScopeContext(sc)
	prevInst := p.activeInstance
	p.activeInstance = key
	out, err := p.invoke("render", false, blockID, scopeKey, ctxJSON)
	p.activeInstance = prevInst
	var t *uiblock.Tree
	var over *limitError
	if err == nil {
		t, err = uiblock.Read([]byte(out))
	}
	if !errors.As(err, &over) && p.rt != nil {
		p.count(&p.renderFailures, "renders", err)
	}
	p.mu.Lock()
	if in, ok := p.inst[key]; ok {
		if err != nil {
			in.err = "render: " + err.Error()
		} else {
			in.tree, in.err = t, ""
		}
	}
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

// setErr notes the last call's error on a block instance.
func (p *plugin) setErr(key string, err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	if in, ok := p.inst[key]; ok {
		in.err = err.Error()
	}
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

// show readies a block instance a stream watches: the runtime up, the block
// loaded and drawn.
func (p *plugin) show(key string) {
	if err := p.ensure(); err != nil {
		p.bump()
		return
	}
	p.mu.Lock()
	in, ok := p.inst[key]
	p.mu.Unlock()
	if !ok {
		return
	}
	if !in.loaded {
		p.load(in)
	}
	p.renderMatching(in.scopeKey, key)
}

// showAll readies every block instance a stream watches.
func (p *plugin) showAll() {
	p.mu.Lock()
	var keys []string
	for key, in := range p.inst {
		if in.subs > 0 {
			keys = append(keys, key)
		}
	}
	p.mu.Unlock()
	slices.Sort(keys)
	for _, key := range keys {
		p.show(key)
	}
	if len(keys) == 0 && p.rt != nil && p.ensure() == nil {
		p.renderAll()
	}
}

// load runs a block instance's load hook with its ScopeContext.
func (p *plugin) load(in *instance) {
	p.mu.Lock()
	in.loading = true
	in.dirty = false
	locale, standalone := in.locale, in.standaloneLocked()
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		in.loading = false
		requeue := in.dirty && in.subs > 0
		p.mu.Unlock()
		if requeue {
			p.post(func() { p.reloadInstance(in.key) })
		}
	}()
	in.loaded, in.lastLoad = true, time.Now()
	if r, err := p.invoke("refresh", false, in.id); err == nil {
		secs, _ := strconv.ParseFloat(r, 64)
		in.refresh = 0
		if secs > 0 && !math.IsInf(secs, 0) {
			in.refresh = max(time.Duration(min(secs, 86400)*float64(time.Second)), minRefresh)
		}
	}
	if p.rt == nil {
		return
	}
	sc := p.m.resolveScope(in.scopeKey)
	sc.Locale = normalizeLocale(locale)
	sc.View = View{Standalone: standalone}
	ctxJSON := marshalScopeContext(sc)
	prevInst := p.activeInstance
	p.activeInstance = in.key
	_, err := p.invoke("load", false, in.id, in.scopeKey, ctxJSON)
	p.activeInstance = prevInst
	if err != nil {
		p.setErr(in.key, fmt.Errorf("load: %w", err))
		return
	}
	p.mu.Lock()
	in.err = ""
	p.mu.Unlock()
}

// tick stops an idle runtime, and loads again the blocks that showing
// ask to be loaded every so often.
func (p *plugin) tick() {
	p.expireAsk(time.Now())
	if p.rt == nil {
		return
	}
	if !p.watched() {
		if p.installStatus().Blocked != "" {
			_ = p.ensure() // closes it
			return
		}
		if time.Since(p.lastUsed) > p.m.lim.Idle {
			p.sleep("idle")
		}
		return
	}
	p.mu.Lock()
	var due []*instance
	for _, in := range p.inst {
		if in.subs > 0 && in.loaded && in.refresh > 0 && time.Since(in.lastLoad) >= in.refresh {
			due = append(due, in)
		}
	}
	p.mu.Unlock()
	if len(due) == 0 || p.ensure() != nil {
		return
	}
	for _, in := range due {
		p.load(in)
		if p.rt != nil {
			p.render(in.key)
		}
	}
}

// afterCall loads the blocks the call asked to be loaded again, and re-renders
// watched instances matching scopeKey (plus global blocks and extraKeys).
func (p *plugin) afterCall(scopeKey string, extraKeys ...string) {
	if p.rt == nil {
		return
	}
	if out, err := p.invoke("takeReloads", false); err == nil {
		var reloads []struct {
			ID    string `json:"id"`
			Scope string `json:"scope"`
		}
		_ = json.Unmarshal([]byte(out), &reloads)
		for _, r := range reloads {
			k := instanceKey(r.ID, r.Scope)
			p.mu.Lock()
			in, ok := p.inst[k]
			p.mu.Unlock()
			if ok && p.rt != nil {
				p.load(in)
				extraKeys = append(extraKeys, k)
			}
		}
	}
	p.renderMatching(scopeKey, extraKeys...)
}

// bridge answers a call of the Host API from the runtime.
func (p *plugin) bridge(ctx context.Context, method string, req []byte) ([]byte, error) {
	return hostapi.Dispatch(ctx, pluginGrants{p: p}, &host{p: p}, method, req)
}
