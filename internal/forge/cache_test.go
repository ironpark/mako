package forge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A read asked again says the ETag it was answered with, and Not Modified
// answers it with what it was answered before; a large reply is not kept.
func TestAReadAskedAgainIsAnsweredFromItsETag(t *testing.T) {
	body, unchanged := `{"n":1}`, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag := `"` + body[:min(len(body), 16)] + `"`
		if r.Header.Get("If-None-Match") == etag {
			unchanged++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	client := &http.Client{Transport: newETagTransport(http.DefaultTransport, 4)}
	read := func() string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/runs", nil)
		req.Header.Set("Authorization", "token a")
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%v %v", resp, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return string(out)
	}
	if read() != `{"n":1}` || read() != `{"n":1}` || unchanged != 1 {
		t.Fatalf("unchanged %d", unchanged)
	}
	body = `{"n":2}`
	if got := read(); got != `{"n":2}` || unchanged != 1 {
		t.Fatalf("after a change read %s, unchanged %d", got, unchanged)
	}
	body = strings.Repeat("x", etagBody+1)
	if read() != body || read() != body || unchanged != 1 {
		t.Fatalf("a large reply was kept: unchanged %d", unchanged)
	}
}

// A reply of no stated length, as one sent in chunks or decompressed, is
// read whole when it is larger than is kept, and is not kept.
func TestALargeReplyOfNoStatedLengthIsReadWhole(t *testing.T) {
	body, unchanged := strings.Repeat("x", etagBody+4096), 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			unchanged++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"big"`)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // sent in chunks, with no Content-Length
		io.WriteString(w, body)
	}))
	defer srv.Close()
	client := &http.Client{Transport: newETagTransport(http.DefaultTransport, 4)}
	for range 2 {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if resp.ContentLength != -1 {
			t.Fatalf("the reply stated its length, %d", resp.ContentLength)
		}
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || len(got) != len(body) {
			t.Fatalf("read %d of %d bytes: %v", len(got), len(body), err)
		}
	}
	if unchanged != 0 {
		t.Fatalf("a large reply was kept: unchanged %d", unchanged)
	}
}

// The transport keeps the latest replies, as many as it holds.
func TestTheETagTransportKeepsTheLatest(t *testing.T) {
	c := newETagTransport(nil, 2)
	c.put(&etagEntry{key: "a"})
	c.put(&etagEntry{key: "b"})
	c.get("a")
	c.put(&etagEntry{key: "c"})
	if c.get("b") != nil || c.get("a") == nil {
		t.Fatal("did not keep the most recently used")
	}
}
