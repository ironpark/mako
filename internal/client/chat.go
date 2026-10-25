package client

import (
	"context"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transcript"
)

// ReadChat reads a session's conversation. The daemon answering, local or a
// peer, may be older than this front-end, so its reply is brought to the
// current form.
func (c *Client) ReadChat(ctx context.Context, psk string, req protocol.ReadChat) (protocol.Chat, error) {
	var out protocol.Chat
	err := c.request(ctx, psk, protocol.TypeReadChat, req, &out)
	transcript.Upgrade(&out)
	return out, err
}
