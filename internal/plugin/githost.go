package plugin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// ErrGitUnavailable is returned when the Manager has no GitHost configured.
var ErrGitUnavailable = errors.New("git unavailable")

// ErrForgeUnavailable is returned when the Manager has no ForgeHost configured.
var ErrForgeUnavailable = errors.New("forge unavailable")

// View describes how a front-end viewer presents a block instance (revision 46;
// extended with custom tab instance and params in revision 49).
type View struct {
	Standalone bool   `json:"standalone"`
	Instance   string `json:"-"`
	Params     string `json:"-"`
}

// GitTarget identifies a git work tree on a node (empty node or "local" means
// the local daemon's node).
type GitTarget struct {
	Node string
	Path string
}

// GitHost is the daemon's side of the git:read and git:write Host API groups.
type GitHost interface {
	// Allowed reports whether t lies within a project location on t.Node, or a
	// session folder or worktree known on t.Node.
	Allowed(t GitTarget) bool
	// Status reads working tree changes for t, optionally relative to base,
	// filling GitChanges.Worktree when a session worktree sits at t.
	Status(ctx context.Context, t GitTarget, base string) (*protocol.GitChanges, error)
	// Log reads up to max commits for t, skipping commits when the repository
	// fingerprint matches known.
	Log(ctx context.Context, t GitTarget, max int, known string) (*protocol.GitHistory, error)
	// Show reads the metadata and changed files of commit hash in t.
	Show(ctx context.Context, t GitTarget, hash string) (*protocol.GitCommitInfo, error)
	// Run carries out r on t. When worktree is true, the host looks up the
	// session worktree at t itself rather than trusting a caller-supplied one.
	Run(ctx context.Context, t GitTarget, r *protocol.GitRun, worktree bool) (string, error)
	// Watch invokes changed whenever the repository at t changes, until ctx
	// ends.
	Watch(ctx context.Context, t GitTarget, changed func()) error
}

// ForgeBranch is the pull request and CI status summary of a branch on its
// forge, backing ForgeService.BranchStatus.
type ForgeBranch struct {
	Forge     string
	HasPR     bool
	PRNumber  int32
	PRTitle   string
	PRState   string
	PRChecks  string
	Runs      int32
	RunsState string
	RunNumber int32
	RunHead   string
}

// ForgeCheck is one CI check run or commit status on a pull request.
type ForgeCheck struct {
	Name       string
	Workflow   string
	Status     string
	Conclusion string
	URL        string
}

// ForgeComment is one comment, review, or inline diff comment on a pull request.
type ForgeComment struct {
	Author string
	Body   string
	Time   string
	Review string
	Path   string
	Line   int32
}

// ForgePRDetail holds a pull request's metadata, checks, and review comments,
// backing ForgeService.PullRequestDetail.
type ForgePRDetail struct {
	Forge      string
	Number     int32
	Title      string
	State      string
	URL        string
	Head       string
	Base       string
	HeadOID    string
	Checks     string
	Body       string
	CheckItems []ForgeCheck
	Comments   []ForgeComment
}

// ForgeFailedLogs holds a failed CI run's trailing error logs, backing
// ForgeService.FailedLogs.
type ForgeFailedLogs struct {
	Logs      string
	Truncated bool
	Workflow  string
	Head      string
	URL       string
}

// ForgeHost is the daemon's side of the forge:read Host API group.
type ForgeHost interface {
	BranchStatus(ctx context.Context, t GitTarget, branch string) (*ForgeBranch, error)
	PullRequestDetail(ctx context.Context, t GitTarget, number int32) (*ForgePRDetail, error)
	FailedLogs(ctx context.Context, t GitTarget, runNumber int32, maxLines int32) (*ForgeFailedLogs, error)
}

// LocalGit implements GitHost directly via gitstat on this machine, polling
// gitstat.Stamp every second for Watch. Intended for tests and single-node use.
type LocalGit struct{}

var _ GitHost = LocalGit{}

func localGitTarget(t GitTarget) (string, error) {
	if t.Node != "" && t.Node != protocol.LocalScopeNode {
		return "", fmt.Errorf("local git host cannot access remote node %q", t.Node)
	}
	if !filepath.IsAbs(t.Path) {
		return "", errors.New("git target path must be absolute")
	}
	return t.Path, nil
}

// Allowed permits any absolute path on the local node.
func (LocalGit) Allowed(t GitTarget) bool {
	_, err := localGitTarget(t)
	return err == nil
}

// Status reads local working tree changes via gitstat.
func (LocalGit) Status(ctx context.Context, t GitTarget, base string) (*protocol.GitChanges, error) {
	dir, err := localGitTarget(t)
	if err != nil {
		return nil, err
	}
	if base != "" {
		return gitstat.BaseStatus(ctx, dir, base)
	}
	return gitstat.Status(ctx, dir)
}

// Log reads local commit history via gitstat.
func (LocalGit) Log(ctx context.Context, t GitTarget, max int, known string) (*protocol.GitHistory, error) {
	dir, err := localGitTarget(t)
	if err != nil {
		return nil, err
	}
	return gitstat.Log(ctx, dir, max, known)
}

// Show reads a local commit's metadata and changed files via gitstat.
func (LocalGit) Show(ctx context.Context, t GitTarget, hash string) (*protocol.GitCommitInfo, error) {
	dir, err := localGitTarget(t)
	if err != nil {
		return nil, err
	}
	return gitstat.Show(ctx, dir, hash)
}

// Run executes a mutating git operation locally via gitstat.
func (LocalGit) Run(ctx context.Context, t GitTarget, r *protocol.GitRun, worktree bool) (string, error) {
	dir, err := localGitTarget(t)
	if err != nil {
		return "", err
	}
	if r == nil {
		return "", errors.New("missing git run request")
	}
	if worktree {
		return "", errors.New("worktree operations are not available on LocalGit")
	}
	req := proto.CloneOf(r)
	req.Root = dir
	req.Worktree = nil
	return gitstat.Run(ctx, req)
}

// Watch polls gitstat.Stamp every second and calls changed whenever it moves,
// returning nil once ctx ends.
func (LocalGit) Watch(ctx context.Context, t GitTarget, changed func()) error {
	dir, err := localGitTarget(t)
	if err != nil {
		return err
	}
	last := gitstat.Stamp(dir)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if cur := gitstat.Stamp(dir); cur != last {
				last = cur
				if changed != nil {
					changed()
				}
			}
		}
	}
}
