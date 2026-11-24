package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func makeFakeGitRepo(t *testing.T, dir, remoteURL string) string {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if remoteURL != "" {
		cfg := "[remote \"origin\"]\n\turl = " + remoteURL + "\n"
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return real
}

func TestScanLocalGitRepos(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}

	alpha := makeFakeGitRepo(t, filepath.Join(root, "alpha"), "git@github.com:acme/alpha.git")
	nested := makeFakeGitRepo(t, filepath.Join(root, "personal", "oss", "mako"), "https://github.com/ironpark/mako.git")
	tooDeep := makeFakeGitRepo(t, filepath.Join(root, "a", "b", "c", "d", "deep"), "https://github.com/acme/deep.git")
	// Inside alpha: should not descend into an already-found repo.
	_ = makeFakeGitRepo(t, filepath.Join(root, "alpha", "subrepo"), "https://github.com/acme/subrepo.git")
	// Skipped directories: hidden & node_modules.
	_ = makeFakeGitRepo(t, filepath.Join(root, ".hidden", "secret"), "https://github.com/acme/secret.git")
	_ = makeFakeGitRepo(t, filepath.Join(root, "node_modules", "pkg"), "https://github.com/acme/pkg.git")
	_ = tooDeep

	// Depth 3 finds alpha (depth 1) and personal/oss/mako (depth 3), skips tooDeep (depth 5), subrepo, hidden, node_modules.
	repos, err := scanLocalGitRepos(context.Background(), root, 3)
	if err != nil {
		t.Fatalf("scanLocalGitRepos(depth=3): %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos (%+v), want 2", len(repos), repos)
	}
	if repos[0].Path != alpha || repos[0].Rel != "alpha" || repos[0].Owner != "github.com/acme" || repos[0].Remote != "github.com/acme/alpha" {
		t.Fatalf("repos[0] = %+v", repos[0])
	}
	if repos[1].Path != nested || repos[1].Rel != "personal/oss/mako" || repos[1].Owner != "github.com/ironpark" || repos[1].Remote != "github.com/ironpark/mako" {
		t.Fatalf("repos[1] = %+v", repos[1])
	}

	// Depth 1 only finds alpha.
	shallow, err := scanLocalGitRepos(context.Background(), root, 1)
	if err != nil {
		t.Fatalf("scanLocalGitRepos(depth=1): %v", err)
	}
	if len(shallow) != 1 || shallow[0].Path != alpha {
		t.Fatalf("shallow = %+v, want only alpha", shallow)
	}

	// Scanning a git repo itself returns it directly.
	direct, err := scanLocalGitRepos(context.Background(), filepath.Join(realRoot, "alpha"), 1)
	if err != nil {
		t.Fatalf("scanLocalGitRepos(direct): %v", err)
	}
	if len(direct) != 1 || direct[0].Path != alpha || direct[0].Rel != "alpha" {
		t.Fatalf("direct = %+v", direct)
	}

	// Scanning a non-directory fails cleanly.
	filePath := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(filePath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanLocalGitRepos(context.Background(), filePath, 1); err == nil {
		t.Fatal("expected error when scanning a regular file")
	}
}

func TestImportGitReposRespectsExistingSpacesAndOwnerRules(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	self := &protocol.Node{Id: "studio", Name: "studio", Os: "darwin", Source: "self"}
	m.Nodes.Set([]*protocol.Node{self, {Id: "box", Name: "box", Os: "linux", Source: "mesh"}})
	m.Conn.Set(connState{OK: true, Self: self})
	m.Spaces.Set(spaceList{
		Default: "side",
		Spaces: []space{
			{ID: "work", Name: "Work", Owners: []string{"github.com/acme"}},
			{ID: "side", Name: "Side"},
		},
	})
	// Currently viewing "side" space.
	m.setSpace(2)
	// Existing repo on studio, plus a repo only on box.
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []keptProject{
			{fileRoot: fileRoot{Node: "studio", Path: "/w/already"}, Owner: "github.com/me", Remote: "github.com/me/already"},
			{fileRoot: fileRoot{Node: "box", Path: "/w/shared"}, Owner: "github.com/me", Remote: "github.com/me/shared"},
		}
		return p
	})

	discovered := []discoveredRepo{
		{Path: "/w/already", Name: "already", Owner: "github.com/me", Remote: "github.com/me/already"},
		{Path: "/w/shared", Name: "shared", Owner: "github.com/me", Remote: "github.com/me/shared"},
		{Path: "/w/acme-api", Name: "acme-api", Owner: "github.com/acme", Remote: "github.com/acme/api"},
		{Path: "/w/local-only", Name: "local-only"},
	}
	marked := m.markExistingRepos("studio", discovered)
	if !marked[0].Existing {
		t.Fatal("expected /w/already on studio to be marked Existing")
	}
	if marked[1].Existing {
		t.Fatal("expected /w/shared on studio not to be marked Existing (only exists on box)")
	}

	added := m.importGitRepos("studio", marked[1:])
	if added != 3 {
		t.Fatalf("importGitRepos added %d, want 3", added)
	}
	kept := ggui.Peek(m.Prefs).Projects
	if len(kept) != 5 {
		t.Fatalf("expected 5 kept projects, got %d: %+v", len(kept), kept)
	}

	// Work space holds acme-api via owner rule (not forced into Side).
	set := m.spacesNow()
	if at, why := set.placeOf("repo:github.com/acme/api", []string{"github.com/acme"}); at != 1 || why != shownByOwner {
		t.Fatalf("acme-api placed at (%d, %v), want (1, shownByOwner)", at, why)
	}
	// local-only (no owner) was moved into the active Side space.
	localKey := dirKey("studio", "/w/local-only")
	if at, why := set.placeOf(localKey, nil); at != 2 || why != byProject {
		t.Fatalf("local-only placed at (%d, %v), want (2, byProject)", at, why)
	}
	// Looks were assigned for newly imported sections.
	if _, ok := set.Looks[localKey]; !ok {
		t.Fatalf("expected look assigned for %q", localKey)
	}
}

