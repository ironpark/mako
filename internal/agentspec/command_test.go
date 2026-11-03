package agentspec

import "testing"

func TestWindowsCommandLine(t *testing.T) {
	short := map[string]string{
		`C:\Program Files\mako\makod.exe`: `C:\PROGRA~1\mako\makod.exe`,
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{`C:\Users\me\mako\bin\makod.exe`, "hook", `C:\Users\me\AppData\Roaming\mako\run\hooks.sock`},
			`C:/Users/me/mako/bin/makod.exe hook C:/Users/me/AppData/Roaming/mako/run/hooks.sock`},
		{[]string{`C:\Program Files\mako\makod.exe`, "statusline", "claude"},
			`C:/PROGRA~1/mako/makod.exe statusline claude`},
		{[]string{`C:\Users\Jane Doe\makod.exe`, "hook", ""},
			`"C:/Users/Jane Doe/makod.exe" hook ""`},
	} {
		if got := windowsCommandLine(tc.args, func(p string) string { return short[p] }); got != tc.want {
			t.Errorf("windowsCommandLine(%q) = %s, want %s", tc.args, got, tc.want)
		}
	}
}
