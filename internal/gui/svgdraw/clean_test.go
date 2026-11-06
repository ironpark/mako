package svgdraw

import (
	"bytes"
	"strings"
	"testing"

	ggsvg "github.com/ironpark/ggfx/svg"
)

// badge is an SVG as shields.io draws one: a clip, a mask, a gradient, and
// text scaled down from a large size, with a shadow under it.
const badge = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="88" height="20" role="img" aria-label="build: passing">
<title>build: passing</title>
<linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>
<clipPath id="r"><rect width="88" height="20" rx="3" fill="#fff"/></clipPath>
<g clip-path="url(#r)"><rect width="37" height="20" fill="#555"/><rect x="37" width="51" height="20" fill="#4c1"/><rect width="88" height="20" fill="url(#s)"/></g>
<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="110">
<text aria-hidden="true" x="195" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" textLength="270">build</text>
<text x="195" y="140" transform="scale(.1)" fill="#fff" textLength="270">build</text>
<a xlink:href="https://x"><text x="615" y="140" transform="scale(.1)">passing</text></a>
</g>
<image x="5" y="3" width="14" height="14" href="data:image/svg+xml;base64,PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0wIDBoMjR2MjRIMHoiLz48L3N2Zz4="/>
<foreignObject><div>not drawn</div></foreignObject>
</svg>`

// A badge, which ggfx cannot read as it comes, is drawn without what it
// cannot draw, and its text is taken out to be set over it.
func TestABadgeIsDrawnWithItsText(t *testing.T) {
	if _, err := ggsvg.Parse(strings.NewReader(badge)); err == nil {
		t.Log("ggfx reads the badge itself now")
	}
	clean, texts, embeds, err := clean([]byte(badge))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ggsvg.Parse(bytes.NewReader(clean)); err != nil {
		t.Fatalf("the cleaned badge does not parse: %v\n%s", err, clean)
	}
	if strings.Contains(string(clean), "clip") || strings.Contains(string(clean), "not drawn") {
		t.Errorf("the clip or foreign content stayed:\n%s", clean)
	}
	if len(embeds) != 1 || !embeds[0].svg || embeds[0].x != 5 || embeds[0].w != 14 || !strings.Contains(string(embeds[0].data), "<svg") {
		t.Errorf("embedded logo %+v", embeds)
	}
	if len(texts) != 3 {
		t.Fatalf("texts %+v", texts)
	}
	shadow, label, link := texts[0], texts[1], texts[2]
	if label.text != "build" || label.x != 19.5 || label.y != 14 || label.size != 11 || label.anchor != "middle" {
		t.Errorf("label %+v", label)
	}
	if shadow.color.(interface{ RGBA() (r, g, b, a uint32) }) == nil || shadow.y != 15 {
		t.Errorf("shadow %+v", shadow)
	}
	if link.text != "passing" || link.x != 61.5 {
		t.Errorf("text in a link %+v", link)
	}
	img, ok := Draw([]byte(badge))
	if !ok || img.Pixels == nil || img.Size.W != 88 || len(img.Labels) != 3 {
		t.Fatalf("image %+v", img)
	}
}
