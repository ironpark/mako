package gui

import (
	"embed"
	"image/color"
	"sync"

	"github.com/ironpark/ggui/ui/icons"
)

// Lucide icons (ISC, see icons/LICENSE), the set Paseo draws with. Only the
// ones the window uses are bundled.
//
//go:embed icons/*.svg
var iconFiles embed.FS

var (
	iconMu    sync.Mutex
	iconCache = map[string]*icons.SVG{}
)

// icon returns a bundled Lucide icon at 16px in the muted text colour.
func icon(name string) *icons.Widget {
	iconMu.Lock()
	svg, ok := iconCache[name]
	if !ok {
		var err error
		svg, err = icons.Load(iconFiles, "icons/"+name+".svg")
		if err != nil {
			panic("sanbo: missing icon " + name + ": " + err.Error())
		}
		iconCache[name] = svg
	}
	iconMu.Unlock()
	return icons.New(svg).Size(16).Color(tokMuted)
}

func iconColored(name string, c color.Color) *icons.Widget { return icon(name).Color(c) }

// agentIcon is the glyph a session row and tab show for an agent.
func agentIcon(agent string) string {
	switch agent {
	case "shell", "":
		return "square-terminal"
	case "claude":
		return "sparkles"
	case "codex":
		return "code-xml"
	default:
		return "bot"
	}
}
