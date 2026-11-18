# mako

**Run coding agents on multiple computers and manage them from one window.**

[한국어](README.ko.md)

mako connects your computers into a private mesh. Start Codex on a build box, review its changes from your laptop, or approve a Claude Code tool call from your phone. The daemon on each host owns its sessions, so you can close the client and reconnect to the same terminal later.

## Quick start

1. On each computer, build from the repository root with the Go version specified in [go.mod](go.mod). See the [build guide](DEVELOP.md#building) for details.

   ```sh
   make build
   ./bin/mako-gui
   ```

   By default, the desktop app uses an existing daemon or starts one inside the app. On a headless host, run `./bin/makod`.

2. Open **Settings → Nodes** and choose **Ask to be let in** next to the other computer. On that computer, open **Settings → Access** and select **Allow**. Nodes discover each other on the local network or your Tailscale network. You can also configure peer addresses manually. [Networking settings](docs/usage.md#configuration)
3. Choose an agent in the tab bar to start a session. From the repository root, you can also use the CLI.

   ```sh
   ./bin/mako nodes
   ./bin/mako agents
   ./bin/mako run claude
   ./bin/mako -node <node-id> run codex
   ./bin/mako ls
   ./bin/mako attach <session-id>
   ```

## What you can do

- **Manage sessions across hosts.** Group sessions by project or host, pin them, and open them in tabs or split panes. Reattach after closing the app or losing the connection.
- **Use a terminal or Chat.** Every session has a Ghostty-based terminal. Compatible agents also offer a Chat view with Markdown replies, tool calls, and agent activity. Claude Code and Codex can show tool approval cards in the app. [Desktop app reference](docs/usage.md#the-desktop-app)
- **Work with remote projects.** Browse and edit files, review diffs, create Git worktrees, open pull requests and issues from GitHub, Forgejo, or Gitea, and open remote `localhost` links through port forwarding. [Desktop app reference](docs/usage.md#the-desktop-app)
- **Connect from Android.** Start remote sessions, open Chat or the terminal, and respond to supported approvals from notifications. [Android app and build instructions](app/README.md)
- **Borrow Android devices.** Use `adb` with an Android device or emulator shared by another node, or lend it to an agent session. [Device sharing guide](docs/devices.md)

## Support

| Area | Status |
|---|---|
| macOS, Linux | macOS is used daily. Linux is built and tested. |
| Windows | Experimental. ConPTY sessions build and their tests compile. Runtime verification is pending. |
| Claude Code, Codex, shell | Stable |
| Antigravity (`agy`) | Beta |
| pi, oh-my-pi, standalone OmO, OpenCode and its OmO plugin | Experimental |

Chat, hooks, approvals, MCP, and session resumption vary by agent. [Agent support matrix and limitations](docs/usage.md#agents)

## Access and trust

Approving a node lets it start programs and read or write files on the approving host. Access is directional and can extend through a chain of trusted hosts. For example, if a desktop accepts your laptop and a build box accepts the desktop, the laptop can reach the build box through the desktop. Nodes can also establish trust through an SSH key. [Access and network reference](docs/usage.md#security)

## Documentation

- [Usage reference](docs/usage.md) covers the desktop app, agent support, SSH trust, configuration, and prompt templates.
- [Project agents over MCP](docs/agent-mcp.md) covers agent discovery and messaging.
- [Development guide](DEVELOP.md) covers builds, architecture, and tests.
- [Protocol reference](docs/protocol.md) describes client and daemon communication.
