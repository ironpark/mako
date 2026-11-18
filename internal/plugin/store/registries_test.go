package store

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/registry"
)

// The default registry, while it is not there, costs a node nothing: it
// is not read until the store is opened, and then fails beside itself,
// not in the log's warnings.
func TestAnUnreachableDefaultIsQuiet(t *testing.T) {
	r := newRig(t, "http://127.0.0.1:1/default/mako-registry.json")
	go r.s.Run(t.Context())
	regs := r.s.Registries()
	if len(regs) != 1 || !regs[0].GetBuiltin() || regs[0].GetError() != "" || regs[0].GetFetchedAt() != 0 {
		t.Fatalf("registries %v", regs)
	}
	if found := r.s.Search(context.Background(), "", ""); len(found) != 0 {
		t.Fatalf("found %v", found)
	}
	if regs = r.s.Registries(); regs[0].GetError() == "" {
		t.Fatalf("the default's failure is not shown: %v", regs)
	}
	if strings.Contains(r.logs.String(), "level=WARN msg=\"plugin registry") || strings.Contains(r.logs.String(), "level=ERROR") {
		t.Fatalf("the default's failure was logged as more than debug:\n%s", r.logs.String())
	}
	// The user may remove it, and add it back.
	if err := r.s.RemoveRegistry("http://127.0.0.1:1/default/mako-registry.json"); err != nil {
		t.Fatal(err)
	}
	if regs = r.s.Registries(); len(regs) != 0 {
		t.Fatalf("after removing the default: %v", regs)
	}
	again := New(Config{DataDir: r.data, Default: "http://127.0.0.1:1/default/mako-registry.json", Loopback: true})
	if regs = again.Registries(); len(regs) != 0 {
		t.Fatalf("the default came back after a restart: %v", regs)
	}
	if err := r.s.AddRegistry(context.Background(), "http://127.0.0.1:1/default/mako-registry.json"); err != nil {
		t.Fatal(err)
	}
	if regs = r.s.Registries(); len(regs) != 1 || !regs[0].GetBuiltin() {
		t.Fatalf("the default added back: %v", regs)
	}
}

func TestRegistriesAreCheckedAsAdded(t *testing.T) {
	r := newRig(t, "")
	for raw, want := range map[string]string{
		"/home/me/registry":                   "not an https URL",
		"file:///etc/mako-registry.json":      "not an https URL",
		"ssh://git@github.com/a/b":            "not an https URL",
		"git://github.com/a/b":                "not an https URL",
		"https://169.254.169.254/latest.json": "IP address",
	} {
		if err := r.s.AddRegistry(context.Background(), raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v; want an error saying %q", raw, err, want)
		}
	}
	r.index("reg", &registry.Index{})
	r.addRegistry("reg")
	if err := r.s.AddRegistry(context.Background(), r.registryURL("reg")); err == nil {
		t.Error("a registry added twice")
	}
	// An enormous or malformed index is refused as it is read, and the
	// registry stays, its error beside it.
	r.serve("/huge/"+registry.IndexName, bytes.Repeat([]byte(" "), registry.MaxIndexBytes+10))
	r.addRegistry("huge")
	r.serve("/bad/"+registry.IndexName, []byte(`{"format":"mako-registry/1","plugins":[{"id":"x","verified":true}]}`))
	r.addRegistry("bad")
	errs := map[string]string{}
	for _, info := range r.s.Registries() {
		errs[info.GetUrl()] = info.GetError()
	}
	if !strings.Contains(errs[r.registryURL("huge")], "4096 KiB") || !strings.Contains(errs[r.registryURL("bad")], "does not read") ||
		errs[r.registryURL("reg")] != "" {
		t.Fatalf("errors %v", errs)
	}
}
