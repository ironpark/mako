package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/internal/protocol"
)

// The keychain the tests' plugins keep their secrets in is a Fake: no
// test reaches the user's own (keychain.System answers none in a test).

func keychainRig(t *testing.T, kc keychain.Keychain) *rig {
	t.Helper()
	return startRig(t, Config{Limits: testLimits, Keychain: kc})
}

// secretSetting is a manifest of a plugin with a secret setting, token,
// whose block shows what the plugin reads of it.
func secretSetting(id string) map[string]string {
	return map[string]string{
		ManifestName: manifest(id, nil, `
[contributes.settings]
token = { type = "secret", label = "Token" }
label = { type = "string", label = "Label" }
`),
		"main.js": block(`const { setting } = await mako.settings.getSetting({ key: "token" }); s.msg = "token=" + setting.text + " set=" + setting.set`, `ui.text(s.msg)`),
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestSecretSettingsAreKeptInTheKeychain(t *testing.T) {
	kc := &keychain.Fake{}
	r := keychainRig(t, kc)
	r.write("sec", secretSetting("sec"))
	r.state("sec", protocol.PluginReady)
	if _, err := r.act(&protocol.PluginAction{PluginId: "sec", Op: protocol.PluginSet, Key: "token", Value: "hunter2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "sec", Op: protocol.PluginSet, Key: "label", Value: "plain"}); err != nil {
		t.Fatal(err)
	}
	service := keychainService(r.data, "sec")
	if v, err := kc.Get(service, "setting:token"); err != nil || v != "hunter2" {
		t.Fatalf("the keychain holds %q, %v", v, err)
	}
	if s := readFile(t, filepath.Join(r.data, "sec", "settings.json")); strings.Contains(s, "hunter2") || !strings.Contains(s, "plain") {
		t.Fatalf("settings.json holds %s", s)
	}
	// The plugin reads it; the front-ends are told it is set, never what.
	w := r.watch("plugin:sec/ui")
	w.until("the token read", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "token=hunter2 set=true") })
	for _, s := range r.state("sec", protocol.PluginRunning).GetSettings() {
		if s.GetKey() == "token" && (s.GetValue() != "" || !s.GetSet()) {
			t.Fatalf("the secret is listed as %v", s)
		}
	}
	if strings.Contains(r.logs.String(), "hunter2") {
		t.Fatal("the secret was logged")
	}
	if store, _ := r.m.SecretStore(); store != kc.Name() {
		t.Fatalf("SecretStore = %q", store)
	}
	// Cleared, it is gone from the keychain.
	if _, err := r.act(&protocol.PluginAction{PluginId: "sec", Op: protocol.PluginSet, Key: "token", Value: ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.Get(service, "setting:token"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("cleared, the keychain still holds it: %v", err)
	}
	// One over what every system's keychain takes is refused.
	if _, err := r.act(&protocol.PluginAction{PluginId: "sec", Op: protocol.PluginSet, Key: "token", Value: strings.Repeat("x", maxSecret+1)}); err == nil {
		t.Fatal("an oversized secret was taken")
	}
}

// Secrets kept in files, a secret setting by an older mako in
// settings.json or a secret in secrets.json on a node that had no
// keychain, move into the keychain once there is one.
func TestFileSecretsMoveIntoTheKeychain(t *testing.T) {
	kc := &keychain.Fake{}
	base := t.TempDir()
	data := filepath.Join(base, "plugin-data", "sec")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte(`{"token":"old","label":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "secrets.json"), []byte(`{"secret:kept":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := startRigIn(t, base, Config{Limits: testLimits, Keychain: kc})
	r.write("sec", secretSetting("sec"))
	r.state("sec", protocol.PluginReady)
	service := keychainService(r.data, "sec")
	if v, err := kc.Get(service, "setting:token"); err != nil || v != "old" {
		t.Fatalf("the setting moved as %q, %v", v, err)
	}
	if v, err := kc.Get(service, "secret:kept"); err != nil || v != "1" {
		t.Fatalf("the secret moved as %q, %v", v, err)
	}
	if s := readFile(t, filepath.Join(data, "settings.json")); strings.Contains(s, "old") || !strings.Contains(s, `"label":"x"`) {
		t.Fatalf("settings.json after: %s", s)
	}
	if _, err := os.Stat(filepath.Join(data, "secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("secrets.json is still there: %v", err)
	}
	var index []string
	_ = json.Unmarshal([]byte(readFile(t, filepath.Join(data, "secrets-index.json"))), &index)
	if strings.Join(index, ",") != "secret:kept,setting:token" {
		t.Fatalf("the index is %v", index)
	}
}

func TestSecretsWithoutAKeychainAreKeptInAFileOfTheUsers(t *testing.T) {
	r := startRig(t, Config{Limits: testLimits, KeychainNote: "no Secret Service runs in this session"})
	r.write("sec", secretSetting("sec"))
	r.state("sec", protocol.PluginReady)
	if _, err := r.act(&protocol.PluginAction{PluginId: "sec", Op: protocol.PluginSet, Key: "token", Value: "hunter2"}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(r.data, "sec", "secrets.json")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json is %v", info.Mode())
	}
	if !strings.Contains(readFile(t, file), "hunter2") || strings.Contains(readFile(t, filepath.Join(r.data, "sec", "settings.json")), "hunter2") {
		t.Fatal("the secret is not where it should be")
	}
	w := r.watch("plugin:sec/ui")
	w.until("the token read", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "token=hunter2 set=true") })
	if store, note := r.m.SecretStore(); store != "file" || note == "" {
		t.Fatalf("SecretStore = %q, %q", store, note)
	}
}

// vaultPlugin keeps secrets through the Host API as its load says.
func vaultPlugin(id, load string) map[string]string {
	return map[string]string{ManifestName: manifest(id, []string{"secrets"}, ""), "main.js": block(load, `ui.text(s.msg)`)}
}

func TestPluginSecretsAreItsOwnAndGoWithIt(t *testing.T) {
	kc := &keychain.Fake{}
	r := keychainRig(t, kc)
	r.write("sec", vaultPlugin("sec", `
		await mako.secrets.setSecret({ name: "tok", value: "v1" });
		await mako.secrets.setSecret({ name: "tok", value: "v2" });
		await mako.secrets.setSecret({ name: "other.one", value: "x" });
		const got = await mako.secrets.getSecret({ name: "tok" });
		const none = await mako.secrets.getSecret({ name: "nope" });
		const del = await mako.secrets.deleteSecret({ name: "other.one" });
		const { names } = await mako.secrets.listSecrets({});
		s.msg = "got " + got.value + " " + got.found + " none " + none.found + " del " + del.found + " names " + names.join(",");`))
	r.state("sec", protocol.PluginWaiting)
	r.grant("sec")
	w := r.watch("plugin:sec/ui")
	w.until("its secrets", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "got v2 true none false del true names tok")
	})
	if v, err := kc.Get(keychainService(r.data, "sec"), "secret:tok"); err != nil || v != "v2" {
		t.Fatalf("the keychain holds %q, %v", v, err)
	}
	// Another plugin, granted secrets too, reads none of them: its own
	// service in the keychain is another.
	r.write("thief", vaultPlugin("thief", `
		const got = await mako.secrets.getSecret({ name: "tok" });
		const { names } = await mako.secrets.listSecrets({});
		s.msg = "thief " + got.found + " [" + names.join(",") + "]";`))
	r.state("thief", protocol.PluginWaiting)
	r.grant("thief")
	r.watch("plugin:thief/ui").until("what it read", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "thief false []") })
	if strings.Contains(r.logs.String(), "v2") {
		t.Fatal("a secret was logged")
	}
	// Removed, the plugin's secrets leave the keychain; the other's stay.
	w.cancel()
	if err := r.m.Remove("sec"); err != nil {
		t.Fatal(err)
	}
	for _, e := range kc.Entries() {
		if e[0] == keychainService(r.data, "sec") {
			t.Fatalf("the removed plugin's secret %s is still in the keychain", e[1])
		}
	}
}

