package daemon

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// screenLiveTailWindow bounds how much of the tail of a transcript file is
// inspected to find the latest logged assistant message for deduplication.
const screenLiveTailWindow = 256 << 10

// screenLivePoller feeds liveHub from a session's PTY screen buffer for agents
// that implement agent.ScreenLive (such as agy). It runs exclusively inside
// liveHub's single lookOut goroutine while at least one WatchChat follower is
// active, and performs screen extraction only when the session is actively
// working and its PTY output or transcript size changed.
type screenLivePoller struct {
	hub    *liveHub
	s      *session.Session
	driver agent.ScreenLive
	logs   agent.Logs
	gen    uint64

	lastOutputs uint64
	lastLogPath string
	lastLogSize int64
	hadLog      bool
	lastLogged  string
	lastEmitted string
	stepSeq     uint64
	step        string
}

func screenLivePollerFactory(hub *liveHub, s *session.Session) func(gen uint64) func(size func() int64) (bool, int64) {
	driver, ok := agents.As[agent.ScreenLive](s.Agent())
	if !ok {
		return nil
	}
	logs, _ := agents.As[agent.Logs](s.Agent())
	return func(gen uint64) func(size func() int64) (bool, int64) {
		p := &screenLivePoller{
			hub:    hub,
			s:      s,
			driver: driver,
			logs:   logs,
			gen:    gen,
		}
		return p.tick
	}
}

// tick inspects the session once per liveTick. When working, it checks the log
// size and (when PTY outputs or the log changed) extracts the in-flight reply
// from the unwrapped terminal screen and updates liveHub. When not working, it
// promptly clears any previously emitted preview. All mutations are fenced by
// p.gen so a cancelled look-out can never clobber a newer one.
func (p *screenLivePoller) tick(size func() int64) (checked bool, logSize int64) {
	info := p.s.Info()
	working := info.GetState() == protocol.StateRunning && info.GetStatus() == protocol.AgentWorking
	if !working {
		if p.lastEmitted != "" {
			p.clear()
		}
		p.lastOutputs = 0
		p.hadLog = false
		return false, 0
	}

	outputs := p.s.Outputs()
	logPath := p.s.TranscriptPath()
	logSize = size()

	logChanged := !p.hadLog || logPath != p.lastLogPath || logSize != p.lastLogSize
	if logChanged {
		p.lastLogPath = logPath
		p.lastLogSize = logSize
		p.hadLog = true
		if logPath != "" && logSize > 0 {
			p.lastLogged = lastLoggedAssistantText(logPath, p.logs)
		} else {
			p.lastLogged = ""
		}
	}

	if outputs == p.lastOutputs && !logChanged {
		return true, logSize
	}

	screen, err := p.s.PlainTextUnwrapped()
	if err != nil {
		return true, logSize
	}
	p.lastOutputs = outputs

	extracted := p.driver.ExtractLive(screen, p.lastLogged)
	switch {
	case extracted == "":
		if p.lastEmitted != "" {
			p.clear()
		}
	case extracted == p.lastEmitted:
		// Unchanged; nothing to post.
	case p.lastEmitted != "" && strings.HasPrefix(extracted, p.lastEmitted):
		delta := extracted[len(p.lastEmitted):]
		if p.hub.takeForGen(p.gen, livePost{
			Session: p.s.ID(),
			Step:    p.step,
			Pieces:  []livePiece{{Kind: "text", Index: 0, Text: delta}},
		}) {
			p.lastEmitted = extracted
		}
	default:
		p.stepSeq++
		p.step = fmt.Sprintf("screen:%d", p.stepSeq)
		if p.hub.takeForGen(p.gen, livePost{
			Session: p.s.ID(),
			Step:    p.step,
			Pieces:  []livePiece{{Kind: "text", Index: 0, Text: extracted}},
		}) {
			p.lastEmitted = extracted
		}
	}
	return true, logSize
}

func (p *screenLivePoller) clear() {
	p.stepSeq++
	p.step = fmt.Sprintf("screen:%d", p.stepSeq)
	_ = p.hub.takeForGen(p.gen, livePost{Session: p.s.ID(), Step: p.step})
	p.lastEmitted = ""
}

// lastLoggedAssistantText reads the tail of path and returns the latest
// assistant message text written since the last user prompt, or "" if none.
func lastLoggedAssistantText(path string, logs agent.Logs) string {
	if path == "" || logs == nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return ""
	}
	var offset int64
	if fi.Size() > screenLiveTailWindow {
		offset = fi.Size() - screenLiveTailWindow
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return ""
		}
	}

	reader := agent.BorrowReader(f)
	var long []byte
	defer func() { agent.GiveBack(reader, long) }()

	if offset > 0 {
		// Discard the leading partial line after seeking into the middle.
		if _, err := agent.ReadRecord(reader, &long); err != nil {
			return ""
		}
	}

	dec := logs.NewDecoder()
	var last string
	for {
		line, err := agent.ReadRecord(reader, &long)
		if len(line) > 0 {
			for _, msg := range dec.Line(line) {
				switch msg.GetRole() {
				case "user":
					last = ""
				case "assistant":
					if t := strings.TrimSpace(msg.GetText()); t != "" {
						last = t
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return last
}
