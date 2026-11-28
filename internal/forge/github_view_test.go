package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

type fakeGitHubAPI struct {
	mu      sync.Mutex
	queries []string
	paths   []string
}

func (f *fakeGitHubAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	reply := func(body string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	if r.URL.Path == "/graphql" {
		var q struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		f.mu.Lock()
		f.queries = append(f.queries, q.Query)
		f.mu.Unlock()
		if !strings.Contains(q.Query, "repository") {
			return reply(`{"data":{"viewer":{"login":"me"}}}`)
		}
		files := `"files":{"nodes":[{"path":"a.go","additions":1,"deletions":0,"changeType":"MODIFIED"}]}`
		if !strings.Contains(q.Query, "files(") {
			files = `"files":{"nodes":[]}`
		}
		return reply(`{"data":{"repository":{"item":{
			"number":7,"title":"Fix it","state":"OPEN","headRefName":"fix","baseRefName":"main","headRefOid":"abc",
			"comments":{"nodes":[
				{"author":{"__typename":"User","login":"alice"},"authorAssociation":"MEMBER","body":"Rename it.","createdAt":"2026-11-28T01:00:00Z"},
				{"author":null,"authorAssociation":"NONE","body":"From a deleted account.","createdAt":"2026-11-28T01:30:00Z"}
			]},
			"reviews":{"nodes":[
				{"author":{"__typename":"Bot","login":"claude"},"authorAssociation":"NONE","body":"Looks off.","state":"COMMENTED","submittedAt":"2026-11-28T02:00:00Z"}
			]},
			` + files + `
		}}}}`)
	}
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.RequestURI())
	f.mu.Unlock()
	// The newest first, as the query asks.
	return reply(`[
		{"user":{"login":"bob","type":"User"},"author_association":"COLLABORATOR","body":"Newest.","path":"a.go","line":3,"created_at":"2026-11-28T04:00:00Z"},
		{"user":{"login":"renovate[bot]","type":"Bot"},"author_association":"NONE","body":"Older.","path":"a.go","line":1,"created_at":"2026-11-28T03:00:00Z"}
	]`)
}

// TestGitHubViewTellsWhoWrote reads each comment's author's tie to the
// repository and whether it is a bot, the account the node reads as, the
// latest line comments, and no files when none are asked for.
func TestGitHubViewTellsWhoWrote(t *testing.T) {
	if err := gitstat.SetHTTPSCredential("https://"+githubHost, gitstat.HTTPSCredential{Username: "x-access-token", Password: "tok"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gitstat.ClearHTTPSCredential("https://" + githubHost) })
	api := &fakeGitHubAPI{}
	saved := hostClient.Transport
	hostClient.Transport = api
	t.Cleanup(func() { hostClient.Transport = saved })
	viewers.Lock()
	viewers.login = ""
	viewers.Unlock()

	g := &github{owner: "o", repo: "r"}
	d, err := g.view(t.Context(), &protocol.ForgeView{Kind: protocol.ForgePR, Number: 7, NoFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.GetViewer() != "me" {
		t.Errorf("viewer = %q; want me", d.GetViewer())
	}
	if len(d.GetFiles()) != 0 {
		t.Errorf("files = %v; want none", d.GetFiles())
	}
	type who struct {
		author, association string
		bot                 bool
	}
	var got []who
	for _, c := range d.GetComments() {
		got = append(got, who{c.GetAuthor(), c.GetAssociation(), c.GetBot()})
	}
	want := []who{{"alice", "MEMBER", false}, {"", "NONE", false}, {"claude", "NONE", true}, {"renovate[bot]", "NONE", true}, {"bob", "COLLABORATOR", false}}
	if len(got) != len(want) {
		t.Fatalf("comments %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("comment %d = %+v; want %+v", i, got[i], want[i])
		}
	}

	api.mu.Lock()
	queries, paths := api.queries, api.paths
	api.mu.Unlock()
	if len(paths) != 1 || !strings.Contains(paths[0], "sort=created") || !strings.Contains(paths[0], "direction=desc") {
		t.Errorf("line comments read as %v; want the newest first", paths)
	}
	for _, q := range queries {
		if strings.Contains(q, "repository") && (strings.Contains(q, "files(") || !strings.Contains(q, "authorAssociation")) {
			t.Errorf("a pull request without files read as %q", q)
		}
	}

	// A view that wants the files reads them; the viewer is kept.
	d, err = g.view(t.Context(), &protocol.ForgeView{Kind: protocol.ForgePR, Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.GetFiles()) != 1 || d.GetViewer() != "me" {
		t.Errorf("files %v, viewer %q; want a.go, me", d.GetFiles(), d.GetViewer())
	}
	api.mu.Lock()
	n := 0
	for _, q := range api.queries {
		if !strings.Contains(q, "repository") {
			n++
		}
	}
	api.mu.Unlock()
	if n != 1 {
		t.Errorf("the viewer was asked %d times; want once, then kept", n)
	}
}
