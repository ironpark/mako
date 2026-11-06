package skin

import (
	"testing"
)

func TestParseHex(t *testing.T) {
	for in, ok := range map[string]bool{"#7ccba0": true, "7CCBA0": true, " #000000 ": true, "#7cc": false, "#gggggg": false, "": false} {
		if _, got := ParseHex(in); got != ok {
			t.Errorf("ParseHex(%q) ok = %v", in, got)
		}
	}
	if c, _ := ParseHex("#7ccba0"); HexString(c) != "#7ccba0" {
		t.Errorf("HexString(ParseHex) = %s", HexString(c))
	}
}
