//go:build !darwin

package gui

import "os/exec"

// setBadge would put label on the app's icon; only the Dock has one.
func setBadge(string) {}

// notify posts a desktop notification where notify-send can.
func notify(title, body string) {
	if path, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command(path, "--app-name=mako", title, body).Run()
	}
}
