// Package decision stores AI provider API keys securely and evaluates
// structured decision-model questions across TypeSafe AI, OpenRouter, OpenAI,
// and Google AI Studio.
package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/pkg/fsutil"
)

const (
	ProviderOpenRouter = "openrouter"
	ProviderTypeSafe   = "typesafe"
	ProviderOpenAI     = "openai"
	ProviderGoogle     = "google"
)

// Providers lists supported API key providers in display order.
var Providers = []string{
	ProviderOpenRouter,
	ProviderTypeSafe,
	ProviderOpenAI,
	ProviderGoogle,
}

// DefaultModels names each provider's default decision model.
var DefaultModels = map[string]string{
	ProviderTypeSafe:   "jev-latest",
	ProviderOpenRouter: "typesafe/jev-latest",
	ProviderOpenAI:     "gpt-4.1-mini",
	ProviderGoogle:     "gemini-2.5-flash",
}

// maxSecret bounds a key length to fit every platform keychain (Windows
// Credential Manager caps entries at 2560 bytes).
const maxSecret = 2048

// NormalizeProvider maps aliases such as "typesafe.ai" or "gemini" to their
// canonical provider ID, returning "" when unknown.
func NormalizeProvider(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openrouter", "openrouter.ai":
		return ProviderOpenRouter
	case "typesafe", "typesafe.ai", "typesafe-ai":
		return ProviderTypeSafe
	case "openai", "api.openai.com":
		return ProviderOpenAI
	case "google", "gemini", "google-ai-studio", "google_ai_studio", "aistudio", "google-ai":
		return ProviderGoogle
	}
	return ""
}

// EnvKey looks up a provider's environment-variable API key fallback.
func EnvKey(provider string) (string, bool) {
	switch NormalizeProvider(provider) {
	case ProviderOpenRouter:
		if v := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); v != "" {
			return v, true
		}
	case ProviderTypeSafe:
		if v := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); v != "" {
			return v, true
		}
	case ProviderOpenAI:
		if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
			return v, true
		}
	case ProviderGoogle:
		for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// Vault keeps provider API keys in the system keychain, falling back to a
// mode-0600 file when no keychain is available.
type Vault struct {
	kc      keychain.Keychain
	note    string
	service string
	index   string
	file    string

	mu     sync.Mutex
	loaded bool
	names  map[string]bool
	values map[string]string
}

// NewVault creates a Vault rooted in the daemon's configuration directory.
func NewVault(dir string, kc keychain.Keychain, note string) *Vault {
	return &Vault{
		kc:      kc,
		note:    note,
		service: keychainService(dir),
		index:   filepath.Join(dir, "provider-keys-index.json"),
		file:    filepath.Join(dir, "provider-keys.json"),
	}
}

func keychainService(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	sum := sha256.Sum256([]byte(abs))
	return "mako providers (" + hex.EncodeToString(sum[:4]) + ")"
}

// SecretStore reports which secret backend holds provider keys ("file" or the
// keychain's name) and an optional note when no keychain is available.
func (v *Vault) SecretStore() (string, string) {
	if v.kc == nil {
		return "file", v.note
	}
	return v.kc.Name(), ""
}

func accountFor(provider string) string { return "key:" + provider }

func (v *Vault) loadLocked() {
	if v.loaded {
		return
	}
	v.loaded = true
	v.names, v.values = map[string]bool{}, map[string]string{}
	var file map[string]string
	if b, err := os.ReadFile(v.file); err == nil {
		_ = json.Unmarshal(b, &file)
	}
	if v.kc == nil {
		for k, val := range file {
			v.names[k], v.values[k] = true, val
		}
		return
	}
	var names []string
	if b, err := os.ReadFile(v.index); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	for _, n := range names {
		v.names[n] = true
	}
	if len(file) == 0 {
		return
	}
	left := map[string]string{}
	for k, val := range file {
		if err := v.kc.Set(v.service, k, val); err != nil {
			left[k] = val
			continue
		}
		v.names[k], v.values[k] = true, val
	}
	if v.saveIndexLocked() != nil {
		return
	}
	_ = v.writeFileLocked(left)
}

func (v *Vault) saveIndexLocked() error {
	names := make([]string, 0, len(v.names))
	for n := range v.names {
		names = append(names, n)
	}
	sort.Strings(names)
	b, _ := json.Marshal(names)
	if err := os.MkdirAll(filepath.Dir(v.index), 0o700); err != nil {
		return err
	}
	return fsutil.Write(v.index, b)
}

