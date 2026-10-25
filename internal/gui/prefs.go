package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ironpark/sanbo/internal/config"
)

// prefs are the desktop app's own settings, kept apart from the daemon's
// config.json in gui.json beside it. Equal keeps a signal holding it from
// notifying on a write that changes nothing.
type prefs struct {
	// Appearance
	Theme    string `json:"theme"`    // system, dark or light
	Language string `json:"language"` // system, en or ko; see i18n.go

	// Terminal
	FontSize     float64 `json:"font_size"`
	LineHeight   float64 `json:"line_height"`  // multiple of the font size
	CursorStyle  string  `json:"cursor_style"` // block, bar or underline, until a program asks for another
	CursorBlink  bool    `json:"cursor_blink"`
	Scrollback   int     `json:"scrollback"` // lines kept per terminal
	CopyOnSelect bool    `json:"copy_on_select"`

	// General
	DefaultAgent   string `json:"default_agent"`
	DefaultProject string `json:"default_project"`
	ConfirmKill    bool   `json:"confirm_kill"`
	Daemon         string `json:"daemon"`      // address of an external daemon; empty is the local one
	DaemonMode     string `json:"daemon_mode"` // auto, embedded or external; see embedded.go

	// Files
	ShowHidden bool `json:"show_hidden"` // list dotfiles in the Files tab

	// The sidebar's sessions: by project, by host, or in one list.
	SidebarGroup string `json:"sidebar_group"`

	// Projects are the folders the sidebar keeps, so one outlives its last
	// session. A folder leaves the list from its own menu.
	Projects []fileRoot `json:"projects,omitempty"`
}

// Equal compares settings by value, projects included, so the prefs signal
// drops redundant writes.
func (p prefs) Equal(o prefs) bool { return reflect.DeepEqual(p, o) }

func defaultPrefs() prefs {
	home, _ := os.UserHomeDir()
	return prefs{
		Theme:          "system",
		Language:       "system",
		FontSize:       13,
		LineHeight:     1.4,
		CursorStyle:    "block",
		CursorBlink:    true,
		Scrollback:     10000,
		CopyOnSelect:   false,
		DefaultAgent:   "claude",
		DefaultProject: home,
		ConfirmKill:    true,
		DaemonMode:     "auto",
		SidebarGroup:   groupProject,
	}
}

// Bounds the settings page offers, also applied to a hand-edited file.
const (
	minFontSize, maxFontSize     = 9, 24
	minLineHeight, maxLineHeight = 1.0, 2.0
	minScrollback, maxScrollback = 0, 100000
)

func (p prefs) normalized() prefs {
	d := defaultPrefs()
	switch p.Theme {
	case "system", "dark", "light":
	default:
		p.Theme = d.Theme
	}
	if _, ok := catalog(p.Language); !ok && p.Language != "system" {
		p.Language = d.Language
	}
	switch p.DaemonMode {
	case "auto", "embedded", "external":
	default:
		p.DaemonMode = d.DaemonMode
	}
	switch p.SidebarGroup {
	case groupProject, groupHost, groupFlat:
	default:
		p.SidebarGroup = d.SidebarGroup
	}
	switch p.CursorStyle {
	case "block", "bar", "underline":
	default:
		p.CursorStyle = d.CursorStyle
	}
	if p.FontSize == 0 {
		p.FontSize = d.FontSize
	}
	p.FontSize = min(max(p.FontSize, minFontSize), maxFontSize)
	if p.LineHeight == 0 {
		p.LineHeight = d.LineHeight
	}
	p.LineHeight = min(max(p.LineHeight, minLineHeight), maxLineHeight)
	p.Scrollback = min(max(p.Scrollback, minScrollback), maxScrollback)
	if p.DefaultAgent == "" {
		p.DefaultAgent = d.DefaultAgent
	}
	if len(p.Projects) > 0 {
		seen := make(map[fileRoot]bool, len(p.Projects))
		projects := make([]fileRoot, 0, len(p.Projects))
		for _, r := range p.Projects {
			if r.Path == "" || seen[r] {
				continue
			}
			seen[r] = true
			projects = append(projects, r)
		}
		p.Projects = projects
	}
	return p
}

func prefsPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gui.json"), nil
}

// loadPrefs reads gui.json, falling back to the defaults for a missing file
// or field.
func loadPrefs() prefs {
	p := defaultPrefs()
	path, err := prefsPath()
	if err != nil {
		return p
	}
	if body, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(body, &p)
	}
	return p.normalized()
}

func savePrefs(p prefs) error {
	path, err := prefsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}
