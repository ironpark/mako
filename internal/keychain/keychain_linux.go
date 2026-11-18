package keychain

import (
	"errors"
	"fmt"
	"slices"

	"github.com/godbus/dbus/v5"
	"github.com/zalando/go-keyring"
)

// On Linux the store is the desktop's Secret Service (GNOME Keyring,
// KWallet and others), over the session's D-Bus; go-keyring speaks its
// protocol. A node with no session bus, or none serving the Secret
// Service, has no keychain: a server, a container, a session over ssh.

const secretService = "org.freedesktop.secrets"

type secretServiceKeychain struct{}

func system() (Keychain, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("no D-Bus session: %w", err)
	}
	var owned bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, secretService).Store(&owned); err != nil {
		return nil, fmt.Errorf("D-Bus: %w", err)
	}
	if !owned {
		var names []string
		if err := conn.BusObject().Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&names); err != nil || !slices.Contains(names, secretService) {
			return nil, errors.New("no Secret Service runs in this session")
		}
	}
	return secretServiceKeychain{}, nil
}

func (secretServiceKeychain) Name() string { return "Secret Service" }

func (secretServiceKeychain) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (secretServiceKeychain) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

func (secretServiceKeychain) Delete(service, account string) error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
