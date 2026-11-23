package plugin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// A plugin's HTTP is the daemon's to make: to a host the plugin's
// manifest names as net:<host> and the user allowed, exactly that host,
// by https on its default port, with no user in the URL and no IP
// address for a host. A redirect is followed only within the same scheme,
// host and port. The connection is refused when the host resolves to an
// address that is not public (loopback, private, link-local, ...), so a
// granted name cannot be pointed at the user's own machine or network.
// Requests and responses are bounded in size and time and in how many a
// minute, and each is logged with the plugin's name.

const (
	maxFetchBody    = 256 << 10
	maxFetchHeaders = 32
	maxHeaderBytes  = 8 << 10
	maxRedirects    = 5
)

var fetchMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// headerName is an HTTP token; refusedHeaders are those the host sets
// itself, or that would change the connection.
var (
	headerName     = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	refusedHeaders = []string{"host", "connection", "content-length", "transfer-encoding", "upgrade", "te", "trailer",
		"keep-alive", "proxy-authorization", "proxy-connection", "expect"}
)

// fetcher makes plugins' requests.
type fetcher struct {
	client  *http.Client
	lim     Limits
	log     *slog.Logger
	testMap map[string]*url.URL // host → where requests for it go instead; see Config.NetTest
}

// newFetcher makes the fetcher. testMap sends a granted host's requests
// to another base URL, a server of a test's on loopback, which the dial
// guard then lets through; it is empty outside development.
func newFetcher(lim Limits, log *slog.Logger, testMap map[string]string) *fetcher {
	f := &fetcher{lim: lim, log: log, testMap: map[string]*url.URL{}}
	allowed := map[string]bool{}
	for host, base := range testMap {
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			f.testMap[host] = u
			allowed[u.Host] = true
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		if allowed[address] {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !publicAddr(ap.Addr()) {
			return fmt.Errorf("the host resolves to %s, which is not a public address", address)
		}
		return nil
	}}
	f.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  lim.HTTPTimeout,
			MaxResponseHeaderBytes: 64 << 10,
			MaxIdleConns:           8,
			IdleConnTimeout:        30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			first := via[0].URL
			if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
				return fmt.Errorf("a redirect to %s://%s, another host than the request's, is refused", req.URL.Scheme, req.URL.Host)
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("more than %d redirects", maxRedirects)
			}
			return nil
		},
	}
	return f
}

// checkURL reads a request's URL and says which granted host it is for,
// or why it may not be made.
func checkURL(raw string, g hostapi.Grants) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("the URL does not read: %v", err)
	}
	host := u.Hostname()
	switch {
	case u.Scheme != "https":
		return nil, "", fmt.Errorf("only https URLs may be fetched")
	case u.Opaque != "" || u.Host == "":
		return nil, "", fmt.Errorf("the URL names no host")
	case u.User != nil:
		return nil, "", fmt.Errorf("a URL with a user or password is refused")
	case u.Port() != "" && u.Port() != "443":
		return nil, "", fmt.Errorf("only the default port may be fetched")
	case net.ParseIP(host) != nil || strings.HasPrefix(u.Host, "["):
		return nil, "", fmt.Errorf("an IP address is refused for a host: a plugin reaches hosts by name")
	case !validHost(host) || !g.Has(hostapi.PermNet+":"+host):
		return nil, "", &hostapi.DeniedError{Method: "http.fetch", Permission: hostapi.PermNet + ":" + host}
	}
	return u, host, nil
}

