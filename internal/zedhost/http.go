package zedhost

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

const maxHTTPRedirects = 10

var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		// IPv4
		"0.0.0.0/8",          // this network
		"100.64.0.0/10",      // shared address space (carrier-grade NAT)
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.2.0/24",       // documentation (TEST-NET-1)
		"192.88.99.0/24",     // 6to4 relay anycast, deprecated
		"198.18.0.0/15",      // benchmarking
		"198.51.100.0/24",    // documentation (TEST-NET-2)
		"203.0.113.0/24",     // documentation (TEST-NET-3)
		"240.0.0.0/4",        // reserved, and the limited broadcast
		"255.255.255.255/32", // limited broadcast
		// IPv6
		"64:ff9b:1::/48", // local-use IPv4/IPv6 translation
		"100::/64",       // discard-only
		"100:0:0:1::/64", // dummy prefix
		"2001::/23",      // IETF protocol assignments, Teredo among them
		"2001:db8::/32",  // documentation
		"3fff::/20",      // documentation
		"5f00::/16",      // segment routing (SRv6) SIDs
		"fec0::/10",      // site-local, deprecated
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

var embedded = []struct {
	prefix netip.Prefix
	at     int
}{
	{netip.MustParsePrefix("::ffff:0:0/96"), 12},   // IPv4-mapped
	{netip.MustParsePrefix("::/96"), 12},           // IPv4-compatible, deprecated
	{netip.MustParsePrefix("::ffff:0:0:0/96"), 12}, // SIIT, IPv4-translated
	{netip.MustParsePrefix("64:ff9b::/96"), 12},    // NAT64, well-known prefix
	{netip.MustParsePrefix("2002::/16"), 2},        // 6to4
}

func embeddedIPv4(a netip.Addr) (netip.Addr, bool) {
	if !a.Is6() {
		return netip.Addr{}, false
	}
	b := a.As16()
	for _, e := range embedded {
		if e.prefix.Contains(a) {
			return netip.AddrFrom4([4]byte(b[e.at : e.at+4])), true
		}
	}
	return netip.Addr{}, false
}

// publicAddr reports whether a is a globally routable public internet address.
func publicAddr(a netip.Addr) bool {
	a = a.WithZone("")
	if !a.IsValid() {
		return false
	}
	if v4, ok := embeddedIPv4(a); ok {
		return publicAddr(v4)
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() ||
		a.IsUnspecified() || a.IsInterfaceLocalMulticast() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func checkExternalURL(u *url.URL) error {
	if u == nil {
		return errors.New("empty URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Opaque != "" || u.Host == "" {
		return errors.New("the URL names no host")
	}
	if u.User != nil {
		return errors.New("a URL with a user or password is refused")
	}
	return nil
}

func ssrfControl(allowed map[string]bool) func(string, string, syscall.RawConn) error {
	return func(_ string, address string, _ syscall.RawConn) error {
		if allowed[address] {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !publicAddr(ap.Addr()) {
			return fmt.Errorf("refusing connection to non-public address %s", address)
		}
		return nil
	}
}

func newSSRFTransport(allowed map[string]bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: ssrfControl(allowed),
	}
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           16,
		IdleConnTimeout:        30 * time.Second,
	}
}

func defaultCheckRedirect(limit int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if err := checkExternalURL(req.URL); err != nil {
			return err
		}
		if len(via) > limit {
			return fmt.Errorf("more than %d redirects", limit)
		}
		return nil
	}
}

func newSSRFClient(timeout time.Duration, allowedAddrs ...string) *http.Client {
	var allowed map[string]bool
	if len(allowedAddrs) > 0 {
		allowed = make(map[string]bool, len(allowedAddrs))
		for _, a := range allowedAddrs {
			allowed[a] = true
		}
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     newSSRFTransport(allowed),
		CheckRedirect: defaultCheckRedirect(maxHTTPRedirects),
	}
}

var (
	sharedTransport      = newSSRFTransport(nil)
	sharedDownloadClient = &http.Client{
		Timeout:       5 * time.Minute,
		Transport:     sharedTransport,
		CheckRedirect: defaultCheckRedirect(maxHTTPRedirects),
	}
	sharedAPIClient = &http.Client{
		Timeout:       30 * time.Second,
		Transport:     sharedTransport,
		CheckRedirect: defaultCheckRedirect(maxHTTPRedirects),
	}
)
