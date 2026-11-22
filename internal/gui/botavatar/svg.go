package botavatar

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/ironpark/ggfx/svg"
	"github.com/ironpark/ggui/widget"
)

// Prepared holds cached body geometry derived once per definition.
type prepared struct {
	def      Definition
	normPath string
	bounds   Bounds
	eyeScale float64
	gaze     GazeBounds
	seed     uint32
}

var (
	prepMu    sync.RWMutex
	prepCache = map[string]*prepared{}

	docMu    sync.Mutex
	docCache = map[docKey]*svg.Document{}
	docOrder []docKey
)

const maxDocCache = 96

type docKey struct {
	id       string
	pathHash uint32
	color    string
	eyeColor string
	state    State
	mood     Mood
	depth    int // rounded to half-units
	reduced  bool
}

func prepareDef(def Definition) *prepared {
	pathHash := HashString(def.Body.Path)
	k := fmt.Sprintf("%s|%x|%.2f|%.2f|%.2f|%.2f", def.ID, pathHash, def.Eyes.X, def.Eyes.Y, def.Eyes.Spacing, def.Eyes.Scale)

	prepMu.RLock()
	if hit, ok := prepCache[k]; ok {
		prepMu.RUnlock()
		return hit
	}
	prepMu.RUnlock()

	analysis, norm, err := AnalyzeBody(def.Body.Path)
	if err != nil {
		// Fall back safely to raw path + standard box if caller passed an unvalidated path.
		norm = def.Body.Path
		analysis = BodyAnalysis{Bounds: Bounds{MinX: 16, MinY: 16, MaxX: 84, MaxY: 84}}
	}
	eyeScale := def.Eyes.Scale
	if eyeScale <= 0 {
		eyeScale = 1
	}
	var gaze GazeBounds
	if len(analysis.Polys) > 0 {
		eyeScale = BoostedEyeScale(analysis.Polys, def.Eyes)
		boosted := def.Eyes
		boosted.Scale = eyeScale
		gaze = GazeRange(analysis.Polys, boosted)
	} else {
		gaze = GazeBounds{Left: 1, Right: 1, Up: 1, Down: 1}
	}
	p := &prepared{
		def:      def,
		normPath: norm,
		bounds:   analysis.Bounds,
		eyeScale: eyeScale,
		gaze:     gaze,
		seed:     HashString(def.ID),
	}

	prepMu.Lock()
	if len(prepCache) >= 64 {
		clear(prepCache)
	}
	prepCache[k] = p
	prepMu.Unlock()
	return p
}

// GazeTravel returns the maximum safe eye offset (dx, dy) in viewBox units for
// interactive pointer tracking via widget.SVGController.Node("oba-gaze").
func GazeTravel(def Definition) (dx, dy float64) {
	p := prepareDef(def)
	gx := max(p.gaze.Left, p.gaze.Right)
	gy := max(p.gaze.Up, p.gaze.Down)
	if gx == 0 {
		gx = 0.6
	}
	if gy == 0 {
		gy = 0.6
	}
	return gx * GazeX * p.eyeScale * 1.4, gy * GazeY * p.eyeScale * 1.4
}

func resolveOpts(def Definition, opts Options) (colorHex, eyeHex string, state State, mood Mood, depth float64, seed uint32) {
	bodyRGB, ok := ParseHex(opts.Color)
	if !ok {
		bodyRGB, _ = ParseHex(DefaultColor(def.ID))
	}
	colorHex = ToHex(bodyRGB)

	if eyeRGB, ok := ParseHex(opts.EyeColor); ok {
		eyeHex = ToHex(eyeRGB)
	} else {
		eyeHex = ToHex(AutoEyeColor(bodyRGB))
	}

	state = opts.State
	if !IsState(state) {
		state = StateIdle
	}
	mood = opts.Mood
	if !IsMood(mood) {
		mood = MoodNeutral
	}
	if opts.Flat {
		depth = 0
	} else if opts.Depth > 0 {
		depth = clamp(opts.Depth, 0, MaxDepth)
	} else {
		depth = DefaultDepth
	}
	seed = opts.Seed
	if seed == 0 {
		seed = HashString(def.ID)
	}
	return
}

