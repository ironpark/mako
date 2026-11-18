package plugin

import (
	"net/netip"
	"strconv"
	"strings"
)

// What a registry's index and the installer check by the manifest's own
// rules, so that a name an index gives is one a manifest could.

// ValidID says s is a plugin's ID a manifest may give.
func ValidID(s string) bool { return pluginID.MatchString(s) && !strings.HasPrefix(s, "mako") }

// ValidPublisher says s is a publisher's name a manifest may give.
func ValidPublisher(s string) bool { return pluginID.MatchString(s) }

// ValidVersion says s is a version a manifest may give.
func ValidVersion(s string) bool { return semver.MatchString(s) }

// ValidHost says h is a host name a net permission may name: not an IP
// address, not localhost.
func ValidHost(h string) bool { return validHost(h) }

// CheckPermissions checks permissions as a manifest's are checked.
func CheckPermissions(perms []string) error {
	_, err := checkPermissions(perms)
	return err
}

// PublicAddr says an address is the internet's, which the daemon's own
// requests for plugins may reach.
func PublicAddr(a netip.Addr) bool { return publicAddr(a) }

// CompareVersions orders two valid versions: by major, minor and patch,
// and a version with a -pre part before the same without; two pre parts
// by their text.
func CompareVersions(a, b string) int {
	na, pa := splitVersion(a)
	nb, pb := splitVersion(b)
	for i := range 3 {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	case pa < pb:
		return -1
	}
	return 1
}

func splitVersion(v string) ([3]int, string) {
	core, pre, _ := strings.Cut(v, "-")
	var out [3]int
	for i, part := range strings.SplitN(core, ".", 3) {
		out[i], _ = strconv.Atoi(part)
	}
	return out, pre
}
