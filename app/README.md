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
- **The terminal** is Termux's emulator and renderer, fed the session's
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
