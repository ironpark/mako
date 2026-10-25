package client

import (
	"context"
	"github.com/ironpark/sanbo/internal/protocol"
)

func (c *Client) ReadChat(ctx context.Context, psk string, req protocol.ReadChat) (protocol.Chat, error) {
	var out protocol.Chat
	err := c.request(ctx, psk, protocol.TypeReadChat, req, &out)
	return out, err
}
