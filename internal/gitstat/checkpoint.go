package gitstat

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ironpark/mako/internal/protocol"
)

// A checkpoint is a shadow commit of a work tree: its tracked files and its
// untracked ones that are not ignored, as they were, made with an index of
// its own so that the repository's index, HEAD and stashes stay as they are.
// A turn's checkpoint is under checkpointRefs, by session; what a restore
// found, to undo it with, is under restoreRefs, scoped by worktree so
// linked worktrees never list or restore each other's pre-restore
// snapshots. Both are under refs/mako/, which the history does not list nor
// fingerprint.
const (
	makoRefs       = "refs/mako/"
	checkpointRefs = makoRefs + "checkpoints/" // <session>/<key>
	restoreRefs    = makoRefs + "pre-restore/" // <worktree>/<key>
)

// checkpointLocks serializes CaptureSnapshot and RestoreSnapshot per work
// tree (keyed by canonical git dir) so concurrent operations never race on
// work tree mutations or ref pruning.
var (
	checkpointLocksMu sync.Mutex
	checkpointLocks   = map[string]*sync.Mutex{}
)

func lockWorktree(gitDir string) func() {
	key := canonicalPath(gitDir)
	checkpointLocksMu.Lock()
	mu := checkpointLocks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		checkpointLocks[key] = mu
	}
	checkpointLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func canonicalPath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return clean
}

// worktreeKey hashes the worktree's own git directory (.git or
// .git/worktrees/<name>) so linked worktrees keep their pre-restore
// snapshots isolated while subfolders of the same worktree share them.
func worktreeKey(gitDir string) string {
	sum := sha256.Sum256([]byte(canonicalPath(gitDir)))
	return hex.EncodeToString(sum[:8])
}

// worktreeGitDir returns the worktree's absolute git directory for root, or
// ErrNoWorkTree when root is outside any git work tree. Other git errors
// (such as dubious ownership or permission failures) are returned as-is.
func worktreeGitDir(ctx context.Context, root string) (string, error) {
	where, err := gitOut(ctx, root, nil, "rev-parse", "--is-inside-work-tree", "--absolute-git-dir")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "not a git repository") || strings.Contains(lower, "must be run in a work tree") {
			return "", fmt.Errorf("checkpoint: %w", ErrNoWorkTree)
		}
		return "", fmt.Errorf("checkpoint: %w", err)
	}
	inside, gitDir, ok := strings.Cut(strings.TrimRight(string(where), "\r\n"), "\n")
	inside = strings.TrimSpace(inside)
	gitDir = strings.TrimRight(gitDir, "\r")
	if !ok || inside != "true" || gitDir == "" || strings.Contains(gitDir, "\n") {
		return "", fmt.Errorf("checkpoint: %w", ErrNoWorkTree)
	}
	return gitDir, nil
}

// The kinds of checkpoint.
const (
	CheckpointTurn       = "turn"        // the work tree as a turn of the agent's found it
	CheckpointPreRestore = "pre-restore" // the work tree as a restore found it
)

// What is kept: a session's newest turns, the repository's newest restores,
// and of either nothing older than checkpointMaxAge. Their objects go with
// the repository's next gc. Tests change them.
var (
	maxTurnCheckpoints    = 100
	maxRestoreCheckpoints = 20
	checkpointMaxAge      = 30 * 24 * time.Hour
)

// checkpointSubject starts a checkpoint commit's message, which goes on
// with its fields as name=value.
const checkpointSubject = "mako checkpoint"

// ErrNoWorkTree is what a capture of a folder that is no git work tree
// fails with.
var ErrNoWorkTree = errors.New("not a git work tree")

// CheckpointEntry is one checkpoint, as ListCheckpoints finds it.
type CheckpointEntry struct {
	Key       string `json:"key"` // the last element of its ref, its own
	Kind      string `json:"kind"`
	Commit    string `json:"commit"`
	SessionID string `json:"session_id,omitempty"` // a turn's
	Prompt    string `json:"prompt,omitempty"`     // a turn's: PromptKey of the words that started it, if the agent said
	CreatedAt int64  `json:"created_at"`           // unix milliseconds
}

