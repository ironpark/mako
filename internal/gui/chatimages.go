package gui

import (
	"slices"
	"strings"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// Images in Chat: those a message holds, which its agent's log held inline
// and its node keeps as files, and those a message or a draft names by
// their paths, as a pasted or dropped image is handed to an agent. Each
// shows as a thumbnail, read from the session's node, that opens the image
// in the Files tab's preview.

// imageWord is a word of text that names an image, and the image's path.
type imageWord struct{ Word, Path string }

// imageWords are the words of text that name images, each once, those
// relative to project resolved against it.
func imageWords(text, project string) []imageWord {
	var out []imageWord
	for _, word := range strings.Fields(text) {
		bare := strings.Trim(word, "\"'`()[]<>,;")
		if !isImage(bare) {
			continue
		}
		path := bare
		if project != "" {
			path = resolvePath(project, bare)
		}
		if !slices.ContainsFunc(out, func(w imageWord) bool { return w.Path == path }) {
			out = append(out, imageWord{Word: word, Path: path})
		}
	}
	return out
}

// imageRefs are the paths of the images text names, as words of their own.
func imageRefs(text, project string) []string {
	var out []string
	for _, w := range imageWords(text, project) {
		out = append(out, w.Path)
	}
	return out
}

// thumbBox is the box a thumbnail fits in.
const (
	thumbW = 240
	thumbH = 180
)

// chatImage reads the image at path on node, once for the window, and
// shows it as a thumbnail. One that cannot be read shows its name, or
// nothing when it is named only: text may name a file that is not there.
func chatImage(m *model, node, path string, named bool) ggui.Widget {
	key := node + "\x00" + path
	img, ok := m.chatImages[key]
	if !ok {
		img = ggui.State(mdImage{Loading: true})
		m.chatImages[key] = img
		fetchImage(m, node, path, func(d decodedImage, err error) {
			if err != nil || d.Img == nil {
				img.Set(mdImage{})
				return
			}
			img.Set(mdImage{Img: ggfx.NewImageFromImage(d.Img)})
		})
	}
	name := baseName(path)
	open := func() { m.openItem(fileItem{Key: key, Node: node, Path: path, Name: name}, 0) }
	return ggui.View(img, func(v mdImage) ggui.Widget {
		var w ggui.Widget
		switch {
		case v.Loading:
			w = ggui.Box(ui.Spinner()).Size(64, 64)
		case v.Img == nil && named:
			return ggui.Box()
		case v.Img == nil:
			w = ggui.Box(ggui.Row(icon("file-image").Size(14).Color(tokMuted), muted(ggui.Text(name).Size(12).NoWrap().Ellipsis())).Gap(6)).Pad(8, 10).MaxWidth(thumbW)
		default:
			b := v.Img.Bounds()
			scale := min(1, thumbW/float64(max(b.Dx(), 1)), thumbH/float64(max(b.Dy(), 1)))
			w = ggui.Image(v.Img).Alt(name).Size(float64(b.Dx())*scale, float64(b.Dy())*scale)
		}
		return ggui.Tap(ggui.Box(w).Fill(tokSurface1).Border(1, tokBorder).Radius(8).Pad(2), open)
	})
}

// chatImages shows the images at paths on node, wrapped in rows: those
// held, then those named.
func chatImages(m *model, node string, held, named []string) ggui.Widget {
	var thumbs []ggui.Widget
	for _, p := range held {
		thumbs = append(thumbs, chatImage(m, node, p, false))
	}
	for _, p := range named {
		if !slices.Contains(held, p) {
			thumbs = append(thumbs, chatImage(m, node, p, true))
		}
	}
	return ggui.Wrap(thumbs...).Gap(6).RunGap(6)
}

// draftImages shows the images a session's draft names, over the
// composer's text, each with a button that takes it out of the draft. They
// are built again only as the images named change, not as words are typed.
func draftImages(m *model, id string, state *chatState) ggui.Widget {
	type named struct {
		Node  string
		Words []imageWord
	}
	images := ggui.Derived(func() named {
		info, ok := m.session(id)
		if !ok {
			return named{}
		}
		return named{Node: info.GetNodeId(), Words: imageWords(state.draft.Get(), info.GetProject())}
	}).WithEqual(deepEqual)
	return ggui.View(images, func(n named) ggui.Widget {
		if len(n.Words) == 0 {
			return ggui.Box()
		}
		thumbs := make([]ggui.Widget, len(n.Words))
		for i, w := range n.Words {
			remove := ui.ButtonOf(icon("x").Size(11).Color(tokMuted), func() {
				m.setComposerDraft(id, strings.Join(strings.Fields(strings.Replace(ggui.Peek(state.draft), w.Word, "", 1)), " ")+" ")
			}).Ghost().Pad(2).Radius(4).Name(T("Remove"))
			thumbs[i] = ggui.Stack(chatImage(m, n.Node, w.Path, true), ggui.Align(ggui.Box(remove).Fill(tokSurface1).Radius(5)).At(1, 0))
		}
		return ggui.Box(ggui.Wrap(thumbs...).Gap(6).RunGap(6)).Pad(0, 0, 4, 0)
	})
}
