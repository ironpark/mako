package botavatar

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RGB is an 8-bit sRGB triple stored as floats in 0..255.
type RGB [3]float64

// DarkInk and LightInk are the two high-contrast eye ink presets.
var (
	DarkInk  = RGB{20, 22, 34}
	LightInk = RGB{250, 250, 255}
)

// ParseHex parses #rgb or #rrggbb strings into RGB.
func ParseHex(input string) (RGB, bool) {
	s := strings.TrimSpace(input)
	if !strings.HasPrefix(s, "#") {
		return RGB{}, false
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return RGB{}, false
	}
	r, err1 := strconv.ParseUint(h[0:2], 16, 8)
	g, err2 := strconv.ParseUint(h[2:4], 16, 8)
	b, err3 := strconv.ParseUint(h[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return RGB{}, false
	}
	return RGB{float64(r), float64(g), float64(b)}, true
}

// ToHex formats an RGB color as #rrggbb.
func ToHex(c RGB) string {
	ch := func(v float64) uint8 {
		return uint8(math.Round(clamp(v, 0, 255)))
	}
	return fmt.Sprintf("#%02x%02x%02x", ch(c[0]), ch(c[1]), ch(c[2]))
}

// Mix linearly interpolates from a to b by t.
func Mix(a, b RGB, t float64) RGB {
	return RGB{
		a[0] + (b[0]-a[0])*t,
		a[1] + (b[1]-a[1])*t,
		a[2] + (b[2]-a[2])*t,
	}
}

// Shade darkens c while keeping its hue rich rather than muddying to gray.
func Shade(c RGB, t float64) RGB {
	mx := max(c[0], max(c[1], c[2]))
	deep := RGB{
		c[0]*0.35 - (mx-c[0])*0.1,
		c[1]*0.35 - (mx-c[1])*0.1,
		c[2]*0.35 - (mx-c[2])*0.1,
	}
	return Mix(c, deep, t)
}

// Tint lightens c toward white.
func Tint(c RGB, t float64) RGB {
	return Mix(c, RGB{255, 255, 255}, t)
}

// Luminance returns WCAG relative luminance.
func Luminance(c RGB) float64 {
	ch := func(v float64) float64 {
		s := v / 255.0
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c[0]) + 0.7152*ch(c[1]) + 0.0722*ch(c[2])
}

// Contrast returns the WCAG contrast ratio between a and b.
func Contrast(a, b RGB) float64 {
	la, lb := Luminance(a), Luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// AutoEyeColor picks the ink with better contrast against body.
func AutoEyeColor(body RGB) RGB {
	if Contrast(body, DarkInk) >= Contrast(body, LightInk) {
		return DarkInk
	}
	return LightInk
}

func toLinear(v float64) float64 {
	s := v / 255.0
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

func fromLinear(v float64) float64 {
	if v <= 0.0031308 {
		return 255.0 * 12.92 * v
	}
	return 255.0 * (1.055*math.Pow(v, 1.0/2.4) - 0.055)
}

// ToOKLCH converts an sRGB color into OKLCH (L in 0..1, C >= 0, H in 0..360).
func ToOKLCH(c RGB) (L, C, H float64) {
	r, g, b := toLinear(c[0]), toLinear(c[1]), toLinear(c[2])
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	L = 0.2104542553*l + 0.793617785*m - 0.0040720468*s
	A := 1.9779984951*l - 2.428592205*m + 0.4505937099*s
	B := 0.0259040371*l + 0.7827717662*m - 0.808675766*s
	H = math.Atan2(B, A) * 180.0 / math.Pi
	if H < 0 {
		H += 360.0
	}
	return L, math.Hypot(A, B), H
}

func oklchLinear(L, C, H float64) [3]float64 {
	rad := H * math.Pi / 180.0
	a, b := C*math.Cos(rad), C*math.Sin(rad)
	l := math.Pow(L+0.3963377774*a+0.2158037573*b, 3)
	m := math.Pow(L-0.1055613458*a-0.0638541728*b, 3)
	s := math.Pow(L-0.0894841775*a-1.291485548*b, 3)
	return [3]float64{
		4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.707614701*s,
	}
}

// OKLCH converts an OKLCH color into sRGB, reducing chroma to fit the gamut
// while preserving lightness and hue.
func OKLCH(L, C, H float64) RGB {
	fits := func(c float64) bool {
		lin := oklchLinear(L, c, H)
		return lin[0] >= -1e-4 && lin[0] <= 1+1e-4 &&
			lin[1] >= -1e-4 && lin[1] <= 1+1e-4 &&
			lin[2] >= -1e-4 && lin[2] <= 1+1e-4
	}
	lo, hi := 0.0, C
	if !fits(hi) {
		for range 20 {
			mid := (lo + hi) / 2
			if fits(mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		hi = lo
	}
	lin := oklchLinear(L, hi, H)
	return RGB{fromLinear(lin[0]), fromLinear(lin[1]), fromLinear(lin[2])}
}

func turnToward(h, to, by float64) float64 {
	d := math.Mod(to-h+540.0, 360.0) - 180.0
	if d < -180 {
		d += 360
	}
	sign := 1.0
	if d < 0 {
		sign = -1.0
	}
	out := math.Mod(h+sign*min(math.Abs(d), by)+360.0, 360.0)
	if out < 0 {
		out += 360
	}
	return out
}

// RibbonPalette returns 9 harmonious OKLCH ribbon hex colors (3 bands × 3 lanes)
// derived from the body color, faithfully porting open-bot-avatar's painter's
// color ramp (warmer as it lightens, cooler as it darkens).
func RibbonPalette(bodyHex string, seed uint32) [9]string {
	body, ok := ParseHex(bodyHex)
	if !ok {
		body, _ = ParseHex(DefaultColorHex)
	}
	light, chroma, hue := ToOKLCH(body)
	const (
		rampLight   = 0.09
		rampHue     = 14.0
		rampMin     = 0.36
		rampMax     = 0.90
		warmHue     = 100.0
		neutralMaxC = 0.04
		neutralTint = 0.025
	)
	neutral := chroma < neutralMaxC
	base := hue
	if neutral {
		base = math.Mod(130.0+Noise(seed, 500)*310.0, 360.0)
	}
	c := max(chroma, 0.17)
	if neutral {
		c = neutralTint
	}
	from := clamp(light, rampMin, rampMax)
	steps := make([]int, 0, 5)
	for k := 1; len(steps) < 5 && k < 10; k++ {
		for _, step := range [2]int{k, -k} {
			l := from + float64(step)*rampLight
			if len(steps) < 5 && l >= rampMin-1e-9 && l <= rampMax+1e-9 {
				steps = append(steps, step)
			}
		}
	}
	for len(steps) < 5 {
		last := 1
		if len(steps) > 0 {
			last = steps[len(steps)-1]
		}
		steps = append(steps, last)
	}
	type shadeStep struct {
		step int
		hex  string
	}
	shades := make([]shadeStep, len(steps))
	for i, step := range steps {
		l := from + float64(step)*rampLight
		target := warmHue + 180.0
		if step > 0 {
			target = warmHue
		}
		h := turnToward(base, target, rampHue*math.Abs(float64(step)))
		away := -1.0
		if step > 0 {
			away = 1.0
		}
		midH := turnToward(h, target, 12.5)
		midL := clamp(l+away*0.065, 0.26, 0.90)
		shades[i] = shadeStep{step: step, hex: ToHex(OKLCH(midL, c, midH))}
	}
	bandShades := [3][3]int{{0, 1, 2}, {2, 0, 3}, {1, 2, 0}}
	var out [9]string
	idx := 0
	for _, pick := range bandShades {
		band := [3]shadeStep{shades[pick[0]], shades[pick[1]], shades[pick[2]]}
		// Lightest on inside lane to darkest on outside.
		if band[0].step < band[1].step {
			band[0], band[1] = band[1], band[0]
		}
		if band[1].step < band[2].step {
			band[1], band[2] = band[2], band[1]
		}
		if band[0].step < band[1].step {
			band[0], band[1] = band[1], band[0]
		}
		for _, s := range band {
			out[idx] = s.hex
			idx++
		}
	}
	return out
}
