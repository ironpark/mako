package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/ironpark/mako/internal/plugin"
)

// Fetcher gets registries' indexes and packages for the daemon: by
// https, from public addresses only, so that a registry cannot point the
// node at the user's own machine or network; following at most
// maxRedirects redirects, each to a URL CheckURL allows (a release's
// download moves to its forge's file host, which is fine: the package's
// hash, from the index, is checked whatever served it); within a time
// and a size, counted as the body is read.
type Fetcher struct {
	client   *http.Client
	loopback bool
}

const maxRedirects = 3

// NewFetcher makes a Fetcher. loopback lets it reach http servers on
// this machine's loopback, for tests alone (internal/testhooks).
func NewFetcher(loopback bool) *Fetcher {
	f := &Fetcher{loopback: loopback}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if loopback && ap.Addr().IsLoopback() {
			return nil
		}
		if !plugin.PublicAddr(ap.Addr()) {
			return fmt.Errorf("the host resolves to %s, which is not a public address", ap.Addr())
		}
		return nil
	}}
	f.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  20 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			MaxIdleConns:           4,
			IdleConnTimeout:        30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("more than %d redirects", maxRedirects)
			}
			if err := CheckURL(req.URL.String(), loopback); err != nil {
				return fmt.Errorf("a redirect to %s is refused: %w", req.URL.Redacted(), err)
			}
			return nil
		},
	}
	return f
}

// Loopback says the fetcher reaches this machine's loopback, for tests.
func (f *Fetcher) Loopback() bool { return f.loopback }

// Get fetches a URL CheckURL allows, refusing a body over max bytes, and
// a request that takes longer than timeout in all.
func (f *Fetcher) Get(ctx context.Context, raw string, max int64, timeout time.Duration) ([]byte, error) {
	if err := CheckURL(raw, f.loopback); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mako-plugins")
	resp, err := f.client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s did not answer within %s", host(raw), timeout)
		}
		return nil, fmt.Errorf("%s: %w", host(raw), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The status's text is the server's to write, and an error is
		// printed in a terminal: the code alone is taken from it.
		return nil, fmt.Errorf("%s answered %d %s", host(raw), resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if resp.ContentLength > max {
		return nil, fmt.Errorf("%s sent %d bytes, over the %d KiB taken", host(raw), resp.ContentLength, max>>10)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, fmt.Errorf("%s did not finish within %s", host(raw), timeout)
		}
		return nil, fmt.Errorf("%s: %w", host(raw), err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s sent more than the %d KiB taken", host(raw), max>>10)
	}
	return b, nil
}

func host(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return short(raw)
}
