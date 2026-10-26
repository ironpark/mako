package client

import (
	"context"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transcript"
)

// ReadChat reads a session's conversation. The daemon answering, local or a
// peer, may be older than this front-end, so its reply is brought to the
// current form.
func (c *Client) ReadChat(ctx context.Context, psk string, req *protocol.ReadChat) (*protocol.Chat, error) {
	out, err := request[*protocol.Chat](ctx, c, psk, protocol.TypeReadChat, req)
	if err != nil {
		return nil, err
	}
	transcript.Upgrade(out)
	return out, nil
}
