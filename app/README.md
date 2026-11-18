# mako for Android

The Android app puts a phone on the mesh: it lists every session on your
computers, says when an agent there waits for an approval or has finished,
and opens a session's conversation and its terminal.

```
 ┌─────────── phone ───────────┐          ┌─────────── laptop ───────────┐
 │ app (Kotlin, Compose)       │          │ makod                        │
 │  └─socket─▶ node (Go, AAR) ─┼─libp2p──▶│ ├ claude  ~/api   (waiting)  │
 └─────────────────────────────┘          └──────────────────────────────┘
```

## How it works

- **A node of its own.** The app runs the same daemon `makod` is, built
  with gomobile from [`mobile`](mobile), its configuration and key in the
  app's private files. The phone joins the mesh over libp2p like any
  machine: found by mDNS on the same Wi-Fi, or linked to by an address you
  add, such as a Tailscale name.
- **The protocol, as the desktop speaks it.** The app talks to its node on
  the node's Unix socket, in the protocol's frames
  ([docs/protocol.md](../docs/protocol.md)), with messages generated from
  [`proto/mako/v2`](../proto/mako/v2) as Java lite
  ([`buf.gen.yaml`](android/buf.gen.yaml)).
- **Trust, one way.** A computer serves the phone once it lets the phone's
  key in. **Ask to join** in the app's list of computers sends the request,
  which mako's window on that computer, or `mako allow <node>`, allows.
  Or **Scan QR code** reads the pairing code the computer shows in
  Settings ▸ Access ▸ Pairing (or `mako pair` prints): the computer lets
  the phone in at once, and the phone keeps its address.
  The phone lets no one in: nothing on the mesh can run anything on it.
- **Chat on the phone.** Subagents start folded into one compact row showing
  active and ended counts. Expand it to open a subagent's conversation and
  tool activity, with active agents first, then go back to the main conversation. Type `/` to browse
  the session's slash commands; tapping one inserts it for you to send.
  Drafts survive tab switches, and a failed send keeps the text.
  Assistant messages render Markdown tables, clickable links, nested lists,
  quotes and code; wide tables and code blocks scroll horizontally.
  Tool calls in a row share one card, and a run of three or more folds into
  a line that counts them and names their tools, as on the desktop: all but
  the last run start folded.
- **Attach files.** The paperclip beside the composer picks files or photos,
  sends them to the session's computer (where they are kept for a day, as
  files dropped on the desktop are), and puts their paths in the draft for
  the agent. Images show as thumbnails over the composer, and in your
  messages, and open full size when tapped.
- **Links in the conversation.** A link to a path, absolute or relative to
  the session's project (`src/main.go:12`, `README.md#L40`), opens that file
  from the session's computer: images previewed, text at its line, folders
  in Project files. An address on that computer's loopback, such as
  `http://localhost:5173`, is forwarded through a tunnel to the same port
  on the phone (another, when that is taken) and opens in the browser; an
  image there is previewed in the app. Other addresses open as they are.
- **Answer questions.** A pending question opens the agent's live input in
  a panel over Chat. Tap to type, or use Enter, arrows and Space in the key
  bar for choices and multiple selections. The session menu's **Answer the
  agent** opens the panel when automatic detection is unavailable. Close
  the panel to return to the conversation without discarding the draft.
- **Project files.** Open **Project files** from the session menu to browse
  folders on that computer, show hidden files, preview text, Markdown and
  images, and copy a file path. Search by name or contents across the project;
  a content match opens at its line. **Git changes** lists changed files and
  opens a colored diff, including renames and deletions. Large text files and
  images have explicit preview limits.
- **Projects first.** Home lists the projects on your computers, as the
  desktop's sidebar has them where a workspace (docs/protocol.md,
  Workspaces) holds spaces, projects, names, looks and pins: a project of
  several folders is one, named and marked with the icon and colour given
  on the desktop; pinned projects and sessions come first; the rest go by
  name. Each row says how many of its sessions need you and how many
  work, how many there are and on which computers, and names the session
  it would open on. Over the list, **Needs you** gathers the sessions
  waiting for you from every space, an approval answered on its row; the
  title is the space shown, a menu of the others; **All**, **Needs you**
  and **Working** filter the projects. A long press pins or unpins a
  project, or starts a session in it. A computer that cannot be reached
  says so over the list, or in its place when none can.
