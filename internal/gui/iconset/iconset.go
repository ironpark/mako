// Package iconset is the window's icons: Lucide's (ISC, see
// icons/LICENSE), the set Paseo draws with, and the agents' own
// (icons/agent-*.svg, drawn for mako). Only the ones the window uses are
// bundled.
package iconset

import (
	"embed"
	"sync"

	"github.com/ironpark/ggui/ui/icons"

	"github.com/ironpark/mako/internal/gui/skin"
)

// Files are the icons, as icons/<name>.svg.
//
//go:embed icons/*.svg
var Files embed.FS

var (
	mu      sync.Mutex
	cache   = map[string]*icons.SVG{}
	bundled sync.Map // name → whether it is bundled
)

// Icon is the bundled icon called name at 16px in the muted text colour.
// It panics on one that is not bundled: the names are the code's own.
func Icon(name string) *icons.Widget {
	mu.Lock()
	svg, ok := cache[name]
	if !ok {
		var err error
		svg, err = Load(name)
		if err != nil {
			mu.Unlock()
			panic("mako: missing icon " + name + ": " + err.Error())
		}
		cache[name] = svg
	}
	mu.Unlock()
	return icons.New(svg).Size(16).Color(skin.Muted)
}

// Load reads the bundled icon called name.
func Load(name string) (*icons.SVG, error) { return icons.Load(Files, "icons/"+name+".svg") }

// Has reports whether the icon called name is bundled.
func Has(name string) bool {
	if ok, seen := bundled.Load(name); seen {
		return ok.(bool)
	}
	_, err := Files.Open("icons/" + name + ".svg")
	bundled.Store(name, err == nil)
	return err == nil
}
