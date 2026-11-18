package plugin

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
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
	Settings    []SettingDecl // in the file's order
}

// BlockDecl is a block a manifest contributes.
type BlockDecl struct {
	ID    string
	Title string
	Slots []string
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
var slots = []string{"left", "right", "top", "bottom"}

// The bounds of what a manifest declares.
const (
	maxPermissions = 16
	maxBlocks      = 16
	maxCommands    = 32
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
	settingKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	semver     = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})(-[0-9A-Za-z.-]{1,32})?$`)
	entryPath  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*\.js$`)
)

type rawManifest struct {
	ID          string   `toml:"id"`
	Version     string   `toml:"version"`
	Publisher   string   `toml:"publisher"`
	API         int      `toml:"api"`
	Entry       string   `toml:"entry"`
	Permissions []string `toml:"permissions"`
	Contributes struct {
		Block []struct {
			ID    string   `toml:"id"`
			Title string   `toml:"title"`
			Slots []string `toml:"slots"`
		} `toml:"block"`
		Command []struct {
			ID    string `toml:"id"`
			Title string `toml:"title"`
		} `toml:"command"`
		Settings map[string]struct {
			Type        string   `toml:"type"`
			Label       string   `toml:"label"`
			Description string   `toml:"description"`
			Options     []string `toml:"options"`
			Default     any      `toml:"default"`
		} `toml:"settings"`
	} `toml:"contributes"`
}

// ParseManifest reads and checks a manifest strictly: a key it does not
// know, a value of the wrong type, or anything out of its grammar or
// bounds is refused, saying what.
func ParseManifest(data []byte) (*Manifest, error) {
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
	case strings.HasPrefix(m.ID, "mako"):
		return nil, fmt.Errorf("id %q: IDs starting with mako are mako's own", m.ID)
	case !semver.MatchString(m.Version):
		return nil, fmt.Errorf("version %q is not major.minor.patch", m.Version)
	case !pluginID.MatchString(m.Publisher):
		return nil, fmt.Errorf("publisher %q is not lowercase letters, digits and hyphens", m.Publisher)
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
		for i, s := range b.Slots {
			if !slices.Contains(slots, s) || slices.Index(b.Slots, s) != i {
				return nil, fmt.Errorf("block %s: slot %q is not one of %s, once", b.ID, s, strings.Join(slots, ", "))
			}
		}
		m.Blocks = append(m.Blocks, BlockDecl{ID: b.ID, Title: b.Title, Slots: b.Slots})
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
		s := SettingDecl{Key: k[2], Type: r.Type, Label: r.Label, Description: r.Description, Options: r.Options, Default: r.Default}
		if err := checkSetting(&s); err != nil {
			return nil, err
		}
		m.Settings = append(m.Settings, s)
	}
	return m, nil
}

// checkEntry refuses an entry that is not a plain relative path to a .js
// file below the folder: no "..", no absolute path, no backslashes.
func checkEntry(e string) error {
	if len(e) > 128 || !entryPath.MatchString(e) || path.Clean(e) != e || strings.Contains(e, "..") {
		return fmt.Errorf("entry %q is not a relative path to a .js file in the plugin's folder", e)
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
	if strings.ContainsFunc(s, spoofing) {
		return fmt.Errorf("%s holds control or reordering characters", what)
	}
	return nil
}

// spoofing says r is a control character or one of Unicode's marks that
// reorder or hide text.
func spoofing(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	case r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF:
		return true
	}
	return false
}
