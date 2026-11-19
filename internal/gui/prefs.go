package gui

import (
	"encoding/json"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
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
	// PaneGlow marks the pane that has the keys with the accent round the
	// edge of what it shows, fading inward.
	PaneGlow bool `json:"pane_glow"`
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
	Notify bool `json:"notify"`
	// SleepIdle stops the programs of agents out of view that have rested
	// SleepAfter minutes, to resume when shown; see sleep.go.
	SleepIdle bool `json:"sleep_idle"`
	// OpenView is how a session's tab opens until it is switched: its
	// terminal, or its chat for an agent whose conversation mako reads.
	OpenView   string `json:"open_view"`
	SleepAfter int    `json:"sleep_after"`
	// Tray keeps the app in the menu bar when its window is closed, and
	// HideDock takes it out of the Dock meanwhile; see tray.go.
	Tray       bool   `json:"tray"`
	HideDock   bool   `json:"hide_dock"`
	Daemon     string `json:"daemon"`      // socket of an external daemon; empty is the local one
	DaemonMode string `json:"daemon_mode"` // auto, embedded or external; see embedded.go

	// Files
	ShowHidden bool `json:"show_hidden"` // list dotfiles in the Files tab
	// RecordStreams records every terminal stream received, a developer
	// option; see streamrec.go.
	RecordStreams bool `json:"record_streams,omitempty"`

	// The side panels' widths, as last dragged: the sizes this device
	// keeps of the window layout's sidebar and details nodes, 0 for the
	// layout's own. LayoutSizes are the sizes kept of its other nodes, by
	// ID; see winstate.go.
	SidebarWidth float64            `json:"sidebar_width,omitempty"`
	DetailsWidth float64            `json:"details_width,omitempty"`
	LayoutSizes  map[string]float64 `json:"layout_sizes,omitempty"`
	// SidebarSplit is how tall the sidebar's projects are over its
	// agents, as last dragged.
	SidebarSplit float64 `json:"sidebar_split,omitempty"`
	// How the sidebar lists agents: grouped (agentGroups), sorted
	// (agentSorts), on one line each when compact, and only those of a
	// status (agentFilters), "" for all.
	AgentGroup   string `json:"agent_group"`
	AgentSort    string `json:"agent_sort"`
	AgentCompact bool   `json:"agent_compact,omitempty"`
	AgentFilter  string `json:"agent_filter,omitempty"`
	// OldPinnedProjects and OldPinned are the pins as gui.json kept them
	// before the workspace shared them (spaceList's); read to move them
	// there, then dropped.
	OldPinnedProjects []scopeKey `json:"pinned_projects,omitempty"`

	// How diffs are drawn: side by side, and with long lines wrapped.
	DiffSplit bool `json:"diff_split,omitempty"`
	DiffWrap  bool `json:"diff_wrap,omitempty"`

	OldPinned []string `json:"pinned,omitempty"`

	// RecentCommands are the palette's commands run lately, the latest
	// first, by their English labels.
	RecentCommands []string `json:"recent_commands,omitempty"`

	// Projects are the folders the sidebar keeps, so one outlives its last
	// session. A folder leaves the list from its own menu.
	Projects []keptProject `json:"projects,omitempty"`
	// Spaces divide the sidebar's sessions by their repositories' owners;
	// Space is the one shown, 0 for every session. See spaces.go.
	// ShownSpace is the ID of the space the sidebar shows, "" for every
	// session. The spaces themselves are the workspace's (workspace.go).
	ShownSpace string `json:"shown_space,omitempty"`
	// The spaces as they were kept here before the workspace kept them,
	// read once to be moved there.
	OldSpaces []space `json:"spaces,omitempty"`
	OldSpace  int     `json:"space,omitempty"`
	// SyncWorkspace has the window follow, and be followed by, the
	// windows of the nodes it trusts both ways: tabs, panes and spaces.
	SyncWorkspace bool `json:"sync_workspace"`

	// Teams are the agent teams the user put together; see teams.go.
	Teams []agentTeam `json:"teams,omitempty"`
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
		WindowBackdrop:  defaultBackdrop(),
		SessionBackdrop: defaultBackdrop(),
		FontSize:        13,
		LineHeight:      1.4,
		CursorStyle:     "block",
		CursorBlink:     true,
		Scrollback:      10000,
		CopyOnSelect:    false,
		DefaultAgent:    "claude",
		DefaultProject:  home,
		ConfirmKill:     true,
		SyncWorkspace:   true,
		ApproveInApp:    true,
		Notify:          true,
		SleepAfter:      5,
		OpenView:        openTerminal,
		DaemonMode:      "auto",
		AgentGroup:      agentGroupFolder,
		AgentSort:       agentSortRecent,
	}
}

