package plugin

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

// APIVersion is the Host API version this build carries out; a manifest's
// api must be it.
const APIVersion = 1

// ManifestName is a plugin folder's manifest.
const ManifestName = "plugin.toml"

// maxManifest bounds a manifest's size, read before it is parsed.
const maxManifest = 64 << 10

// Manifest is a plugin's plugin.toml, checked.
type Manifest struct {
	ID          string
	Version     string
	Publisher   string
	API         int
	Entry       string   // the entry module, relative to the folder
	Permissions []string // as written, each checked
	Blocks      []BlockDecl
	Commands    []CommandDecl
	Tools       []ToolDecl
	Settings    []SettingDecl // in the file's order
}

// ToolDecl is an MCP tool a manifest contributes to running agents.
type ToolDecl struct {
	ID          string
	Description string
	Scope       string
	Params      []ToolParamDecl
}

// ToolParamDecl is one parameter of a contributed MCP tool.
type ToolParamDecl struct {
	Name        string
	Type        string // "string", "number", "boolean"
	Description string
	Required    bool
}

// Block chrome hints declared on [[contributes.block]].
const (
	BlockChromeAuto    = "auto"
	BlockChromeToolbar = "toolbar"
)

var blockChromes = []string{BlockChromeAuto, BlockChromeToolbar}

// BlockDecl is a block a manifest contributes.
type BlockDecl struct {
	ID     string
	Title  string
	Icon   string
	Slots  []string
	Scope  string
	Chrome string
	Hidden bool
}

// CommandDecl is a command a manifest contributes.
type CommandDecl struct {
	ID    string
	Title string
}

// SettingDecl is a setting a manifest declares. Default is a string, a
// bool or a float64, as Type takes; nil for none.
type SettingDecl struct {
	Key         string
	Type        string
	Label       string
	Description string
	Options     []string
	Default     any
	Scope       string
}

// The types of setting.
const (
	SettingString  = "string"
	SettingSecret  = "secret"
	SettingBoolean = "boolean"
	SettingNumber  = "number"
	SettingSelect  = "select"
)

var settingTypes = []string{SettingString, SettingSecret, SettingBoolean, SettingNumber, SettingSelect}

// slots are where a manifest may suggest a block goes.
var slots = []string{"left", "right", "top", "bottom", "sidebar", "tab"}

// The bounds of what a manifest declares.
const (
	maxPermissions = 16
	maxBlocks      = 16
	maxCommands    = 32
	maxTools       = 16
	maxToolParams  = 16
	maxSettings    = 32
	maxOptions     = 32
	maxTitle       = 80
	maxDescription = 300
)

var (
	// pluginID is a plugin's ID, and a publisher's: lowercase words of
	// letters and digits joined by hyphens, which are its folder's name
	// and part of its blocks' names.
	pluginID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	// partID is a block's or command's ID within its plugin.
	partID     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	toolID     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	settingKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	semver     = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})(-[0-9A-Za-z.-]{1,32})?$`)
	entryPath  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*\.(js|wasm)$`)
	iconNameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

var toolParamTypes = []string{"string", "number", "boolean"}

type rawManifest struct {
	ID          string   `toml:"id"`
	Version     string   `toml:"version"`
	Publisher   string   `toml:"publisher"`
	API         int      `toml:"api"`
	Entry       string   `toml:"entry"`
	Permissions []string `toml:"permissions"`
	Contributes struct {
		Block []struct {
			ID     string   `toml:"id"`
			Title  string   `toml:"title"`
			Icon   string   `toml:"icon"`
			Slots  []string `toml:"slots"`
			Scope  string   `toml:"scope"`
			Chrome string   `toml:"chrome"`
			Hidden bool     `toml:"hidden"`
		} `toml:"block"`
		Command []struct {
			ID    string `toml:"id"`
			Title string `toml:"title"`
		} `toml:"command"`
		Tool []struct {
			ID          string `toml:"id"`
			Description string `toml:"description"`
			Scope       string `toml:"scope"`
			Params      map[string]struct {
				Type        string `toml:"type"`
				Description string `toml:"description"`
				Required    bool   `toml:"required"`
			} `toml:"params"`
		} `toml:"tool"`
		Settings map[string]struct {
			Type        string   `toml:"type"`
			Label       string   `toml:"label"`
			Description string   `toml:"description"`
			Options     []string `toml:"options"`
			Default     any      `toml:"default"`
			Scope       string   `toml:"scope"`
		} `toml:"settings"`
	} `toml:"contributes"`
}

// ParseManifest reads and checks a manifest strictly: a key it does not
// know, a value of the wrong type, or anything out of its grammar or
// bounds is refused, saying what.
func ParseManifest(data []byte) (*Manifest, error) {
	return parseManifest(data, false)
}

