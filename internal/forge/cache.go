package forge

import (
	"bytes"
	"container/list"
	"io"
	"net/http"
	"sync"
)

// etagTransport asks a read again by the ETag it was last answered with,
// which a forge answers, unchanged, with Not Modified: GitHub without
// counting it against the rate limit, so a branch's runs can be watched
// closely for nothing. The latest replies are kept, as many as max, each
// no larger than etagBody; a reply is kept by its request's URL, Accept
// and login, as one login may see what another may not.
type etagTransport struct {
	next http.RoundTripper
	mu   sync.Mutex
	max  int
	lru  *list.List // of *etagEntry, the latest first
	keys map[string]*list.Element
}

// etagBody bounds a reply kept: a list or a run, not a log or a diff.
const etagBody = 1 << 20

type etagEntry struct {
	key, etag string
	header    http.Header
	body      []byte
}

func newETagTransport(next http.RoundTripper, max int) *etagTransport {
	return &etagTransport{next: next, max: max, lru: list.New(), keys: map[string]*list.Element{}}
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.next.RoundTrip(req)
	}
	key := req.Header.Get("Authorization") + "\x00" + req.Header.Get("Accept") + "\x00" + req.URL.String()
	kept := t.get(key)
	if kept != nil {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", kept.etag)
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && kept != nil:
		resp.Body.Close()
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
		resp.Header = kept.header.Clone()
		resp.Body, resp.ContentLength = io.NopCloser(bytes.NewReader(kept.body)), int64(len(kept.body))
	case resp.StatusCode == http.StatusOK && resp.Header.Get("ETag") != "" && resp.ContentLength <= etagBody:
		body, err := io.ReadAll(io.LimitReader(resp.Body, etagBody+1))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(body) <= etagBody {
			t.put(&etagEntry{key: key, etag: resp.Header.Get("ETag"), header: resp.Header.Clone(), body: body})
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}

func (t *etagTransport) get(key string) *etagEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.keys[key]
	if !ok {
		return nil
	}
	t.lru.MoveToFront(e)
	return e.Value.(*etagEntry)
}

func (t *etagTransport) put(entry *etagEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.keys[entry.key]; ok {
		e.Value = entry
		t.lru.MoveToFront(e)
	} else {
		t.keys[entry.key] = t.lru.PushFront(entry)
	}
	for t.lru.Len() > t.max {
		last := t.lru.Remove(t.lru.Back()).(*etagEntry)
		delete(t.keys, last.key)
	}
}
