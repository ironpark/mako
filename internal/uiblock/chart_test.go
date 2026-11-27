package uiblock

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

func TestAChartIsRead(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"column","children":[{"kind":"chart","key":"c","chart":"line","text":"Builds",
		"labels":["mon","tue","wed"],"series":[{"name":"passed","tone":"success","values":[3,4.5,-1]},{"values":[0,1,2]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := tr.Root.GetChildren()[0]
	if c.GetChart() != "line" || len(c.GetSeries()) != 2 || c.GetSeries()[0].GetTone() != "success" ||
		fmt.Sprint(c.GetSeries()[0].GetValues()) != "[3 4.5 -1]" || strings.Join(c.GetLabels(), ",") != "mon,tue,wed" || c.GetText() != "Builds" {
		t.Fatalf("the chart is %v", c)
	}
	// A chart's points change by patch, as any field does.
	next := proto.CloneOf(tr.Root)
	next.Children[0].Series[0].Values = []float64{3, 4.5, 7}
	got, err := protocol.ApplyUi(tr.Root, protocol.DiffUi(tr.Root, next))
	if err != nil || !proto.Equal(got, next) {
		t.Fatalf("the patch came to %v, %v", got, err)
	}
}

func TestChartsOutOfBoundsAreRefused(t *testing.T) {
	values := func(n int) string {
		v := make([]int, n)
		b, _ := json.Marshal(v)
		return string(b)
	}
	series := func(n, points int) string {
		s := make([]string, n)
		for i := range s {
			s[i] = `{"values":` + values(points) + `}`
		}
		return "[" + strings.Join(s, ",") + "]"
	}
	cases := map[string]struct{ tree, want string }{
		"type":      {`{"kind":"chart","chart":"radar","series":[{"values":[1]}]}`, "not one of"},
		"none":      {`{"kind":"chart","chart":"line","series":[]}`, "1 to 4 series"},
		"many":      {`{"kind":"chart","chart":"line","series":` + series(5, 2) + `}`, "1 to 4 series"},
		"long":      {`{"kind":"chart","chart":"line","series":` + series(1, 201) + `}`, "1 to 200 values"},
		"all":       {`{"kind":"chart","chart":"line","series":` + series(4, 160) + `}`, "at most 600 points"},
		"null":      {`{"kind":"chart","chart":"line","series":[{"values":[1,null]}]}`, "finite"},
		"huge":      {`{"kind":"chart","chart":"line","series":[{"values":[1e300]}]}`, "finite"},
		"string":    {`{"kind":"chart","chart":"line","series":[{"values":["1"]}]}`, "does not read"},
		"colour":    {`{"kind":"chart","chart":"bar","series":[{"tone":"#ff0000","values":[1]}]}`, "colour token"},
		"color":     {`{"kind":"chart","chart":"bar","series":[{"color":"#ff0000","values":[1]}]}`, "unknown field"},
		"labels":    {`{"kind":"chart","chart":"bar","labels":["a"],"series":[{"values":[1,2]}]}`, "2 values for 1 labels"},
		"label":     {`{"kind":"chart","chart":"bar","labels":["` + strings.Repeat("x", 33) + `"],"series":[{"values":[1]}]}`, "a label is at most"},
		"spoof":     {`{"kind":"chart","chart":"bar","labels":["a\u202eb"],"series":[{"values":[1]}]}`, "reordering"},
		"name":      {`{"kind":"chart","chart":"bar","series":[{"name":"a\u0007","values":[1]}]}`, "reordering"},
		"elsewhere": {`{"kind":"text","text":"x","series":[{"values":[1]}]}`, "only a chart"},
		"tap":       {`{"kind":"chart","chart":"line","series":[{"values":[1]}],"onTap":["x"]}`, "takes onChange, not onTap"},
		"children":  {`{"kind":"chart","chart":"line","series":[{"values":[1]}],"children":[{"kind":"text"}]}`, "holds no children"},
	}
	for name, c := range cases {
		if _, err := Read([]byte(c.tree)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestDonutAndPieCharts(t *testing.T) {
	for _, typ := range []string{"donut", "pie"} {
		tr, err := Read([]byte(`{"kind":"chart","chart":"` + typ + `","text":"Breakdown","labels":["Docs","Code","Empty"],"series":[{"name":"count","tone":"accent","values":[12,5,0]}]}`))
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if tr.Root.GetChart() != typ || len(tr.Root.GetSeries()) != 1 || len(tr.Root.GetLabels()) != 3 {
			t.Fatalf("%s: unexpected node %+v", typ, tr.Root)
		}
		// Diff/patch round-trip.
		next := proto.CloneOf(tr.Root)
		next.Series[0].Values = []float64{10, 8, 2}
		got, err := protocol.ApplyUi(tr.Root, protocol.DiffUi(tr.Root, next))
		if err != nil || !proto.Equal(got, next) {
			t.Fatalf("%s patch: %v %v", typ, got, err)
		}
	}
	var vals17, labels17 []string
	for i := range 17 {
		vals17 = append(vals17, "1")
		labels17 = append(labels17, fmt.Sprintf(`"s%d"`, i))
	}
	bad := map[string]struct{ tree, want string }{
		"two-series": {`{"kind":"chart","chart":"donut","labels":["a"],"series":[{"values":[1]},{"values":[2]}]}`, "1 series"},
		"no-labels":  {`{"kind":"chart","chart":"pie","series":[{"values":[1,2]}]}`, "requires 2 labels"},
		"mismatch":   {`{"kind":"chart","chart":"donut","labels":["a"],"series":[{"values":[1,2]}]}`, "requires 2 labels"},
		"negative":   {`{"kind":"chart","chart":"pie","labels":["a","b"],"series":[{"values":[1,-0.5]}]}`, "non-negative"},
		"too-many":   {`{"kind":"chart","chart":"donut","labels":[` + strings.Join(labels17, ",") + `],"series":[{"values":[` + strings.Join(vals17, ",") + `]}]}`, "1 to 16 values"},
	}
	for name, c := range bad {
		if _, err := Read([]byte(c.tree)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestChartM3TypesSizingAndOnChange(t *testing.T) {
	for _, typ := range []string{"area", "stacked-bar", "heatmap"} {
		tr, err := Read([]byte(fmt.Sprintf(
			`{"kind":"chart","chart":%q,"key":"ch","text":"Metrics","maxHeight":180,"fill":true,"onChange":["pick"],"labels":["A","B","C"],"series":[{"name":"S1","tone":"accent","values":[1,2,3]},{"name":"S2","tone":"success","values":[4,5,6]}]}`,
			typ,
		)))
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if !tr.Root.GetChange() || !tr.Root.GetFill() || tr.Root.GetMaxHeight() != 180 {
			t.Fatalf("%s: unexpected node props %+v", typ, tr.Root)
		}
		for _, okVal := range []string{"1", "1:2", `{"index":2,"label":"C"}`, `{"row":1,"col":2,"series":"S2","label":"C","value":6}`} {
			m, err := tr.Message(&protocol.BlockEvent{Node: "", Kind: protocol.UiChange, Value: okVal})
			if err != nil || m.Name != "pick" {
				t.Fatalf("%s valid change %q failed: m=%+v err=%v", typ, okVal, m, err)
			}
		}
		for _, badVal := range []string{"", "-1", "99", "5:0", `{"index":99}`, `{"row":9,"col":0}`, `{"bad":1}`} {
			if _, err := tr.Message(&protocol.BlockEvent{Node: "", Kind: protocol.UiChange, Value: badVal}); err == nil {
				t.Errorf("%s invalid change %q unexpectedly accepted", typ, badVal)
			}
		}
	}
}
