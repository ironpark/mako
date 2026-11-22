package botavatar

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Geometry constants matching open-bot-avatar's contract v1.
const (
	EyeW          = 4.0
	EyeH          = 11.0
	BodyMin       = 6.0
	BodyMax       = 94.0
	EyeMargin     = 1.2
	GazeX         = 1.5
	GazeY         = 1.1
	EyeClearance  = 1.5
	EyeBoost      = 1.45
	EyeBoostPad   = 3.0
	HappyHalfW    = 0.72
	HappyBase     = 0.10
	HappyPeak     = 0.42
	HappyStroke   = 0.60
	pathMaxChars  = 6000
	pathMaxSegs   = 400
	pathMaxSubs   = 16
	pathCoordMin  = -20.0
	pathCoordMax  = 120.0
	pathFlatness  = 0.08
)

// Point is a 2D coordinate in viewBox units.
type Point [2]float64

// Bounds is an axis-aligned bounding box in viewBox units.
type Bounds struct {
	MinX, MinY, MaxX, MaxY float64
}

// Segment is one normalized absolute SVG path command.
type Segment struct {
	C                      byte // M, L, C, Q, A, Z
	X, Y                   float64
	X1, Y1, X2, Y2         float64
	RX, RY, Rot            float64
	LargeFlag, SweepFlag   int
}

// BodyAnalysis caches flattened polygons and bounds of an avatar silhouette.
type BodyAnalysis struct {
	Polys  [][]Point
	Bounds Bounds
	Area   float64
}

// GazeBounds stores normalized gaze travel (0..1) per direction.
type GazeBounds struct {
	Left, Right, Up, Down float64
}

var (
	slugRe     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
	badPathChr = regexp.MustCompile(`[^MmLlHhVvCcSsQqTtAaZz0-9.,eE+\-\s]`)
	tokenRe    = regexp.MustCompile(`([MmLlHhVvCcSsQqTtAaZz])|([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)`)
)

var argCounts = map[byte]int{
	'M': 2, 'L': 2, 'H': 1, 'V': 1,
	'C': 6, 'S': 4, 'Q': 4, 'T': 2,
	'A': 7, 'Z': 0,
}

type pathToken struct {
	isCmd bool
	cmd   byte
	num   float64
}

func tokenizePath(d string) ([]pathToken, error) {
	if loc := badPathChr.FindStringIndex(d); loc != nil {
		return nil, fmt.Errorf("unsupported character %q", d[loc[0]:loc[1]])
	}
	matches := tokenRe.FindAllStringSubmatch(d, -1)
	tokens := make([]pathToken, 0, len(matches))
	for _, m := range matches {
		if m[1] != "" {
			tokens = append(tokens, pathToken{isCmd: true, cmd: m[1][0]})
		} else if m[2] != "" {
			v, err := strconv.ParseFloat(m[2], 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("invalid number %q", m[2])
			}
			tokens = append(tokens, pathToken{num: v})
		}
	}
	return tokens, nil
}

