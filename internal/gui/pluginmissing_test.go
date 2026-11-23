package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestMissingPluginBlockValidatesPrefixAndParts(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	for _, name := range []string{"foo/bar", "agent:s1/b", "plugin:/b", "plugin:a/", "plugin:a"} {
		m := newModel(config.Default(), "")
		m.post = func(func()) {}
		m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "n1", Revision: protocol.RevisionPluginStore}})

		p := ggui.ProbeBuilder(func() ggui.Widget {
			return missingPluginBlock(m, name)
		}, ggui.Sz(400, 240))
		p.Frame()
		sem := p.Semantics().String()
		p.Close()

		if !strings.Contains(sem, T("Missing block")) {
			t.Errorf("%q: expected Missing block widget, got:\n%s", name, sem)
		}
		if len(m.store.asking) != 0 {
			t.Errorf("%q: unexpectedly queried plugin store: %v", name, m.store.asking)
		}
	}

	// Well-formed plugin:<id>/<block> with a known store listing offers installation.
	m := newModel(config.Default(), "")
	m.post = func(func()) {}
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "n1", Revision: protocol.RevisionPluginStore}})
	m.store.Known.Set(map[string][]*protocol.PluginListing{
		"ci-status": {{
			Id:          "ci-status",
			Version:     "1.0.0",
			Registry:    "official",
			Fingerprint: "abcdef0123456789",
		}},
	})
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return missingPluginBlock(m, "plugin:ci-status/ci")
	}, ggui.Sz(480, 260))
	defer p.Close()
	p.Frame()
	sem := p.Semantics().String()
	if !strings.Contains(sem, T("Not installed on this node")) || !strings.Contains(sem, Tf("Install %s on this node", "ci-status")) {
		t.Fatalf("expected install offer for ci-status, got:\n%s", sem)
	}
	if !m.store.asking["ci-status"] {
		t.Fatalf("expected lookUpPlugin to mark ci-status as asked: %v", m.store.asking)
	}
}