// Document returns a cached parsed *svg.Document ready for widget.SVG.
func Document(def Definition, opts Options) (*svg.Document, error) {
	colorHex, eyeHex, state, mood, depth, _ := resolveOpts(def, opts)
	key := docKey{
		id:       def.ID,
		pathHash: HashString(def.Body.Path),
		color:    colorHex,
		eyeColor: eyeHex,
		state:    state,
		mood:     mood,
		depth:    int(math.Round(depth * 2)),
		reduced:  opts.ReducedMotion,
	}

	docMu.Lock()
	if hit, ok := docCache[key]; ok {
		docMu.Unlock()
		return hit, nil
	}
	docMu.Unlock()

	src := RenderAnimatedSVG(def, opts)
	doc, err := widget.ParseSVG([]byte(src))
	if err != nil {
		return nil, err
	}

	docMu.Lock()
	if hit, ok := docCache[key]; ok {
		docMu.Unlock()
		return hit, nil
	}
	if len(docOrder) >= maxDocCache {
		evict := docOrder[0]
		docOrder = docOrder[1:]
		delete(docCache, evict)
	}
	docCache[key] = doc
	docOrder = append(docOrder, key)
	docMu.Unlock()
	return doc, nil
}

// RenderStillSVG renders a calm static SVG snapshot of def.
func RenderStillSVG(def Definition, opts Options) string {
	opts.ReducedMotion = true
	return RenderAnimatedSVG(def, opts)
}

