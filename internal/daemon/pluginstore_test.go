package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/plugin/pack"
	pluginregistry "github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// A front-end adds a registry, searches it, installs from it and removes
// what it installed, over the daemon's socket.
func TestPluginStoreOverTheSocket(t *testing.T) {
	testRegistries = true
	t.Cleanup(func() { testRegistries = false })
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	data, _, err := pack.Build(map[string][]byte{
		"plugin.toml": []byte("id = \"hello\"\nversion = \"1.0.0\"\npublisher = \"test\"\napi = 1\nentry = \"main.js\"\npermissions = [\"storage\"]\n\n[[contributes.block]]\nid = \"hi\"\ntitle = \"Hello\"\n"),
		"main.js":     []byte(`export default { activate(m) { m.blocks.register("hi", { state: {}, render: (s, ui) => ui.text("hi") }) } }`),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	sum := sha256.Sum256(data)
	idx := &pluginregistry.Index{Format: pluginregistry.Format, Name: "test", Plugins: []*pluginregistry.Entry{{
		ID: "hello", Publisher: "test", Key: pack.PublicKey(priv),
		Versions: []*pluginregistry.Version{{Version: "1.0.0", API: 1, URL: srv.URL + "/hello.makoplugin", SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(data)), Permissions: []string{"storage"}}},
	}}}
	index, err := idx.Marshal(true)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("/reg/mako-registry.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(index) })
	mux.HandleFunc("/hello.makoplugin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })

	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	regs, err := c.Registries(ctx)
	if err != nil || len(regs.GetRegistries()) != 0 {
		t.Fatalf("a test's daemon reads %v, %v; it should read no registry on the internet", regs, err)
	}
	regs, err = c.RegistryAction(ctx, &protocol.RegistryAction{Op: protocol.RegistryAdd, Url: srv.URL + "/reg/mako-registry.json"})
	if err != nil || len(regs.GetRegistries()) != 1 || regs.GetRegistries()[0].GetPlugins() != 1 {
		t.Fatalf("added: %v, %v", regs, err)
	}
	found, err := c.SearchPlugins(ctx, &protocol.SearchPlugins{Query: "hel"})
	if err != nil || len(found.GetListings()) != 1 || found.GetListings()[0].GetFingerprint() != pack.Fingerprint(pack.PublicKey(priv)) {
		t.Fatalf("found %v, %v", found, err)
	}
	done, err := c.InstallPlugin(ctx, &protocol.InstallPlugin{Op: protocol.InstallOp, Id: "hello"})
	if err != nil || !slices.Equal(done.GetAdded(), []string{"storage"}) {
		t.Fatalf("installed %v, %v", done, err)
	}
	for {
		list, err := c.Plugins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ps := list.GetPlugins(); len(ps) == 1 && ps[0].GetInstalled() && ps[0].GetState() == protocol.PluginWaiting &&
			ps[0].GetKey() == pack.PublicKey(priv) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("never listed as installed: %v", list)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if _, err := c.InstallPlugin(ctx, &protocol.InstallPlugin{Op: protocol.RemoveOp, Id: "hello"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.Plugins(ctx); len(list.GetPlugins()) != 0 {
		t.Fatalf("after removing: %v", list)
	}
	// A refusal comes over the socket with its code and what it names,
	// for a window to say it in its user's language.
	_, err = c.InstallPlugin(ctx, &protocol.InstallPlugin{Op: protocol.RemoveOp, Id: "hello"})
	var ce *protocol.CodedError
	if !errors.As(err, &ce) || ce.Code != protocol.CodeNotInstalled || strings.Join(ce.Args, ",") != "hello" {
		t.Fatalf("removing twice: %#v", err)
	}
}

// Installing is a node's own: no peer, let in or not, installs on it,
// searches its registries or changes them.
func TestPeersCannotInstall(t *testing.T) {
	d := meshDaemon(t, "a")
	for _, msg := range []struct {
		typ  protocol.Type
		body proto.Message
	}{
		{protocol.TypeListRegistries, &protocol.ListRegistries{}},
		{protocol.TypeRegistryAction, &protocol.RegistryAction{Op: protocol.RegistryAdd, Url: "https://github.com/a/b"}},
		{protocol.TypeSearchPlugins, &protocol.SearchPlugins{Query: "x"}},
		{protocol.TypeInstallPlugin, &protocol.InstallPlugin{Op: protocol.InstallOp, Id: "x"}},
		{protocol.TypeInstallPlugin, &protocol.InstallPlugin{Op: protocol.RemoveOp, Id: "x"}},
	} {
		env, err := protocol.Encode(msg.typ, "1", msg.body)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.dispatch(context.Background(), nil, env, true); !errors.Is(err, errStorePeer) {
			t.Fatalf("a peer's %s: %v", msg.typ, err)
		}
	}
}
