package plugin

import (
	"os"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/uiblock"
)

// docs/plugins.md lists every method of the Host API and every kind of
// node, so that it cannot fall behind the schema.
func TestDocsListTheAPI(t *testing.T) {
	data, err := os.ReadFile("../../docs/plugins.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, m := range hostapi.Methods {
		if !strings.Contains(doc, "`"+m.Name) {
			t.Errorf("docs/plugins.md does not list %s", m.Name)
		}
	}
	for _, k := range uiblock.Kinds {
		if !strings.Contains(doc, "`"+k+"`") {
			t.Errorf("docs/plugins.md does not list the kind %s", k)
		}
	}
	for _, p := range hostapi.Permissions {
		if !strings.Contains(doc, "`"+p) {
			t.Errorf("docs/plugins.md does not list the permission %s", p)
		}
	}
}
