package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/config"
)

// A trace of the wheel's way through a terminal, for finding where a
// scroll stalls: set MAKO_WHEEL_TRACE=1 and it is written, a line an
// event, to diag/wheel-trace.log in mako's folder.
//
// It opens on its first line, appending: a process that only starts, as
// the agent's hooks running mako-gui do, leaves it be.
var wheelTrace *traceLog

func init() {
	if os.Getenv("MAKO_WHEEL_TRACE") != "" {
		wheelTrace = &traceLog{start: time.Now()}
	}
}

type traceLog struct {
	mu    sync.Mutex
	f     *os.File
	start time.Time
}

// tracef writes a line of the trace, stamped in milliseconds; nothing
// without one.
func (l *traceLog) tracef(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		dir, err := config.Dir()
		if err != nil {
			return
		}
		_ = os.MkdirAll(filepath.Join(dir, "diag"), 0o700)
		if l.f, err = os.OpenFile(filepath.Join(dir, "diag", "wheel-trace.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return
		}
		fmt.Fprintf(l.f, "--- pid %d\n", os.Getpid())
	}
	fmt.Fprintf(l.f, "%9.1f "+format+"\n", append([]any{float64(time.Since(l.start).Microseconds()) / 1000}, args...)...)
}
