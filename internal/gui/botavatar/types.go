// Package botavatar is a Go port of Open Bot Avatar
// (https://github.com/DominikScholz/open-bot-avatar, MIT License) tailored for
// ggui's native SMIL/CSS SVG animation engine.
//
// Each avatar definition is geometry-only: one monochrome silhouette path in a
// 0 0 100 100 viewBox plus an eye-pair placement. Runtime rendering infers a
// layered 3D volume, boosts eyes safely within the face room, derives OKLCH
// ribbon palettes from the body color, and emits self-animating SVG documents
// with named nodes (oba-root, oba-body, oba-eyes, oba-gaze) for interactive
// pointer tracking via widget.SVGController.
package botavatar

import "math"

// Eyes describes the eye pair placement inside the 0..100 viewBox.
type Eyes struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Spacing float64 `json:"spacing"`
	Scale   float64 `json:"scale"`
}

// Body holds the silhouette path of an avatar.
type Body struct {
	Path string `json:"path"`
}

// Definition follows the Open Bot Avatar v1 JSON contract.
type Definition struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Body    Body   `json:"body"`
	Eyes    Eyes   `json:"eyes"`
}

// State describes what the bot is doing.
type State string

const (
	StateIdle     State = "idle"
	StateThinking State = "thinking"
	StateWorking  State = "working"
	StateWaiting  State = "waiting"
	StateBlocked  State = "blocked"
	StateDone     State = "done"
)

// AllStates lists every supported avatar state in display order.
var AllStates = []State{
	StateIdle,
	StateThinking,
	StateWorking,
	StateWaiting,
	StateBlocked,
	StateDone,
}

// IsState reports whether s is a recognized avatar state.
func IsState(s State) bool {
	switch s {
	case StateIdle, StateThinking, StateWorking, StateWaiting, StateBlocked, StateDone:
		return true
	}
	return false
}

// Mood describes how the bot feels on top of any state.
type Mood string

const (
	MoodNeutral Mood = "neutral"
	MoodHappy   Mood = "happy"
)

// AllMoods lists every supported avatar mood.
var AllMoods = []Mood{MoodNeutral, MoodHappy}

// IsMood reports whether m is a recognized avatar mood.
func IsMood(m Mood) bool {
	return m == MoodNeutral || m == MoodHappy
}

// Options configures SVG generation for an avatar definition.
type Options struct {
	Color         string  // Body color (#rgb or #rrggbb). Empty uses default color.
	EyeColor      string  // Eye ink (#rgb or #rrggbb). Empty auto-picks high-contrast ink.
	State         State   // Default StateIdle.
	Mood          Mood    // Default MoodNeutral.
	Depth         float64 // Volume depth 0..14. Zero or negative uses DefaultDepth unless Flat is true.
	Flat          bool    // When true, renders flat (depth = 0).
	Seed          uint32  // Optional animation phase/ribbon seed. Zero hashes def.ID.
	ReducedMotion bool    // Emits a calm static SVG when true.
}

const (
	// DefaultColorHex is the Open Bot Avatar default orange.
	DefaultColorHex = "#ff6a1a"
	// DefaultEyeColor is the default dark eye ink.
	DefaultEyeColor = "#151515"
	// DefaultDepth is the natural implied volume depth.
	DefaultDepth = 6.0
	// MaxDepth caps implied volume depth.
	MaxDepth = 14.0
)

// HashString computes the 32-bit FNV-1a hash used by open-bot-avatar for seeds.
func HashString(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// Noise returns deterministic stateless hash noise in [0, 1).
func Noise(seed uint32, n int32) float64 {
	h := seed ^ uint32(n)*0x9e3779b1
	h = (h ^ (h >> 16)) * 0x85ebca6b
	h = (h ^ (h >> 13)) * 0xc2b2ae35
	h ^= h >> 16
	return float64(h) / 4294967296.0
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
