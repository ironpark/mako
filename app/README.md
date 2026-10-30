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
  from Chat to reveal the main session list with its filter and scroll
  position preserved. A compact switch in the session toolbar also changes
  views, replacing the back arrow and separate tab strip. The terminal scrolls back with one finger vertically;
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
```

`make core` builds the node alone and `make proto` generates the messages
again after a schema change. The app's sources are in
[`android/app/src/main/java/dev/ironpark/mako`](android/app/src/main/java/dev/ironpark/mako):

    MakoApp, Mesh      the node and what the app knows of the mesh
    MeshService        the foreground service, and notifications
    net/               frames on the node's socket, and its requests
    term/              an attached session, its terminal, and the keyboard
    ui/                the screens: sessions, a session, computers, a new session

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
