package session

import (
	"strings"
	"testing"
	"time"
	"unsafe"
)

// A program runs on a pseudoconsole: its output reaches the screen, and
// its exit ends the session with its code.
func TestAProgramRunsOnAPseudoconsole(t *testing.T) {
	s, err := Start(Spec{ID: "s", Command: []string{"cmd.exe", "/d", "/c", "echo goodbye& exit 3"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not exit")
	}
	if snap, err := s.Snapshot(false); err != nil || !strings.Contains(snap.GetText(), "goodbye") {
		t.Fatalf("snapshot %q, %v", snap.GetText(), err)
	}
	if code := s.Info().GetExitCode(); code != 3 {
		t.Fatalf("exit code %d", code)
	}
}

// What it types reaches the program, and closing the session ends one
// that is still running.
func TestTypingAndClosing(t *testing.T) {
	s, err := Start(Spec{ID: "s", Command: []string{"cmd.exe", "/d", "/q"}, Cols: 60, Rows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(80, 24); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write([]byte("echo typed-in\r"))
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _ := s.Snapshot(false)
		if strings.Count(snap.GetText(), "typed-in") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("screen %q", snap.GetText())
		}
		time.Sleep(50 * time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("closing hung")
	}
}

// A batch file runs through cmd, which takes the rest of the line whole.
func TestBatchFilesRunInCmd(t *testing.T) {
	got := commandLine([]string{`C:\npm\claude.cmd`, "--model", "a b"}, `C:\Windows\cmd.exe`)
	if want := `C:\Windows\cmd.exe /d /s /c "C:\npm\claude.cmd --model "a b""`; got != want {
		t.Errorf("%s, want %s", got, want)
	}
	if got := commandLine([]string{`C:\bin\agy.exe`, "-i", "hi"}, ""); got != `C:\bin\agy.exe -i hi` {
		t.Errorf("%s", got)
	}
}

// The environment is a sorted block of NUL-ended variables, ended by one
// more NUL.
func TestEnvBlock(t *testing.T) {
	p, err := envBlock([]string{"b=2", "A=1", "=C:=C:\\"})
	if err != nil {
		t.Fatal(err)
	}
	var units []uint16
	for i := 0; ; i++ {
		u := *(*uint16)(unsafe.Add(unsafe.Pointer(p), 2*i))
		units = append(units, u)
		if u == 0 && len(units) > 1 && units[len(units)-2] == 0 {
			break
		}
	}
	var s strings.Builder
	for _, u := range units {
		s.WriteRune(rune(u))
	}
	if got := s.String(); got != "=C:=C:\\\x00A=1\x00b=2\x00\x00" {
		t.Errorf("%q", got)
	}
}
