package gui

import (
	"fmt"

	"github.com/ironpark/sanbo/internal/protocol"
)

// tokens writes a token count as the chat footer does: 950, 12.3k, 1.2M.
func tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
}

func trimZero(s string) string {
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

// usageLine is a conversation's tokens in brief: how full its context is,
// and what it has spent.
func usageLine(u protocol.Usage) string {
	ctx := ""
	if u.Context > 0 {
		ctx = Tf("context %s", tokens(u.Context))
		if u.Window > 0 {
			ctx = Tf("context %d%%", u.Context*100/u.Window)
		}
		ctx += " · "
	}
	return ctx + Tf("%s in · %s out", tokens(u.Input+u.CachedInput), tokens(u.Output))
}

// usageDetail is a conversation's tokens in full.
func usageDetail(u protocol.Usage) string {
	s := Tf("Input %s (cached %s) · output %s", tokens(u.Input+u.CachedInput), tokens(u.CachedInput), tokens(u.Output))
	if u.Context > 0 {
		s += "\n" + Tf("Latest prompt %s tokens", tokens(u.Context))
		if u.Window > 0 {
			s += Tf(" of %s", tokens(u.Window))
		}
	}
	return s
}