// RenderAnimatedSVG emits a self-contained SVG with implied 3D volume layers,
// state-driven SMIL animations (idle breathing/blinking/wander, happy hop,
// working ribbon sweep, thinking bouncing dots, waiting sigh/slits, blocked
// exclamation alarm, done celebration ribbon cage), and named control nodes
// ("oba-root", "oba-body", "oba-eyes", "oba-gaze") for SVGController.
func RenderAnimatedSVG(def Definition, opts Options) string {
	p := prepareDef(def)
	colorHex, eyeHex, state, mood, depth, seed := resolveOpts(def, opts)
	bodyRGB, _ := ParseHex(colorHex)
	b := p.bounds
	cx := (b.MinX + b.MaxX) / 2
	cy := (b.MinY + b.MaxY) / 2
	bx, by := cx, b.MaxY
	still := opts.ReducedMotion

	var out strings.Builder
	out.Grow(4096)
	out.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="-16 -16 132 132">`)

	// Optional ribbons behind body (working & done states).
	if !still && (state == StateWorking || state == StateDone) {
		writeRibbons(&out, b, colorHex, seed, state == StateDone, false)
	}

	// Thinking and Blocked states replace the body with animated marks,
	// matching open-bot-avatar's signature state choreography.
	if state == StateThinking || state == StateBlocked {
		writeStateMark(&out, b, colorHex, state, still)
		out.WriteString(`</svg>`)
		return out.String()
	}

	// Root group: holds body motion (hop, roll, squash-and-stretch about base)
	// plus SVGController node hook ("oba-root").
	out.WriteString(`<g id="oba-root">`)
	if !still {
		writeRootMotion(&out, bx, by, state, mood)
	} else if state == StateWaiting {
		fmt.Fprintf(&out, `<g transform="rotate(7 %s %s)">`, fmtNum(bx), fmtNum(by))
	} else if state == StateWorking {
		fmt.Fprintf(&out, `<g transform="rotate(3 %s %s)">`, fmtNum(bx), fmtNum(by))
	}

	// Implied 3D layered volume body (back-to-front extruded silhouette stack).
	out.WriteString(`<g id="oba-body">`)
	writeVolumeLayers(&out, p.normPath, bodyRGB, colorHex, depth, cx, cy, still)
	out.WriteString(`</g>`)

	// Eyes group: outer "oba-eyes" has autonomous motion, inner "oba-gaze" is
	// left clean for interactive pointer gaze overrides via SVGController.
	out.WriteString(`<g id="oba-eyes">`)
	if !still {
		writeEyeWander(&out, p, state, mood)
	}
	out.WriteString(`<g id="oba-gaze">`)
	writeEyes(&out, p, eyeHex, state, mood, still)
	out.WriteString(`</g></g>`)

	if still && (state == StateWaiting || state == StateWorking) {
		out.WriteString(`</g>`)
	}
	out.WriteString(`</g>`)

	// Front ribbons cross in front of the body for true 3D wrap-around depth.
	if !still && (state == StateWorking || state == StateDone) {
		writeRibbons(&out, b, colorHex, seed, state == StateDone, true)
	}

	out.WriteString(`</svg>`)
	return out.String()
}

// writeVolumeLayers draws a back-to-front stack of slightly offset, shaded
// silhouette layers that produce open-bot-avatar's signature 3/4 extruded look.
func writeVolumeLayers(out *strings.Builder, pathD string, bodyRGB RGB, bodyHex string, depth, cx, cy float64, still bool) {
	if depth <= 0.1 {
		fmt.Fprintf(out, `<path d="%s" fill="%s"/>`, pathD, bodyHex)
		return
	}
	// Resting 3/4 perspective direction (matching BASE_YAW = -0.3, BASE_PITCH = 0.2):
	// back slices peek slightly right and down from the front face.
	layers := 5
	if depth < 4 {
		layers = 3
	}
	maxDX := depth * 0.62
	maxDY := depth * 0.36

	for i := layers; i >= 1; i-- {
		frac := float64(i) / float64(layers)
		dx := maxDX * frac
		dy := maxDY * frac
		// Subtle warm-rich shading toward the rear wall; keep back silhouette crisp.
		shadeAmt := 0.20 + 0.26*frac
		c := ToHex(Shade(bodyRGB, shadeAmt))
		fmt.Fprintf(out, `<g transform="translate(%s %s)">`, fmtNum(dx), fmtNum(dy))
		if !still && i == layers {
			// Gentle 3D yaw parallax on rear extrusion.
			swayX := 0.55 * frac
			fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="%s 0;%s 0;%s 0" dur="6.1s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.5;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`,
				fmtNum(-swayX), fmtNum(swayX), fmtNum(-swayX))
		}
		fmt.Fprintf(out, `<path d="%s" fill="%s"/>`, pathD, c)
		out.WriteString(`</g>`)
	}
	// Front face in crisp flat body color.
	fmt.Fprintf(out, `<path d="%s" fill="%s"/>`, pathD, bodyHex)
	_ = cx
	_ = cy
}

