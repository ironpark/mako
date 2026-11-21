Android 8.0+ · arm64-v8a and x86_64 · signed release APK

- **Mako subagent support**: spawned subagents now group under their parent tab just like on desktop, with compact live status dots (`FamilyDotsPill`) on tabs and overview cards, an interactive switcher lane (`AgentFamilyLane`) under the session header to jump between lead and subagents, active subagent count in the working indicator, and parent breadcrumbs (`↳ <parent>`) in Home's `Needs you` list.
- **Natural back-navigation transitions**: removed scale-down distortion and spring settle tails from route transitions (`MakoMotion`, `Nav`) so pressing Back slides and fades cleanly without shrinking or lingering.
- **Cleaner Claude chat transcripts**: stripped Claude Code's `The <plugin> plugin sent a message:` wrapper and boilerplate footer from decoded user turns, keeping Mako inter-agent messages clean on mobile as well.

Download the APK below. SHA256SUMS contains its checksum. This release uses a persistent release signing key. If a locally built debug APK is already installed, Android cannot update it with this differently signed APK: back up your app data before replacing it.
