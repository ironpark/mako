package plugin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// A plugin may call what needs no permission, ui, its own settings and
// log, from the start. Everything else is a permission its manifest asks
// for, by a name the Host API's schema gives its methods
// (hostapi.Permissions), which the user allows before the plugin runs:
// net:<host> for HTTP to one host, exactly, and the rest by name. exec is
// in the schema but no manifest may ask for it in this version.

// implicit are the groups every plugin has, which a manifest does not
// name.
var implicit = []string{"ui", "log", "settings", "none"}

// checkPermissions checks a manifest's permissions: each known, net with
// a host, none twice, and not too many.
func checkPermissions(perms []string) ([]string, error) {
	if len(perms) > maxPermissions {
		return nil, fmt.Errorf("more than %d permissions", maxPermissions)
	}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if err := checkPermission(p); err != nil {
			return nil, err
		}
		if slices.Contains(out, p) {
			return nil, fmt.Errorf("permission %q twice", p)
		}
		out = append(out, p)
	}
	return out, nil
}

func checkPermission(p string) error {
	if host, ok := strings.CutPrefix(p, hostapi.PermNet+":"); ok {
		if !validHost(host) {
			return fmt.Errorf("permission %q: net:<host> takes a host name, lowercase, without a port, scheme, wildcard or IP address", p)
		}
		return nil
	}
	switch {
	case p == hostapi.PermNet:
		return fmt.Errorf("permission net names its host: net:<host>")
	case p == hostapi.PermExec:
		return fmt.Errorf("permission exec is not granted to plugins in this version of mako")
	case slices.Contains(implicit, p):
		return fmt.Errorf("permission %q: every plugin has it; leave it out", p)
	case slices.Contains(hostapi.Permissions, p):
		return nil
	}
	return fmt.Errorf("permission %q is not one mako knows", p)
}

// validHost says h is a DNS name of two labels or more, lowercase, each
// of letters, digits and inner hyphens, the last not all digits: not an
// IP address, not localhost, no port, no trailing dot.
func validHost(h string) bool {
	if len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != ""
}

// grants are the permissions a plugin holds: those both asked for by its
// manifest and allowed by the user.
type grants map[string]bool

// grantsOf are the permissions of m the user allowed.
func grantsOf(m *Manifest, allowed []string) grants {
	g := grants{}
	for _, p := range m.Permissions {
		if slices.Contains(allowed, p) {
			g[p] = true
		}
	}
	return g
}

// Has makes grants a hostapi.Grants: PermNet is held with any net:<host>.
func (g grants) Has(perm string) bool {
	if perm == hostapi.PermNone {
		return true
	}
	if perm == hostapi.PermNet {
		for p := range g {
			if strings.HasPrefix(p, hostapi.PermNet+":") {
				return true
			}
		}
		return false
	}
	return g[perm]
}

// net says the plugin may reach host.
func (g grants) net(host string) bool { return g[hostapi.PermNet+":"+host] }

// asked are the permissions m asks for that the user has not allowed.
func asked(m *Manifest, allowed []string) []string {
	var out []string
	for _, p := range m.Permissions {
		if !slices.Contains(allowed, p) {
			out = append(out, p)
		}
	}
	return out
}