// writeRootMotion attaches state-specific SMIL transform animations to oba-root.
func writeRootMotion(out *strings.Builder, bx, by float64, state State, mood Mood) {
	bxs, bys := fmtNum(bx), fmtNum(by)
	switch {
	case state == StateIdle && mood == MoodHappy:
		// Hopping joyously every 0.92s with squash on landing and playful roll.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 -4.2;0 0;0 0.6;0 0" keyTimes="0;0.42;0.82;0.91;1" dur="0.92s" repeatCount="indefinite" calcMode="spline" keySplines="0.33 0 0.35 1;0.65 0 0.75 1;0.4 0 0.6 1;0.4 0 0.6 1"/>`)
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="rotate" additive="sum" values="-4 %s %s;4 %s %s;-4 %s %s" dur="1.84s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.5;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`,
			bxs, bys, bxs, bys, bxs, bys)

	case state == StateWorking:
		// Focused forward lean (+3°) with brisk 0.7s rhythmic work bob.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 -1.2;0 0" dur="0.7s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.5;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`)
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="rotate" additive="sum" values="2.4 %s %s;3.8 %s %s;2.4 %s %s" dur="0.7s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.5;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`,
			bxs, bys, bxs, bys, bxs, bys)

	case state == StateWaiting:
		// Leaning sideways (+7°) with a slow 6.5s sigh.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="rotate" additive="sum" values="6 %s %s;8.4 %s %s;6 %s %s" dur="6.5s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.33;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`,
			bxs, bys, bxs, bys, bxs, bys)
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 -1.1;0 0.2;0 0" keyTimes="0;0.18;0.38;1" dur="6.5s" repeatCount="indefinite" calcMode="spline" keySplines="0.4 0 0.6 1;0.4 0 0.6 1;0.4 0 0.6 1"/>`)

	case state == StateDone:
		// Celebratory lift & joyful sway cycle.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 -3.6;0 -0.4;0 -2.6;0 0;0 0" keyTimes="0;0.22;0.42;0.56;0.70;1" dur="4.4s" repeatCount="indefinite" calcMode="spline" keySplines="0.33 0 0.4 1;0.4 0 0.6 1;0.33 0 0.4 1;0.4 0 0.6 1;0.4 0 0.6 1"/>`)
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="rotate" additive="sum" values="0 %s %s;-6 %s %s;6 %s %s;-3 %s %s;0 %s %s;0 %s %s" keyTimes="0;0.18;0.38;0.54;0.70;1" dur="4.4s" repeatCount="indefinite" calcMode="spline" keySplines="0.42 0 0.58 1;0.42 0 0.58 1;0.42 0 0.58 1;0.42 0 0.58 1;0.42 0 0.58 1"/>`,
			bxs, bys, bxs, bys, bxs, bys, bxs, bys, bxs, bys, bxs, bys)

	default:
		// Calm idle breathing & gentle sway.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 -1.1;0 0" dur="3.4s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.5;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`)
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="rotate" additive="sum" values="-2.1 %s %s;2.1 %s %s;-2.1 %s %s" dur="5.3s" repeatCount="indefinite" calcMode="spline" keyTimes="0;0.5;1" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`,
			bxs, bys, bxs, bys, bxs, bys)
	}
}

// writeEyeWander adds subtle autonomous eye gaze saccades inside oba-eyes.
func writeEyeWander(out *strings.Builder, p *prepared, state State, mood Mood) {
	if mood == MoodHappy || state == StateDone {
		return // Happy arcs stay centered and cheerful.
	}
	rx := max(p.gaze.Right, 0.35) * GazeX * p.eyeScale
	lx := max(p.gaze.Left, 0.35) * GazeX * p.eyeScale
	uy := max(p.gaze.Up, 0.35) * GazeY * p.eyeScale
	dy := max(p.gaze.Down, 0.35) * GazeY * p.eyeScale

	switch state {
	case StateWorking:
		// Brisk scanning down at the work.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="%s %s;%s %s;%s %s;%s %s;%s %s" keyTimes="0;0.24;0.50;0.76;1" dur="2.1s" repeatCount="indefinite" calcMode="spline" keySplines="0.4 0 0.2 1;0.4 0 0.2 1;0.4 0 0.2 1;0.4 0 0.2 1"/>`,
			fmtNum(-lx*0.65), fmtNum(dy*0.45),
			fmtNum(rx*0.75), fmtNum(dy*0.60),
			fmtNum(-lx*0.30), fmtNum(dy*0.30),
			fmtNum(rx*0.45), fmtNum(dy*0.55),
			fmtNum(-lx*0.65), fmtNum(dy*0.45))
	case StateWaiting:
		// Glancing off to the side.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="%s %s;%s %s;%s %s" keyTimes="0;0.5;1" dur="6.5s" repeatCount="indefinite" calcMode="spline" keySplines="0.42 0 0.58 1;0.42 0 0.58 1"/>`,
			fmtNum(rx*0.45), fmtNum(dy*0.15),
			fmtNum(rx*0.72), fmtNum(dy*0.15),
			fmtNum(rx*0.45), fmtNum(dy*0.15))
	default:
		// Calm idle saccades with holds at center.
		fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 0;%s %s;%s %s;0 0;0 0;%s %s;%s %s;0 0" keyTimes="0;0.22;0.26;0.46;0.50;0.72;0.76;0.94;1" dur="7.6s" repeatCount="indefinite"/>`,
			fmtNum(rx*0.58), fmtNum(-uy*0.25),
			fmtNum(rx*0.58), fmtNum(-uy*0.25),
			fmtNum(-lx*0.52), fmtNum(dy*0.20),
			fmtNum(-lx*0.52), fmtNum(dy*0.20))
	}
}

