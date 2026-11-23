package plugin

import (
	"os"
	"strings"
	"testing"
)

func TestExampleManifest(t *testing.T) {
	data, err := os.ReadFile("../../examples/plugins/ci-status/plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "ci-status" || m.Entry != "main.js" || len(m.Blocks) != 1 || len(m.Commands) != 1 || len(m.Settings) != 2 {
		t.Fatalf("%+v", m)
	}
	if m.Settings[0].Key != "repo" || m.Settings[1].Key != "token" || m.Settings[1].Type != SettingSecret {
		t.Fatalf("settings out of the file's order: %+v", m.Settings)
	}
	if strings.Join(m.Permissions, ",") != "net:api.github.com,storage,sessions:read" {
		t.Fatalf("permissions %v", m.Permissions)
	}
}

func TestManifestIsReadStrictly(t *testing.T) {
	good := manifest("good", []string{"storage", "net:api.github.com"}, `
[contributes.settings]
mode = { type = "select", label = "Mode", options = ["a", "b"], default = "a" }
n = { type = "number", default = 3 }
on = { type = "boolean", default = true }
`)
	if _, err := ParseManifest([]byte(good)); err != nil {
		t.Fatal(err)
	}
	swap := func(old, new string) string {
		if !strings.Contains(good, old) {
			t.Fatalf("the manifest has no %q", old)
		}
		return strings.Replace(good, old, new, 1)
	}
	cases := map[string]struct{ manifest, want string }{
		"unknown key":       {"extra = 1\n" + good, "unknown keys"},
		"unknown table key": {swap(`title = "Test"`, "title = \"Test\"\ncolor = \"#f00\""), "unknown keys"},
		"wrong type":        {swap(`api = 1`, `api = "1"`), ""},
		"api":               {swap(`api = 1`, `api = 2`), "api 2 is not supported"},
		"id":                {swap(`id = "good"`, `id = "Good"`), "id"},
		"id dots":           {swap(`id = "good"`, `id = "../good"`), "id"},
		"mako id":           {swap(`id = "good"`, `id = "mako-core"`), "mako's own"},
		"version":           {swap(`version = "0.1.0"`, `version = "1"`), "version"},
		"publisher":         {swap(`publisher = "test"`, `publisher = "Te st"`), "publisher"},
		"entry up":          {swap(`entry = "main.js"`, `entry = "../main.js"`), "entry"},
		"entry abs":         {swap(`entry = "main.js"`, `entry = "/main.js"`), "entry"},
		"entry not js":      {swap(`entry = "main.js"`, `entry = "main.sh"`), "entry"},
		"entry unclean":     {swap(`entry = "main.js"`, `entry = "./main.js"`), "entry"},
		"exec":              {swap(`"storage"`, `"exec"`), "exec is not granted"},
		"bare net":          {swap(`"storage"`, `"net"`), "names its host"},
		"net ip":            {swap(`"net:api.github.com"`, `"net:140.82.112.6"`), "IP address"},
		"net localhost":     {swap(`"net:api.github.com"`, `"net:localhost"`), "host name"},
		"net port":          {swap(`"net:api.github.com"`, `"net:api.github.com:443"`), "host name"},
		"net wildcard":      {swap(`"net:api.github.com"`, `"net:*.github.com"`), "host name"},
		"net upper":         {swap(`"net:api.github.com"`, `"net:API.github.com"`), "host name"},
		"net scheme":        {swap(`"net:api.github.com"`, `"net:https://api.github.com"`), "host name"},
		"implicit":          {swap(`"storage"`, `"ui"`), "every plugin has it"},
		"unknown perm":      {swap(`"storage"`, `"root"`), "not one mako knows"},
		"twice":             {swap(`"storage"`, `"net:api.github.com"`), "twice"},
		"block id":          {swap(`id = "ui"`, `id = "mako.sidebar"`), "block id"},
		"block title":       {swap(`title = "Test"`, `title = ""`), "empty"},
		"spoofed title":     {swap(`title = "Test"`, "title = \"Settings\u202e\""), "reordering"},
		"slot":              {swap(`title = "Test"`, "title = \"Test\"\nslots = [\"center\"]"), "slot"},
		"command id":        {swap(`id = "go"`, `id = "go now"`), "command id"},
		"setting key":       {swap(`mode = {`, `Mode = {`), "keys are"},
		"setting type":      {swap(`type = "number"`, `type = "color"`), "type"},
		"select options":    {swap(`type = "select", label = "Mode", options = ["a", "b"], default = "a"`, `type = "select"`), "options"},
		"default type":      {swap(`default = true`, `default = "yes"`), "default"},
		"default option":    {swap(`default = "a"`, `default = "c"`), "default"},
		"block scope":       {swap(`title = "Test"`, "title = \"Test\"\nscope = \"workspace\""), "scope"},
		"setting scope":     {swap(`type = "boolean", default = true`, `type = "boolean", default = true, scope = "org"`), "scope"},
	}
	for name, c := range cases {
		_, err := ParseManifest([]byte(c.manifest))
		if err == nil {
			t.Errorf("%s: taken", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused with %q; want %q", name, err, c.want)
		}
	}
	if _, err := ParseManifest(make([]byte, maxManifest+1)); err == nil {
		t.Error("a huge manifest was taken")
	}
}

func TestManifestScopesNormalized(t *testing.T) {
	raw := manifest("scoped", nil, `
[[contributes.block]]
id = "proj"
title = "Project"
scope = "project"

[[contributes.tool]]
id = "t_def"
description = "Default scope tool"

[[contributes.tool]]
id = "t_space"
description = "Space scope tool"
scope = "space"

[contributes.settings]
a = { type = "string" }
b = { type = "string", scope = "session" }
`)
	m, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Blocks[0].Scope != "global" || m.Blocks[1].Scope != "project" {
		t.Fatalf("block scopes = %+v", m.Blocks)
	}
	if m.Tools[0].Scope != "global" || m.Tools[1].Scope != "space" {
		t.Fatalf("tool scopes = %+v", m.Tools)
	}
	if m.Settings[0].Scope != "global" || m.Settings[1].Scope != "session" {
		t.Fatalf("setting scopes = %+v", m.Settings)
	}
	// Invalid tool scope rejected strictly.
	badTool := strings.Replace(raw, `scope = "space"`, `scope = "cluster"`, 1)
	if _, err := ParseManifest([]byte(badTool)); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected invalid tool scope error, got %v", err)
	}
}
