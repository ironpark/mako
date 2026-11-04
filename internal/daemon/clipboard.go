package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
)

// A program of a session that copies with its node's clipboard tools, as
// a shell's `pbcopy` or an editor's yank does, copies to the clipboard of
// that node, which for a session on another node is not the user's. The
// daemon puts its own tools first in a session's PATH, which copy as the
// tools do and pass what was copied on to the front-ends attached, as a
// program's own OSC 52 goes.

// clipArg, as a program's first argument, runs it as one of those tools;
// as a hookMessage's kind, it is what a session's program copied, its
// input the text, a JSON string.
const clipArg = "clip"

// clipTools are the clipboard tools stood in for. Windows's clip.exe is
// not: a PATH of its own does not come before System32 there.
var clipTools = []string{"pbcopy", "xclip", "xsel", "wl-copy"}

// writeClipTools writes, in dir's "clip" folder, a script for each of
// clipTools that runs program as it with the hook socket; it returns the
// folder, "" where there are none.
func writeClipTools(dir, program, socket string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", nil
	}
	folder := filepath.Join(dir, "clip")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", err
	}
	script := "#!/bin/sh\nexec " + agentspec.JoinArgs([]string{program, clipArg, socket}) + " \"$0\" \"$@\"\n"
	for _, tool := range clipTools {
		if err := os.WriteFile(filepath.Join(folder, tool), []byte(script), 0o700); err != nil {
			return "", err
		}
	}
	return folder, nil
}

// runClip is a clipboard tool, run by a script of writeClipTools as
// args[1], with the daemon's hook socket args[0] and the tool's own
// arguments after. It runs the node's own tool, where there is one, and
// passes what it copies on for the session in MAKO_SESSION; it never
// fails for want of a tool, as one on a node without a display would.
func runClip(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		return 2
	}
	socket, self, toolArgs := args[0], args[1], args[2:]
	tool := filepath.Base(self)
	real := lookPathBut(tool, filepath.Dir(self))
	text, copying := clipInput(tool, toolArgs, stdin)
	if !copying {
		if real == "" {
			fmt.Fprintf(stderr, "%s: command not found\n", tool)
			return 127
		}
		return run(real, toolArgs, stdin, stdout, stderr)
	}
	code := 0
	if real != "" {
		code = run(real, toolArgs, bytes.NewReader(text), stdout, io.Discard)
	}
	session := os.Getenv("MAKO_SESSION")
	if session != "" {
		input, _ := json.Marshal(string(text))
		if conn, err := sendHook(socket, hookMessage{Session: session, Seq: time.Now().UnixNano(), Kind: clipArg, Input: input}); err == nil {
			_ = conn.Close()
			return 0 // copied for the user, wherever the node's own tool failed
		}
	}
	return code
}

func run(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// lookPathBut is tool as PATH finds it outside the folder skip, "" for
// none.
func lookPathBut(tool, skip string) string {
	skip = filepath.Clean(skip)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || filepath.Clean(dir) == skip {
			continue
		}
		path := filepath.Join(dir, tool)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return path
		}
	}
	return ""
}

// clipInput is the text tool, run with args, puts on the clipboard, read
// from where it reads it: copying is false where it reads the clipboard,
// clears it, or copies to another selection than the clipboard, as xclip
// and xsel do to the primary one unless told.
func clipInput(tool string, args []string, stdin io.Reader) (text []byte, copying bool) {
	read := func(files []string) ([]byte, bool) {
		if len(files) == 0 {
			b, err := io.ReadAll(io.LimitReader(stdin, hookLimit))
			return b, err == nil
		}
		var out []byte
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, false
			}
			out = append(out, b...)
		}
		return out, true
	}
	switch tool {
	case "pbcopy":
		return read(nil)
	case "wl-copy":
		var words []string
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == "--":
				words = append(words, args[i+1:]...)
				i = len(args)
			case a == "-c" || a == "--clear" || a == "-p" || a == "--primary":
				return nil, false
			case a == "-t" || a == "--type" || a == "-s" || a == "--seat":
				i++
			case strings.HasPrefix(a, "-"):
			default:
				words = append(words, a)
			}
		}
		if len(words) > 0 {
			return []byte(strings.Join(words, " ")), true
		}
		return read(nil)
	case "xclip":
		clipboard, out := false, false
		var files []string
		for i := 0; i < len(args); i++ {
			a := strings.TrimLeft(args[i], "-")
			switch {
			case args[i] == a: // not a flag
				files = append(files, args[i])
			case a == "selection" || a == "sel" || a == "se":
				if i+1 < len(args) {
					i++
					clipboard = strings.HasPrefix(args[i], "c")
				}
			case a == "o" || a == "out" || a == "output":
				out = true
			case a == "t" || a == "target" || a == "d" || a == "display" || a == "l" || a == "loops":
				i++
			}
		}
		if !clipboard || out {
			return nil, false
		}
		return read(files)
	case "xsel":
		clipboard, in, other := false, false, false
		for _, a := range args {
			switch {
			case a == "--clipboard":
				clipboard = true
			case a == "--input":
				in = true
			case a == "--output" || a == "--clear" || a == "--delete" || a == "--append":
				other = true
			case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
				clipboard = clipboard || strings.ContainsRune(a, 'b')
				in = in || strings.ContainsRune(a, 'i')
				other = other || strings.ContainsAny(a, "ocda")
			}
		}
		// Without -i or -o, xsel reads what is piped in, and prints the
		// selection to a terminal.
		if !clipboard || other || !in && !piped(stdin) {
			return nil, false
		}
		return read(nil)
	}
	return nil, false
}

// piped reports whether r is not a terminal.
func piped(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice == 0
}
