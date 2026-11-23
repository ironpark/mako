//go:build android

package daemon

import (
	"context"
	"errors"
	"time"
)

type androidFileWatcher struct{}

func defaultFileWatcher() FileWatcher { return androidFileWatcher{} }

func (androidFileWatcher) NewPathWatcher(func(string, ...any)) PathWatcher {
	return androidPathWatcher{}
}

func (androidFileWatcher) NewTreeWatcher(time.Duration) TreeWatcher {
	return androidTreeWatcher{}
}

type androidPathWatcher struct{}

func (androidPathWatcher) Set([]string) {}

func (androidPathWatcher) Run(ctx context.Context, _ chan<- []string) {
	<-ctx.Done()
}

type androidTreeWatcher struct{}

func (androidTreeWatcher) Set([]string) {}

func (androidTreeWatcher) Run(context.Context, chan<- []string) error {
	return errors.New("file tree watching is unavailable on android")
}
