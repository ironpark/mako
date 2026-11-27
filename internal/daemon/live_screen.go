package daemon

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

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
	hub      *liveHub
	s        *session.Session
	driver   agent.ScreenLive
	baseline agent.ScreenLiveBaseline
	turn     agent.ScreenLiveTurn
	status   agent.ScreenLiveStatus
	activity agent.ScreenLiveActivity
	ghost    agent.ScreenGhost
	logs     agent.Logs
	gen      uint64

	lastOutputs     uint64
	lastLogPath     string
	lastLogSize     int64
	hadLog          bool
	lastLogged      string
	lastUser        string
	awaitingReply   bool
	lastEmitted     string
	lastStatus      string
	lastGhost       string
	ghostOutputs    uint64
	hasGhostScan    bool
	wasWorking      bool
	idleOutputs     uint64
	hasIdleBaseline bool
	idleText        string
	idleTurns       int
	idlePrompt      string
	stepSeq         uint64
	step            string
}

// screenPollerFactory makes the per-tick screen poller of session s: one
// that reads an agent's in-flight reply off the screen (agent.ScreenLive),
// its ghost text suggestion (agent.ScreenGhost), or both. It returns nil
// when the agent does neither, and reports whether the poller reads the
// reply, which makes the look-out a screen-live one.
func screenPollerFactory(hub *liveHub, s *session.Session) (func(gen uint64) func(size func() int64) (bool, int64), bool) {
	driver, hasLive := agents.As[agent.ScreenLive](s.Agent())
	ghost, hasGhost := agents.As[agent.ScreenGhost](s.Agent())
	if !hasLive && !hasGhost {
		return nil, false
	}
	var baseline agent.ScreenLiveBaseline
	var turn agent.ScreenLiveTurn
	var status agent.ScreenLiveStatus
	var activity agent.ScreenLiveActivity
	if hasLive {
		baseline, _ = driver.(agent.ScreenLiveBaseline)
		turn, _ = driver.(agent.ScreenLiveTurn)
		status, _ = driver.(agent.ScreenLiveStatus)
		activity, _ = driver.(agent.ScreenLiveActivity)
	}
	logs, _ := agents.As[agent.Logs](s.Agent())
	return func(gen uint64) func(size func() int64) (bool, int64) {
		p := &screenLivePoller{
			hub:      hub,
			s:        s,
			driver:   driver,
			baseline: baseline,
			turn:     turn,
			status:   status,
			activity: activity,
			ghost:    ghost,
			logs:     logs,
			gen:      gen,
		}
		return p.tick
	}, hasLive
}

