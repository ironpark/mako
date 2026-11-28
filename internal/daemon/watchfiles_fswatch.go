//go:build !android

package daemon

import (
	"context"
	"time"

	"github.com/ironpark/fswatch"
)

type fswatchFactory struct{}

func defaultFileWatcher() FileWatcher { return fswatchFactory{} }

func (fswatchFactory) NewPathWatcher(logf func(string, ...any)) PathWatcher {
	return &fswatchPathWatcher{w: fswatch.New(fswatch.Options{Logf: logf})}
}

func (fswatchFactory) NewTreeWatcher(settle time.Duration) TreeWatcher {
	// The patterns are fswatch's own, so the only error NewIgnore has, a
	// pattern that does not parse, cannot happen.
	ignore, err := fswatch.NewIgnore(fswatch.IgnoreOptions{Defaults: true, Git: true})
	if err != nil {
		panic(err)
	}
	return &fswatchTreeWatcher{
		t: fswatch.NewTree(fswatch.TreeOptions{Ignore: ignore, Settle: settle}),
	}
}

type fswatchPathWatcher struct {
	w *fswatch.Watcher
}

func (p *fswatchPathWatcher) Set(paths []string) {
	p.w.Set(paths)
}

func (p *fswatchPathWatcher) Run(ctx context.Context, out chan<- []string) {
	batches := make(chan fswatch.Batch)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.w.Run(ctx, batches)
	}()
	defer func() {
		for {
			select {
			case <-batches:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case b := <-batches:
			paths := b.Paths()
			if len(paths) == 0 {
				continue
			}
			select {
			case out <- paths:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

type fswatchTreeWatcher struct {
	t *fswatch.Tree
}

func (t *fswatchTreeWatcher) Set(trees []string) {
	t.t.Set(trees)
}

func (t *fswatchTreeWatcher) Run(ctx context.Context, out chan<- []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	batches := make(chan fswatch.TreeBatch)
	done := make(chan error, 1)
	go func() { done <- t.t.Run(ctx, batches) }()
	for {
		select {
		case b := <-batches:
			select {
			case out <- b.Dirs:
			case err := <-done:
				return err
			}
		case err := <-done:
			return err
		}
	}
}
