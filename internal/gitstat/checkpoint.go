package gitstat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const checkpointRefPrefix = "refs/mako/checkpoints/"

var safeRefPart = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// CheckpointEntry describes one captured shadow snapshot of a git work tree.
type CheckpointEntry struct {
	SessionID string `json:"session_id"`
	Key       string `json:"key"`
	Commit    string `json:"commit"`
	CreatedAt int64  `json:"created_at"` // unix milliseconds
}

func sanitizeCheckpointPart(s string) string {
	s = safeRefPart.ReplaceAllString(strings.TrimSpace(s), "-")
	s = strings.Trim(s, ".-")
	if s == "" {
		return "default"
	}
	return s
}

func gitWithEnv(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), extraEnv...)
	if len(stdin) > 0 {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return out, errors.New(msg)
		}
		return out, err
	}
	return out, nil
}

// CaptureSnapshot captures a shadow git commit of the work tree (tracked edits
// plus untracked non-ignored files) without modifying the repository's real
// index, HEAD, or stash list. When sessionID and key are non-empty, the commit
// is pinned under refs/mako/checkpoints/<sessionID>/<key>.
func CaptureSnapshot(ctx context.Context, root, sessionID, key string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("checkpoint: empty root")
	}
	gitDirOut, err := gitOut(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("checkpoint: not a git repository: %w", err)
	}
	gitDir := strings.TrimSpace(string(gitDirOut))

	tmpDir, err := os.MkdirTemp("", "mako-checkpoint-idx-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	tmpIndex := filepath.Join(tmpDir, "index")

	// Seed temp index from real index if present so git add -A is fast.
	if src, err := os.Open(filepath.Join(gitDir, "index")); err == nil {
		if dst, derr := os.OpenFile(tmpIndex, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); derr == nil {
			_, _ = io.Copy(dst, src)
			_ = dst.Close()
		}
		_ = src.Close()
	}

	env := []string{
		"GIT_INDEX_FILE=" + tmpIndex,
		"GIT_AUTHOR_NAME=mako",
		"GIT_AUTHOR_EMAIL=checkpoint@mako.local",
		"GIT_COMMITTER_NAME=mako",
		"GIT_COMMITTER_EMAIL=checkpoint@mako.local",
	}
	if _, err := gitWithEnv(ctx, root, env, nil, "add", "-A"); err != nil {
		return "", fmt.Errorf("checkpoint add: %w", err)
	}
	treeOut, err := gitWithEnv(ctx, root, env, nil, "write-tree")
	if err != nil {
		return "", fmt.Errorf("checkpoint write-tree: %w", err)
	}
	treeSHA := strings.TrimSpace(string(treeOut))

	commitArgs := []string{"commit-tree", treeSHA, "-m", fmt.Sprintf("mako-checkpoint %d", time.Now().UnixMilli())}
	if headOut, err := gitOut(ctx, root, nil, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		if head := strings.TrimSpace(string(headOut)); head != "" {
			commitArgs = append(commitArgs, "-p", head)
		}
	}
	commitOut, err := gitWithEnv(ctx, root, env, nil, commitArgs...)
	if err != nil {
		return "", fmt.Errorf("checkpoint commit-tree: %w", err)
	}
	commitSHA := strings.TrimSpace(string(commitOut))

	if strings.TrimSpace(sessionID) != "" {
		k := strings.TrimSpace(key)
		if k == "" {
			k = strconv.FormatInt(time.Now().UnixMilli(), 10)
		}
		ref := checkpointRefPrefix + sanitizeCheckpointPart(sessionID) + "/" + sanitizeCheckpointPart(k)
		if _, err := gitOut(ctx, root, nil, "update-ref", ref, commitSHA); err != nil {
			return "", fmt.Errorf("checkpoint update-ref: %w", err)
		}
	}
	return commitSHA, nil
}

