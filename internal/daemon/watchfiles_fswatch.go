//go:build !android

package daemon

import (
	"context"
	"time"

	"github.com/ironpark/ggui/pkg/fswatch"
)

type fswatchFactory struct{}

func defaultFileWatcher() FileWatcher { return fswatchFactory{} }

func (fswatchFactory) NewPathWatcher(logf func(string, ...any)) PathWatcher {
	return &fswatchPathWatcher{w: fswatch.New(fswatch.Options{Logf: logf})}
}

func (fswatchFactory) NewTreeWatcher(settle time.Duration) TreeWatcher {
	return &fswatchTreeWatcher{
		t: fswatch.NewTree(fswatch.TreeOptions{
			Ignore: fswatch.NewIgnore(fswatch.IgnoreConfig{Patterns: fswatch.DefaultPatterns, Git: true}).Match,
			Settle: settle,
		}),
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
	return t.t.Run(ctx, out)
}