// ParsePath parses an SVG path string into normalized absolute segments.
func ParsePath(d string) ([]Segment, error) {
	if strings.TrimSpace(d) == "" {
		return nil, errors.New("path is empty")
	}
	if len(d) > pathMaxChars {
		return nil, fmt.Errorf("path longer than %d characters", pathMaxChars)
	}
	tokens, err := tokenizePath(d)
	if err != nil {
		return nil, err
	}
	var (
		out      []Segment
		cx, cy   float64
		sx, sy   float64
		hasPrevC bool
		prevCX   float64
		prevCY   float64
		hasPrevQ bool
		prevQX   float64
		prevQY   float64
		cmd      byte
		subpaths int
		open     bool
	)
	for i := 0; i < len(tokens); {
		if tokens[i].isCmd {
			cmd = tokens[i].cmd
			i++
			if cmd == 'Z' || cmd == 'z' {
				if !open {
					return nil, errors.New("Z without an open subpath")
				}
				out = append(out, Segment{C: 'Z'})
				cx, cy = sx, sy
				hasPrevC, hasPrevQ, open = false, false, false
				continue
			}
		} else if cmd == 0 {
			return nil, errors.New("path must start with M")
		} else if cmd == 'Z' || cmd == 'z' {
			return nil, errors.New("numbers after Z")
		}

		up := cmd
		rel := false
		if cmd >= 'a' && cmd <= 'z' {
			up = cmd - ('a' - 'A')
			rel = true
		}
		if len(out) == 0 && up != 'M' {
			return nil, errors.New("path must start with M")
		}
		n, ok := argCounts[up]
		if !ok {
			return nil, fmt.Errorf("unsupported command %c", cmd)
		}
		if i+n > len(tokens) {
			return nil, fmt.Errorf("command %c expects %d numbers", cmd, n)
		}
		var args [7]float64
		for k := 0; k < n; k++ {
			if tokens[i+k].isCmd {
				return nil, fmt.Errorf("command %c expects %d numbers", cmd, n)
			}
			args[k] = tokens[i+k].num
		}
		i += n
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		nextHasC, nextHasQ := false, false
		var nextCX, nextCY, nextQX, nextQY float64

		switch up {
		case 'M':
			if open {
				out = append(out, Segment{C: 'Z'})
			}
			cx, cy = args[0]+ox, args[1]+oy
			sx, sy = cx, cy
			out = append(out, Segment{C: 'M', X: cx, Y: cy})
			subpaths++
			open = true
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L':
			cx, cy = args[0]+ox, args[1]+oy
			out = append(out, Segment{C: 'L', X: cx, Y: cy})
		case 'H':
			cx = args[0] + ox
			out = append(out, Segment{C: 'L', X: cx, Y: cy})
		case 'V':
			cy = args[0] + oy
			out = append(out, Segment{C: 'L', X: cx, Y: cy})
		case 'C':
			seg := Segment{C: 'C', X1: args[0] + ox, Y1: args[1] + oy, X2: args[2] + ox, Y2: args[3] + oy, X: args[4] + ox, Y: args[5] + oy}
			out = append(out, seg)
			nextHasC, nextCX, nextCY = true, seg.X2, seg.Y2
			cx, cy = seg.X, seg.Y
		case 'S':
			x1, y1 := cx, cy
			if hasPrevC {
				x1, y1 = 2*cx-prevCX, 2*cy-prevCY
			}
			seg := Segment{C: 'C', X1: x1, Y1: y1, X2: args[0] + ox, Y2: args[1] + oy, X: args[2] + ox, Y: args[3] + oy}
			out = append(out, seg)
			nextHasC, nextCX, nextCY = true, seg.X2, seg.Y2
			cx, cy = seg.X, seg.Y
		case 'Q':
			seg := Segment{C: 'Q', X1: args[0] + ox, Y1: args[1] + oy, X: args[2] + ox, Y: args[3] + oy}
			out = append(out, seg)
			nextHasQ, nextQX, nextQY = true, seg.X1, seg.Y1
			cx, cy = seg.X, seg.Y
		case 'T':
			x1, y1 := cx, cy
			if hasPrevQ {
				x1, y1 = 2*cx-prevQX, 2*cy-prevQY
			}
			seg := Segment{C: 'Q', X1: x1, Y1: y1, X: args[0] + ox, Y: args[1] + oy}
			out = append(out, seg)
			nextHasQ, nextQX, nextQY = true, x1, y1
			cx, cy = seg.X, seg.Y
		case 'A':
			rx, ry, rot, la, sw := args[0], args[1], args[2], args[3], args[4]
			if rx < 0 || ry < 0 {
				return nil, errors.New("arc radii must be non-negative")
			}
			if (la != 0 && la != 1) || (sw != 0 && sw != 1) {
				return nil, errors.New("arc flags must be 0 or 1")
			}
			cx, cy = args[5]+ox, args[6]+oy
			out = append(out, Segment{C: 'A', RX: rx, RY: ry, Rot: rot, LargeFlag: int(la), SweepFlag: int(sw), X: cx, Y: cy})
		}

		hasPrevC, prevCX, prevCY = nextHasC, nextCX, nextCY
		hasPrevQ, prevQX, prevQY = nextHasQ, nextQX, nextQY
		if len(out) > pathMaxSegs {
			return nil, fmt.Errorf("more than %d segments", pathMaxSegs)
		}
	}
	if open {
		out = append(out, Segment{C: 'Z'})
	}
	if subpaths > pathMaxSubs {
		return nil, fmt.Errorf("more than %d subpaths", pathMaxSubs)
	}
	checkCoord := func(v float64) error {
		if v < pathCoordMin || v > pathCoordMax {
			return fmt.Errorf("coordinate %.2f outside 0..100 viewBox", v)
		}
		return nil
	}
	for _, s := range out {
		switch s.C {
		case 'M', 'L':
			if err := checkCoord(s.X); err != nil {
				return nil, err
			}
			if err := checkCoord(s.Y); err != nil {
				return nil, err
			}
		case 'C':
			for _, v := range []float64{s.X1, s.Y1, s.X2, s.Y2, s.X, s.Y} {
				if err := checkCoord(v); err != nil {
					return nil, err
				}
			}
		case 'Q':
			for _, v := range []float64{s.X1, s.Y1, s.X, s.Y} {
				if err := checkCoord(v); err != nil {
					return nil, err
				}
			}
		case 'A':
			for _, v := range []float64{s.RX, s.RY, s.X, s.Y} {
				if err := checkCoord(v); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

func fmtNum(n float64) string {
	r := math.Round(n*100) / 100
	if r == 0 {
		return "0"
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// SerializePath writes normalized absolute path segments compactly.
func SerializePath(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		switch s.C {
		case 'M':
			b.WriteByte('M')
			b.WriteString(fmtNum(s.X))
			b.WriteByte(' ')
			b.WriteString(fmtNum(s.Y))
		case 'L':
			b.WriteByte('L')
			b.WriteString(fmtNum(s.X))
			b.WriteByte(' ')
			b.WriteString(fmtNum(s.Y))
		case 'C':
			fmt.Fprintf(&b, "C%s %s %s %s %s %s", fmtNum(s.X1), fmtNum(s.Y1), fmtNum(s.X2), fmtNum(s.Y2), fmtNum(s.X), fmtNum(s.Y))
		case 'Q':
			fmt.Fprintf(&b, "Q%s %s %s %s", fmtNum(s.X1), fmtNum(s.Y1), fmtNum(s.X), fmtNum(s.Y))
		case 'A':
			fmt.Fprintf(&b, "A%s %s %s %d %d %s %s", fmtNum(s.RX), fmtNum(s.RY), fmtNum(s.Rot), s.LargeFlag, s.SweepFlag, fmtNum(s.X), fmtNum(s.Y))
		case 'Z':
			b.WriteByte('Z')
		}
	}
	return b.String()
}

func sampleArc(x0, y0 float64, s Segment, out *[]Point) {
	rx, ry := s.RX, s.RY
	x, y := s.X, s.Y
	if rx == 0 || ry == 0 || (x0 == x && y0 == y) {
		*out = append(*out, Point{x, y})
		return
	}
	phi := s.Rot * math.Pi / 180.0
	cosP, sinP := math.Cos(phi), math.Sin(phi)
	dx, dy := (x0-x)/2, (y0-y)/2
	x1p := cosP*dx + sinP*dy
	y1p := -sinP*dx + cosP*dy
	rx, ry = math.Abs(rx), math.Abs(ry)
	lambda := (x1p*x1p)/(rx*rx) + (y1p*y1p)/(ry*ry)
	if lambda > 1 {
		k := math.Sqrt(lambda)
		rx *= k
		ry *= k
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	coef := math.Sqrt(max(0, num/den))
	if s.LargeFlag == s.SweepFlag {
		coef = -coef
	}
	cxp := coef * rx * y1p / ry
	cyp := -coef * ry * x1p / rx
	cx := cosP*cxp - sinP*cyp + (x0+x)/2
	cy := sinP*cxp + cosP*cyp + (y0+y)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if s.SweepFlag == 0 && dt > 0 {
		dt -= 2 * math.Pi
	}
	if s.SweepFlag == 1 && dt < 0 {
		dt += 2 * math.Pi
	}
	step := 2 * math.Acos(max(-1.0, 1.0-pathFlatness/max(rx, ry)))
	steps := min(128, max(4, int(math.Ceil(math.Abs(dt)/step))))
	for k := 1; k <= steps; k++ {
		t := t1 + dt*float64(k)/float64(steps)
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		*out = append(*out, Point{cosP*ex - sinP*ey + cx, sinP*ex + cosP*ey + cy})
	}
}

// FlattenPath converts path segments into closed polygons (one per subpath).
func FlattenPath(segs []Segment) [][]Point {
	var polys [][]Point
	var cur []Point
	var px, py float64
	for _, s := range segs {
		switch s.C {
		case 'M':
			if len(cur) >= 3 {
				polys = append(polys, cur)
			}
			cur = []Point{{s.X, s.Y}}
			px, py = s.X, s.Y
		case 'L':
			cur = append(cur, Point{s.X, s.Y})
			px, py = s.X, s.Y
		case 'C':
			m := max(
				math.Hypot(px-2*s.X1+s.X2, py-2*s.Y1+s.Y2),
				math.Hypot(s.X1-2*s.X2+s.X, s.Y1-2*s.Y2+s.Y),
			)
			n := min(64, max(2, int(math.Ceil(math.Sqrt(0.75*m/pathFlatness)))))
			for k := 1; k <= n; k++ {
				t := float64(k) / float64(n)
				u := 1 - t
				cur = append(cur, Point{
					u*u*u*px + 3*u*u*t*s.X1 + 3*u*t*t*s.X2 + t*t*t*s.X,
					u*u*u*py + 3*u*u*t*s.Y1 + 3*u*t*t*s.Y2 + t*t*t*s.Y,
				})
			}
			px, py = s.X, s.Y
		case 'Q':
			m := math.Hypot(px-2*s.X1+s.X, py-2*s.Y1+s.Y)
			n := min(64, max(2, int(math.Ceil(math.Sqrt(0.25*m/pathFlatness)))))
			for k := 1; k <= n; k++ {
				t := float64(k) / float64(n)
				u := 1 - t
				cur = append(cur, Point{
					u*u*px + 2*u*t*s.X1 + t*t*s.X,
					u*u*py + 2*u*t*s.Y1 + t*t*s.Y,
				})
			}
			px, py = s.X, s.Y
		case 'A':
			sampleArc(px, py, s, &cur)
			px, py = s.X, s.Y
		case 'Z':
			if len(cur) >= 3 {
				polys = append(polys, cur)
				px, py = cur[0][0], cur[0][1]
			}
			cur = nil
		}
	}
	if len(cur) >= 3 {
		polys = append(polys, cur)
	}
	return polys
}

// PolyBounds returns the axis-aligned bounding box of polys.
func PolyBounds(polys [][]Point) Bounds {
	b := Bounds{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for _, p := range polys {
		for _, pt := range p {
			if pt[0] < b.MinX {
				b.MinX = pt[0]
			}
			if pt[1] < b.MinY {
				b.MinY = pt[1]
			}
			if pt[0] > b.MaxX {
				b.MaxX = pt[0]
			}
			if pt[1] > b.MaxY {
				b.MaxY = pt[1]
			}
		}
	}
	if math.IsInf(b.MinX, 1) {
		return Bounds{MinX: 20, MinY: 20, MaxX: 80, MaxY: 80}
	}
	return b
}

// Winding computes the nonzero winding number of (x, y) against polys.
func Winding(polys [][]Point, x, y float64) int {
	w := 0
	for _, poly := range polys {
		n := len(poly)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			xi, yi := poly[i][0], poly[i][1]
			xj, yj := poly[j][0], poly[j][1]
			if yj <= y {
				if yi > y && (xi-xj)*(y-yj)-(x-xj)*(yi-yj) > 0 {
					w++
				}
			} else if yi <= y && (xi-xj)*(y-yj)-(x-xj)*(yi-yj) < 0 {
				w--
			}
		}
	}
	return w
}

// IsInside reports whether (x, y) is filled under the nonzero rule.
func IsInside(polys [][]Point, x, y float64) bool {
	return Winding(polys, x, y) != 0
}

func insideRow(polys [][]Point, y float64, xs []float64, out []byte) {
	type hit struct {
		x float64
		d int
	}
	var hits []hit
	for _, poly := range polys {
		n := len(poly)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			xj, yj := poly[j][0], poly[j][1]
			xi, yi := poly[i][0], poly[i][1]
			if yj <= y && yi > y {
				hits = append(hits, hit{xj + (y-yj)*(xi-xj)/(yi-yj), 1})
			} else if yi <= y && yj > y {
				hits = append(hits, hit{xj + (y-yj)*(xi-xj)/(yi-yj), -1})
			}
		}
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].x < hits[b].x })
	w, h := 0, 0
	for k, x := range xs {
		for h < len(hits) && hits[h].x < x {
			w += hits[h].d
			h++
		}
		if w != 0 {
			out[k] = 1
		} else {
			out[k] = 0
		}
	}
}

// FilledArea estimates the filled area of polys on a 1-unit grid.
func FilledArea(polys [][]Point, b Bounds) float64 {
	var xs []float64
	for x := math.Floor(b.MinX) + 0.5; x < b.MaxX; x += 1 {
		xs = append(xs, x)
	}
	if len(xs) == 0 {
		return 0
	}
	row := make([]byte, len(xs))
	total := 0
	for y := math.Floor(b.MinY) + 0.5; y < b.MaxY; y += 1 {
		insideRow(polys, y, xs, row)
		for _, v := range row {
			total += int(v)
		}
	}
	return float64(total)
}

// EyeCenters returns the left and right eye center coordinates.
func EyeCenters(e Eyes) [2]Point {
	return [2]Point{
		{e.X - e.Spacing/2, e.Y},
		{e.X + e.Spacing/2, e.Y},
	}
}

func eyeOutlinePoints(cx, cy, scale, margin float64) []Point {
	hw := (EyeW*scale)/2 + margin
	hh := (EyeH*scale)/2 + margin
	straight := hh - hw
	pts := make([]Point, 0, 23)
	for k := 0; k < 16; k++ {
		a := float64(k) / 16.0 * math.Pi * 2
		x := math.Cos(a) * hw
		sa := math.Sin(a)
		sign := 0.0
		if sa > 0 {
			sign = 1
		} else if sa < 0 {
			sign = -1
		}
		y := sa*hw + sign*max(0, straight)
		pts = append(pts, Point{cx + x, cy + y})
	}
	for _, f := range [3]float64{-0.5, 0, 0.5} {
		pts = append(pts, Point{cx - hw, cy + f*straight}, Point{cx + hw, cy + f*straight})
	}
	pts = append(pts, Point{cx, cy})
	return pts
}

func drawnEyeOutlinePoints(cx, cy, scale, margin float64) []Point {
	pts := eyeOutlinePoints(cx, cy, scale, margin)
	w, h := EyeW*scale, EyeH*scale
	x0, x1 := cx-HappyHalfW*w, cx+HappyHalfW*w
	yb, yc := cy+HappyBase*h, cy-HappyPeak*h
	r := (HappyStroke*w)/2 + margin
	for i := 0; i <= 8; i++ {
		t := float64(i) / 8.0
		m := 1 - t
		x := m*m*x0 + 2*m*t*cx + t*t*x1
		y := (m*m+t*t)*yb + 2*m*t*yc
		for k := 0; k < 8; k++ {
			a := float64(k) / 8.0 * math.Pi * 2
			pts = append(pts, Point{x + math.Cos(a)*r, y + math.Sin(a)*r})
		}
	}
	return pts
}

func eyesFit(polys [][]Point, e Eyes, dx, dy, margin float64, includeArc bool) (Point, bool) {
	for _, c := range EyeCenters(e) {
		var pts []Point
		if includeArc {
			pts = drawnEyeOutlinePoints(c[0]+dx, c[1]+dy, e.Scale, margin)
		} else {
			pts = eyeOutlinePoints(c[0]+dx, c[1]+dy, e.Scale, margin)
		}
		for _, pt := range pts {
			if !IsInside(polys, pt[0], pt[1]) {
				return pt, false
			}
		}
	}
	return Point{}, true
}

// BoostedEyeScale enlarges the definition's eyes up to EyeBoost× while keeping
// EyeBoostPad clearance from the silhouette outline.
func BoostedEyeScale(polys [][]Point, e Eyes) float64 {
	for _, k := range []float64{EyeBoost, 1.38, 1.30, 1.22, 1.15, 1.08} {
		cand := e
		cand.Scale = e.Scale * k
		if _, ok := eyesFit(polys, cand, 0, 0, EyeBoostPad, true); ok {
			return cand.Scale
		}
	}
	return e.Scale
}

// GazeRange computes safe normalized gaze travel in each direction.
func GazeRange(polys [][]Point, e Eyes) GazeBounds {
	reach := func(ux, uy float64) float64 {
		for _, k := range []float64{1, 0.8, 0.6, 0.4, 0.2} {
			if _, ok := eyesFit(polys, e, ux*k*GazeX*e.Scale, uy*k*GazeY*e.Scale, EyeClearance, true); ok {
				return k
			}
		}
		return 0
	}
	return GazeBounds{
		Left:  reach(-1, 0),
		Right: reach(1, 0),
		Up:    reach(0, -1),
		Down:  reach(0, 1),
	}
}

// AnalyzeBody parses, flattens, and measures a body path.
func AnalyzeBody(path string) (BodyAnalysis, string, error) {
	segs, err := ParsePath(path)
	if err != nil {
		return BodyAnalysis{}, "", err
	}
	norm := SerializePath(segs)
	polys := FlattenPath(segs)
	b := PolyBounds(polys)
	return BodyAnalysis{Polys: polys, Bounds: b, Area: FilledArea(polys, b)}, norm, nil
}

// Validate checks a Definition against the Open Bot Avatar v1 contract and
// returns the normalized copy along with any validation errors.
func Validate(def Definition) (Definition, []string) {
	var errs []string
	if def.Version != 1 {
		errs = append(errs, "version must be 1")
	}
	if !slugRe.MatchString(def.ID) {
		errs = append(errs, "id must be a lowercase slug of 1-40 chars [a-z0-9-]")
	}
	name := strings.TrimSpace(def.Name)
	if name == "" || len(name) > 40 || strings.ContainsAny(name, "<>\x00\n\r\t") {
		errs = append(errs, "name must be 1-40 printable characters without < or >")
	}
	e := def.Eyes
	validEyes := true
	for _, v := range []float64{e.X, e.Y, e.Spacing, e.Scale} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			validEyes = false
		}
	}
	if !validEyes {
		errs = append(errs, "eyes fields must be finite numbers")
	} else {
		if e.Scale < 0.6 || e.Scale > 1.8 {
			errs = append(errs, fmt.Sprintf("eyes.scale %.2f out of range 0.6..1.8", e.Scale))
		}
		minSpacing := EyeW * e.Scale * 1.75
		if e.Spacing < minSpacing || e.Spacing > 40 {
			errs = append(errs, fmt.Sprintf("eyes.spacing %.2f out of range %.1f..40", e.Spacing, minSpacing))
		}
	}

	body, norm, err := AnalyzeBody(def.Body.Path)
	if err != nil {
		errs = append(errs, "body.path: "+err.Error())
	} else {
		b := body.Bounds
		if b.MinX < BodyMin || b.MinY < BodyMin || b.MaxX > BodyMax || b.MaxY > BodyMax {
			errs = append(errs, "body.path leaves safe area (6..94)")
		}
		w, h := b.MaxX-b.MinX, b.MaxY-b.MinY
		if max(w, h) < 40 || min(w, h) < 14 {
			errs = append(errs, fmt.Sprintf("body is too small (%.1fx%.1f)", w, h))
		}
		if body.Area < 600 {
			errs = append(errs, fmt.Sprintf("body fill area %.0f is too small (min 600)", body.Area))
		}
		if validEyes {
			if miss, ok := eyesFit(body.Polys, e, 0, 0, EyeMargin, false); !ok {
				errs = append(errs, fmt.Sprintf("eyes outside body near (%.1f, %.1f)", miss[0], miss[1]))
			}
		}
	}

	if len(errs) > 0 {
		return Definition{}, errs
	}
	return Definition{
		Version: 1,
		ID:      def.ID,
		Name:    name,
		Body:    Body{Path: norm},
		Eyes:    e,
	}, nil
}

// ParseJSON unmarshals and validates an Open Bot Avatar v1 JSON definition.
func ParseJSON(data []byte) (Definition, error) {
	var def Definition
	if err := json.Unmarshal(data, &def); err != nil {
		return Definition{}, fmt.Errorf("parse avatar JSON: %w", err)
	}
	norm, errs := Validate(def)
	if len(errs) > 0 {
		return Definition{}, errors.New(strings.Join(errs, "; "))
	}
	return norm, nil
}