// Checkpoint is what a capture is of: the session and its turn's prompt,
// as PromptKey has it, for a turn.
type Checkpoint struct {
	Kind    string
	Session string
	Prompt  string
}

// PromptKey is what a turn's checkpoint knows of the words the turn started
// with: enough to find the turn by them, "" for none.
func PromptKey(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// gitEnvOut is gitOut with extraEnv set too.
func gitEnvOut(ctx context.Context, root string, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, root, stdin, args)
	cmd.Env = append(cmd.Env, extraEnv...)
	return outputOf(cmd)
}

// checkpointEnv is git's environment for work on the temporary index at
// index: commits made there are mako's.
func checkpointEnv(index string) []string {
	return []string{
		"GIT_INDEX_FILE=" + index,
		"GIT_AUTHOR_NAME=mako",
		"GIT_AUTHOR_EMAIL=checkpoint@mako.local",
		"GIT_COMMITTER_NAME=mako",
		"GIT_COMMITTER_EMAIL=checkpoint@mako.local",
	}
}

// tempIndex is a path for an index of its own, in a folder removed with
// done.
func tempIndex() (index string, done func(), err error) {
	dir, err := os.MkdirTemp("", "mako-checkpoint-*")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, "index"), func() { _ = os.RemoveAll(dir) }, nil
}

// CaptureSnapshot commits the work tree at root, whole, as a checkpoint of
// cp's kind, and keeps it under its ref, dropping those no longer kept. A
// turn's checkpoint needs its session.
func CaptureSnapshot(ctx context.Context, root string, cp Checkpoint) (CheckpointEntry, error) {
	if strings.TrimSpace(root) == "" {
		return CheckpointEntry{}, errors.New("checkpoint: no folder")
	}
	sid := ""
	switch cp.Kind {
	case CheckpointTurn:
		if sid = sessionRefPart(cp.Session); sid == "" {
			return CheckpointEntry{}, errors.New("checkpoint: no session")
		}
	case CheckpointPreRestore:
	default:
		return CheckpointEntry{}, fmt.Errorf("checkpoint: unknown kind %q", cp.Kind)
	}
	gitDir, err := worktreeGitDir(ctx, root)
	if err != nil {
		return CheckpointEntry{}, err
	}
	unlock := lockWorktree(gitDir)
	defer unlock()
	e, err := captureCommit(ctx, root, gitDir, cp)
	if err != nil {
		return CheckpointEntry{}, err
	}
	if err := publishCheckpoint(ctx, root, gitDir, sid, e); err != nil {
		return CheckpointEntry{}, err
	}
	return e, nil
}