- **A project is a workspace of tabs.** Opening a project shows one of its
  sessions, the one waiting for you, else the one looked at last, with a
  bar at the bottom as a phone's browser has: back to the projects
  (marked when a session elsewhere waits), the tab shown with how many
  there are (marked when another waits), a new tab, and the session's
  actions. Swipe along the bar's tab for the one beside it. Tap it for
  every tab as a card, saying what its session waits on or last said,
  and for the panels beside the project. The tabs are the sessions on
  the computers, in the order they started, pinned first, those ended
  last: a tab is not closed from the phone but by **Stop session…**,
  which ends its agent on its computer, after asking. Each tab opens on
  the view it was left on.
- **Spaces and projects, as the desktop reads them.** Without a
  workspace, or one new enough, the projects are the folders and
  repositories the sessions are in, each marked with the look its key
  draws.
  The phone's node lets no one in, so it shares no computer's workspace:
  the app reads one through it (`get_workspace` with the computer's
  `node_id`), and gives its pins to that computer (`workspace` with
  `to_node`), which takes edits from a node it lets in from revision 25
  on; against an older one the pins are read only. Of the computers that
  let the phone in, the first by node ID holding a workspace is shown;
  they normally hold the same one. If the phone's own node holds one, as
  it does once someone has made it and a computer let each other in,
  that comes first. With none, or none new enough, the projects are the
  folders, as above. The app reads the workspace again when the computers come
  or go, and when one's record says its workspace changed; a computer
  that does not say, reached through another or older, is read again
  with the sessions while the app is on screen.
  The reading is a port of `internal/workspace/view.go` and the
  desktop's grouping (`workspace/`, `ui/SessionGroups.kt`), the projects
  and tabs made of it are `ui/Projects.kt`, and the pins
  are the deltas a desktop window gives; the computer stamps them. The
  unit tests check both against fixtures the Go tests make
  (`src/test/resources/workspace`): `go test ./internal/workspace
  ./internal/gui -run Android` fails while they differ from what the Go
  code makes, and with `MAKO_ANDROID_FIXTURES=1` writes them again.
