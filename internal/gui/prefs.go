package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ironpark/mako/internal/config"
)

// prefs are the desktop app's own settings, kept apart from the daemon's
// config.json in gui.json beside it. Equal keeps a signal holding it from
// notifying on a write that changes nothing.
type prefs struct {
	// Appearance
	Theme    string `json:"theme"`    // system, dark, light, or one of the user's themes (themes.go)
	Language string `json:"language"` // system, en or ko; see i18n.go
	// UsageOverlay shows an agent's token usage over its session, in the
	// corner UsageCorner names: top-left, top-right, bottom-left or
	// bottom-right.
	UsageOverlay bool   `json:"usage_overlay"`
	UsageCorner  string `json:"usage_corner"`
	// Background images, under the window and under each session.
	WindowBackdrop  backdrop `json:"window_backdrop"`
	SessionBackdrop backdrop `json:"session_backdrop"`

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
	// ApproveInApp has agents ask for approvals in the window rather than
	// their terminals while it is open.
	ApproveInApp bool `json:"approve_in_app"`
	// Notify tells the desktop when an agent out of view finishes or asks.
	Notify     bool   `json:"notify"`
	Daemon     string `json:"daemon"`      // socket of an external daemon; empty is the local one
	DaemonMode string `json:"daemon_mode"` // auto, embedded or external; see embedded.go

	// Files
	ShowHidden bool `json:"show_hidden"` // list dotfiles in the Files tab

	// The sidebar's sessions: by project, by host, or in one list.
	SidebarGroup string `json:"sidebar_group"`
	// The side panels' widths, as last dragged.
	SidebarWidth float64 `json:"sidebar_width,omitempty"`
	DetailsWidth float64 `json:"details_width,omitempty"`
	// Collapsed are the sidebar's groups folded to their headings, by how
	// it groups and the group: "project:<node>:<path>", "host:<node>".
	Collapsed []string `json:"collapsed,omitempty"`

	// How diffs are drawn: side by side, and with long lines wrapped.
	DiffSplit bool `json:"diff_split,omitempty"`
	DiffWrap  bool `json:"diff_wrap,omitempty"`

	// Pinned are the sessions the sidebar lists first.
	Pinned []string `json:"pinned,omitempty"`

	// RecentCommands are the palette's commands run lately, the latest
	// first, by their English labels.
	RecentCommands []string `json:"recent_commands,omitempty"`

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
		Theme:           "system",
		Language:        "system",
		UsageOverlay:    true,
		UsageCorner:     "top-right",
		WindowBackdrop:  backdrop{Opacity: defaultBackdropOpacity},
		SessionBackdrop: backdrop{Opacity: defaultBackdropOpacity},
		FontSize:        13,
		LineHeight:      1.4,
		CursorStyle:     "block",
		CursorBlink:     true,
		Scrollback:      10000,
		CopyOnSelect:    false,
		DefaultAgent:    "claude",
		DefaultProject:  home,
		ConfirmKill:     true,
		ApproveInApp:    true,
		Notify:          true,
		DaemonMode:      "auto",
		SidebarGroup:    groupProject,
	}
}

// Bounds the settings page offers, also applied to a hand-edited file.
const (
	minFontSize, maxFontSize     = 9, 24
	minLineHeight, maxLineHeight = 1.0, 2.0
	minScrollback, maxScrollback = 0, 100000
	defaultBackdropOpacity       = 0.25
)

func (p prefs) normalized() prefs {
	d := defaultPrefs()
	// A theme of the user's is checked when the window paints: paletteFor
	// takes one that is gone for the system's.
	if p.Theme == "" {
		p.Theme = d.Theme
	}
	for _, b := range []*backdrop{&p.WindowBackdrop, &p.SessionBackdrop} {
		b.Opacity = min(max(b.Opacity, 0), 1)
	}
	if _, ok := catalog(p.Language); !ok && p.Language != "system" {
		p.Language = d.Language
	}
	if _, ok := usageCornerLabels[p.UsageCorner]; !ok {
		p.UsageCorner = d.UsageCorner
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
	if p.SidebarWidth == 0 {
		p.SidebarWidth = sidebarWidth
	}
	p.SidebarWidth = min(max(p.SidebarWidth, minSidebarWidth), maxSidebarWidth)
	if p.DetailsWidth == 0 {
		p.DetailsWidth = detailsWidth
	}
	p.DetailsWidth = min(max(p.DetailsWidth, minDetailsWidth), maxDetailsWidth)
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