func TestImportProjectsDialogFlow(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	root := t.TempDir()
	existingPath := makeFakeGitRepo(t, filepath.Join(root, "existing"), "git@github.com:me/existing.git")
	newPath := makeFakeGitRepo(t, filepath.Join(root, "fresh"), "git@github.com:me/fresh.git")

	m := newModel(config.Default(), "")
	self := &protocol.Node{Id: "studio", Name: "studio", Os: "darwin", Source: "self"}
	m.Nodes.Set([]*protocol.Node{self})
	m.Conn.Set(connState{OK: true, Self: self})
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []keptProject{{fileRoot: fileRoot{Node: "studio", Path: existingPath}}}
		return p
	})

	p := ggui.ProbeBuilder(func() ggui.Widget { return importProjectsDialog(m) }, ggui.Sz(800, 700))
	defer p.Close()

	m.openImportProjects()
	m.Proj.Import.Folder.Set(root)
	m.startImportScan()

	deadline := time.Now().Add(3 * time.Second)
	for ggui.Peek(m.Proj.Import.Scanning) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for range 3 {
		p.Frame()
	}

	repos := ggui.Peek(m.Proj.Import.Repos)
	if len(repos) != 2 {
		t.Fatalf("expected 2 discovered repos, got %+v", repos)
	}
	btn, ok := p.Semantics().Find(ggui.RoleButton, Tf("Import %d", 1))
	if !ok {
		t.Fatalf("expected Import 1 button:\n%s", p.Semantics().String())
	}
	p.Click(btn.Rect.Origin.Add(ggui.Pt(btn.Rect.Size.W/2, btn.Rect.Size.H/2)))
	for range 3 {
		p.Frame()
	}

	if ggui.Peek(m.Proj.Import.Open) {
		t.Fatal("expected import dialog closed after clicking Import")
	}
	foundNew := false
	for _, k := range ggui.Peek(m.Prefs).Projects {
		if k.Node == "studio" && k.Path == newPath && k.Owner == "github.com/me" && k.Remote == "github.com/me/fresh" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("expected %q imported with owner/remote, got %+v", newPath, ggui.Peek(m.Prefs).Projects)
	}
}