func parseManifest(data []byte, bundled bool) (*Manifest, error) {
	if len(data) > maxManifest {
		return nil, fmt.Errorf("%s is over %d KiB", ManifestName, maxManifest>>10)
	}
	var raw rawManifest
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys %s", ManifestName, strings.Join(keys, ", "))
	}
	m := &Manifest{ID: raw.ID, Version: raw.Version, Publisher: raw.Publisher, API: raw.API, Entry: raw.Entry}
	switch {
	case !pluginID.MatchString(m.ID):
		return nil, fmt.Errorf("id %q is not lowercase letters, digits and hyphens", m.ID)
	case strings.HasPrefix(m.ID, "mako") && (!bundled || !strings.HasPrefix(m.ID, "mako-")):
		return nil, fmt.Errorf("id %q: IDs starting with mako are mako's own", m.ID)
	case !semver.MatchString(m.Version):
		return nil, fmt.Errorf("version %q is not major.minor.patch", m.Version)
	case !pluginID.MatchString(m.Publisher):
		return nil, fmt.Errorf("publisher %q is not lowercase letters, digits and hyphens", m.Publisher)
	case !bundled && m.Publisher == "mako":
		return nil, fmt.Errorf("publisher %q is mako's own", m.Publisher)
	case m.API != APIVersion:
		return nil, fmt.Errorf("api %d is not supported: this mako carries out api %d", m.API, APIVersion)
	}
	if err := checkEntry(m.Entry); err != nil {
		return nil, err
	}
	if m.Permissions, err = checkPermissions(raw.Permissions); err != nil {
		return nil, err
	}
	if len(raw.Contributes.Block) > maxBlocks {
		return nil, fmt.Errorf("more than %d blocks", maxBlocks)
	}
	for _, b := range raw.Contributes.Block {
		if !partID.MatchString(b.ID) {
			return nil, fmt.Errorf("block id %q is not lowercase letters, digits and hyphens", b.ID)
		}
		if slices.ContainsFunc(m.Blocks, func(o BlockDecl) bool { return o.ID == b.ID }) {
			return nil, fmt.Errorf("block %q twice", b.ID)
		}
		if err := checkText("block "+b.ID+" title", b.Title, maxTitle, true); err != nil {
			return nil, err
		}
		if b.Icon != "" && (len(b.Icon) > 48 || !iconNameRe.MatchString(b.Icon)) {
			return nil, fmt.Errorf("block %s: icon %q is not an icon's name", b.ID, b.Icon)
		}
		for i, s := range b.Slots {
			if !slices.Contains(slots, s) || slices.Index(b.Slots, s) != i {
				return nil, fmt.Errorf("block %s: slot %q is not one of %s, once", b.ID, s, strings.Join(slots, ", "))
			}
		}
		scope, err := normalizeScope(b.Scope)
		if err != nil {
			return nil, fmt.Errorf("block %s: %w", b.ID, err)
		}
		chrome := b.Chrome
		if chrome == "" {
			chrome = BlockChromeAuto
		}
		if !slices.Contains(blockChromes, chrome) {
			return nil, fmt.Errorf("block %s: chrome %q is not one of %s", b.ID, chrome, strings.Join(blockChromes, ", "))
		}
		if b.Hidden && !bundled {
			return nil, fmt.Errorf("block %s: hidden is only for bundled plugins", b.ID)
		}
		m.Blocks = append(m.Blocks, BlockDecl{ID: b.ID, Title: b.Title, Icon: b.Icon, Slots: b.Slots, Scope: scope, Chrome: chrome, Hidden: b.Hidden})
	}
	if len(raw.Contributes.Command) > maxCommands {
		return nil, fmt.Errorf("more than %d commands", maxCommands)
	}
	for _, c := range raw.Contributes.Command {
		if !partID.MatchString(c.ID) {
			return nil, fmt.Errorf("command id %q is not lowercase letters, digits and hyphens", c.ID)
		}
		if slices.ContainsFunc(m.Commands, func(o CommandDecl) bool { return o.ID == c.ID }) {
			return nil, fmt.Errorf("command %q twice", c.ID)
		}
		if err := checkText("command "+c.ID+" title", c.Title, maxTitle, true); err != nil {
			return nil, err
		}
		m.Commands = append(m.Commands, CommandDecl{ID: c.ID, Title: c.Title})
	}
	if len(raw.Contributes.Tool) > maxTools {
		return nil, fmt.Errorf("more than %d tools", maxTools)
	}
	for _, tl := range raw.Contributes.Tool {
		if !toolID.MatchString(tl.ID) {
			return nil, fmt.Errorf("tool id %q is not lowercase letters, digits and underscores", tl.ID)
		}
		if slices.ContainsFunc(m.Tools, func(o ToolDecl) bool { return o.ID == tl.ID }) {
			return nil, fmt.Errorf("tool %q twice", tl.ID)
		}
		if err := checkText("tool "+tl.ID+" description", tl.Description, maxDescription, true); err != nil {
			return nil, err
		}
		scope, err := normalizeScope(tl.Scope)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", tl.ID, err)
		}
		if scope == protocol.ScopeFolder {
			return nil, fmt.Errorf("tool %s: scope %q is not supported for tools", tl.ID, scope)
		}
		if len(tl.Params) > maxToolParams {
			return nil, fmt.Errorf("tool %s: more than %d params", tl.ID, maxToolParams)
		}
		decl := ToolDecl{ID: tl.ID, Description: tl.Description, Scope: scope}
		paramNames := make([]string, 0, len(tl.Params))
		for k := range tl.Params {
			paramNames = append(paramNames, k)
		}
		slices.Sort(paramNames)
		for _, k := range paramNames {
			rp := tl.Params[k]
			if !settingKey.MatchString(k) {
				return nil, fmt.Errorf("tool %s param %q: names are lowercase letters, digits and underscores", tl.ID, k)
			}
			typ := rp.Type
			if typ == "" {
				typ = "string"
			}
			if !slices.Contains(toolParamTypes, typ) {
				return nil, fmt.Errorf("tool %s param %s: type %q is not string, number or boolean", tl.ID, k, typ)
			}
			if err := checkText("tool "+tl.ID+" param "+k+" description", rp.Description, maxDescription, false); err != nil {
				return nil, err
			}
			decl.Params = append(decl.Params, ToolParamDecl{Name: k, Type: typ, Description: rp.Description, Required: rp.Required})
		}
		m.Tools = append(m.Tools, decl)
	}
	if len(raw.Contributes.Settings) > maxSettings {
		return nil, fmt.Errorf("more than %d settings", maxSettings)
	}
	// The settings are listed in the file's order, which the decoded map
	// does not keep.
	for _, k := range md.Keys() {
		if len(k) != 3 || k[0] != "contributes" || k[1] != "settings" {
			continue
		}
		r := raw.Contributes.Settings[k[2]]
		scope, err := normalizeScope(r.Scope)
		if err != nil {
			return nil, fmt.Errorf("setting %s: %w", k[2], err)
		}
		s := SettingDecl{Key: k[2], Type: r.Type, Label: r.Label, Description: r.Description, Options: r.Options, Default: r.Default, Scope: scope}
		if err := checkSetting(&s); err != nil {
			return nil, err
		}
		m.Settings = append(m.Settings, s)
	}
	return m, nil
}

