package client

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// SubscribeSession follows a session's host (protocol.SubscribeSession):
// fn is called with each frame, the HostSubscribed that starts the feed
// or starts it over, or a HostEvent, the other nil, until ctx ends, the
// stream does, or fn returns an error, which SubscribeSession returns.
func (c *Client) SubscribeSession(ctx context.Context, req *protocol.SubscribeSession, fn func(*protocol.HostSubscribed, *protocol.HostEvent) error) error {
	s, err := c.stream(ctx)
	if err != nil {
		return err
	}
	defer s.Hangup()
	stop := context.AfterFunc(ctx, func() { _ = s.Hangup() })
	defer stop()
	if err := s.Send(protocol.TypeSubscribeSession, "", req); err != nil {
		return err
	}
	for {
		env, err := s.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch env.Type() {
		case protocol.TypeHostSubscribed:
			first, err := transport.Reply[*protocol.HostSubscribed](env)
			if err != nil {
				return err
			}
			err = fn(first, nil)
			if err != nil {
				return err
			}
		case protocol.TypeHostEvent:
			ev, err := transport.Reply[*protocol.HostEvent](env)
			if err != nil {
				return err
			}
			if err := fn(nil, ev); err != nil {
				return err
			}
		default:
			if _, err := transport.Reply[*protocol.HostEvent](env); err != nil {
				return err
			}
			return fmt.Errorf("client: a session host sent %s", env.Type())
		}
	}
}

// errStartOver is a replay that does not apply to the state held.
var errStartOver = errors.New("client: the session host's events do not apply to the state held")

// FollowSession follows a session's host as SubscribeSession does, and
// keeps its state: fn is called with the state after each change, and the
// event that made it, nil when the state came whole. held is the state
// held from an earlier follow, nil for none: the host replays what came
// after it, if it still can. FollowSession returns the state as it last
// held it, to follow from again once the stream is back.
func (c *Client) FollowSession(ctx context.Context, req *protocol.SubscribeSession, held *protocol.HostState, fn func(*protocol.HostState, *protocol.HostEvent)) (*protocol.HostState, error) {
	for {
		r := proto.CloneOf(req)
		r.LastSeenSeq = held.GetSeq()
		err := c.SubscribeSession(ctx, r, func(first *protocol.HostSubscribed, ev *protocol.HostEvent) error {
			if first != nil {
				if first.GetState() != nil {
					held = first.GetState()
					fn(held, nil)
					return nil
				}
				if held == nil {
					return errStartOver
				}
				for _, ev := range first.GetReplay() {
					if !protocol.ApplyHostEvent(held, ev) {
						return errStartOver
					}
					fn(held, ev)
				}
				return nil
			}
			if held == nil || !protocol.ApplyHostEvent(held, ev) {
				return errStartOver
			}
			fn(held, ev)
			return nil
		})
		if errors.Is(err, errStartOver) && held.GetSeq() != 0 {
			held = nil
			continue
		}
		return held, err
	}
}

// SessionAction changes a session's host: its draft, a prompt to send or
// queue, a queued prompt to cancel, or an approval to answer.
func (c *Client) SessionAction(ctx context.Context, req *protocol.SessionAction) (*protocol.SessionActionResult, error) {
	return request[*protocol.SessionActionResult](ctx, c, protocol.TypeSessionAction, req)
}
