package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/protocol"
)

// plugins lists the node's plugins, or acts on one as the window would
// for the user: allowing what it asks for is the user's to do, from the
// window or from here.
func plugins(ctx context.Context, c *client.Client, args []string) error {
	if len(args) == 0 {
		list, err := c.Plugins(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tVERSION\tSTATE\tASKS FOR\tGRANTED\tFROM\tUPDATE\tERROR")
		for _, p := range list.GetPlugins() {
			from := "folder"
			if p.GetInstalled() {
				from = p.GetSource() + " " + pack.Fingerprint(p.GetKey())
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.GetId(), p.GetVersion(), p.GetState(),
				strings.Join(p.GetAsked(), ","), strings.Join(p.GetGranted(), ","), from, p.GetUpdate(), p.GetError())
		}
		if err := w.Flush(); err != nil {
			return err
		}
		for _, p := range list.GetPlugins() {
			if a := p.GetAgentAsk(); a != nil {
				fmt.Printf("\n%s asks to start %s in %s (%s): %s\n  mako plugins allow-agent %s once|always, or deny-agent %s\n",
					p.GetId(), a.GetAgent(), a.GetProject(), a.GetRoot(), oneLine(a.GetPrompt(), 200), p.GetId(), p.GetId())
			}
		}
		switch store := list.GetSecrets(); store {
		case "":
		case "file":
			fmt.Printf("\nSecrets: kept in a file of each plugin's that only you can read; this node has no keychain (%s).\n", list.GetSecretsNote())
		default:
			fmt.Printf("\nSecrets: kept in %s.\n", store)
		}
		return nil
	}
	op, rest := args[0], args[1:]
	if op == "init" {
		return initPlugin(rest)
	}
	if storeOps[op] {
		return pluginStore(ctx, c, op, rest)
	}
	if len(rest) == 0 {
		return fmt.Errorf("plugins %s needs a plugin's ID", op)
	}
	req := &protocol.PluginAction{PluginId: rest[0], Op: op}
	switch op {
	case protocol.PluginGrant, protocol.PluginDeny, protocol.PluginEnable, protocol.PluginDisable, protocol.PluginReload:
	case protocol.PluginRun:
		if len(rest) < 2 {
			return fmt.Errorf("plugins run needs a command")
		}
		req.Command = rest[1]
	case protocol.PluginSet:
		if len(rest) < 2 {
			return fmt.Errorf("plugins set needs a setting's key")
		}
		req.Key, req.Value = rest[1], strings.Join(rest[2:], " ")
	case "allow-agent", "deny-agent":
		// The ask answered is the one the plugin has waiting now.
		ask, err := waitingAsk(ctx, c, rest[0])
		if err != nil {
			return err
		}
		req.Op, req.Key = protocol.PluginDenyAgent, ask
		if op == "allow-agent" {
			if len(rest) < 2 || (rest[1] != protocol.AgentOnce && rest[1] != protocol.AgentAlways) {
				return fmt.Errorf("plugins allow-agent <id> once|always")
			}
			req.Op, req.Value = protocol.PluginAllowAgent, rest[1]
		}
	case "forget-agents":
		req.Op = protocol.PluginForgetAgents
	default:
		return fmt.Errorf("unknown plugins command %q", op)
	}
	done, err := c.PluginAction(ctx, req)
	if err != nil {
		return err
	}
	if done.GetOpenUrl() != "" {
		fmt.Println(done.GetOpenUrl())
	}
	return nil
}

// waitingAsk is the ID of the agent the plugin asks to start now.
func waitingAsk(ctx context.Context, c *client.Client, id string) (string, error) {
	list, err := c.Plugins(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range list.GetPlugins() {
		if p.GetId() == id && p.GetAgentAsk() != nil {
			return p.GetAgentAsk().GetId(), nil
		}
	}
	return "", fmt.Errorf("the plugin %s asks to start no agent now", id)
}

// oneLine is s on one line, cut at n bytes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

var scaffoldPluginID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// initPlugin scaffolds a starter plugin folder under mako's plugins/ directory
// (or a custom directory passed as the second argument).
func initPlugin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mako plugins init <id> [dir]")
	}
	id := args[0]
	if !scaffoldPluginID.MatchString(id) || strings.HasPrefix(id, "mako") {
		return fmt.Errorf("plugin id %q must be lowercase letters, digits and hyphens, and not start with mako", id)
	}
	var dir string
	if len(args) >= 2 {
		dir = args[1]
	} else {
		cfgDir, err := config.Dir()
		if err != nil {
			return err
		}
		dir = filepath.Join(cfgDir, "plugins", id)
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %s already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifest := fmt.Sprintf(`id = %q
version = "0.1.0"
publisher = "local"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "main"
title = %q
slots = ["right", "bottom"]

[[contributes.command]]
id = "ping"
title = "Ping %s"

[[contributes.tool]]
id = "status"
description = "Return the current status of the %s plugin"

[contributes.settings]
greeting = { type = "string", label = "Greeting", default = "Hello from %s" }
`, id, strings.ToUpper(id[:1])+id[1:], id, id, id)

	mainJS := fmt.Sprintf(`// Starter mako plugin: %s
let mako;

async function load(s) {
  const res = await mako.settings.getSetting({ key: "greeting" });
  s.greeting = res.setting && res.setting.text ? res.setting.text : "Hello";
  const saved = await mako.storage.getItem({ key: "count" });
  s.count = saved.found ? Number(saved.value) || 0 : 0;
}

function render(s, ui) {
  return ui.column([
    ui.card([
      ui.row([
        ui.icon("package", { tone: "accent" }),
        ui.text(s.greeting, { weight: "semibold" }).grow(),
        ui.pill(String(s.count), { tone: "accent" }),
      ], { gap: 8 }),
      ui.row([
        ui.button("Increment", { icon: "plus", tone: "accent", onTap: ["inc"] }),
        ui.button("Reset", { onTap: ["reset"] }),
      ], { gap: 8 }),
    ], { gap: 8 }),
  ], { gap: 8 });
}

export default {
  activate(m) {
    mako = m;
    mako.blocks.register("main", {
      state: { greeting: "", count: 0 },
      load,
      render,
      on: {
        async inc(s) {
          s.count += 1;
          await mako.storage.setItem({ key: "count", value: String(s.count) });
          await mako.log.write({ level: "info", message: "incremented count to " + s.count });
        },
        async reset(s) {
          s.count = 0;
          await mako.storage.setItem({ key: "count", value: "0" });
          await mako.commands.runCommand({ id: "mako.toast", arg: "Reset counter" });
        },
      },
    });
    mako.commands.register("ping", async () => {
      await mako.commands.runCommand({ id: "mako.toast", arg: "Pong from %s" });
    });
    mako.tools.register("status", () => {
      const s = mako.blocks.state("main");
      return { greeting: s.greeting, count: s.count };
    });
  },
};
`, id, id)

	jsConfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "checkJs": true
  }
}
`
	for name, content := range map[string]string{
		"plugin.toml":   manifest,
		"main.js":       mainJS,
		"jsconfig.json": jsConfig,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("Created plugin %s in %s\n", id, dir)
	fmt.Println("Next: allow permissions in Settings ▸ Plugins (or `mako plugins grant " + id + "`) and open the block from Settings ▸ Plugins or the command palette.")
	return nil
}