// tick inspects the session once per liveTick. When working, it checks the log
// size and (when PTY outputs or the log changed) extracts the in-flight reply
// and activity status from the unwrapped terminal screen and updates liveHub.
// When running but not foreground-working, it clears any foreground preview,
// snapshots the idle screen baseline so the next turn cannot briefly flash the
// previous turn's response, and surfaces any active background Swarm/Agent
// activity rows via ScreenLiveActivity. All mutations are fenced by p.gen so a
// cancelled look-out can never clobber a newer one.
func (p *screenLivePoller) tick(size func() int64) (checked bool, logSize int64) {
	info := p.s.Info()
	running := info.GetState() == protocol.StateRunning
	working := running && info.GetStatus() == protocol.AgentWorking
	if !working {
		if !running {
			if p.lastEmitted != "" || p.lastStatus != "" {
				p.clear()
			}
			p.clearGhost()
			p.wasWorking = false
			p.lastOutputs = 0
			p.hadLog = false
			return false, 0
		}
		outputs := p.s.Outputs()
		wasWorking := p.wasWorking
		p.wasWorking = false
		p.lastOutputs = 0
		p.hadLog = false
		if p.ghost != nil && !p.hub.hasNativeSuggestion(p.s.ID()) {
			if !p.hasGhostScan || outputs != p.ghostOutputs || wasWorking {
				p.ghostOutputs = outputs
				p.hasGhostScan = true
				if styled, err := p.s.StyledTextUnwrapped(); err == nil {
					got := p.ghost.ExtractGhost(styled)
					if got != p.lastGhost {
						p.lastGhost = got
						p.hub.setSuggestionForGen(p.gen, p.s.ID(), got)
					}
				}
			}
		}
		if p.driver == nil {
			return false, 0
		}
		needScan := !p.hasIdleBaseline || outputs != p.idleOutputs || p.lastEmitted != "" || wasWorking
		if !needScan {
			return false, 0
		}
		screen, err := p.s.PlainTextUnwrapped()
		if err != nil {
			if p.lastEmitted != "" || p.lastStatus != "" {
				p.clear()
			}
			return false, 0
		}
		if p.baseline != nil {
			p.idleText = p.baseline.ExtractBaseline(screen)
		}
		if p.turn != nil {
			p.idleTurns, p.idlePrompt = p.turn.ExtractTurn(screen)
		}
		p.idleOutputs = outputs
		p.hasIdleBaseline = true

		var activity string
		if p.activity != nil {
			activity = p.activity.ExtractActivity(screen)
		}
		switch {
		case activity == "":
			if p.lastEmitted != "" || p.lastStatus != "" {
				p.clear()
			}
		case p.lastEmitted == "" && activity == p.lastStatus:
			// Unchanged background activity; nothing to post.
		case p.lastEmitted == "" && p.lastStatus != "":
			if p.hub.takeForGen(p.gen, livePost{
				Session: p.s.ID(),
				Step:    p.step,
				Pieces:  []livePiece{{Kind: "thinking", Index: 0, Text: activity}},
			}) {
				p.lastStatus = activity
			}
		default:
			p.stepSeq++
			p.step = fmt.Sprintf("screen:%d", p.stepSeq)
			if p.hub.takeForGen(p.gen, livePost{
				Session: p.s.ID(),
				Step:    p.step,
				Pieces:  []livePiece{{Kind: "thinking", Index: 0, Text: activity}},
			}) {
				p.lastEmitted = ""
				p.lastStatus = activity
			}
		}
		return false, 0
	}
	p.wasWorking = true
	p.clearGhost()
	if p.driver == nil {
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
			p.lastLogged, p.lastUser, p.awaitingReply = lastLoggedTurn(logPath, p.logs)
		} else {
			p.lastLogged, p.lastUser, p.awaitingReply = "", "", false
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

	var curTurns int
	var curPrompt string
	hasTurn := p.turn != nil
	if hasTurn {
		curTurns, curPrompt = p.turn.ExtractTurn(screen)
	}

	extracted := p.driver.ExtractLive(screen, p.lastLogged)

	// Gate A: When a new user prompt has already been logged in the transcript
	// (awaitingReply == true) but the TUI has not yet painted that new prompt
	// into the conversation body, everything currently visible above the
	// composer still belongs to the previous turn and must be suppressed.
	if extracted != "" && hasTurn && p.awaitingReply && p.lastUser != "" {
		if p.hasIdleBaseline {
			if curTurns <= p.idleTurns {
				extracted = ""
			}
		} else if curPrompt != "" && !strings.HasPrefix(curPrompt, "/") && !screenPromptMatches(curPrompt, p.lastUser) {
			extracted = ""
		}
	}

	// Gate B: Suppress pre-turn idle baseline text (including viewport-shifted
	// subsets/supersets/overlaps when the spinner or composer box resizes) until
	// the TUI paints the new prompt or genuine new reply text appears.
	if p.idleText != "" {
		samePromptTurn := hasTurn && curTurns <= p.idleTurns && curPrompt == p.idlePrompt
		switch {
		case extracted == "":
			// Only retire idleText once a new prompt turn actually appeared or
			// no stale prompt remains; if we suppressed above while still on
			// the old prompt turn, keep idleText armed.
			if !samePromptTurn {
				p.idleText = ""
			}
		case extracted == p.idleText ||
			p.driver.ExtractLive(screen, p.idleText) == "" ||
			isStaleOverlap(extracted, p.idleText, samePromptTurn):
			extracted = ""
		default:
			// Genuine new reply text appeared; retire the pre-turn baseline.
			p.idleText = ""
		}
	}
	var status string
	if p.status != nil {
		status = p.status.ExtractStatus(screen)
	}

	switch {
	case extracted == "" && status == "":
		if p.lastEmitted != "" || p.lastStatus != "" {
			p.clear()
		}
	case extracted == p.lastEmitted && status == p.lastStatus:
		// Unchanged; nothing to post.
	case extracted == "" && p.lastEmitted == "" && status != "" && p.lastStatus != "":
		// Activity status updated in place without a text block.
		if p.hub.takeForGen(p.gen, livePost{
			Session: p.s.ID(),
			Step:    p.step,
			Pieces:  []livePiece{{Kind: "thinking", Index: 0, Text: status}},
		}) {
			p.lastStatus = status
		}
	case extracted != "" && p.lastEmitted != "" && strings.HasPrefix(extracted, p.lastEmitted) && (status != "") == (p.lastStatus != ""):
		var pieces []livePiece
		if len(extracted) > len(p.lastEmitted) {
			pieces = append(pieces, livePiece{Kind: "text", Index: 0, Text: extracted[len(p.lastEmitted):]})
		}
		if status != p.lastStatus && status != "" {
			pieces = append(pieces, livePiece{Kind: "thinking", Index: 1, Text: status})
		}
		if p.hub.takeForGen(p.gen, livePost{
			Session: p.s.ID(),
			Step:    p.step,
			Pieces:  pieces,
		}) {
			p.lastEmitted = extracted
			p.lastStatus = status
		}
	default:
		p.stepSeq++
		p.step = fmt.Sprintf("screen:%d", p.stepSeq)
		var pieces []livePiece
		nextIdx := int32(0)
		if extracted != "" {
			pieces = append(pieces, livePiece{Kind: "text", Index: nextIdx, Text: extracted})
			nextIdx++
		}
		if status != "" {
			pieces = append(pieces, livePiece{Kind: "thinking", Index: nextIdx, Text: status})
		}
		if p.hub.takeForGen(p.gen, livePost{
			Session: p.s.ID(),
			Step:    p.step,
			Pieces:  pieces,
		}) {
			p.lastEmitted = extracted
			p.lastStatus = status
		}
	}
	return true, logSize
}

func (p *screenLivePoller) clear() {
	p.stepSeq++
	p.step = fmt.Sprintf("screen:%d", p.stepSeq)
	_ = p.hub.takeForGen(p.gen, livePost{Session: p.s.ID(), Step: p.step})
	p.lastEmitted = ""
	p.lastStatus = ""
}

func (p *screenLivePoller) clearGhost() {
	p.hasGhostScan = false
	if p.lastGhost != "" {
		p.lastGhost = ""
		p.hub.setSuggestionForGen(p.gen, p.s.ID(), "")
	}
}

// lastLoggedAssistantText returns the latest logged assistant message text from
// path across turns, or "" if none.
func lastLoggedAssistantText(path string, logs agent.Logs) string {
	last, _, _ := lastLoggedTurn(path, logs)
	return last
}

// lastLoggedTurn reads the tail of path and returns the latest logged assistant
// message text across turns, the latest logged user prompt text, and whether
// the conversation is currently awaiting the first assistant reply to that user
// prompt (i.e. a user message was logged after the latest assistant message).
func lastLoggedTurn(path string, logs agent.Logs) (lastAssistant, lastUser string, awaitingReply bool) {
	if path == "" || logs == nil {
		return "", "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return "", "", false
	}
	var offset int64
	if fi.Size() > screenLiveTailWindow {
		offset = fi.Size() - screenLiveTailWindow
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", "", false
		}
	}

	reader := agent.BorrowReader(f)
	var long []byte
	defer func() { agent.GiveBack(reader, long) }()

	if offset > 0 {
		// Discard the leading partial line after seeking into the middle.
		if _, err := agent.ReadRecord(reader, &long); err != nil {
			return "", "", false
		}
	}

	dec := logs.NewDecoder()
	for {
		line, err := agent.ReadRecord(reader, &long)
		if len(line) > 0 {
			for _, msg := range dec.Line(line) {
				switch msg.GetRole() {
				case "assistant":
					if t := strings.TrimSpace(msg.GetText()); t != "" {
						lastAssistant = t
						awaitingReply = false
					}
				case "user":
					if t := strings.TrimSpace(msg.GetText()); t != "" {
						lastUser = t
						awaitingReply = true
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return lastAssistant, lastUser, awaitingReply
}

// screenPromptMatches reports whether the latest user prompt echoed on screen
// corresponds to the latest logged user prompt (tolerating whitespace
// normalization and multi-line/truncated terminal echoes).
func screenPromptMatches(screenPrompt, loggedUser string) bool {
	ns := normalizeCompactText(screenPrompt)
	nu := normalizeCompactText(loggedUser)
	if ns == "" || nu == "" {
		return false
	}
	if ns == nu {
		return true
	}
	// Multi-line or long user prompts may be partially echoed or wrapped on screen.
	if strings.HasPrefix(nu, ns) || strings.HasPrefix(ns, nu) {
		return utf8.RuneCountInString(ns) >= 4 || ns == nu
	}
	return false
}

// isStaleOverlap detects when extracted text still belongs to the pre-turn
// idleText even after the viewport shifted by a few lines (e.g. when a spinner
// appeared or a multi-line draft shrank on submit).
func isStaleOverlap(extracted, idleText string, samePromptTurn bool) bool {
	na := normalizeCompactText(extracted)
	nb := normalizeCompactText(idleText)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	// Bidirectional containment covers both a scrolled-off tail and a newly
	// uncovered superset when the composer box shrinks on submit.
	minLen := min(utf8.RuneCountInString(na), utf8.RuneCountInString(nb))
	if minLen >= 12 && (strings.Contains(nb, na) || strings.Contains(na, nb)) {
		return true
	}
	if !samePromptTurn {
		return false
	}
	// On the exact same prompt turn, check whether a substantial prefix or
	// suffix overlaps (covers top+bottom 1-line shift simultaneously).
	ra := []rune(na)
	rb := []rune(nb)
	const probe = 24
	if len(ra) >= probe && len(rb) >= probe {
		if strings.Contains(nb, string(ra[:probe])) || strings.Contains(nb, string(ra[len(ra)-probe:])) {
			return true
		}
	}
	return false
}

func normalizeCompactText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		b.WriteRune(r)
		inSpace = false
	}
	return strings.TrimSpace(b.String())
}
