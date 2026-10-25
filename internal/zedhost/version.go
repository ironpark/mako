package zedhost

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// apiVersion is the version of Zed's extension API a component is built
// against, which decides the shape of a few of its records.
type apiVersion struct{ major, minor, patch uint16 }

func (v apiVersion) atLeast(major, minor, patch uint16) bool {
	if v.major != major {
		return v.major > major
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}

func (v apiVersion) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// The API versions the host speaks: context servers came in 0.2.0.
var (
	minAPI = apiVersion{0, 2, 0}
	maxAPI = apiVersion{0, 8, 0}
)

// apiSection is the custom section zed_extension_api writes the version
// in: a name of 15 bytes, then major, minor and patch as big-endian u16s.
var apiSection = []byte("\x0fzed:api-version")

// parseAPIVersion reads the version from the wasm's custom section, or
// else from the manifest's lib.version, and checks it is one the host
// speaks.
func parseAPIVersion(manifest string, wasm []byte) (apiVersion, error) {
	var v apiVersion
	if i := bytes.Index(wasm, apiSection); i >= 0 && len(wasm) >= i+len(apiSection)+6 {
		b := wasm[i+len(apiSection):]
		v = apiVersion{binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:]), binary.BigEndian.Uint16(b[4:])}
	} else {
		parts := strings.Split(manifest, ".")
		if len(parts) != 3 {
			return v, fmt.Errorf("no extension API version")
		}
		var n [3]uint16
		for i, p := range parts {
			x, err := strconv.ParseUint(p, 10, 16)
			if err != nil {
				return v, fmt.Errorf("extension API version %q", manifest)
			}
			n[i] = uint16(x)
		}
		v = apiVersion{n[0], n[1], n[2]}
	}
	if !v.atLeast(minAPI.major, minAPI.minor, minAPI.patch) {
		return v, fmt.Errorf("extension API %s is older than %s", v, minAPI)
	}
	if v.major != maxAPI.major || v.minor > maxAPI.minor {
		return v, fmt.Errorf("extension API %s is newer than %s", v, maxAPI)
	}
	return v, nil
}
