//go:build !darwin

package usage

import "context"

func readClaudeKeychainPlatform(context.Context, string, string) (string, error) {
	return "", nil
}
