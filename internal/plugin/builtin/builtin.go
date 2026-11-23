package builtin

import "embed"

// FS embeds the first-party plugins bundled inside Mako. Each bundled plugin
// lives in a top-level directory named for its plugin ID (e.g. "mako-git/").
//
//go:embed all:mako-git
var FS embed.FS
