package keychain

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// On Windows the store is the Credential Manager, a generic credential of
// each secret, which go-keyring reaches by system calls.

type credentialManager struct{}

func system() (Keychain, error) { return credentialManager{}, nil }

func (credentialManager) Name() string { return "Windows Credential Manager" }

func (credentialManager) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (credentialManager) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

func (credentialManager) Delete(service, account string) error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