func TestPluginSecretsOutOfBoundsAreRefused(t *testing.T) {
	kc := &keychain.Fake{}
	r := keychainRig(t, kc)
	cases := map[string]struct{ load, want string }{
		"dots":    {`await mako.secrets.setSecret({ name: "../x", value: "v" })`, "a secret's name"},
		"slash":   {`await mako.secrets.getSecret({ name: "a/b" })`, "a secret's name"},
		"empty":   {`await mako.secrets.getSecret({ name: "" })`, "a secret's name"},
		"long":    {`await mako.secrets.setSecret({ name: "x".repeat(65), value: "v" })`, "a secret's name"},
		"size":    {`await mako.secrets.setSecret({ name: "big", value: "x".repeat(2049) })`, "at most 2048 bytes"},
		"many":    {`for (let i = 0; i < 33; i++) await mako.secrets.setSecret({ name: "k" + i, value: "v" })`, "its most"},
		"flood":   {`for (let i = 0; i < 100; i++) await mako.secrets.listSecrets({})`, "times a minute"},
		"control": {`await mako.secrets.setSecret({ name: "a\nb", value: "v" })`, "a secret's name"},
	}
	for id, c := range cases {
		r.write(id, vaultPlugin(id, c.load+`; s.msg = "got through"`))
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
		tree := r.watch("plugin:"+id+"/ui").until(c.want, func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "error:") })
		if !hasText(tree, c.want) {
			t.Errorf("%s: %v", id, texts(tree))
		}
	}
	// A keychain that fails leaves no name in the index that it does not
	// hold.
	kc.Fail = errors.New("the keychain is locked")
	r.write("locked", vaultPlugin("locked", `await mako.secrets.setSecret({ name: "a", value: "v" }); s.msg = "got through"`))
	r.state("locked", protocol.PluginWaiting)
	r.grant("locked")
	r.watch("plugin:locked/ui").until("the keychain's error", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "locked") })
	if strings.Contains(readFile(t, filepath.Join(r.data, "locked", "secrets-index.json")), "secret:a") {
		t.Fatal("the index names a secret the keychain refused")
	}
	kc.Fail = nil
	healthy(t, r)
}
