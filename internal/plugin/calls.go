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
	case method == "load" || method == "event" || method == "command":
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

// render draws each block again, keeping the last tree that read when a
// render fails.
func (p *plugin) renderAll() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.inst))
	for id := range p.inst {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	slices.Sort(ids)
	for _, id := range ids {
		if p.rt == nil {
			return
		}
		p.render(id)
	}
}

func (p *plugin) render(id string) {
	out, err := p.invoke("render", false, id)
	var t *tree
	var over *limitError
	if err == nil {
		t, err = readTree([]byte(out))
	}
	if !errors.As(err, &over) && p.rt != nil {
		p.count(&p.renderFailures, "renders", err)
	}
	p.mu.Lock()
	if in, ok := p.inst[id]; ok {
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

// setErr notes the last call's error on a block.
func (p *plugin) setErr(id string, err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	if in, ok := p.inst[id]; ok {
		in.err = err.Error()
	}
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

// show readies a block a stream watches: the runtime up, the block loaded
// and drawn.
func (p *plugin) show(id string) {
	if err := p.ensure(); err != nil {
		p.bump()
		return
	}
	p.mu.Lock()
	in, ok := p.inst[id]
	p.mu.Unlock()
	if !ok {
		return
	}
	if !in.loaded {
		p.load(in)
	}
	p.renderAll()
}

// showAll readies every block a stream watches.
func (p *plugin) showAll() {
	p.mu.Lock()
	var ids []string
	for id, in := range p.inst {
		if in.subs > 0 {
			ids = append(ids, id)
		}
	}
	p.mu.Unlock()
	slices.Sort(ids)
	for _, id := range ids {
		p.show(id)
	}
	if len(ids) == 0 && p.rt != nil {
		p.renderAll()
	}
}

// load runs a block's load.
func (p *plugin) load(in *instance) {
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
	if _, err := p.invoke("load", false, in.id); err != nil {
		p.setErr(in.id, fmt.Errorf("load: %w", err))
		return
	}
	p.mu.Lock()
	in.err = ""
	p.mu.Unlock()
}

// tick stops an idle runtime, and loads again the blocks that showing
// ask to be loaded every so often.
func (p *plugin) tick() {
	if p.rt == nil {
		return
	}
	if !p.watched() {
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
	for _, in := range due {
		p.load(in)
	}
	if len(due) > 0 {
		p.renderAll()
	}
}

// afterCall loads the blocks the call asked to be loaded again, and
// draws every block.
func (p *plugin) afterCall() {
	if p.rt == nil {
		return
	}
	if out, err := p.invoke("takeReloads", false); err == nil {
		var ids []string
		_ = json.Unmarshal([]byte(out), &ids)
		for _, id := range ids {
			p.mu.Lock()
			in, ok := p.inst[id]
			p.mu.Unlock()
			if ok && p.rt != nil {
				p.load(in)
			}
		}
	}
	p.renderAll()
}

// bridge answers a call of the Host API from the runtime.
func (p *plugin) bridge(ctx context.Context, method string, req []byte) ([]byte, error) {
	return hostapi.Dispatch(ctx, p.grants(), &host{p: p}, method, req)
}
