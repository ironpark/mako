package gui

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/fonts/gofont"

	"github.com/ironpark/sanbo/internal/config"
)

// Preview renders the window against a live daemon into PNGs in dir,
// without showing it, for reviewing layout: the workspace with a session
// attached, the dialogs, the settings pages, and the light theme. It is a
// development aid; the daemon should be running a session or two. The theme
// setting it switches through is put back when it is done.
func Preview(cfg *config.Config, addr, dir string, width, height int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		m      *model
		p      *ggui.Probe
		img    *ggfx.Image
		window *ggfx.Window
		steps  []previewStep
		since  time.Time
	)
	shot := func(name string) previewStep {
		return func() (bool, error) {
			img.Clear()
			p.Draw(img)
			f, err := os.Create(filepath.Join(dir, name+".png"))
			if err != nil {
				return false, err
			}
			defer f.Close()
			return true, png.Encode(f, img)
		}
	}
	do := func(fn func()) previewStep { return func() (bool, error) { fn(); return true, nil } }
	frames := func(n int) previewStep {
		left := n
		return func() (bool, error) {
			time.Sleep(20 * time.Millisecond)
			left--
			return left <= 0, nil
		}
	}
	until := func(what string, cond func() bool) previewStep {
		return func() (bool, error) {
			if since.IsZero() {
				since = time.Now()
			}
			if cond() {
				since = time.Time{}
				return true, nil
			}
			if time.Since(since) > 10*time.Second {
				return false, fmt.Errorf("preview: timed out waiting for %s", what)
			}
			return false, nil
		}
	}
	setTheme := func(theme string) func() {
		return func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = theme; return p }) }
	}

	return ggfx.Run(ggfx.HandlerFunc(func(ev ggfx.Event) error {
		switch ev.(type) {
		case ggfx.StartEvent:
			w, err := ggfx.NewWindow(&ggfx.WindowOptions{Title: "sanbo preview", Width: 64, Height: 64, Hidden: true})
			if err != nil {
				return err
			}
			window = w
			// A probe draws in Go Regular; Go Mono stands in for the
			// platform's monospaced font so the grid lines up as it will.
			ggui.SetDefaultMonoFont(gofont.Mono())
			m = newModel(cfg, addr)
			saved := ggui.Peek(m.Prefs)
			p = ggui.ProbeBuilder(func() ggui.Widget { return ggui.Provide(ggui.ReducedMotionKey, true, build(m)) },
				ggui.Sz(float64(width), float64(height)))
			p.Setup(func() {
				setup(m)
				m.start(ctx, p.Post)
			})
			img = ggfx.NewImage(width, height)
			steps = []previewStep{
				do(setTheme("dark")),
				until("the daemon", func() bool { return ggui.Peek(m.Conn).OK || ggui.Peek(m.Conn).Err != "" }),
				frames(2),
				shot("01-empty"),
				do(func() {
					if len(ggui.Peek(m.Sessions)) == 0 { // a fresh daemon, as the app's own is
						home, _ := os.UserHomeDir()
						m.launch("shell", "", home)
					}
				}),
				until("a session", func() bool { return len(ggui.Peek(m.Sessions)) > 0 }),
				do(func() {
					sessions := ggui.Peek(m.Sessions)
					for i := len(sessions) - 1; i >= 0; i-- {
						m.selectSession(sessions[i].ID)
					}
				}),
				until("a terminal", func() bool { return slices.Contains(ggui.Peek(m.Attached), ggui.Peek(m.Selected)) }),
				frames(30), // the resize reaches the program and its redraw comes back
				shot("02-session"),
				do(func() { m.DetailsOpen.Set(true) }), frames(2), shot("03-details"),
				do(func() { m.DetailsOpen.Set(false); m.openNew("", "") }), frames(2), shot("04-new-session"),
				do(func() { m.NewOpen.Set(false); m.openSettings("general") }), frames(2), shot("05-settings-general"),
				do(func() { m.Section.Set("appearance") }), frames(2), shot("06-settings-appearance"),
				do(func() { m.Section.Set("terminal") }), frames(2), shot("07-settings-terminal"),
				do(func() { m.Section.Set("daemon") }), frames(2), shot("08-settings-daemon"),
				do(func() { m.Section.Set("host") }), frames(2), shot("08-settings-host"),
				do(func() { m.Section.Set("agents") }), frames(2), shot("08-settings-agents"),
				do(func() { m.Section.Set("shortcuts") }), frames(2), shot("09-settings-shortcuts"),
				do(setTheme("light")), frames(2), shot("10-light-settings"),
				do(func() { m.Page.Set("workspace") }), frames(10), shot("11-light-session"),
				do(func() { m.Prefs.Set(saved) }), frames(2),
			}
		case ggfx.FrameEvent:
			p.Frame()
			if len(steps) == 0 {
				m.shutdown()
				p.Close()
				return ggfx.Termination
			}
			done, err := steps[0]()
			if err != nil {
				m.shutdown()
				p.Close()
				return err
			}
			if done {
				steps = steps[1:]
			}
			window.RequestFrame()
		}
		return nil
	}), nil)
}

// previewStep runs on a frame and reports whether it is finished.
type previewStep func() (done bool, err error)
