package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// drivePreview renders the window as a driver says, a command at a time,
// for a harness that brings the daemon to a state the window shows (a
// plugin asking, a block drawn, a change staged) and has it rendered.
// The driver writes a command into sig/cmd, moved there whole; the
// preview takes it, carries it out, and writes it into sig/ack. The
// commands:
//
//	theme dark|light          the window's theme
//	settings <section>        Settings at a section: plugins, ...
//	workspace                 back to the sessions
//	wait <text>               until the window's text holds it (2 minutes)
//	install <id>              install a plugin from the store, as its button does
//	shot <name>               a PNG of the window, after it settles
//	done                      the end
func drivePreview(m *model, p func() *ggui.Probe, sig string, do func(func()) previewStep,
	frames func(int) previewStep, shot func(string) previewStep) []previewStep {
	cmdFile, ackFile := filepath.Join(sig, "cmd"), filepath.Join(sig, "ack")
	var (
		pending []previewStep // the command's own steps, run before the next is read
		waiting string
		since   time.Time
		ended   bool
	)
	ack := func(cmd string) previewStep {
		return do(func() { _ = os.WriteFile(ackFile, []byte(cmd), 0o644) })
	}
	step := func() (bool, error) {
		if len(pending) > 0 {
			done, err := pending[0]()
			if err != nil {
				return false, err
			}
			if done {
				pending = pending[1:]
			}
			return false, nil
		}
		if waiting != "" {
			if strings.Contains(p().Semantics().String(), waiting) {
				waiting = ""
				return false, os.WriteFile(ackFile, []byte("wait"), 0o644)
			}
			if time.Since(since) > 2*time.Minute {
				return false, fmt.Errorf("preview: timed out waiting for %q", waiting)
			}
			return false, nil
		}
		if ended {
			return true, nil
		}
		data, err := os.ReadFile(cmdFile)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			return false, nil
		}
		_ = os.Remove(cmdFile)
		cmd := strings.TrimSpace(string(data))
		verb, arg, _ := strings.Cut(cmd, " ")
		switch verb {
		case "theme":
			pending = []previewStep{do(func() { m.Prefs.Update(func(pr prefs) prefs { pr.Theme = arg; return pr }) }), frames(10), ack(cmd)}
		case "settings":
			pending = []previewStep{do(func() { m.openSettings(arg) }), frames(20), ack(cmd)}
		case "workspace":
			pending = []previewStep{do(func() { m.Page.Set("workspace") }), frames(10), ack(cmd)}
		case "wait":
			waiting, since = arg, time.Now()
		case "install":
			pending = []previewStep{do(func() { m.installPlugin(protocol.InstallOp, arg, "") }), frames(5), ack(cmd)}
		case "frames":
			n, _ := strconv.Atoi(arg)
			pending = []previewStep{frames(max(n, 1)), ack(cmd)}
		case "shot":
			pending = []previewStep{frames(30), shot(arg), ack(cmd)}
		case "done":
			ended = true
			_ = os.WriteFile(ackFile, []byte(cmd), 0o644)
		default:
			return false, fmt.Errorf("preview: unknown command %q", cmd)
		}
		return false, nil
	}
	return []previewStep{
		do(func() { m.Prefs.Update(func(pr prefs) prefs { pr.Theme = "dark"; return pr }) }),
		func() (bool, error) { return ggui.Peek(m.Conn).OK, nil },
		ack("ready"),
		step,
	}
}