// captureCommit builds the shadow commit object for root without updating
// any ref or pruning yet. Caller holds the worktree lock.
func captureCommit(ctx context.Context, root, gitDir string, cp Checkpoint) (CheckpointEntry, error) {
	index, done, err := tempIndex()
	if err != nil {
		return CheckpointEntry{}, err
	}
	defer done()
	// The real index, copied, tells git which files are as they were, which
	// it then need not read again.
	if err := copyFile(filepath.Join(gitDir, "index"), index); err != nil && !errors.Is(err, os.ErrNotExist) {
		return CheckpointEntry{}, fmt.Errorf("checkpoint: %w", err)
	}
	env := checkpointEnv(index)
	if _, err := gitEnvOut(ctx, root, env, nil, "add", "-A"); err != nil {
		return CheckpointEntry{}, fmt.Errorf("checkpoint add: %w", cmp.Or(ctx.Err(), err))
	}
	tree, err := gitEnvOut(ctx, root, env, nil, "write-tree")
	if err != nil {
		return CheckpointEntry{}, fmt.Errorf("checkpoint write-tree: %w", cmp.Or(ctx.Err(), err))
	}
	now := time.Now().UnixMilli()
	subject := fmt.Sprintf("%s kind=%s time=%d", checkpointSubject, cp.Kind, now)
	if key := strings.TrimSpace(cp.Prompt); cp.Kind == CheckpointTurn && key != "" && !strings.ContainsFunc(key, unicode.IsSpace) {
		subject += " prompt=" + key
	}
	args := []string{"commit-tree", "--no-gpg-sign", strings.TrimSpace(string(tree)), "-m", subject}
	hasHead, err := repoHasHead(ctx, root)
	if err != nil {
		return CheckpointEntry{}, fmt.Errorf("checkpoint: %w", err)
	}
	if hasHead {
		args = append(args, "-p", "HEAD")
	}
	commit, err := gitEnvOut(ctx, root, env, nil, args...)
	if err != nil {
		return CheckpointEntry{}, fmt.Errorf("checkpoint commit-tree: %w", cmp.Or(ctx.Err(), err))
	}
	e := CheckpointEntry{Kind: cp.Kind, Commit: strings.TrimSpace(string(commit)), SessionID: cp.Session, CreatedAt: now}
	if cp.Kind == CheckpointTurn {
		e.Prompt = strings.TrimSpace(cp.Prompt)
	} else {
		e.SessionID = ""
	}
	if err := checkHash(e.Commit); err != nil {
		return CheckpointEntry{}, fmt.Errorf("checkpoint commit-tree: %w", err)
	}
	e.Key = fmt.Sprintf("%d-%s", now, short(e.Commit))
	return e, nil
}

// publishCheckpoint writes e's ref under refs/mako/ and prunes old entries.
// Caller holds the worktree lock.
func publishCheckpoint(ctx context.Context, root, gitDir, sid string, e CheckpointEntry) error {
	ref := restoreRefs + worktreeKey(gitDir) + "/" + e.Key
	if e.Kind == CheckpointTurn {
		ref = checkpointRefs + sid + "/" + e.Key
	}
	if _, err := gitOut(ctx, root, nil, "update-ref", ref, e.Commit); err != nil {
		return fmt.Errorf("checkpoint update-ref: %w", cmp.Or(ctx.Err(), err))
	}
	// A prune that fails leaves more kept than need be, which the next
	// capture's prune takes up: the checkpoint is made.
	_ = pruneCheckpoints(ctx, root, time.Now(), ref)
	return nil
}

// short is a commit's hash as a message names it.
func short(hash string) string { return hash[:min(len(hash), 12)] }

// copyFile copies the file at from to a new file at to.
func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// sessionRefPart is a session's ID as an element of a ref, "" for none:
// a daemon's IDs are hex, kept as they are, and anything else that ref
// names cannot hold is refused.
func sessionRefPart(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.HasPrefix(id, ".") || strings.HasPrefix(id, "-") || strings.ContainsFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return ""
	}
	return id
}

// checkpointRef is one checkpoint's ref and what its commit's subject says.
type checkpointRef struct {
	Ref      string
	Worktree string // worktreeKey for CheckpointPreRestore refs
	CheckpointEntry
}

// readCheckpoints lists the checkpoints under the refs prefixes name.
func readCheckpoints(ctx context.Context, root string, prefixes ...string) ([]checkpointRef, error) {
	out, err := gitOut(ctx, root, nil, append([]string{"for-each-ref", "--format=%(refname)%00%(objectname)%00%(contents:subject)"}, prefixes...)...)
	if err != nil {
		return nil, cmp.Or(ctx.Err(), err)
	}
	var list []checkpointRef
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 3 {
			continue
		}
		c := checkpointRef{Ref: f[0], CheckpointEntry: CheckpointEntry{Commit: f[1]}}
		rest, ok := strings.CutPrefix(f[2], checkpointSubject+" ")
		if !ok {
			continue // not mako's
		}
		for field := range strings.FieldsSeq(rest) {
			name, value, _ := strings.Cut(field, "=")
			switch name { // its kind is its ref's
			case "time":
				c.CreatedAt, _ = strconv.ParseInt(value, 10, 64)
			case "prompt":
				c.Prompt = value
			}
		}
		switch {
		case strings.HasPrefix(c.Ref, restoreRefs):
			tail := strings.TrimPrefix(c.Ref, restoreRefs)
			if wt, key, hasWT := strings.Cut(tail, "/"); hasWT {
				c.Worktree, c.Key = wt, key
			} else {
				c.Key = tail
			}
			c.Kind = CheckpointPreRestore
		case strings.HasPrefix(c.Ref, checkpointRefs):
			c.SessionID, c.Key, _ = strings.Cut(strings.TrimPrefix(c.Ref, checkpointRefs), "/")
			c.Kind = CheckpointTurn
		default:
			continue
		}
		if c.CreatedAt == 0 {
			ms, _, _ := strings.Cut(c.Key, "-")
			c.CreatedAt, _ = strconv.ParseInt(ms, 10, 64)
		}
		list = append(list, c)
	}
	// Newest first; the key, which starts with the time, settles a tie.
	slices.SortFunc(list, func(a, b checkpointRef) int {
		return cmp.Or(cmp.Compare(b.CreatedAt, a.CreatedAt), strings.Compare(b.Key, a.Key))
	})
	return list, nil
}

