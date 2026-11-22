Android 8.0+ · arm64-v8a and x86_64 · signed release APK

- **Rich plugin UI v2 (rev 41–44)**: added native Compose rendering for `wrap`, `grid`, `split`, `collapsible`, `breadcrumb`, `tree`/`tree_item`, interactive pan/zoom `graph` canvases, `donut`/`pie`/bar charts, drag-and-drop interactions, `fill` roots, and clickable markdown links (`onLink`) for plugins such as Kanban and OKF Knowledge.
- **Plugin scopes & live sync**: added global, space, project (`repo:`-aware), and session scope routing (`BlockScope`) with live `WatchBlock` updates and P2P CRDT-synced plugin data across the mesh.
- **Plugin localization (i18n)**: sends the device locale on `WatchBlock`, `BlockEvent`, and `PluginAction` requests so plugins automatically display localized UI text.
- **Live agent activity in Chat**: surfaces real-time agent activity status in Chat alongside streaming responses.

Download the APK below. SHA256SUMS contains its checksum. This release uses a persistent release signing key. If a locally built debug APK is already installed, Android cannot update it with this differently signed APK: back up your app data before replacing it.
