package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Images a log holds inline, as base64, are kept as files on the node, so
// a chat names them by their paths and a front-end reads them as it reads
// any file: ChatMessage.images.

// ImageDir is where the images of logs are kept: the user's cache, or the
// temporary folder.
func ImageDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "mako", "images")
	}
	return filepath.Join(os.TempDir(), "mako-images")
}

// maxImage bounds the base64 of an image kept.
const maxImage = 32 << 20

// imageExts are the files an image of each type is kept as.
var imageExts = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
}

// kept remembers the images kept while the node runs, by imageKey, so a
// log read again, as it is while its agent writes on, finds each without
// hashing it or looking for its file.
var kept struct {
	sync.Mutex
	paths map[string]string
}

// imageKey tells an image's data apart cheaply: its type, its length and
// its ends, which differ between images as their headers do not.
func imageKey(mediaType, data string) string {
	const n = 64
	return mediaType + "\x00" + strconv.Itoa(len(data)) + "\x00" + data[:min(n, len(data))] + data[max(len(data)-n, 0):]
}

// KeepImage keeps the image data, base64 of mediaType, as a file named by
// its contents, unless it is kept already, and returns its path.
func KeepImage(mediaType, data string) (string, error) {
	ext, ok := imageExts[mediaType]
	if !ok {
		return "", errors.New("not an image mako shows: " + mediaType)
	}
	if len(data) > maxImage {
		return "", errors.New("the image is too large")
	}
	key := imageKey(mediaType, data)
	kept.Lock()
	path, ok := kept.paths[key]
	kept.Unlock()
	if ok {
		return path, nil
	}
	path, err := keepImage(ext, data)
	if err == nil {
		kept.Lock()
		if kept.paths == nil {
			kept.paths = map[string]string{}
		}
		kept.paths[key] = path
		kept.Unlock()
	}
	return path, err
}

// keepImage writes data, base64 of an image, to the file named by its
// contents with ext, unless it is there.
func keepImage(ext, data string) (string, error) {
	sum := sha256.Sum256([]byte(data))
	dir := ImageDir()
	path := filepath.Join(dir, hex.EncodeToString(sum[:12])+ext)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".image-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(raw)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}

// KeepDataURL is KeepImage for a data: URL, as the Responses API gives an
// image.
func KeepDataURL(url string) (string, error) {
	head, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	mediaType, enc, _ := strings.Cut(head, ";")
	if !ok || !strings.HasPrefix(url, "data:") || enc != "base64" {
		return "", errors.New("not an inline image")
	}
	return KeepImage(mediaType, data)
}