// pruneCheckpoints drops the checkpoints of root no longer kept, as of now,
// but never keep, the ref of one just made.
func pruneCheckpoints(ctx context.Context, root string, now time.Time, keep string) error {
	list, err := readCheckpoints(ctx, root, checkpointRefs, restoreRefs)
	if err != nil {
		return err
	}
	oldest := now.Add(-checkpointMaxAge).UnixMilli()
	kept := map[string]int{} // bucketed by turn session or pre-restore worktree
	var drop strings.Builder
	for _, c := range list { // newest first
		limit := maxTurnCheckpoints
		bucket := "turn:" + c.SessionID
		if c.Kind == CheckpointPreRestore {
			limit = maxRestoreCheckpoints
			bucket = "pre-restore:" + c.Worktree
		}
		if c.Ref == keep || (c.CreatedAt >= oldest && kept[bucket] < limit) {
			kept[bucket]++
			continue
		}
		fmt.Fprintf(&drop, "delete %s\x00%s\x00", c.Ref, c.Commit)
	}
	return deleteRefs(ctx, root, drop.String())
}

// deleteRefs runs update-ref's NUL-delimited commands, if any, at once.
func deleteRefs(ctx context.Context, root, commands string) error {
	if commands == "" {
		return nil
	}
	_, err := gitOut(ctx, root, []byte(commands), "update-ref", "--stdin", "-z")
	return err
}

// DeleteCheckpoints drops the turn checkpoints of a session, as when it is
// closed for good. What restores found stays: another session may undo one.
func DeleteCheckpoints(ctx context.Context, root, sessionID string) error {
	sid := sessionRefPart(sessionID)
	if sid == "" {
		return nil
	}
	list, err := readCheckpoints(ctx, root, checkpointRefs+sid+"/")
	if err != nil {
		return err
	}
	var drop strings.Builder
	for _, c := range list {
		fmt.Fprintf(&drop, "delete %s\x00%s\x00", c.Ref, c.Commit)
	}
	return deleteRefs(ctx, root, drop.String())
}

// ListCheckpoints lists the checkpoints of the session's turns, and what
// restores of root's work tree found, newest first.
func ListCheckpoints(ctx context.Context, root, sessionID string) ([]CheckpointEntry, error) {
	gitDir, err := worktreeGitDir(ctx, root)
	if err != nil {
		return nil, err
	}
	prefixes := []string{restoreRefs + worktreeKey(gitDir) + "/"}
	if sid := sessionRefPart(sessionID); sid != "" {
		prefixes = append(prefixes, checkpointRefs+sid+"/")
	}
	refs, err := readCheckpoints(ctx, root, prefixes...)
	if err != nil {
		return nil, err
	}
	list := make([]CheckpointEntry, len(refs))
	for i, c := range refs {
		list[i] = c.CheckpointEntry
	}
	return list, nil
}

// treeChange is a path a restore changes: to remove, or to write from the
// checkpoint, which the work tree's snapshot may not have (Added).
type treeChange struct {
	Path   string
	Remove bool
	Added  bool
}

