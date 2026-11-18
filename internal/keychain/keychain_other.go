//go:build !darwin && !linux && !windows

package keychain

import "errors"

// The other systems have no store mako reaches.
func system() (Keychain, error) { return nil, errors.New("mako reaches no keychain on this system") }
