package gui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A stream is kept by agent and system, in parts, only while the option is
// on, and no more once the recordings are full.
func TestTerminalStreamsAreRecorded(t *testing.T) {
	dir := t.TempDir()
	r := newStreamRecorder(dir)
	tap := r.tap("abc123", "claude:opus", "linux")
	tap.write([]byte("off"))
	r.on.Store(true)
	chunk := bytes.Repeat([]byte("x"), recordPart/2+1)
	tap.write([]byte("\x1b[1mhi"))
	tap.write(chunk)
	tap.write(chunk) // goes in a second part
	tap.close()
	parts, _ := filepath.Glob(filepath.Join(dir, "claude", "linux", "*-abc123-*.pty"))
	if len(parts) != 2 {
		t.Fatalf("parts: %v", parts)
	}
	first, _ := os.ReadFile(parts[0])
	if !bytes.HasPrefix(first, []byte("\x1b[1mhi")) || len(first) != 6+len(chunk) {
		t.Fatalf("first part: %d bytes", len(first))
	}

	r.used = recordMax - 1 // full
	shell := r.tap("s", "", "")
	shell.write([]byte("more"))
	shell.close()
	if _, err := os.Stat(filepath.Join(dir, "shell", "unknown")); err == nil {
		t.Fatal("a full recorder recorded")
	}
}