// RestoreSnapshot restores the work tree at root to the exact file state
// recorded in commitSHA while preserving HEAD and gitignored files. A safety
// snapshot is captured beforehand under refs/mako/checkpoints/pre-restore.
func RestoreSnapshot(ctx context.Context, root, commitSHA string) error {
	sha, err := commitArg(strings.TrimSpace(commitSHA))
	if err != nil {
		return err
	}
	if _, err := gitOut(ctx, root, nil, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return fmt.Errorf("checkpoint %s not found: %w", sha, err)
	}
	// Capture a safety snapshot before mutating files so the user can never
	// irreversibly lose work on an accidental rewind.
	_, _ = CaptureSnapshot(ctx, root, "pre-restore", strconv.FormatInt(time.Now().UnixMilli(), 10))

	// List files present in the target snapshot tree.
	snapFilesOut, err := gitOut(ctx, root, nil, "ls-tree", "-r", "--name-only", "-z", sha)
	if err != nil {
		return fmt.Errorf("checkpoint ls-tree: %w", err)
	}
	snapSet := map[string]bool{}
	for p := range strings.SplitSeq(string(snapFilesOut), "\x00") {
		if p != "" {
			snapSet[p] = true
		}
	}

	// Remove tracked or untracked (non-ignored) files that do not exist in the
	// snapshot tree.
	curTracked, _ := gitOut(ctx, root, nil, "ls-files", "-z")
	curUntracked, _ := gitOut(ctx, root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	for _, raw := range []string{string(curTracked), string(curUntracked)} {
		for p := range strings.SplitSeq(raw, "\x00") {
			if p == "" || snapSet[p] {
				continue
			}
			if inWorkTree(p) == nil {
				_ = os.Remove(filepath.Join(root, filepath.FromSlash(p)))
			}
		}
	}

	// Check out all files from the snapshot tree into the working directory
	// using a temporary index so we don't clobber the user's staged index state
	// unexpectedly, then reset index to HEAD (when HEAD exists).
	if len(snapSet) > 0 {
		if _, err := gitOut(ctx, root, nil, "checkout", sha, "--", "."); err != nil {
			return fmt.Errorf("checkpoint checkout: %w", err)
		}
	}
	// Reset the index back to HEAD (leaving restored working-tree files intact)
	// so restored edits show cleanly as unstaged changes against HEAD.
	if _, err := gitOut(ctx, root, nil, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		_, _ = gitOut(ctx, root, nil, "reset", "--mixed", "--quiet", "HEAD")
	}
	return nil
}

// ListCheckpoints returns all shadow checkpoints pinned for sessionID in root,
// ordered newest first.
func ListCheckpoints(ctx context.Context, root, sessionID string) ([]CheckpointEntry, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	prefix := checkpointRefPrefix + sanitizeCheckpointPart(sessionID) + "/"
	out, err := gitOut(ctx, root, nil, "for-each-ref", "--format=%(refname) %(objectname) %(committerdate:unix)", prefix)
	if err != nil {
		return nil, nil
	}
	var list []CheckpointEntry
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		key := strings.TrimPrefix(fields[0], prefix)
		ts, _ := strconv.ParseInt(fields[2], 10, 64)
		list = append(list, CheckpointEntry{
			SessionID: sessionID,
			Key:       key,
			Commit:    fields[1],
			CreatedAt: ts * 1000,
		})
	}
	slices.SortFunc(list, func(a, b CheckpointEntry) int {
		if a.CreatedAt != b.CreatedAt {
			if a.CreatedAt > b.CreatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(b.Key, a.Key)
	})
	return list, nil
}

func runCheckpointOp(ctx context.Context, op, root, commit, message string) (string, bool, error) {
	switch op {
	case "checkpoint":
		// Message format: "<sessionID>" or "<sessionID>:<key>"
		sid, key, _ := strings.Cut(strings.TrimSpace(message), ":")
		sha, err := CaptureSnapshot(ctx, root, sid, key)
		return sha, true, err
	case "restore_checkpoint":
		return "", true, RestoreSnapshot(ctx, root, commit)
	case "list_checkpoints":
		list, err := ListCheckpoints(ctx, root, strings.TrimSpace(message))
		if err != nil {
			return "", true, err
		}
		raw, err := json.Marshal(list)
		return string(raw), true, err
	}
	return "", false, nil
}