// gitlinkMode is a submodule's mode in a tree, which a restore leaves be.
const gitlinkMode = "160000"

// RestoreSnapshot puts the files at root, and under it, back as the
// checkpoint commit has them, leaving HEAD, the index and ignored files as
// they are. It captures the work tree first, as a pre-restore checkpoint
// it returns, and changes nothing if it cannot.
func RestoreSnapshot(ctx context.Context, root, commit string) (CheckpointEntry, error) {
	sha, err := commitArg(strings.TrimSpace(commit))
	if err != nil {
		return CheckpointEntry{}, err
	}
	gitDir, err := worktreeGitDir(ctx, root)
	if err != nil {
		return CheckpointEntry{}, err
	}
	unlock := lockWorktree(gitDir)
	defer unlock()
	if _, err := gitOut(ctx, root, nil, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return CheckpointEntry{}, fmt.Errorf("checkpoint %s not found: %w", sha, cmp.Or(ctx.Err(), err))
	}
	// Build the safety commit before mutating anything, but hold off on
	// publishing its ref/pruning until restorable checks pass so a refused
	// restore never leaves a phantom pre-restore ref or evicts a real one.
	safety, err := captureCommit(ctx, root, gitDir, Checkpoint{Kind: CheckpointPreRestore})
	if err != nil {
		return CheckpointEntry{}, fmt.Errorf("restore: no safety snapshot, so nothing was restored: %w", err)
	}
	// The safety snapshot is the work tree as it is: what differs from the
	// checkpoint is all a restore writes or removes.
	changes, err := restoreChanges(ctx, root, safety.Commit, sha)
	if err != nil {
		return CheckpointEntry{}, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return CheckpointEntry{}, err
	}
	defer dir.Close()
	removed := map[string]bool{}
	var write []string
	for _, c := range changes {
		if c.Remove {
			removed[c.Path] = true
		} else {
			write = append(write, c.Path)
		}
	}
	// Nothing is changed when what is in the way of a file to write is in
	// neither snapshot, as an ignored file is: the restore would lose it.
	for _, c := range changes {
		if !c.Remove {
			if err := restorable(dir, c, removed); err != nil {
				return CheckpointEntry{}, err
			}
		}
	}
	if err := publishCheckpoint(ctx, root, gitDir, "", safety); err != nil {
		return CheckpointEntry{}, fmt.Errorf("restore: no safety snapshot, so nothing was restored: %w", err)
	}
	for _, c := range changes {
		if !c.Remove {
			continue
		}
		if err := dir.Remove(filepath.FromSlash(c.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return safety, fmt.Errorf("restore: %w (the work tree as it was is in safety snapshot %s)", err, short(safety.Commit))
		}
		removeEmptyParents(dir, c.Path)
	}
	if len(write) == 0 {
		return safety, nil
	}
	index, done, err := tempIndex()
	if err != nil {
		return safety, err
	}
	defer done()
	env := checkpointEnv(index)
	if _, err := gitEnvOut(ctx, root, env, nil, "read-tree", sha); err != nil {
		return safety, fmt.Errorf("restore read-tree: %w", cmp.Or(ctx.Err(), err))
	}
	// Paths are read relative to root, as the changes are.
	if _, err := gitEnvOut(ctx, root, env, []byte(strings.Join(write, "\x00")+"\x00"), "checkout-index", "-f", "-z", "--stdin"); err != nil {
		return safety, fmt.Errorf("restore checkout-index: %w (the work tree as it was is in safety snapshot %s)", cmp.Or(ctx.Err(), err), short(safety.Commit))
	}
	return safety, nil
}

// restoreChanges is what differs between the work tree's snapshot from
// and the checkpoint to, at root and under it, by paths relative to root.
func restoreChanges(ctx context.Context, root, from, to string) ([]treeChange, error) {
	out, err := gitOut(ctx, root, nil, "diff-tree", "-r", "-z", "--no-renames", "--relative", "--raw", from, to)
	if err != nil {
		return nil, fmt.Errorf("restore diff-tree: %w", cmp.Or(ctx.Err(), err))
	}
	var changes []treeChange
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta, p := strings.Fields(strings.TrimPrefix(fields[i], ":")), fields[i+1]
		if len(meta) != 5 || inWorkTree(p) != nil || p == "" || hasDotGitSegment(p) {
			return nil, fmt.Errorf("restore: cannot read the change %q of %q", fields[i], p)
		}
		if meta[0] == gitlinkMode || meta[1] == gitlinkMode {
			continue
		}
		changes = append(changes, treeChange{Path: p, Remove: meta[4] == "D", Added: meta[4] == "A"})
	}
	return changes, nil
}