// writeEyes draws the left and right eyes in the style of the active state/mood.
func writeEyes(out *strings.Builder, p *prepared, eyeHex string, state State, mood Mood, still bool) {
	centers := EyeCenters(p.def.Eyes)
	s := p.eyeScale
	w := EyeW * s
	h := EyeH * s

	happy := mood == MoodHappy || state == StateDone
	slit := !happy && state == StateWaiting
	working := !happy && state == StateWorking

	for _, c := range centers {
		ex, ey := c[0], c[1]
		switch {
		case happy:
			hw := w * HappyHalfW
			base := ey + h*HappyBase
			peak := ey - h*HappyPeak
			sw := w * HappyStroke
			fmt.Fprintf(out, `<path d="M%s %sQ%s %s %s %s" fill="none" stroke="%s" stroke-width="%s" stroke-linecap="round"/>`,
				fmtNum(ex-hw), fmtNum(base), fmtNum(ex), fmtNum(peak), fmtNum(ex+hw), fmtNum(base), eyeHex, fmtNum(sw))

		case slit:
			// Flat slightly tipped slit for waiting bots.
			slitW := w * 1.9
			slitH := w * 0.52
			r := slitH / 2
			fmt.Fprintf(out, `<g transform="rotate(11.5 %s %s)">`, fmtNum(ex), fmtNum(ey))
			fmt.Fprintf(out, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`,
				fmtNum(ex-slitW/2), fmtNum(ey-slitH/2), fmtNum(slitW), fmtNum(slitH), fmtNum(r), eyeHex)
			out.WriteString(`</g>`)

		default:
			// Standard rounded pill eye with natural blink animation.
			eh := h
			if working {
				eh = h * 0.84 // Focused squint while working.
			}
			r := min(w, eh) / 2
			fmt.Fprintf(out, `<g transform="translate(%s %s)">`, fmtNum(ex), fmtNum(ey))
			if !still {
				dur := "4.2s"
				if working {
					dur = "3.1s"
				}
				fmt.Fprintf(out, `<g><animateTransform attributeName="transform" type="scale" values="1 1;1 1;1 0.1;1 1;1 1" keyTimes="0;0.88;0.92;0.96;1" dur="%s" repeatCount="indefinite"/>`, dur)
			}
			fmt.Fprintf(out, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`,
				fmtNum(-w/2), fmtNum(-eh/2), fmtNum(w), fmtNum(eh), fmtNum(r), eyeHex)
			if !still {
				out.WriteString(`</g>`)
			}
			out.WriteString(`</g>`)
		}
	}
}

