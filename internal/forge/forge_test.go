package forge

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// A pull request's diff is read a file at a time, each line numbered on
// its sides.
func TestParseDiff(t *testing.T) {
	files := ParseDiff(`diff --git a/a.go b/a.go
index 1..2 100644
--- a/a.go
+++ b/a.go
@@ -2,3 +2,4 @@ func f() {
 one
-two
+deux
+trois
 three
\ No newline at end of file
diff --git a/old name.txt b/new name.txt
similarity index 90%
rename from old name.txt
rename to new name.txt
diff --git a/img.png b/img.png
new file mode 100644
Binary files /dev/null and b/img.png differ
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package gone
`)
	a := files["a.go"]
	if a == nil || len(a.Lines) != 6 {
		t.Fatalf("a.go %v", a)
	}
	if l := a.Lines[2]; l.Op != "-" || l.Text != "two" || l.Old != 3 || l.New != 0 {
		t.Errorf("removed %v", l)
	}
	if l := a.Lines[4]; l.Op != "+" || l.Text != "trois" || l.New != 4 {
		t.Errorf("added %v", l)
	}
	if l := a.Lines[5]; l.Op != " " || l.Old != 4 || l.New != 5 {
		t.Errorf("context %v", l)
	}
	if files["new name.txt"] == nil || !files["img.png"].GetBinary() {
		t.Errorf("files %v", files)
	}
	if g := files["gone.go"]; g == nil || len(g.Lines) != 2 || g.Lines[1].Old != 1 {
		t.Errorf("gone.go %v", g)
	}
}

// A pull request's checks sum up as their worst.
func TestRollup(t *testing.T) {
	ok := check{Status: "COMPLETED", Conclusion: "SUCCESS"}
	for _, c := range []struct {
		checks []check
		want   string
	}{
		{nil, ""},
		{[]check{ok}, "success"},
		{[]check{ok, {Status: "IN_PROGRESS"}}, "pending"},
		{[]check{ok, {State: "PENDING"}}, "pending"},
		{[]check{{Status: "IN_PROGRESS"}, {Status: "COMPLETED", Conclusion: "FAILURE"}}, "failure"},
		{[]check{{State: "ERROR"}}, "failure"},
		{[]check{{State: "SUCCESS"}}, "success"},
	} {
		if got := rollup(c.checks); got != c.want {
			t.Errorf("rollup %v = %q, want %q", c.checks, got, c.want)
		}
	}
	if p := (check{Context: "ci/x", State: "FAILURE", TargetURL: "u"}).proto(); p.GetName() != "ci/x" || p.GetStatus() != "completed" || p.GetConclusion() != "failure" || p.GetUrl() != "u" {
		t.Errorf("status %v", p)
	}
}

// A Forgejo or Gitea run's one status reads as GitHub's status and
// conclusion, and a pull request's state as GitHub's.
func TestGiteaReadsAsGitHub(t *testing.T) {
	for status, want := range map[string][2]string{
		"running": {"in_progress", ""}, "waiting": {"queued", ""}, "success": {"completed", "success"}, "failure": {"completed", "failure"},
	} {
		it := gTask{ID: 3, Name: "ci", Status: status}.proto()
		if it.GetState() != want[0] || it.GetChecks() != want[1] || it.GetTitle() != "ci" {
			t.Errorf("%s: %v", status, it)
		}
	}
	merged := gPR{Number: 1, State: "closed", Merged: true, Draft: true}.proto("pr")
	if merged.GetState() != "MERGED" || !merged.GetDraft() {
		t.Errorf("pull request %v", merged)
	}
	if reviewState("REQUEST_CHANGES") != "CHANGES_REQUESTED" || reviewState("COMMENT") != "COMMENTED" {
		t.Error("review states")
	}
}

func TestCustomPortHTTPSForgejoDetectionAndAuth(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const wantTok = "forgejo-pat-999"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			w.Write([]byte(`{"version":"1.22.0"}`))
		case "/api/forgejo/v1/version":
			w.Write([]byte(`{"version":"9.0.0"}`))
		case "/api/v1/repos/team/app/pulls":
			if got := r.Header.Get("Authorization"); got != "token "+wantTok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`[{"number":7,"title":"Custom port PR","state":"open","user":{"login":"ada"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origClient := hostClient
	hostClient = srv.Client()
	t.Cleanup(func() {
		hostClient = origClient
		gitstat.ClearHTTPSCredential(srv.URL)
	})

	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", srv.URL + "/team/app.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}

	if err := gitstat.SetHTTPSCredential(srv.URL, gitstat.HTTPSCredential{Password: wantTok}); err != nil {
		t.Fatal(err)
	}

	items, err := List(t.Context(), &protocol.ForgeList{Root: root, Kind: protocol.ForgePR})
	if err != nil {
		t.Fatalf("List on custom-port Forgejo failed: %v", err)
	}
	if items.GetForge() != "forgejo" {
		t.Fatalf("forge = %q, want forgejo", items.GetForge())
	}
	if len(items.GetItems()) != 1 || items.GetItems()[0].GetNumber() != 7 || items.GetItems()[0].GetTitle() != "Custom port PR" {
		t.Fatalf("unexpected PR items: %+v", items.GetItems())
	}
}