// removedSameFile reports whether p (or a case-only alias of p on a
// case-insensitive filesystem that resolves to the same os.FileInfo) is
// scheduled for removal before write.
func removedSameFile(dir *os.Root, p string, info os.FileInfo, removed map[string]bool) bool {
	if removed[p] {
		return true
	}
	if info == nil {
		return false
	}
	for r := range removed {
		if !strings.EqualFold(r, p) {
			continue
		}
		if rInfo, err := dir.Lstat(filepath.FromSlash(r)); err == nil && os.SameFile(info, rInfo) {
			return true
		}
	}
	return false
}

// restorable refuses to write c's file over what is in its way, if that
// is in no snapshot and not removed first: an ignored file, or a folder
// with one left in it.
func restorable(dir *os.Root, c treeChange, removed map[string]bool) error {
	var at string
	for seg := range strings.SplitSeq(c.Path, "/") {
		at = path.Join(at, seg)
		info, err := dir.Lstat(filepath.FromSlash(at))
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil
		case err != nil:
			return fmt.Errorf("restore: %w", err)
		case removedSameFile(dir, at, info, removed):
			return nil // gone before the file is written
		case at != c.Path && info.IsDir() && info.Mode()&os.ModeSymlink == 0:
			continue
		case at != c.Path:
			return fmt.Errorf("restore: %s is in the way of %s, and in no snapshot; move it away first", at, c.Path)
		case info.IsDir():
			if left := fileLeftIn(dir, at, removed); left != "" {
				return fmt.Errorf("restore: %s would replace folder %s, which holds %s; move it away first", c.Path, at, left)
			}
		case c.Added:
			// The work tree's snapshot does not have it: it is ignored.
			return fmt.Errorf("restore: %s is in the way, and in no snapshot; move it away first", c.Path)
		}
	}
	return nil
}

// fileLeftIn is a file under folder p of dir that is not removed, "" for
// none.
func fileLeftIn(dir *os.Root, p string, removed map[string]bool) string {
	f, err := dir.Open(filepath.FromSlash(p))
	if err != nil {
		return p
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return p
	}
	for _, e := range entries {
		name := path.Join(p, e.Name())
		switch {
		case e.IsDir():
			if left := fileLeftIn(dir, name, removed); left != "" {
				return left
			}
		default:
			if removed[name] {
				continue
			}
			if info, err := dir.Lstat(filepath.FromSlash(name)); err == nil && removedSameFile(dir, name, info, removed) {
				continue
			}
			return name
		}
	}
	return ""
}

// removeEmptyParents removes the folders p was in that it left empty, up to
// the root.
func removeEmptyParents(dir *os.Root, p string) {
	for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
		if dir.Remove(filepath.FromSlash(d)) != nil {
			return // not empty, or gone
		}
	}
}

// runCheckpointOp runs the checkpoint operations; Message names the
// session.
func runCheckpointOp(ctx context.Context, op, root, commit, session string) (string, error) {
	switch op {
	case protocol.GitOpRestoreCheckpoint:
		safety, err := RestoreSnapshot(ctx, root, commit)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(safety)
		return string(raw), err
	case protocol.GitOpListCheckpoints:
		list, err := ListCheckpoints(ctx, root, session)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(list)
		return string(raw), err
	}
	return "", fmt.Errorf("unknown git operation %q", op)
}