// Bounds the settings page offers, also applied to a hand-edited file.
const (
	minFontSize, maxFontSize     = 9, 24
	minLineHeight, maxLineHeight = 1.0, 2.0
	minScrollback, maxScrollback = 0, 100000
	minSleepAfter, maxSleepAfter = 1, 240 // minutes
	defaultBackdropOpacity       = 0.25
)

func (p prefs) normalized() prefs {
	d := defaultPrefs()
	// A theme of the user's is checked when the window paints: skin.Pick
	// takes one that is gone for the system's.
	if p.Theme == "" {
		p.Theme = d.Theme
	}
	p.WindowBackdrop, p.SessionBackdrop = p.WindowBackdrop.normalized(), p.SessionBackdrop.normalized()
	if _, ok := i18n.Catalog(p.Language); !ok && p.Language != "system" {
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
	if !slices.Contains(agentGroups, p.AgentGroup) {
		p.AgentGroup = d.AgentGroup
	}
	if !slices.Contains(agentSorts, p.AgentSort) {
		p.AgentSort = d.AgentSort
	}
	if !slices.Contains(agentFilters, p.AgentFilter) {
		p.AgentFilter = d.AgentFilter
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
	if p.OpenView != openChat {
		p.OpenView = openTerminal
	}
	if p.SleepAfter == 0 {
		p.SleepAfter = d.SleepAfter
	}
	p.SleepAfter = min(max(p.SleepAfter, minSleepAfter), maxSleepAfter)
	// A width of 0 is none kept: the window layout's applies.
	if p.SidebarWidth != 0 {
		p.SidebarWidth = min(max(p.SidebarWidth, minSidebarWidth), maxSidebarWidth)
	}
	if p.DetailsWidth != 0 {
		p.DetailsWidth = min(max(p.DetailsWidth, minDetailsWidth), maxDetailsWidth)
	}
	for id, size := range p.LayoutSizes {
		if !(size > 0) || math.IsInf(size, 0) {
			p.LayoutSizes = maps.Clone(p.LayoutSizes)
			delete(p.LayoutSizes, id)
		}
	}
	if p.SidebarSplit == 0 {
		p.SidebarSplit = sidebarSplit
	}
	p.SidebarSplit = min(max(p.SidebarSplit, minSidebarSplit), maxSidebarSplit)
	if p.DefaultAgent == "" {
		p.DefaultAgent = d.DefaultAgent
	}
	if len(p.Projects) > 0 {
		seen := make(map[fileRoot]bool, len(p.Projects))
		projects := make([]keptProject, 0, len(p.Projects))
		for _, r := range p.Projects {
			if r.Path == "" || seen[r.fileRoot] {
				continue
			}
			seen[r.fileRoot] = true
			projects = append(projects, r)
		}
		p.Projects = projects
	}
	return p
}

func prefsPath() (string, error) { return configFile("gui.json") }

// configFile is the path of a file of the window's in mako's folder.
func configFile(name string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
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

// prefsFile is gui.json as p is written down.
func prefsFile(p prefs) (path string, body []byte, err error) {
	if body, err = json.MarshalIndent(p, "", "  "); err != nil {
		return "", nil, err
	}
	path, err = configFile("gui.json")
	return path, append(body, '\n'), err
}

// savePrefs writes gui.json now; the window writes it through its
// fileWriter instead, off the UI goroutine.
func savePrefs(p prefs) error {
	path, body, err := prefsFile(p)
	if err != nil {
		return err
	}
	return writeWindowFile(path, body)
}
