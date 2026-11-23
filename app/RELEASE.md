Android 8.0+ · arm64-v8a and x86_64 · signed release APK

- **Protocol rev 45–47 compatibility**: updated Android and mobile node protobuf bindings for plugin assist (rev 45), bundled Git plugin & extended UI schema (rev 46: badges, tri-state checks, DAG rows, standalone chrome, folder scopes, and window intents), and P2P plugin/agent config sync (rev 47).
- **Mobile node & mesh reliability**: updated the embedded Go mobile core (`makocore.aar`) with protocol rev 47 support, P2P trust handshake race and ordering fixes, Antigravity subagent running-state derivation fixes, and expanded mobile unit test coverage.
- **Mesh compatibility**: pairs cleanly with desktop daemons running P2P plugin and agent config CRDT sync and bundled plugins.

Download the APK below. SHA256SUMS contains its checksum. This release uses a persistent release signing key. If a locally built debug APK is already installed, Android cannot update it with this differently signed APK: back up your app data before replacing it.