// writeRibbons renders 3D wrap-around OKLCH colored ribbon arcs either behind
// (front=false) or in front of (front=true) the avatar body. Splitting each
// tilted elliptical orbit into its back half-arc and front half-arc lets the
// ribbons genuinely weave behind and in front of the body using pure SMIL!
func writeRibbons(out *strings.Builder, b Bounds, bodyHex string, seed uint32, cage bool, front bool) {
	cx := (b.MinX + b.MaxX) / 2
	cy := (b.MinY + b.MaxY) / 2
	rxBase := max((b.MaxX-b.MinX)/2, (b.MaxY-b.MinY)/2) + 8.5
	ryBase := rxBase * 0.36
	pal := RibbonPalette(bodyHex, seed)

	type hoopSpec struct {
		rollDeg float64
		dur     string
		begin   string
	}
	var hoops []hoopSpec
	if cage {
		hoops = []hoopSpec{
			{rollDeg: -14, dur: "2.2s", begin: "0s"},
			{rollDeg: 46, dur: "2.2s", begin: "0.22s"},
			{rollDeg: 106, dur: "2.2s", begin: "0.44s"},
		}
	} else {
		hoops = []hoopSpec{
			{rollDeg: -12, dur: "3.4s", begin: "0s"},
		}
	}

	sideName := "back"
	if front {
		sideName = "front"
	}
	fmt.Fprintf(out, `<g id="oba-ribbons-%s">`, sideName)
	for hi, h := range hoops {
		fmt.Fprintf(out, `<g transform="rotate(%s %s %s)">`, fmtNum(h.rollDeg), fmtNum(cx), fmtNum(cy))
		for lane := 0; lane < 3; lane++ {
			rx := rxBase + float64(lane)*5.2
			ry := ryBase + float64(lane)*2.1
			col := pal[(hi*3+lane)%len(pal)]
			sw := 5.2 - float64(lane)*0.6
			if cage {
				sw = 3.8 - float64(lane)*0.35
			}
			// Half-ellipse arc: top half (sweep=1) crosses in front of the body,
			// bottom half (sweep=0) sweeps behind it.
			var d string
			if front {
				d = fmt.Sprintf("M%s %sA%s %s 0 0 1 %s %s",
					fmtNum(cx-rx), fmtNum(cy), fmtNum(rx), fmtNum(ry), fmtNum(cx+rx), fmtNum(cy))
			} else {
				d = fmt.Sprintf("M%s %sA%s %s 0 0 1 %s %s",
					fmtNum(cx+rx), fmtNum(cy), fmtNum(rx), fmtNum(ry), fmtNum(cx-rx), fmtNum(cy))
			}
			// Approximate half-ellipse perimeter (Ramanujan / 2).
			perim := math.Pi * (3*(rx+ry) - math.Sqrt((3*rx+ry)*(rx+3*ry))) / 2
			dash := perim * 0.58
			gap := perim * 1.9
			startOff := perim + dash
			endOff := -perim * 0.8
			if !front {
				// Back half leads/follows the front half by half a cycle so the
				// ribbon seamlessly passes around the sides.
				startOff = perim*1.5 + dash
				endOff = -perim * 0.3
			}
			laneDelay := float64(lane) * 0.11
			begin := fmt.Sprintf("%.2fs", parseSeconds(h.begin)+laneDelay)
			fmt.Fprintf(out, `<path d="%s" fill="none" stroke="%s" stroke-width="%s" stroke-linecap="round" stroke-dasharray="%s %s" opacity="0.92">`,
				d, col, fmtNum(sw), fmtNum(dash), fmtNum(gap))
			fmt.Fprintf(out, `<animate attributeName="stroke-dashoffset" values="%s;%s" dur="%s" begin="%s" repeatCount="indefinite"/>`,
				fmtNum(startOff), fmtNum(endOff), h.dur, begin)
			fmt.Fprintf(out, `<animate attributeName="opacity" values="0;0.95;0.95;0" keyTimes="0;0.12;0.78;1" dur="%s" begin="%s" repeatCount="indefinite"/>`,
				h.dur, begin)
			out.WriteString(`</path>`)
		}
		out.WriteString(`</g>`)
	}
	out.WriteString(`</g>`)
}

func parseSeconds(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "s")
	var v float64
	_, _ = fmt.Sscanf(s, "%f", &v)
	return v
}

