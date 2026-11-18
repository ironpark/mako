// Package keychain keeps small secrets in the system's own store of them:
// the macOS Keychain, the Secret Service of a Linux desktop over D-Bus, or
// Windows' Credential Manager, each reached without cgo. A secret is a
// value under a service and an account, as all three name them.
//
// A node may have none of them: a Linux server with no desktop session,
// say. System then says why, and the caller keeps its secrets elsewhere
// and tells the user so. A test binary never reaches the user's own
// store: System answers none there, and a test hands its code a Fake.
package keychain

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keychain is a store of secrets.
type Keychain interface {
	// Name is the store as the user knows it: "macOS Keychain", ...
	Name() string
	// Get reads the secret of service and account; ErrNotFound if none.
	Get(service, account string) (string, error)
	// Set writes it, replacing what was there.
	Set(service, account, value string) error
	// Delete removes it; ErrNotFound if there was none.
	Delete(service, account string) error
}

var (
	// ErrNotFound is a secret the store does not hold.
	ErrNotFound = errors.New("the keychain holds no such secret")
	// ErrTimeout is a store that did not answer in time: it may be
	// waiting for the user to unlock it or to allow mako in.
	ErrTimeout = fmt.Errorf("the keychain did not answer within %v; it may be waiting for you to unlock it", opTimeout)
)

// opTimeout bounds each call of the system's store, which may hold it
// on a prompt nobody answers.
const opTimeout = 20 * time.Second

// EnvVar, set to "file", has a node keep its secrets in a file of its own
// even when the system has a keychain: for a headless node, or a harness
// that must not touch the user's keychain.
const EnvVar = "MAKO_KEYCHAIN"

// System is the system's store of secrets, or nil and why there is none.
func System() (Keychain, error) {
	if testing.Testing() {
		return nil, errors.New("a test does not reach the user's keychain")
	}
	if strings.EqualFold(os.Getenv(EnvVar), "file") {
		return nil, fmt.Errorf("%s=file", EnvVar)
	}
	kc, err := system()
	if err != nil {
		return nil, err
	}
	return timed{kc}, nil
}

// timed bounds each call of a store in time. A call that runs over is
// left to finish on its own; the caller goes on without its answer.
type timed struct{ k Keychain }

func (t timed) Name() string { return t.k.Name() }

func (t timed) Get(service, account string) (string, error) {
	var v string
	err := within(func() (err error) { v, err = t.k.Get(service, account); return })
	return v, err
}

func (t timed) Set(service, account, value string) error {
	return within(func() error { return t.k.Set(service, account, value) })
}

func (t timed) Delete(service, account string) error {
	return within(func() error { return t.k.Delete(service, account) })
}

func within(fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(opTimeout):
		return ErrTimeout
	}
}

// Fake is a store in memory, for tests. Its zero value is empty and
// ready.
type Fake struct {
	mu   sync.Mutex
	data map[[2]string]string
	// Fail, when set, is returned by every call: a keychain gone wrong.
	Fail error
	// Calls counts the calls made of it.
	Calls int
}

func (f *Fake) Name() string { return "test keychain" }

func (f *Fake) Get(service, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Fail != nil {
		return "", f.Fail
	}
	v, ok := f.data[[2]string{service, account}]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *Fake) Set(service, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Fail != nil {
		return f.Fail
	}
	if f.data == nil {
		f.data = map[[2]string]string{}
	}
	f.data[[2]string{service, account}] = value
	return nil
}

func (f *Fake) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Fail != nil {
		return f.Fail
	}
	k := [2]string{service, account}
	if _, ok := f.data[k]; !ok {
		return ErrNotFound
	}
	delete(f.data, k)
	return nil
}

// Entries are the service and account of every secret held, sorted.
func (f *Fake) Entries() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][2]string, 0, len(f.data))
	for k := range f.data {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	return out
}
