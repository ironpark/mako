package uiblock

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestTier5ValidChartChangeJaggedAndMixedRejected(t *testing.T) {
	tr, err := Read([]byte(`{
		"kind":"chart","key":"hm","chart":"heatmap","onChange":["pick"],
		"series":[{"name":"r0","values":[1,2,3,4,5]},{"name":"r1","values":[9]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, good := range []string{`{"row":0,"col":4}`, `{"row":1,"col":0}`, `1:0`, `{"index":0}`} {
		if _, err := tr.Message(&protocol.BlockEvent{Node: tr.Root.GetId(), Kind: protocol.UiChange, Value: good}); err != nil {
			t.Fatalf("expected valid chart change %s to be accepted, got %v", good, err)
		}
	}
	for _, bad := range []string{
		`{"row":1,"col":4}`,
		`1:4`,
		`{"index":0,"row":-1,"col":999}`,
		`{"index":0,"row":0,"col":0}`,
		`{"row":0}`,
		`{"col":0}`,
	} {
		if _, err := tr.Message(&protocol.BlockEvent{Node: tr.Root.GetId(), Kind: protocol.UiChange, Value: bad}); err == nil {
			t.Fatalf("expected invalid chart change %s to be rejected", bad)
		}
	}
}
