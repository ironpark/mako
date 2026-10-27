package session

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// proc is the agent's process on a pseudoconsole: reading it reads the
// console's output, as a terminal's, and writing it types.
//
// A pseudoconsole's output does not end when its process does, so a
// watcher closes the console once the process exits, which ends the
// output after the last of it.
type proc struct {
	console windows.Handle
	process windows.Handle
	id      int
	in      *os.File // the console's input, written
	out     *os.File // the console's output, read

	closeConsole func()
	exited       chan struct{}
	exitCode     int64
}

// startProc starts argv in dir with env, on a pseudoconsole cols by rows.
func startProc(argv []string, dir string, env []string, cols, rows uint16) (p *proc, err error) {
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, err
	}
	// The console holds its ends of the pipes from here, and the process
	// started on it; this one keeps only its own.
	defer windows.CloseHandle(inRead)
	defer windows.CloseHandle(outWrite)
	in, out := os.NewFile(uintptr(inWrite), "console-in"), os.NewFile(uintptr(outRead), "console-out")
	defer func() {
		if err != nil {
			in.Close()
			out.Close()
		}
	}()

	var console windows.Handle
	if err := windows.CreatePseudoConsole(coord(cols, rows), inRead, outWrite, 0, &console); err != nil {
		return nil, err
	}
	closeConsole := sync.OnceFunc(func() { windows.ClosePseudoConsole(console) })
	defer func() {
		if err != nil {
			closeConsole()
		}
	}()

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// The attribute's value is the console's handle itself.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&console)), unsafe.Sizeof(console)); err != nil {
		return nil, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// No standard handles of this process's, so the program's are the
	// console's.
	si.Flags = windows.STARTF_USESTDHANDLES

	line, err := windows.UTF16PtrFromString(commandLine(argv, os.Getenv("ComSpec")))
	if err != nil {
		return nil, err
	}
	block, err := envBlock(env)
	if err != nil {
		return nil, err
	}
	var cwd *uint16
	if dir != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, err
		}
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(nil, line, nil, nil, false, flags, block, cwd, &si.StartupInfo, &pi); err != nil {
		return nil, err
	}
	windows.CloseHandle(pi.Thread)

	p = &proc{
		console: console, process: pi.Process, id: int(pi.ProcessId),
		in: in, out: out, closeConsole: closeConsole, exited: make(chan struct{}),
	}
	go p.watch()
	return p, nil
}

// watch waits for the process to exit, keeps its code, and closes the
// console, which ends its output.
func (p *proc) watch() {
	p.exitCode = -1
	if _, err := windows.WaitForSingleObject(p.process, windows.INFINITE); err == nil {
		var code uint32
		if windows.GetExitCodeProcess(p.process, &code) == nil {
			p.exitCode = int64(code)
		}
	}
	close(p.exited)
	p.closeConsole()
}

// coord is a size as a console takes it.
func coord(cols, rows uint16) windows.Coord {
	return windows.Coord{X: int16(min(cols, 0x7fff)), Y: int16(min(rows, 0x7fff))}
}

func (p *proc) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		err = os.ErrClosed // the console closed
	}
	return n, err
}

func (p *proc) Write(b []byte) (int, error) { return p.in.Write(b) }

// Close closes the console and this process's ends of its pipes.
func (p *proc) Close() error {
	p.closeConsole()
	return errors.Join(p.in.Close(), p.out.Close())
}

func (p *proc) resize(cols, rows uint16) error {
	return windows.ResizePseudoConsole(p.console, coord(cols, rows))
}

func (p *proc) pid() int { return p.id }

func (p *proc) kill() {
	select {
	case <-p.exited:
	default:
		_ = windows.TerminateProcess(p.process, 1)
	}
}

// wait is the process's exit code once it exits, -1 when it says none.
func (p *proc) wait() int64 {
	<-p.exited
	windows.CloseHandle(p.process)
	return p.exitCode
}

// idle cannot tell a console's foreground program, so a session never
// counts as idle.
func (p *proc) idle() bool { return false }

// commandLine is argv as Windows passes a command line. A batch file, as
// npm's shims are, runs only in cmd, which takes the line after /c whole.
func commandLine(argv []string, comspec string) string {
	line := windows.ComposeCommandLine(argv)
	switch strings.ToLower(filepath.Ext(argv[0])) {
	case ".cmd", ".bat":
		return windows.EscapeArg(cmp.Or(comspec, "cmd.exe")) + ` /d /s /c "` + line + `"`
	}
	return line
}

// envBlock is env as CreateProcess takes it: each variable ended by a
// NUL, the whole by another, sorted by name, case aside.
func envBlock(env []string) (*uint16, error) {
	env = slices.Clone(env)
	slices.SortStableFunc(env, func(a, b string) int {
		return strings.Compare(strings.ToUpper(name(a)), strings.ToUpper(name(b)))
	})
	var block []uint16
	for _, kv := range env {
		if strings.ContainsRune(kv, 0) {
			return nil, windows.ERROR_BAD_ENVIRONMENT
		}
		block = append(block, windows.StringToUTF16(kv)...)
	}
	block = append(block, 0)
	return &block[0], nil
}

// name is a variable's name, "=C:" for the drive variables cmd keeps.
func name(kv string) string {
	if i := strings.IndexByte(kv[min(1, len(kv)):], '='); i >= 0 {
		return kv[:i+1]
	}
	return kv
}