// fetch makes a plugin's request.
func (f *fetcher) fetch(ctx context.Context, plugin string, g hostapi.Grants, rate *rateLimit, req *hostapi.FetchRequest) (*hostapi.FetchResponse, error) {
	u, host, err := checkURL(req.GetUrl(), g)
	if err != nil {
		f.log.Warn("plugin net refused", "plugin", plugin, "url", trimURL(req.GetUrl()), "err", err)
		return nil, err
	}
	method := strings.ToUpper(cmp.Or(req.GetMethod(), http.MethodGet))
	if !slices.Contains(fetchMethods, method) {
		return nil, fmt.Errorf("method %q is not one of %s", method, strings.Join(fetchMethods, ", "))
	}
	if len(req.GetBody()) > maxFetchBody {
		return nil, fmt.Errorf("the body is over %d KiB", maxFetchBody>>10)
	}
	if len(req.GetHeaders()) > maxFetchHeaders {
		return nil, fmt.Errorf("more than %d headers", maxFetchHeaders)
	}
	size := 0
	for k, v := range req.GetHeaders() {
		size += len(k) + len(v)
		if !headerName.MatchString(k) || strings.ContainsAny(v, "\r\n\x00") || slices.Contains(refusedHeaders, strings.ToLower(k)) ||
			strings.HasPrefix(strings.ToLower(k), "proxy-") {
			return nil, fmt.Errorf("header %q may not be set", k)
		}
	}
	if size > maxHeaderBytes {
		return nil, fmt.Errorf("the headers are over %d KiB", maxHeaderBytes>>10)
	}
	if !rate.take() {
		return nil, fmt.Errorf("more than %d requests a minute", f.lim.HTTPPerMinute)
	}
	target := *u
	if base, ok := f.testMap[host]; ok {
		target.Scheme, target.Host = base.Scheme, base.Host
	}
	ctx, cancel := context.WithTimeout(ctx, f.lim.HTTPTimeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(req.GetBody()))
	if err != nil {
		return nil, err
	}
	hreq.Host = host
	for k, v := range req.GetHeaders() {
		hreq.Header.Set(k, v)
	}
	if hreq.Header.Get("User-Agent") == "" {
		hreq.Header.Set("User-Agent", "mako-plugin/"+plugin)
	}
	start := time.Now()
	resp, err := f.client.Do(hreq)
	if err != nil {
		f.log.Warn("plugin net failed", "plugin", plugin, "method", method, "host", host, "path", u.Path, "err", err)
		if uerr, ok := errors.AsType[*url.Error](err); ok {
			err = uerr.Err
		}
		return nil, fmt.Errorf("%s %s: %w", method, host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(f.lim.HTTPBody)+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, host, err)
	}
	if len(body) > f.lim.HTTPBody {
		f.log.Warn("plugin net response too large", "plugin", plugin, "host", host, "path", u.Path)
		return nil, fmt.Errorf("the response is over %d KiB", f.lim.HTTPBody>>10)
	}
	f.log.Info("plugin net", "plugin", plugin, "method", method, "host", host, "path", u.Path,
		"status", resp.StatusCode, "bytes", len(body), "took", time.Since(start).Round(time.Millisecond))
	out := &hostapi.FetchResponse{Status: uint32(resp.StatusCode), Headers: map[string]string{}, Body: strings.ToValidUTF8(string(body), "\uFFFD")}
	for k, vs := range resp.Header {
		if len(out.Headers) >= 64 {
			break
		}
		out.Headers[strings.ToLower(k)] = strings.ToValidUTF8(strings.Join(vs, ", "), "\uFFFD")
	}
	return out, nil
}

// trimURL is a URL for the log, without its query, which may hold a token.
func trimURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if len(raw) > 200 {
		raw = raw[:200]
	}
	return raw
}

// rateLimit lets n events through a window, and refuses the rest.
type rateLimit struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	times  []time.Time
	now    func() time.Time // the clock; tests stop it
}

func newRateLimit(n int, window time.Duration) *rateLimit {
	return &rateLimit{n: n, window: window, now: time.Now}
}

func (r *rateLimit) take() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for len(r.times) > 0 && now.Sub(r.times[0]) > r.window {
		r.times = r.times[1:]
	}
	if len(r.times) >= r.n {
		return false
	}
	r.times = append(r.times, now)
	return true
}
