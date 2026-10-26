//go:build !unix

package session

import "os"

// foregroundIsSelf cannot tell here, so a session never counts as idle.
func foregroundIsSelf(*os.File, int) bool { return false }
