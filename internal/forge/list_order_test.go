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

// fakeGitHubList answers a list of pull requests, and one of runs, keeping
// the queries asked.
type fakeGitHubList struct {
	mu      sync.Mutex
	queries []string
}

func (f *fakeGitHubList) RoundTrip(r *http.Request) (*http.Response, error) {
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
		return reply(`{"data":{"repository":{"list":{"nodes":[
			{"number":9,"title":"From a fork","state":"OPEN","isCrossRepository":true,"authorAssociation":"NONE","author":{"login":"eve"}},
			{"number":8,"title":"Ours","state":"OPEN","isCrossRepository":false,"authorAssociation":"MEMBER","author":{"login":"alice"}}
		],"pageInfo":{"hasNextPage":false}}}}}`)
	}
	return reply(`{"workflow_runs":[
		{"id":31,"name":"ci","status":"completed","conclusion":"failure","repository":{"full_name":"o/r"},"head_repository":{"full_name":"eve/r"}},
		{"id":30,"name":"ci","status":"completed","conclusion":"failure","repository":{"full_name":"o/r"},"head_repository":{"full_name":"O/R"}},
		{"id":29,"name":"ci","status":"completed","conclusion":"failure","repository":{"full_name":"o/r"},"head_repository":null}
	]}`)
}

// A list asks for the newest opened first when its order says so, and
// tells a fork's pull request and run, and an author's tie, from the
// repository's own.
func TestGitHubListSaysOrderAndOrigin(t *testing.T) {
	if err := gitstat.SetHTTPSCredential("https://"+githubHost, gitstat.HTTPSCredential{Username: "x-access-token", Password: "tok"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gitstat.ClearHTTPSCredential("https://" + githubHost) })
	api := &fakeGitHubList{}
	saved := hostClient.Transport
	hostClient.Transport = api
	t.Cleanup(func() { hostClient.Transport = saved })

	g := &github{owner: "o", repo: "r"}
	for _, order := range []string{"", protocol.ForgeOrderCreated} {
		items, err := g.list(t.Context(), &protocol.ForgeList{Kind: protocol.ForgePR, State: "all", Order: order, Max: 20})
		if err != nil {
			t.Fatal(err)
		}
		got := items.GetItems()
		if len(got) != 2 || !got[0].GetFork() || got[0].GetAssociation() != "NONE" || got[1].GetFork() || got[1].GetAssociation() != "MEMBER" {
			t.Fatalf("items = %v", got)
		}
	}
	api.mu.Lock()
	queries := api.queries
	api.mu.Unlock()
	if !strings.Contains(queries[0], "field: UPDATED_AT") || !strings.Contains(queries[1], "field: CREATED_AT") {
		t.Fatalf("queries ordered as %q and %q", queries[0], queries[1])
	}

	runs, err := g.list(t.Context(), &protocol.ForgeList{Kind: protocol.ForgeRuns, Max: 20})
	if err != nil {
		t.Fatal(err)
	}
	if r := runs.GetItems(); len(r) != 3 || !r[0].GetFork() || r[1].GetFork() || !r[2].GetFork() {
		t.Fatalf("runs = %v; want fork, own, and null head_repository (deleted fork) treated as fork", r)
	}
}

func TestGiteaListSaysOrderAndForks(t *testing.T) {
	if p := pullsPath("", "open", "20"); p != "/pulls?sort=recentupdate&state=open&limit=20" {
		t.Errorf("pulls path = %q", p)
	}
	if p := pullsPath(protocol.ForgeOrderCreated, "all", "20"); strings.Contains(p, "sort=") {
		t.Errorf("pulls path of the newest = %q; the API lists the newest first unsorted", p)
	}
	g := &gitea{owner: "o", repo: "r"}
	var own, fork gPR
	own.Head.Repo.FullName, fork.Head.Repo.FullName = "O/r", "eve/r"
	if g.prItem(own).GetFork() || !g.prItem(fork).GetFork() {
		t.Error("a fork's pull request is not told from the repository's own")
	}
}
