package protocol

import "slices"

// Apply makes the conversation p describes from the one held before it, c.
// It returns nil when p does not apply to c, as when c is not the
// conversation p was made from: the holder then starts over.
func (p *HostChatPatch) Apply(c *Chat) *Chat {
	out := &Chat{
		Supported:    p.GetSupported(),
		Conversation: p.GetConversation(),
		Truncated:    p.GetTruncated(),
		Start:        p.GetStart(),
		Usage:        p.GetUsage(),
		Agents:       p.GetAgents(),
	}
	if p.GetWhole() {
		out.Messages = p.GetMessages()
		return out
	}
	// A patch is of the conversation held, and of no other.
	if c == nil || c.GetConversation() != p.GetConversation() {
		return nil
	}
	old := c.GetMessages()
	drop, keep := int(p.GetDrop()), int(p.GetKeep())
	// Each checked on its own, so that their sum cannot overflow.
	if drop < 0 || keep < 0 || drop > len(old) || keep > len(old)-drop {
		return nil
	}
	out.Messages = append(slices.Clone(old[drop:drop+keep]), p.GetMessages()...)
	return out
}

// ApplyHostEvent applies ev to state, in place: what a front-end holding a
// session's host state does with each event, in seq order. It reports
// false when ev does not apply, as a conversation patch of another
// conversation or an event no later than the state, and the front-end
// should subscribe again from nothing.
func ApplyHostEvent(state *HostState, ev *HostEvent) bool {
	if ev.GetSeq() <= state.GetSeq() {
		return false
	}
	switch b := ev.GetBody().(type) {
	case *HostEvent_Info:
		state.Info = b.Info
	case *HostEvent_Chat:
		chat := b.Chat.Apply(state.GetChat())
		if chat == nil {
			return false
		}
		state.Chat = chat
	case *HostEvent_Live:
		state.Live = b.Live
	case *HostEvent_Draft:
		state.Draft = b.Draft
	case *HostEvent_Queue:
		state.Queue = b.Queue
	case *HostEvent_Clients:
		state.Clients = b.Clients
	}
	// A prompt sent and an approval answered change no state of their
	// own: the info and conversation events after them do.
	state.Seq = ev.GetSeq()
	return true
}