- **Panels.** A project's tabs list the panels beside it, the plugins'
  of the space shown and those its sessions' agents show, and home ends
  with **Panels of** the computer whose workspace shows, for them all: the blocks its plugins draw, as its
  window lays them out for the space shown, and the blocks its agents
  show (`show_ui`), each opening a page of its own, framed with the
  plugin or the agent it comes from: the page's top bar holds the app's
  own words, and the block's title, the block's to choose, sits in the
  frame's strip after its origin. A computer from revision 35 on lists its
  panels by their titles (`list_panels`), which the lists, home's row and
  a page's frame show before the block says its own; a plugin the computer
  lacks is listed last, dimmed, saying so. A block's charts, line, bar and
  sparkline, are drawn on the phone, their bounds checked again there
  (`ui/BlockChart.kt`); a kind newer than the app says it cannot be shown
  rather than leave a hole. A page coming back from the background
  with the link down keeps the tree it showed, under a note that it is
  reconnecting; a block whose plugin the computer does not have says so,
  and is not tried again until the page shows anew. The phone does not
  lay a window out: it shows the blocks of the document's
  `variants.compact`, or of the document folded, in order
  (docs/protocol.md, "Layout documents");
  it has screens of its own for the window's other panels. Taps, toggles,
  choices and a form's submit go to the computer, and the block's
  changes come back as they happen. The computer serves them to a node
  it lets in from revision 30 on. Allowing a plugin, enabling it or
  starting it again stays the computer's: a block that waits says so.
  The ops that change a block's tree are applied with a port of the
  daemon's (`blocks/UiOps.kt`), and the pages read with a port of
  `internal/layoutdoc` (`workspace/WindowLayout.kt`, its JSON read as
  Go's encoding/json reads it, `workspace/Json.kt`), both checked
  against fixtures the Go tests make (`src/test/resources/blocks`):
  `go test ./internal/uiblock ./internal/layoutdoc -run Android` fails
  while they differ, and with `MAKO_ANDROID_FIXTURES=1` writes them
  again.
- **Notifications.** The main screen's **Settings** controls approval,
  waiting and completed-turn alerts separately, including whether they appear
  while the app is open. Choices persist and take effect immediately. Android
  permissions and system notification settings are accessible there too.
- **App updates.** **Settings → App updates** checks stable `android-v*`
  GitHub releases, downloads their APK with progress, and verifies its SHA-256,
  package, version and signing certificate before opening Android's installer.
  Android asks for install permission if needed. Debug builds cannot be
  updated with the differently signed release APK.
- **Touch controls.** Swipe horizontally between Chat and Terminal; the
  next view follows your finger before you release. Swipe right again
  from Chat to reveal the projects with their filter and scroll
  position preserved. A compact switch in the session toolbar also changes
  views, replacing the back arrow and separate tab strip. The toolbar stays
  in place between Chat and Terminal; only the view under it moves. The terminal scrolls back with one finger vertically;
  use two fingers to pan or pinch to zoom, and double-tap to reset zoom and
  return to the live screen. Agent icons match the desktop throughout the app.
- **The terminal** bundles D2Coding 1.4.0 (the standard, non-ligature
  build) for rendering and measuring character cells, including Korean.
  The original font is from [NAVER's release](https://github.com/naver/d2-coding-font/releases/tag/VER1.4.0);
  its [SIL Open Font License](android/app/src/main/assets/licenses/D2Coding-OFL.txt)
  ships in the APK. Termux's emulator and renderer are fed the session's
  attach stream. It draws the session at the size its desktop window gave
  it, fitted to the phone's width, with pinch to zoom and drag to scroll
  back; **Fit to this screen** resizes the session itself.
- **Staying linked.** A foreground service keeps the node running while
  the app is away, and notifies of approvals, with **Allow** and **Deny**
  on the notification, and of turns that end. **Disconnect** on its
  notification stops it.

## Building

Go, gomobile (`go install golang.org/x/mobile/cmd/gomobile@latest`), a JDK
17 or later, and the Android SDK with an NDK and platform 37.

```sh
make android     # the node's AAR, then the debug APK
make install     # and onto the connected device
(cd android && ./gradlew :app:testDebugUnitTest)   # the unit tests, on the JVM
```

`make core` builds the node alone and `make proto` generates the messages
again after a schema change. The app's sources are in
[`android/app/src/main/java/dev/ironpark/mako`](android/app/src/main/java/dev/ironpark/mako):

    MakoApp, Mesh      the node and what the app knows of the mesh
    MeshService        the foreground service, and notifications
    blocks/            a block's tree followed, its ops applied, its form
    net/               frames on the node's socket, and its requests
    term/              an attached session, its terminal, and the keyboard
    ui/                the screens: projects, a project's tabs, a session, computers, a new session, panels
    workspace/         the workspace as the desktop reads it, its window layouts, and the edits the app gives

## Trying it on the emulator

The emulator reaches the computer it runs on at `10.0.2.2`. Run a daemon
there on a port of its own, add `10.0.2.2:<port>` as an address in the
app, tap **Ask to join**, and allow it:

```sh
MAKO_HOME=/tmp/mako-dev makod -listen :4300 &
MAKO_HOME=/tmp/mako-dev mako allow <the phone's node>
```

## CI releases

The Android workflow tests and builds pushes to `main` and pull requests.
Pushing an `android-vX.Y.Z` tag also builds the signed release APK and publishes
it with SHA-256 checksums to GitHub Releases. Update `app/RELEASE.md` before
tagging. The tag sets `versionName`; the workflow run number sets `versionCode`.

Release signing uses repository secrets `ANDROID_KEYSTORE_BASE64` (a base64
PKCS12 keystore containing alias `mako`) and `ANDROID_KEYSTORE_PASSWORD`.
Keep a secure backup of this key: subsequent APKs need the same signature.
Local release builds remain unsigned unless `ANDROID_KEYSTORE_PATH` and
`ANDROID_KEYSTORE_PASSWORD` are set; debug builds use Android's debug key.
