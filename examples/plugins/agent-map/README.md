# Agent Map (`agent-map`)

Live central-content-area topology view of every project and agent session running in Mako.

## What it demonstrates

- **Left sidebar menu → central content area (`slots = ["sidebar"]`, `icon = "network"`)**: Adds an **Agent Map** entry to the top of the left sidebar above Projects. Clicking it keeps the sidebar visible and fills the entire central workspace area with the interactive topology map.
- **Live sessions & events (`sessions:read`)**: Lists all sessions via `mako.sessions.listSessions()` and subscribes to `sessionStarted`, `sessionEnded`, `sessionSlept`, `sessionStatus`, and `agentMessage` for live updates and inter-agent message edges.
- **Interactive force-directed graph (`ui.graph`)**: Groups sessions around project hubs, colors nodes by live status (`working`, `waiting`, `done`, `stopped`), and pairs the map with a split inspector pane (`ui.split`).
- **One-click session navigation (`ctx.ui.openSession`)**: Clicking **Jump to session** exits the map view and opens that agent's session tab in the workspace.
- **Bilingual EN/KO UI (`mako.i18n`, `ctx.t`)**.
