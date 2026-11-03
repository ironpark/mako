package agentspec

import (
	"runtime"
	"strings"
)

// CommandLine is args as a command line an agent hands its shell, as it
// runs a hook or a status line. Elsewhere that shell is a POSIX one, and
// the line is JoinArgs'. On Windows, it depends on the agent. Claude Code
// runs it in Git Bash, while Codex and the Node agents run it in cmd.exe
// or PowerShell, and each of those reads a POSIX single quote
// differently. So the line is the words one form reads alike in all of
// them: forward slashes in place of backslashes, a path's short name in
// place of one with spaces, and double quotes only when nothing else
// will do, which PowerShell reads as a string rather than a command.
func CommandLine(args []string) string {
	if runtime.GOOS != "windows" {
		return JoinArgs(args)
	}
	return windowsCommandLine(args, shortPath)
}

// windowsUnsafe are the characters one of Git Bash, cmd.exe or PowerShell
// reads as more than itself in a bare word; a ~, as in a short name, and
// a # only begin something there.
const windowsUnsafe = " \t\n'\"\\$`;&|<>()*?[]^%!,={}@"

func unsafeWord(w string) bool {
	return strings.ContainsAny(w, windowsUnsafe) || strings.HasPrefix(w, "~") || strings.HasPrefix(w, "#")
}

func windowsCommandLine(args []string, short func(string) string) string {
	words := make([]string, len(args))
	for i, a := range args {
		w := strings.ReplaceAll(a, `\`, "/")
		if a != "" && unsafeWord(w) {
			if s := strings.ReplaceAll(short(a), `\`, "/"); s != "" && !unsafeWord(s) {
				w = s
			}
		}
		if w == "" || unsafeWord(w) {
			// No shell reads a double quote in it alike; the path is
			// a Windows one, which cannot hold one.
			w = `"` + strings.ReplaceAll(w, `"`, "") + `"`
		}
		words[i] = w
	}
	return strings.Join(words, " ")
}