func TestImportGitReposKeepsProjectsInOtherSpaces(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	self := &protocol.Node{Id: "studio", Name: "studio", Os: "darwin", Source: "self"}
	m.Nodes.Set([]*protocol.Node{self, {Id: "box", Name: "box", Os: "linux", Source: "mesh"}})
	m.Conn.Set(connState{OK: true, Self: self})

	// Space 1 ("work", Default) holds:
	// - owner rule "github.com/acme" (lowercase)
	// - an existing default-placed repo on box: github.com/me/default-in-work
	// - an explicit multi-folder project "p-suite" whose root had empty Remote (inferred from box's keptProject)
	// - a legacy dirKey entry for /w/legacy-dir on studio
	// Space 2 ("side", currently active) is where the user triggers import.
	m.Spaces.Set(spaceList{
		Default: "work",
		Spaces: []space{
			{ID: "work", Name: "Work", Owners: []string{"github.com/acme"}, Projects: []string{"p-suite", dirKey("studio", "/w/legacy-dir")}},
			{ID: "side", Name: "Side"},
		},
		Projects: []project{
			{
				ID:   "p-suite",
				Name: "Suite",
				Roots: []projectRoot{
					{Locations: []fileRoot{{Node: "box", Path: "/srv/suite-api"}}},
				},
			},
		},
	})
	m.setSpace(2) // viewing "side"
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []keptProject{
			{fileRoot: fileRoot{Node: "box", Path: "/srv/default-in-work"}, Owner: "github.com/me", Remote: "github.com/me/default-in-work"},
			{fileRoot: fileRoot{Node: "box", Path: "/srv/suite-api"}, Owner: "github.com/other", Remote: "github.com/other/suite-api"},
		}
		return p
	})

	discovered := []discoveredRepo{
		// Case-insensitive owner match -> must go to "work", not "side".
		{Path: "/w/acme-upper", Name: "acme-upper", Owner: "github.com/ACME", Remote: "github.com/acme/upper"},
		// Already exists in "work" (Default space) via box -> must NOT be stolen into "side".
		{Path: "/w/default-in-work", Name: "default-in-work", Owner: "github.com/me", Remote: "github.com/me/default-in-work"},
		// Matches p-suite root via inferred remote -> must join p-suite in "work" and backfill Remote/Owner.
		{Path: "/w/suite-api", Name: "suite-api", Owner: "github.com/other", Remote: "github.com/other/suite-api"},
		// Legacy dirKey explicitly in "work" -> must stay in "work" and migrate key to repo:.
		{Path: "/w/legacy-dir", Name: "legacy-dir", Owner: "github.com/legacy", Remote: "github.com/legacy/dir"},
		// Brand-new unmatched repo -> goes into active "side" space.
		{Path: "/w/brand-new", Name: "brand-new", Owner: "github.com/me", Remote: "github.com/me/brand-new"},
	}

	if added := m.importGitRepos("studio", discovered); added != len(discovered) {
		t.Fatalf("importGitRepos added %d, want %d", added, len(discovered))
	}

	set := m.spacesNow()
	if at, why := set.placeOf("repo:github.com/acme/upper", []string{"github.com/ACME"}); at != 1 || why != shownByOwner {
		t.Fatalf("acme-upper placed at (%d, %v), want (1, shownByOwner)", at, why)
	}
	if at, why := set.placeOf("repo:github.com/me/default-in-work", []string{"github.com/me"}); at != 1 || why != byDefault {
		t.Fatalf("default-in-work placed at (%d, %v), want (1, byDefault)", at, why)
	}
	if at, why := set.placeOf("repo:github.com/legacy/dir", []string{"github.com/legacy"}); at != 1 || why != byProject {
		t.Fatalf("legacy-dir placed at (%d, %v), want (1, byProject)", at, why)
	}
	if at, why := set.placeOf("repo:github.com/me/brand-new", []string{"github.com/me"}); at != 2 || why != byProject {
		t.Fatalf("brand-new placed at (%d, %v), want (2, byProject)", at, why)
	}

	// Verify suite-api joined p-suite in "work" and backfilled Remote/Owner.
	pi := projectByID(set.Projects, "p-suite")
	if pi < 0 || len(set.Projects[pi].Roots) != 1 {
		t.Fatalf("p-suite missing: %+v", set.Projects)
	}
	root := set.Projects[pi].Roots[0]
	if root.Remote != "github.com/other/suite-api" || root.Owner != "github.com/other" {
		t.Fatalf("p-suite root remote/owner not backfilled: %+v", root)
	}
	wantLoc := fileRoot{Node: "studio", Path: "/w/suite-api"}
	foundLoc := false
	for _, l := range root.Locations {
		if l == wantLoc {
			foundLoc = true
		}
	}
	if !foundLoc {
		t.Fatalf("studio clone did not join p-suite: %+v", root.Locations)
	}
}
