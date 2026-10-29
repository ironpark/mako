# Devices

A device attached to one node can be used from another: an Android phone
on the desktop's USB, from adb on a build box, and from an agent working
there. This is the design, what is built, and what is left.

## Contents

- [Words](#words)
- [The model](#the-model)
- [Where a lease lives](#where-a-lease-lives)
- [Android, through adb](#android-through-adb)
- [Using it](#using-it)
- [Trust](#trust)
- [Other kinds](#other-kinds)
- [Phases](#phases)

## Words

`internal/agent` calls what knows an agent a *driver*, so a device has
other words:

- A node **lends** the devices attached to it, of the kinds its
  configuration **shares**.
- A node **borrows** a device, for its own programs, under a **lease**.
- The device's node holds it for the borrower under a **claim**.

## The model

```
 node B, the device attached                node A, borrowing it
 ┌────────────────────────────────┐          ┌────────────────────────────────┐
 │ kind: lists devices, serves a  │  claim   │ lease: a port on the loopback  │
 │ connection to one              │◀────────▶│   ADB_SERVER_SOCKET=tcp:…      │
 │ claims: who holds what         │  device  │   ANDROID_SERIAL=…             │
 │                                │  conns   │ sessions started with it       │
 └────────────────────────────────┘          └────────────────────────────────┘
```

- **A kind** (`internal/device`) has two sides. On the device's node it
  lists the devices of its sort attached there, and serves one connection a
  borrower's program made to one. On the borrower it serves each
  connection made to the lease's port, through a link to the device's
  node, and says what environment tells the kind's tools where the device
  is. Each kind says whether a device of it is lent to one node at a time
  (exclusive) or to many.
- **A claim** is held on the device's node for the borrower. The borrower
  opens a stream with `claim` and keeps it open; the device's node holds
  the device while the stream is open and for **30 s** after it ends, so a
  dropped network does not hand the device to another node before the
  borrower claims it again. The device's node looks at the device every
  5 s and says on the stream when its state changes, or that it is
  detached.
- **A lease** is the borrower's side: a port on its loopback, and the
  environment that points the kind's tools at it. A connection made to the
  port goes to the device's node as a `device_conn` stream, under the
  claim, where the kind serves it, unless the borrower's side of the kind
  answers it itself, as adb's does its forwards.
- **A port of the device's node** can be listened on for the borrower
  (`device_listen`), for a device to reach the borrower through it, as adb
  reverse needs. Trust runs one way, so the device's node cannot open a
  stream to the borrower: the borrower waits for each connection there on a
  stream of its own (`device_accept`), and opens the next as one comes.
- Every stream is addressed to the device's node and goes through the
  daemons between, as attaches and tunnels do, so a device is reached
  wherever a request would be.

## Where a lease lives

A lease belongs to the **daemon of the node whose programs use the
device**, not to a window. Sessions outlive windows, and a session started
with a device keeps it while the window is closed.

So when the laptop drives an agent on the build box with the desktop's
phone, the build box borrows the phone: its daemon claims it from the
desktop, and lends it on its own loopback to the agent. The laptop only
asks. The build box must reach the desktop, along a chain of nodes each
letting in the one before, or it cannot borrow it.

A lease is held by those who borrowed it: by hand (`mako borrow`, or the
window's devices menu) and by each session started with the device.
Giving it back by hand leaves it to the sessions holding it, and a session
lets go as it ends; the lease ends when no one holds it, or its daemon
stops. Two sessions with one device share its lease.

While the claim's stream is down the lease is `reconnecting`, and the
daemon claims the device again, with the same claim ID, every 1 to 15 s;
the device's node resumes the claim if it has not lapsed. While the device
is not attached to its node the lease is `detached`, and held again once
it is. Leases are not saved: a session started with a device borrows it
again when it starts again.

A device is named across the mesh by its ref, `node/kind/serial`, or by
its node's name and serial, its serial, or its name. The borrowing node
resolves the name among the devices it reaches, so `mako -node box -device
pixel` means the pixel the build box sees.

## Android, through adb

adb has a server, on the node the device is attached to, and clients
that talk to it on a TCP port. A borrower's adb client is pointed, with
`ADB_SERVER_SOCKET`, at the lease's port, and each of its connections is
carried to a proxy on the device's node, in front of the adb server. The
proxy answers as a server with one device:

- **It lists that device alone.** `host:devices`, `devices-l`,
  `track-devices`, its binary form (`track-devices-proto-binary`, which
  Android Studio uses) and `list-forward` are filtered to the device lent.
  The text form of the protobuf listing is refused rather than passed
  unfiltered.
- **It sends to that device what was meant for any.** `transport-any`,
  `-usb` and `-local`, their `tport` forms, `host-usb:`, `host-local:`,
  and device requests with no device (`host:get-state`, `wait-for-…`) go
  to the device lent. A device service sent with no transport chosen,
  which the server would give any device, has the device's transport
  chosen first.
- **It refuses another device**, by serial or by transport ID.
- **It does not let the server be stopped.** `host:kill` is refused: the
  server belongs to the device's node and serves its other programs.
  `host:killforward-all`, which would remove every device's forwards
  there, is refused too.

The filter keeps a borrower to the device it asked for. It is not a
security boundary: see [Trust](#trust).

### Forwards

A forward or reverse listens where the borrower's programs are, as it
would with the device attached to the borrower:

- **`adb forward`** is answered on the borrower. It listens on the
  borrower's loopback, as the adb server would on its own, and opens each
  connection made there to the device as the server would: the device's
  transport, then the forward's remote service (`tcp:…`,
  `localabstract:…`, `jdwp:…`). `forward --list`, `--remove` and
  `--remove-all` are the borrower's own forwards. adb asks for a forward
  on a connection whose transport it chose first, and the borrower answers
  it there too. Only TCP ports are forwarded from the borrower.
- **`adb reverse`** to a TCP port has the device's node listen on a port of
  its own loopback for the borrower, and the device reverse to that port
  in place of the one asked for; each connection the device makes is
  carried back to the port asked for, on the borrower's loopback.
  `reverse --list` shows the borrower's port. A reverse to another kind of
  socket goes to the device's node as it is.

### What to know

- **adb versions must match.** A client that finds a server of another
  protocol version stops the server to start its own. The proxy refuses
  the stop, so the client fails instead. A lease's warnings say so when
  the borrower's adb differs from the device's server.
- **The lease's port must stay up.** With `ADB_SERVER_SOCKET` on a local
  port and nothing answering, an adb client starts a server of its own
  there, which sees no devices. The daemon holds the port for as long as
  the lease lasts.
- **Android Studio** takes a manually managed server by its port alone on
  localhost: the lease's port, which `ANDROID_ADB_SERVER_PORT` also names.
  Whether its debugger attaches through the proxy is not yet tried.
- **Tried with an emulator.** Through two daemons, adb 36.0.0's client and
  server with an Android 16 emulator: `devices`, `shell`, `push`,
  `logcat`, `forward` (listening on the borrower only), `reverse` (reaching
  a server on the borrower), their `--list` and `--remove-all`, and
  `kill-server` refused. Not yet `install` or a phone on USB.
- **The adb server starts on demand.** A node that shares adb devices and
  finds no server running runs `adb start-server`, at most every 30 s,
  when its server is the default one.

## Using it

On the node the device is attached to, share its kind in `config.json`:

```json
"devices": {"share": ["adb"]}
```

`adb_server` names another server than `127.0.0.1:5037`. A node lists its
devices of every kind, shared or not, so a borrower can be told why one is
not lent.

From any node:

```sh
mako devices                           # the mesh's devices
eval "$(mako borrow pixel)"            # borrow one here, and set the environment
adb shell                              # reaches the phone on the other node
mako -device pixel run claude          # start an agent that has the device
mako -node box -device pixel run claude  # ...on another node, which borrows it
mako leases                            # the devices this node borrows
mako return pixel                      # give it back
```

A device is named by its serial, its name (model), `node/serial`, or its
full ref, `node-id/kind/serial`. `mako return` gives back what was
borrowed by hand; a session's devices go back as it ends.

The desktop app has a devices menu in the sidebar's header while another
node lends a device or this one borrows one: it borrows a device for this
node, copies a lease's environment as `export` lines, and gives one back.

## Trust

A node that lets another in already lets it run programs, `adb` among
them. Lending adds no new power, so it is not authorized again. What
lending adds is care: `devices.share` keeps a node's devices to itself
until it says otherwise, an exclusive kind's device is lent to one node at
a time, and the device's node lists who borrows each.

## Other kinds

Phase 1 is adb. These are the kinds that fit the same model, and what
limits each.

| Kind | How | Limits |
|---|---|---|
| Serial port | The device's node opens the port and speaks RFC 2217 to the borrower's tools (`rfc2217://localhost:port`, which esptool and pyserial take) | Exclusive. A pty would lose DTR and RTS, which esptool and avrdude toggle to reset a board, and baud rate changes, so a pty is for watching output only. Listing ports on macOS without cgo means reading `/dev/cu.*`. |
| iOS Simulator | The Mac runs the simulator; the borrower sees its screen and sends touches, in a window tab and as MCP tools | Only a Mac lends one. Screen capture and synthesized clicks need a logged-in desktop, so a headless Mac needs `simctl` with HID injection through CoreSimulator's private interface, in a helper mako would carry, since idb is little maintained. |
| iOS device | The device's node's usbmuxd socket, lent with `USBMUXD_SOCKET_ADDRESS` | libimobiledevice's tools honour it and Xcode does not. From iOS 17, debugging goes through RemoteXPC tunnels, which this does not carry: install, logs and files only. |
| Bluetooth LE | GATT operations as requests (scan, connect, read, write, notify), and as MCP tools | No virtual adapter, except on Linux, where `/dev/vhci` could stand for a Linux node's adapter. |
| Any USB device | USB/IP | Borrowed only on Linux (vhci_hcd) or Windows (usbip-win2): macOS has no USB/IP client without a DriverKit entitlement. Lent by Linux through `usbip-host`; elsewhere it needs libusb, and so cgo. |

Agents get a device as tools too: a screenshot, a tap, logcat, a serial
read, as MCP servers of the session. That is how a coding agent checks
its work on a device on another node.

## Phases

1. **Done:** claims and leases between daemons; adb's proxy; `mako
   devices`, `borrow`, `return`, `leases`, and `run -device`; sessions
   started with devices.
2. **Done:** adb forwards and reverses on the borrower; leases held by
   sessions and by hand; the device's node saying when a device changed or
   went; devices named and resolved by the borrowing node; the window's
   devices menu.
3. Serial ports, through RFC 2217; choosing a session's devices in the
   window.
4. The iOS Simulator in a window tab, and device tools for agents.
5. usbmuxd, Bluetooth LE.
6. USB/IP, borrowed on Linux.
