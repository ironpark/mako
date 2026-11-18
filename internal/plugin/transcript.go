package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// A plugin with sessions:read reads a session's conversation a page at a
// time, newest first, as the daemon reads it for the window: a session of
// this node's alone. A page is bounded in messages, in each message's
// text and in all; a page's cursor names the log's end the page was read
// up to and how many of that window's messages were given, so that pages
// read on while the agent writes neither skip nor repeat a message.

const (
	transcriptPage     = 20
	maxTranscriptPage  = 50
	maxTranscriptText  = 8 << 10
	maxTranscriptCall  = 1 << 10
	maxTranscriptRes   = 2 << 10
	maxTranscriptBytes = 256 << 10
	transcriptsPerMin  = 30
	// maxWindow is the most messages a read of a log gives, as the daemon
	// reads one (transcript's limit): a cursor's count is no more.
	maxWindow = 1000
)

// TranscriptFunc reads a page of the conversation of a session of this
// node's: the messages of its log up to the byte offset before, the
// log's end for 0. It returns the chat and the offset it read up to.
type TranscriptFunc func(ctx context.Context, sessionID string, before int64) (*protocol.Chat, int64, error)

// cursor is a page's place: the window of the log up to end, and how
// many of its messages, from its last, pages gave already.
type cursor struct {
	end  int64
	skip int
}

func (c cursor) String() string { return strconv.FormatInt(c.end, 10) + "." + strconv.Itoa(c.skip) }

func parseCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	a, b, ok := strings.Cut(s, ".")
	end, err1 := strconv.ParseInt(a, 10, 64)
	skip, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || end <= 0 || skip < 0 || skip > maxWindow {
		return cursor{}, errors.New("before is a cursor a page gave, or empty")
	}
	return cursor{end, skip}, nil
}

// cut is s at most n bytes, cut on a rune's start; and whether it was.
func cut(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

func (h *host) SessionsReadTranscript(ctx context.Context, req *hostapi.ReadTranscriptRequest) (*hostapi.ReadTranscriptResponse, error) {
	if !h.p.trRate.take() {
		return nil, fmt.Errorf("the plugin read transcripts more than %d times a minute", transcriptsPerMin)
	}
	read := h.p.m.cfg.Transcript
	if read == nil {
		return nil, errors.New("this node reads no transcripts")
	}
	id := req.GetSessionId()
	if !slices.ContainsFunc(h.p.m.cfg.Sessions(), func(s *protocol.SessionInfo) bool { return s.GetId() == id }) {
		return nil, fmt.Errorf("no session %q on this node", id)
	}
	c, err := parseCursor(req.GetBefore())
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	switch {
	case limit == 0:
		limit = transcriptPage
	case limit > maxTranscriptPage:
		limit = maxTranscriptPage
	}
	chat, end, err := read(ctx, id, c.end)
	if err != nil {
		return nil, err
	}
	out := &hostapi.ReadTranscriptResponse{Supported: chat.GetSupported(), Truncated: chat.GetTruncated()}
	msgs := chat.GetMessages()
	left := len(msgs) - min(c.skip, len(msgs))
	take := min(limit, left)
	page := msgs[left-take : left]
	// Newest first into the page's bytes; what does not fit is left for
	// the next page.
	size, from := 0, len(page)
	conv := make([]*hostapi.TranscriptMessage, len(page))
	for i := len(page) - 1; i >= 0; i-- {
		m, cutOne := transcriptMessage(page[i])
		n := len(m.Text) + len(m.Detail) + len(m.Result) + len(m.Tool) + len(m.Kind) + 64
		if size+n > maxTranscriptBytes && i < len(page)-1 {
			out.Truncated = true
			break
		}
		size += n
		conv[i], from = m, i
		out.Truncated = out.Truncated || cutOne
	}
	out.Messages = conv[from:]
	given := c.skip + len(page) - from
	switch {
	case given < len(msgs):
		out.Before = cursor{end, given}.String()
	case chat.GetStart() > 0:
		out.Before = cursor{chat.GetStart(), 0}.String()
	}
	return out, nil
}

// transcriptMessage is a message as a plugin reads it, its parts cut to
// their bounds.
func transcriptMessage(m *protocol.ChatMessage) (*hostapi.TranscriptMessage, bool) {
	text, a := cut(m.GetText(), maxTranscriptText)
	detail, b := cut(m.GetDetail(), maxTranscriptCall)
	result, c := cut(m.GetResult(), maxTranscriptRes)
	tool, _ := cut(m.GetTool(), 256)
	return &hostapi.TranscriptMessage{Role: m.GetRole(), Text: text, Tool: tool, Detail: detail, Result: result,
		Kind: m.GetKind(), Error: m.GetError(), Pending: m.GetPending()}, a || b || c
}
