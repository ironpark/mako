Android 8.0+ · arm64-v8a and x86_64 · signed release APK

- A message sent to an agent reaches the agent itself: Claude Code showing a subagent or its transcript, or Codex showing its agents or a sub-agent's thread, is taken back to its own prompt first, rather than the message going to the subagent or being lost.
- A question Claude asks (AskUserQuestion) is answered in the desktop window's form; in the app, Allow hands it back to the agent's terminal, where it can be answered as before.
- The mako node inside the app is brought up to date with the desktop app's: a request's wait is bounded end to end across computers, an agent that exits is waited for and what it left swept up, and an attached terminal's stream keeps one reader once it ends.

Download the APK below. SHA256SUMS contains its checksum. This release uses a persistent release signing key. If a locally built debug APK is already installed, Android cannot update it with this differently signed APK: back up your app data before replacing it.