// checkEntry refuses an entry that is not a plain relative path to a .js or
// .wasm file below the folder: no "..", no absolute path, no backslashes.
func checkEntry(e string) error {
	if len(e) > 128 || !entryPath.MatchString(e) || path.Clean(e) != e || strings.Contains(e, "..") {
		return fmt.Errorf("entry %q is not a relative path to a .js or .wasm file in the plugin's folder", e)
	}
	return nil
}

func checkSetting(s *SettingDecl) error {
	if !settingKey.MatchString(s.Key) {
		return fmt.Errorf("setting %q: keys are lowercase letters, digits and underscores", s.Key)
	}
	if !slices.Contains(settingTypes, s.Type) {
		return fmt.Errorf("setting %s: type %q is not one of %s", s.Key, s.Type, strings.Join(settingTypes, ", "))
	}
	if s.Label == "" {
		s.Label = s.Key
	}
	if err := checkText("setting "+s.Key+" label", s.Label, maxTitle, true); err != nil {
		return err
	}
	if err := checkText("setting "+s.Key+" description", s.Description, maxDescription, false); err != nil {
		return err
	}
	if (s.Type == SettingSelect) != (len(s.Options) > 0) {
		return fmt.Errorf("setting %s: options are for a select, which has them", s.Key)
	}
	if len(s.Options) > maxOptions {
		return fmt.Errorf("setting %s: more than %d options", s.Key, maxOptions)
	}
	for i, o := range s.Options {
		if err := checkText("setting "+s.Key+" option", o, maxTitle, true); err != nil {
			return err
		}
		if slices.Index(s.Options, o) != i {
			return fmt.Errorf("setting %s: option %q twice", s.Key, o)
		}
	}
	if s.Default == nil {
		return nil
	}
	ok := false
	switch v := s.Default.(type) {
	case string:
		ok = s.Type == SettingString || s.Type == SettingSelect && slices.Contains(s.Options, v)
		ok = ok && len(v) <= maxDescription
	case bool:
		ok = s.Type == SettingBoolean
	case int64:
		s.Default, ok = float64(v), s.Type == SettingNumber
	case float64:
		ok = s.Type == SettingNumber
	}
	if !ok {
		return fmt.Errorf("setting %s: default %v is not a %s's value", s.Key, s.Default, s.Type)
	}
	return nil
}

// checkText refuses text over max runes, empty when required, or holding
// control characters or the marks that reorder text, which could make a
// title read as something else.
func checkText(what, s string, max int, required bool) error {
	if required && strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if len([]rune(s)) > max {
		return fmt.Errorf("%s is over %d characters", what, max)
	}
	if strings.ContainsFunc(s, uiblock.Spoofing) {
		return fmt.Errorf("%s holds control or reordering characters", what)
	}
	return nil
}