// writeStateMark renders the iconic open-bot-avatar marks for Thinking (three
// hopping dots) and Blocked (jumping, shaking exclamation mark) with 3D shade.
func writeStateMark(out *strings.Builder, b Bounds, colorHex string, state State, still bool) {
	cx := (b.MinX + b.MaxX) / 2
	cy := (b.MinY + b.MaxY) / 2
	u := clamp(min(b.MaxX-b.MinX, b.MaxY-b.MinY), 38, 76)
	bodyRGB, _ := ParseHex(colorHex)
	shadowHex := ToHex(Shade(bodyRGB, 0.36))

	out.WriteString(`<g id="oba-root"><g id="oba-body"><g id="oba-eyes"><g id="oba-gaze">`)
	switch state {
	case StateThinking:
		r := 0.115 * u
		step := 0.31 * u
		for i := 0; i < 3; i++ {
			dx := cx + float64(i-1)*step
			delay := fmt.Sprintf("%.2fs", float64(i)*0.17)
			fmt.Fprintf(out, `<g transform="translate(%s %s)">`, fmtNum(dx), fmtNum(cy))
			if !still {
				fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" additive="sum" values="0 0;0 %s;0 0;0 0" keyTimes="0;0.2;0.4;1" dur="1.25s" begin="%s" repeatCount="indefinite" calcMode="spline" keySplines="0.33 0 0.4 1;0.4 0 0.6 1;0 0 1 1"/>`,
					fmtNum(-0.18*u), delay)
			}
			fmt.Fprintf(out, `<circle cx="2" cy="1.5" r="%s" fill="%s"/>`, fmtNum(r), shadowHex)
			fmt.Fprintf(out, `<circle cx="0" cy="0" r="%s" fill="%s"/>`, fmtNum(r), colorHex)
			out.WriteString(`</g>`)
		}

	case StateBlocked:
		barW := 0.22 * u
		barH := 0.50 * u
		dotR := 0.12 * u
		gap := 0.10 * u
		total := barH + gap + 2*dotR
		barY := -total/2 + barH/2
		dotY := total/2 - dotR
		jump := -0.10 * u

		fmt.Fprintf(out, `<g transform="translate(%s %s)">`, fmtNum(cx), fmtNum(cy))
		if !still {
			fmt.Fprintf(out, `<animateTransform attributeName="transform" type="rotate" additive="sum" values="0;-10;10;-6;4;0;0" keyTimes="0;0.06;0.12;0.18;0.24;0.30;1" dur="2.6s" repeatCount="indefinite"/>`)
		}
		// Bar
		fmt.Fprintf(out, `<g>`)
		if !still {
			fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" values="0 0;0 %s;0 0;0 0" keyTimes="0;0.15;0.30;1" dur="2.6s" repeatCount="indefinite" calcMode="spline" keySplines="0.33 0 0.4 1;0.4 0 0.6 1;0 0 1 1"/>`,
				fmtNum(jump))
		}
		fmt.Fprintf(out, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`,
			fmtNum(-barW/2+2.2), fmtNum(barY-barH/2+1.6), fmtNum(barW), fmtNum(barH), fmtNum(barW/2), shadowHex)
		fmt.Fprintf(out, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`,
			fmtNum(-barW/2), fmtNum(barY-barH/2), fmtNum(barW), fmtNum(barH), fmtNum(barW/2), colorHex)
		out.WriteString(`</g>`)
		// Dot (lags the bar slightly on jump)
		fmt.Fprintf(out, `<g>`)
		if !still {
			fmt.Fprintf(out, `<animateTransform attributeName="transform" type="translate" values="0 0;0 %s;0 0;0 0" keyTimes="0;0.16;0.30;1" dur="2.6s" repeatCount="indefinite" calcMode="spline" keySplines="0.33 0 0.4 1;0.4 0 0.6 1;0 0 1 1"/>`,
				fmtNum(jump*0.45))
		}
		fmt.Fprintf(out, `<circle cx="2.2" cy="%s" r="%s" fill="%s"/>`, fmtNum(dotY+1.6), fmtNum(dotR), shadowHex)
		fmt.Fprintf(out, `<circle cx="0" cy="%s" r="%s" fill="%s"/>`, fmtNum(dotY), fmtNum(dotR), colorHex)
		out.WriteString(`</g>`)
		out.WriteString(`</g>`)
	}
	out.WriteString(`</g></g></g></g>`)
}
