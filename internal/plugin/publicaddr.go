package plugin

import "net/netip"

// What the daemon reaches for plugins and their registries is the
// internet alone: an address IANA's special-purpose registries (RFC 6890
// and its updates, for IPv4 and IPv6) do not mark global is refused,
// whatever name resolved to it, and so is one that embeds an IPv4
// address in IPv6 by a translation or a tunnel (mapped, compatible,
// SIIT, NAT64, 6to4) unless that IPv4 address is public itself: a
// translator on the user's network would otherwise reach their own
// machines for whoever names the IPv6 form. A block the registries mark
// global in part (a few anycast addresses among the IETF's protocol
// assignments) is refused whole, which costs nothing a plugin needs.

// nonPublic are the special-purpose blocks not marked global, beyond
// what netip's own predicates tell (loopback, private, link-local,
// multicast, unspecified).
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

// publicAddr says an address is one of the internet's.
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

// embedded are the IPv6 blocks that carry an IPv4 address, and where.
var embedded = []struct {
	prefix netip.Prefix
	at     int // the IPv4 address's first byte
}{
	{netip.MustParsePrefix("::ffff:0:0/96"), 12},   // IPv4-mapped
	{netip.MustParsePrefix("::/96"), 12},           // IPv4-compatible, deprecated
	{netip.MustParsePrefix("::ffff:0:0:0/96"), 12}, // SIIT, IPv4-translated
	{netip.MustParsePrefix("64:ff9b::/96"), 12},    // NAT64, well-known prefix
	{netip.MustParsePrefix("2002::/16"), 2},        // 6to4
}

// embeddedIPv4 is the IPv4 address an IPv6 address carries by one of the
// embedded blocks.
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