func (v *Vault) writeFileLocked(values map[string]string) error {
	if len(values) == 0 {
		if err := os.Remove(v.file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.file), 0o700); err != nil {
		return err
	}
	return fsutil.Write(v.file, b)
}

// Has reports whether a key for provider is persisted in the vault.
func (v *Vault) Has(provider string) bool {
	p := NormalizeProvider(provider)
	if p == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	return v.names[accountFor(p)]
}

// Get reads provider's persisted key without consulting environment variables.
func (v *Vault) Get(provider string) (string, bool, error) {
	p := NormalizeProvider(provider)
	if p == "" {
		return "", false, fmt.Errorf("unknown provider %q", provider)
	}
	acct := accountFor(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if !v.names[acct] {
		return "", false, nil
	}
	if val, ok := v.values[acct]; ok {
		return val, true, nil
	}
	if v.kc == nil {
		return "", false, nil
	}
	val, err := v.kc.Get(v.service, acct)
	if errors.Is(err, keychain.ErrNotFound) {
		delete(v.names, acct)
		_ = v.saveIndexLocked()
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	v.values[acct] = val
	return val, true, nil
}

// Set stores or clears provider's API key. An empty value removes it.
func (v *Vault) Set(provider, value string) error {
	p := NormalizeProvider(provider)
	if p == "" {
		return fmt.Errorf("unknown provider %q", provider)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return v.Delete(p)
	}
	if !utf8.ValidString(value) {
		return errors.New("API key must be valid UTF-8")
	}
	if len(value) > maxSecret {
		return fmt.Errorf("API key is longer than %d bytes", maxSecret)
	}
	acct := accountFor(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if v.kc == nil {
		next := mapsClone(v.values)
		next[acct] = value
		if err := v.writeFileLocked(next); err != nil {
			return err
		}
		v.names[acct] = true
		v.values[acct] = value
		return nil
	}
	had := v.names[acct]
	v.names[acct] = true
	if err := v.saveIndexLocked(); err != nil {
		if !had {
			delete(v.names, acct)
		}
		return err
	}
	if err := v.kc.Set(v.service, acct, value); err != nil {
		if !had {
			delete(v.names, acct)
			_ = v.saveIndexLocked()
		}
		return err
	}
	v.values[acct] = value
	return nil
}

// Delete removes provider's persisted API key.
func (v *Vault) Delete(provider string) error {
	p := NormalizeProvider(provider)
	if p == "" {
		return fmt.Errorf("unknown provider %q", provider)
	}
	acct := accountFor(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if v.kc == nil {
		next := mapsClone(v.values)
		delete(next, acct)
		if err := v.writeFileLocked(next); err != nil {
			return err
		}
		delete(v.names, acct)
		delete(v.values, acct)
		return nil
	}
	if err := v.kc.Delete(v.service, acct); err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return err
	}
	delete(v.names, acct)
	delete(v.values, acct)
	return v.saveIndexLocked()
}

// ResolveKey returns the API key for provider, preferring the vault over
// environment variables.
func (v *Vault) ResolveKey(provider string) (key, source string, err error) {
	if val, ok, err := v.Get(provider); err != nil {
		return "", "", err
	} else if ok && val != "" {
		return val, "vault", nil
	}
	if val, ok := EnvKey(provider); ok {
		return val, "env", nil
	}
	return "", "", nil
}

// Status reports whether each provider has a key stored or in the environment.
type Status struct {
	ID           string
	Set          bool
	EnvSet       bool
	DefaultModel string
}

// ListStatuses returns the status of every supported provider in display order.
func (v *Vault) ListStatuses(customDefaults map[string]string) []Status {
	out := make([]Status, len(Providers))
	for i, p := range Providers {
		_, envOK := EnvKey(p)
		def := DefaultModels[p]
		if custom := strings.TrimSpace(customDefaults[p]); custom != "" {
			def = custom
		}
		out[i] = Status{
			ID:           p,
			Set:          v.Has(p),
			EnvSet:       envOK,
			DefaultModel: def,
		}
	}
	return out
}

func mapsClone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func isValidProvider(id string) bool {
	return slices.Contains(Providers, id)
}
