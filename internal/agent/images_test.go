package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// An image a message holds inline is kept as a file, once for its
// contents, and the message names it by its path; prose beside it folds
// in with it.
func TestImagesInlineAreKeptAsFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	png := []byte("\x89PNG\r\n\x1a\nimage")
	data := base64.StdEncoding.EncodeToString(png)
	raw, _ := json.Marshal(map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}},
		map[string]any{"type": "text", "text": "what is this?"},
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + data},
	}})
	msgs := ParseMessage(raw, nil)
	if len(msgs) != 1 || msgs[0].GetText() != "what is this?" || len(msgs[0].GetImages()) != 2 {
		t.Fatalf("parsed %v", msgs)
	}
	if a, b := msgs[0].GetImages()[0], msgs[0].GetImages()[1]; a != b {
		t.Fatalf("one image kept twice: %s, %s", a, b)
	}
	if got, err := os.ReadFile(msgs[0].GetImages()[0]); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("kept %q, %v", got, err)
	}
	if _, err := KeepImage("image/svg+xml", data); err == nil {
		t.Fatal("kept an image of a type mako does not show")
	}
}
