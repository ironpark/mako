# Agent Map (`agent-map`)

Live central-content-area multi-machine topology map of every machine, project, agent, and subagent running in Mako.

## What it demonstrates

- **Left sidebar menu → central content area (`slots = ["sidebar"]`, `icon = "network"`)**: Adds **Agent Map** to the left sidebar and fills the central workspace with the interactive topology canvas and split inspector.
- **Multi-machine mesh & enriched sessions (`sessions:read`)**: Combines `mako.sessions.listSessions()` with `mako.mesh.listNodes()` and `mako.mesh.listSessions()` to build the `Machine → Project → Agent → Subagent` hierarchy, including `parentSessionId` subagent edges, `branch`, `profile`, `approvalTool` warning beacons, and `startedAt` / `lastActivityAt` timestamps.
- **Interactive card graph (`ui.graph`, `variant: "cards"`, `minimap: true`)**: Renders rich card nodes grouped into cluster hulls, supports `force` / `hierarchy` / `radial` layouts, 60fps live status pulses & active message/subagent edges, drag-drop node coordinate persistence (`storage`), double-tap session navigation (`onDoubleTap`), and right-click context menu actions (`Open session`, `Send message`, `Run subagent`, `Kill session`).
- **Session actions, project lookup & storage (`storage`, `sessions:write`, `fs:project`)**: Persists dragged node coordinates in `ctx.storage` (resilient to corrupt JSON), resolves local project roots via `mako.files.listProjects()`, sends messages via `mako.messages.sendMessage`, spawns child subagents with `parentSessionId` via `mako.messages.startAgent`, and stops plugin-owned sessions via `mako.messages.stopAgent`.
- **Bilingual EN/KO UI (`mako.i18n`, `ctx.t`)**.
