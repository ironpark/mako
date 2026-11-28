package sshremote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	maxRemoteRead  = 4 << 20
	maxRemoteImage = 5 << 20
)

// ListDir lists entries of dirPath on the remote SSH host. Leading "~" is
// expanded against remoteHome.
func ListDir(ctx context.Context, h config.SSHHost, controlPath, remoteHome, dirPath string) (*protocol.Dir, error) {
	target := ExpandRemotePath(dirPath, remoteHome)
	if target == "" {
		target = "~"
	}
	// Use python3 when present for exact, fast, NUL-free metadata; otherwise
	// fall back to portable POSIX sh + ls/stat.
	script := fmt.Sprintf(`%s; _p=%s
case "$_p" in '~') _p="$HOME";; '~/'*) _p="$HOME/${_p#~/}";; esac
if [ ! -d "$_p" ]; then echo "not a directory: $_p" >&2; exit 1; fi
_abs="$(cd "$_p" 2>/dev/null && pwd -P)" || _abs="$_p"
printf 'PATH=%%s\n' "$_abs"
if command -v python3 >/dev/null 2>&1; then
  python3 - "$_abs" <<'PY'
import os, sys
root = sys.argv[1]
try:
    it = list(os.scandir(root))
except Exception as e:
    sys.stderr.write(str(e) + "\n")
    sys.exit(1)
for e in it:
    try:
        is_link = e.is_symlink()
        st = os.stat(e.path) if is_link else e.stat(follow_symlinks=False)
        is_dir = e.is_dir(follow_symlinks=True)
        size = 0 if is_dir else st.st_size
        mtime = int(st.st_mtime)
    except OSError:
        is_dir, is_link, size, mtime = False, e.is_symlink(), 0, 0
    name = e.name.replace("\n", "")
    print(f"E\t{1 if is_dir else 0}\t{1 if is_link else 0}\t{size}\t{mtime}\t{name}")
PY
else
  for _f in "$_abs"/* "$_abs"/.[!.]* "$_abs"/..?*; do
    [ -e "$_f" ] || [ -L "$_f" ] || continue
    _n="${_f##*/}"
    _d=0; [ -d "$_f" ] && _d=1
    _l=0; [ -L "$_f" ] && _l=1
    _s=0; [ "$_d" = "0" ] && _s="$(wc -c < "$_f" 2>/dev/null | tr -d ' ' || echo 0)"
    printf 'E\t%%s\t%%s\t%%s\t0\t%%s\n' "$_d" "$_l" "$_s" "$_n"
  done
fi`, remotePathPrelude, ShellQuote(target))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return nil, err
	}
	return parseListDirOutput(string(out), target), nil
}

func parseListDirOutput(raw, fallbackPath string) *protocol.Dir {
	dir := &protocol.Dir{Path: fallbackPath}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if p, ok := strings.CutPrefix(line, "PATH="); ok {
			dir.Path = p
			continue
		}
		if !strings.HasPrefix(line, "E\t") {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 || parts[5] == "" {
			continue
		}
		size, _ := strconv.ParseInt(parts[3], 10, 64)
		mtime, _ := strconv.ParseInt(parts[4], 10, 64)
		entry := &protocol.DirEntry{
			Name: parts[5],
			Dir:  parts[1] == "1",
			Link: parts[2] == "1",
			Size: size,
		}
		if mtime > 0 {
			entry.ModTime = timestamppb.New(time.Unix(mtime, 0))
		}
		dir.Entries = append(dir.Entries, entry)
	}
	sort.Slice(dir.Entries, func(i, j int) bool {
		a, b := dir.Entries[i], dir.Entries[j]
		if a.GetDir() != b.GetDir() {
			return a.GetDir()
		}
		return strings.ToLower(a.GetName()) < strings.ToLower(b.GetName())
	})
	return dir
}

// ReadFile reads a file (or raw byte chunk / image) from the remote SSH host.
func ReadFile(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.ReadFile) (*protocol.File, error) {
	target := ExpandRemotePath(req.GetPath(), remoteHome)
	limit := int(req.GetMax())
	if req.GetImage() {
		limit = maxRemoteImage
	} else if limit <= 0 || limit > maxRemoteRead {
		limit = maxRemoteRead
	}

	// First stat the file to get size and mtime.
	statScript := fmt.Sprintf(`_p=%s
case "$_p" in '~') _p="$HOME";; '~/'*) _p="$HOME/${_p#~/}";; esac
if [ ! -f "$_p" ]; then echo "file not found: $_p" >&2; exit 1; fi
if command -v python3 >/dev/null 2>&1; then
  python3 - "$_p" <<'PY'
import os, sys
st = os.stat(sys.argv[1])
print(f"{st.st_size}\t{int(st.st_mtime)}\t{os.path.realpath(sys.argv[1])}")
PY
else
  _sz="$(wc -c < "$_p" | tr -d ' ')"
  printf '%%s\t0\t%%s\n' "$_sz" "$_p"
fi`, ShellQuote(target))

	statOut, err := runSSH(ctx, h, controlPath, statScript)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(statOut)), "\t", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("failed to stat remote file %q", target)
	}
	size, _ := strconv.ParseInt(parts[0], 10, 64)
	mtime, _ := strconv.ParseInt(parts[1], 10, 64)
	resolvedPath := target
	if len(parts) == 3 && parts[2] != "" {
		resolvedPath = parts[2]
	}

	offset := req.GetOffset()
	if req.GetRaw() && offset < 0 {
		offset = max(size+offset, 0)
	}
	if offset < 0 {
		offset = 0
	}
	if req.GetImage() && size > maxRemoteImage {
		return nil, fmt.Errorf("image exceeds %d MB limit", maxRemoteImage>>20)
	}

	// Read bytes from offset up to limit+1 so we know if truncated.
	readCount := limit + 1
	if req.GetRaw() || req.GetImage() {
		readCount = limit
	}
	readScript := fmt.Sprintf(`_p=%s
case "$_p" in '~') _p="$HOME";; '~/'*) _p="$HOME/${_p#~/}";; esac
if [ %d -gt 0 ]; then
  dd if="$_p" bs=1 skip=%d count=%d 2>/dev/null
else
  head -c %d "$_p"
fi`, ShellQuote(resolvedPath), offset, offset, readCount, readCount)

	data, err := runSSH(ctx, h, controlPath, readScript)
	if err != nil {
		return nil, err
	}

	res := &protocol.File{
		Path: resolvedPath,
		Size: size,
	}
	if mtime > 0 {
		res.ModTime = timestamppb.New(time.Unix(mtime, 0))
	}
	if req.GetRaw() || req.GetImage() {
		res.Data = data
		res.Truncated = offset+int64(len(data)) < size
		return res, nil
	}
	if len(data) > limit {
		res.Truncated = true
		data = data[:limit]
	}
	if isBinaryData(data) {
		res.Binary = true
		return res, nil
	}
	res.Data = data
	return res, nil
}

func isBinaryData(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	return len(sample) > 0 && !utf8.Valid(sample)
}

// WriteFile writes or appends bytes to a file on the remote SSH host.
func WriteFile(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.WriteFile) (*protocol.File, error) {
	target := ExpandRemotePath(req.GetPath(), remoteHome)
	if target == "" {
		return nil, errors.New("path is required")
	}
	var script string
	if req.GetOffset() > 0 {
		// Append chunk at offset for chunked uploads.
		script = fmt.Sprintf(`_p=%s
case "$_p" in '~') _p="$HOME";; '~/'*) _p="$HOME/${_p#~/}";; esac
cat >> "$_p"
_sz="$(wc -c < "$_p" | tr -d ' ')"
printf '%%s\n' "$_sz"`, ShellQuote(target))
	} else {
		script = fmt.Sprintf(`_p=%s
case "$_p" in '~') _p="$HOME";; '~/'*) _p="$HOME/${_p#~/}";; esac
mkdir -p "$(dirname "$_p")"
_tmp="$_p.mako.tmp.$$"
cat > "$_tmp" && mv -f "$_tmp" "$_p"
_sz="$(wc -c < "$_p" | tr -d ' ')"
printf '%%s\n' "$_sz"`, ShellQuote(target))
	}
	out, err := runSSHStdin(ctx, h, controlPath, script, bytes.NewReader(req.GetData()))
	if err != nil {
		return nil, err
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return &protocol.File{
		Path:    target,
		Size:    size,
		ModTime: timestamppb.Now(),
	}, nil
}

// FileOp performs create, mkdir, rename, delete, or copy on the remote SSH host.
func FileOp(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.FileOp) (*protocol.File, error) {
	p := ExpandRemotePath(req.GetPath(), remoteHome)
	to := ExpandRemotePath(req.GetTo(), remoteHome)
	var script string
	switch req.GetOp() {
	case protocol.FileCreate:
		script = fmt.Sprintf(`_p=%s; [ ! -e "$_p" ] || { echo "already exists" >&2; exit 1; }; mkdir -p -- "$(dirname -- "$_p")" && : > "$_p"`, ShellQuote(p))
	case protocol.FileMkdir:
		script = fmt.Sprintf(`_p=%s; [ ! -e "$_p" ] || { echo "already exists" >&2; exit 1; }; mkdir -p -- "$_p"`, ShellQuote(p))
	case protocol.FileRename:
		script = fmt.Sprintf(`_p=%s; _to=%s; [ ! -e "$_to" ] || { echo "destination exists" >&2; exit 1; }; mkdir -p -- "$(dirname -- "$_to")" && mv -- "$_p" "$_to"`, ShellQuote(p), ShellQuote(to))
	case protocol.FileDelete:
		script = fmt.Sprintf(`rm -rf -- %s`, ShellQuote(p))
	case protocol.FileCopy:
		script = fmt.Sprintf(`_p=%s; _to=%s; [ ! -e "$_to" ] || { echo "destination exists" >&2; exit 1; }; mkdir -p -- "$(dirname -- "$_to")" && cp -R -- "$_p" "$_to"`, ShellQuote(p), ShellQuote(to))
	default:
		return nil, fmt.Errorf("unsupported file op %q", req.GetOp())
	}
	if _, err := runSSH(ctx, h, controlPath, script); err != nil {
		return nil, err
	}
	resPath := p
	if to != "" && (req.GetOp() == protocol.FileRename || req.GetOp() == protocol.FileCopy) {
		resPath = to
	}
	return &protocol.File{Path: resPath, ModTime: timestamppb.Now()}, nil
}

// StageFile uploads a dropped file/image chunk to ~/.cache/mako/mako-drops on
// the remote SSH host so remote agents can read it by path.
func StageFile(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.StageFile) (*protocol.Staged, error) {
	remotePath := req.GetPath()
	if remotePath == "" {
		name := path.Base(path.Clean("/" + req.GetName()))
		if name == "/" || name == "." || name == "" {
			return nil, errors.New("a staged file needs a name")
		}
		var id [8]byte
		_, _ = rand.Read(id[:])
		base := ExpandRemotePath("~/.cache/mako/mako-drops", remoteHome)
		remotePath = path.Join(base, time.Now().Format("20060102-150405")+"-"+hex.EncodeToString(id[:]), name)
	}
	var script string
	if req.GetOffset() > 0 {
		script = fmt.Sprintf(`_p=%s; cat >> "$_p"; wc -c < "$_p" | tr -d ' '`, ShellQuote(remotePath))
	} else {
		script = fmt.Sprintf(`_p=%s; mkdir -p "$(dirname "$_p")" && chmod 700 "$(dirname "$_p")" && cat > "$_p" && chmod 600 "$_p"; wc -c < "$_p" | tr -d ' '`, ShellQuote(remotePath))
	}
	out, err := runSSHStdin(ctx, h, controlPath, script, bytes.NewReader(req.GetData()))
	if err != nil {
		return nil, err
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return &protocol.Staged{Path: remotePath, Size: size}, nil
}

// Search lists project files or searches text inside root on the remote SSH host.
func Search(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.Search) (*protocol.SearchResult, error) {
	root := ExpandRemotePath(req.GetRoot(), remoteHome)
	if req.GetFiles() {
		script := fmt.Sprintf(`%s; _r=%s
if git -C "$_r" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git -C "$_r" ls-files -co --exclude-standard | head -n 50001
else
  cd "$_r" && find . -maxdepth 8 \( -name .git -o -name node_modules -o -name target -o -name vendor \) -prune -o -type f -print | sed 's|^\./||' | head -n 50001
fi`, remotePathPrelude, ShellQuote(root))
		out, err := runSSH(ctx, h, controlPath, script)
		if err != nil {
			return nil, err
		}
		res := &protocol.SearchResult{}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(res.Files) >= 50000 {
				res.Truncated = true
				break
			}
			res.Files = append(res.Files, line)
		}
		return res, nil
	}

	q := strings.TrimSpace(req.GetQuery())
	if q == "" {
		return nil, errors.New("nothing to search for")
	}
	script := fmt.Sprintf(`%s; _r=%s; _q=%s
if command -v rg >/dev/null 2>&1; then
  cd "$_r" && rg --no-heading --line-number --column --color never --fixed-strings --smart-case --no-require-git --path-separator / --glob '!node_modules/' --max-count 100 --max-columns 300 -- "$_q" . || true
elif git -C "$_r" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git -C "$_r" grep -n --column -I -i --untracked --fixed-strings -e "$_q" || true
else
  cd "$_r" && grep -rnI -F --exclude-dir=.git --exclude-dir=node_modules -- "$_q" . || true
fi | head -n 1001`, remotePathPrelude, ShellQuote(root), ShellQuote(q))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return nil, err
	}
	return parseSearchMatches(string(out)), nil
}

func parseSearchMatches(raw string) *protocol.SearchResult {
	res := &protocol.SearchResult{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		if len(res.Matches) >= 1000 {
			res.Truncated = true
			break
		}
		p, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		p = strings.TrimPrefix(p, "./")
		lineStr, rest, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		lineNo, err := strconv.Atoi(lineStr)
		if err != nil {
			continue
		}
		col := 1
		text := rest
		if colStr, afterCol, ok := strings.Cut(rest, ":"); ok {
			if c, err := strconv.Atoi(colStr); err == nil && c > 0 {
				col = c
				text = afterCol
			}
		}
		if len(text) > 300 {
			text = text[:300]
		}
		res.Matches = append(res.Matches, &protocol.SearchMatch{
			Path: p,
			Line: int64(lineNo),
			Col:  int64(col),
			Text: text,
		})
	}
	return res
}

// RemoteGitHead describes a remote repository's branch, root, owner, remote,
// and head stamp for sidebar project grouping.
type RemoteGitHead struct {
	Branch string
	Repo   string
	Owner  string
	Remote string
	Stamp  string
}

// GitHead queries branch, toplevel, origin remote URL, and HEAD stamp on the
// remote SSH host in a single roundtrip.
func GitHead(ctx context.Context, h config.SSHHost, controlPath, remoteHome, dir string) RemoteGitHead {
	target := ExpandRemotePath(dir, remoteHome)
	if target == "" {
		return RemoteGitHead{}
	}
	script := fmt.Sprintf(`%s; _d=%s
_r="$(git -C "$_d" --no-optional-locks rev-parse --show-toplevel 2>/dev/null)" || exit 0
_b="$(git -C "$_r" --no-optional-locks symbolic-ref --short HEAD 2>/dev/null || git -C "$_r" --no-optional-locks rev-parse --short HEAD 2>/dev/null || true)"
_u="$(git -C "$_r" --no-optional-locks remote get-url origin 2>/dev/null || true)"
_h="$(git -C "$_r" --no-optional-locks rev-parse HEAD 2>/dev/null || true)"
printf 'ROOT=%%s\nBRANCH=%%s\nURL=%%s\nHEAD=%%s\n' "$_r" "$_b" "$_u" "$_h"`, remotePathPrelude, ShellQuote(target))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return RemoteGitHead{}
	}
	var head RemoteGitHead
	var originURL string
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ROOT":
			head.Repo = v
		case "BRANCH":
			head.Branch = v
		case "URL":
			originURL = v
		case "HEAD":
			head.Stamp = v
		}
	}
	if originURL != "" {
		if _, owner, remote, err := gitstat.ParseGitURL(originURL); err == nil {
			head.Owner = owner
			head.Remote = remote
		}
	}
	return head
}

// GitStatus reads git status, branches, upstream ahead/behind, stashes, and
// diff line counts on the remote SSH host.
func GitStatus(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.GitStatus) (*protocol.GitChanges, error) {
	target := ExpandRemotePath(req.GetPath(), remoteHome)
	script := fmt.Sprintf(`%s; _d=%s
_r="$(git -C "$_d" --no-optional-locks rev-parse --show-toplevel 2>/dev/null)" || { printf 'NOT_REPO=1\n'; exit 0; }
printf 'ROOT=%%s\n' "$_r"
printf '===STATUS===\n'
git -C "$_r" --no-optional-locks status --porcelain=v2 --branch -uall 2>/dev/null || true
printf '===NUMSTAT===\n'
git -C "$_r" --no-optional-locks diff --numstat HEAD 2>/dev/null || git -C "$_r" --no-optional-locks diff --numstat 2>/dev/null || true
printf '===BRANCHES===\n'
git -C "$_r" --no-optional-locks for-each-ref --format='%%(refname:short)' refs/heads 2>/dev/null || true
printf '===REMOTES===\n'
git -C "$_r" --no-optional-locks for-each-ref --format='%%(refname:short)' refs/remotes 2>/dev/null || true
printf '===STASH===\n'
git -C "$_r" --no-optional-locks stash list --format='%%gd%%x09%%ct%%x09%%gs' 2>/dev/null || true`, remotePathPrelude, ShellQuote(target))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return nil, err
	}
	return ParseGitStatusOutput(string(out)), nil
}

// ParseGitStatusOutput parses the multi-section output produced by GitStatus.
func ParseGitStatusOutput(raw string) *protocol.GitChanges {
	ch := &protocol.GitChanges{}
	section := ""
	type statNum struct {
		added, removed int64
		binary         bool
	}
	nums := map[string]statNum{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		switch line {
		case "NOT_REPO=1":
			return &protocol.GitChanges{NotRepo: true}
		case "===STATUS===", "===NUMSTAT===", "===BRANCHES===", "===REMOTES===", "===STASH===":
			section = line
			continue
		}
		if section == "" {
			if r, ok := strings.CutPrefix(line, "ROOT="); ok {
				ch.Root = r
			}
			continue
		}
		switch section {
		case "===STATUS===":
			parsePorcelainV2Line(ch, line)
		case "===NUMSTAT===":
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) == 3 {
				if parts[0] == "-" || parts[1] == "-" {
					nums[parts[2]] = statNum{binary: true}
				} else {
					a, _ := strconv.ParseInt(parts[0], 10, 64)
					r, _ := strconv.ParseInt(parts[1], 10, 64)
					nums[parts[2]] = statNum{added: a, removed: r}
				}
			}
		case "===BRANCHES===":
			if b := strings.TrimSpace(line); b != "" {
				ch.Branches = append(ch.Branches, b)
			}
		case "===REMOTES===":
			if b := strings.TrimSpace(line); b != "" && !strings.HasSuffix(b, "/HEAD") {
				ch.RemoteBranches = append(ch.RemoteBranches, b)
			}
		case "===STASH===":
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) == 3 {
				idx := int64(len(ch.Stashes))
				if open := strings.IndexByte(parts[0], '{'); open >= 0 {
					if close := strings.IndexByte(parts[0][open:], '}'); close > 1 {
						if parsed, err := strconv.ParseInt(parts[0][open+1:open+close], 10, 64); err == nil {
							idx = parsed
						}
					}
				}
				ts, _ := strconv.ParseInt(parts[1], 10, 64)
				st := &protocol.GitStash{Index: idx, Message: parts[2]}
				if ts > 0 {
					st.Time = timestamppb.New(time.Unix(ts, 0))
				}
				ch.Stashes = append(ch.Stashes, st)
			}
		}
	}

	for _, f := range ch.Files {
		if n, ok := nums[f.GetPath()]; ok {
			f.Added, f.Removed, f.Binary = n.added, n.removed, n.binary
		}
	}
	slices.Sort(ch.Branches)
	slices.Sort(ch.RemoteBranches)
	sort.Slice(ch.Files, func(i, j int) bool { return ch.Files[i].GetPath() < ch.Files[j].GetPath() })
	return ch
}

func parsePorcelainV2Line(ch *protocol.GitChanges, line string) {
	switch {
	case strings.HasPrefix(line, "# branch.head "):
		b := strings.TrimPrefix(line, "# branch.head ")
		if b != "(detached)" {
			ch.Branch = b
		}
	case strings.HasPrefix(line, "# branch.upstream "):
		ch.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
	case strings.HasPrefix(line, "# branch.ab "):
		fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
		if len(fields) >= 2 {
			ch.Ahead, _ = strconv.ParseInt(strings.TrimPrefix(fields[0], "+"), 10, 64)
			ch.Behind, _ = strconv.ParseInt(strings.TrimPrefix(fields[1], "-"), 10, 64)
		}
	case strings.HasPrefix(line, "? "):
		p := strings.TrimPrefix(line, "? ")
		ch.Files = append(ch.Files, &protocol.GitFile{Path: p, Status: protocol.GitAdded})
	case strings.HasPrefix(line, "1 "):
		fields := strings.SplitN(line, " ", 9)
		if len(fields) == 9 {
			ch.Files = append(ch.Files, &protocol.GitFile{Path: fields[8], Status: xyStatus(fields[1])})
		}
	case strings.HasPrefix(line, "2 "):
		fields := strings.SplitN(line, " ", 10)
		if len(fields) == 10 {
			to, from, _ := strings.Cut(fields[9], "\t")
			ch.Files = append(ch.Files, &protocol.GitFile{Path: to, From: from, Status: protocol.GitRenamed})
		}
	case strings.HasPrefix(line, "u "):
		fields := strings.SplitN(line, " ", 11)
		if len(fields) == 11 {
			ch.Files = append(ch.Files, &protocol.GitFile{Path: fields[10], Status: protocol.GitConflicted})
		}
	}
}

func xyStatus(xy string) string {
	if len(xy) < 2 {
		return protocol.GitModified
	}
	if xy[0] == 'D' || xy[1] == 'D' {
		return protocol.GitDeleted
	}
	if xy[0] == 'A' || xy[1] == 'A' {
		return protocol.GitAdded
	}
	if xy[0] == 'R' || xy[1] == 'R' {
		return protocol.GitRenamed
	}
	return protocol.GitModified
}

func rejectLeadingDash(field, v string) error {
	if strings.HasPrefix(strings.TrimSpace(v), "-") {
		return fmt.Errorf("invalid %s %q", field, v)
	}
	return nil
}

// GitDiff computes the unified diff for one file on the remote SSH host.
func GitDiff(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.GitDiff) (*protocol.Diff, error) {
	if err := rejectLeadingDash("commit", req.GetCommit()); err != nil {
		return nil, err
	}
	if err := rejectLeadingDash("base", req.GetBase()); err != nil {
		return nil, err
	}
	root := ExpandRemotePath(req.GetRoot(), remoteHome)
	var gitArgs []string
	switch {
	case req.GetCommit() != "":
		gitArgs = []string{"-C", root, "--no-optional-locks", "show", "--format=", "-U3", req.GetCommit(), "--", req.GetPath()}
	case req.GetBase() != "":
		gitArgs = []string{"-C", root, "--no-optional-locks", "diff", "-U3", req.GetBase() + "...HEAD", "--", req.GetPath()}
	default:
		gitArgs = []string{"-C", root, "--no-optional-locks", "diff", "-U3", "HEAD", "--", req.GetPath()}
	}
	// If the file is untracked, `git diff HEAD` returns empty; fall back to `--no-index -- /dev/null <path>`.
	script := fmt.Sprintf(`%s; _out="$(git %s 2>/dev/null || true)"
if [ -z "$_out" ] && [ -f %s ]; then
  git -C %s --no-optional-locks diff --no-index -U3 -- /dev/null %s 2>/dev/null || true
else
  printf '%%s\n' "$_out"
fi`, remotePathPrelude, ShellJoin(gitArgs), ShellQuote(path.Join(root, req.GetPath())), ShellQuote(root), ShellQuote(req.GetPath()))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return nil, err
	}
	return ParseUnifiedDiff(req.GetPath(), string(out)), nil
}

// ParseUnifiedDiff converts a standard unified diff into protocol.Diff lines.
func ParseUnifiedDiff(filePath, raw string) *protocol.Diff {
	d := &protocol.Diff{Path: filePath}
	var oldLine, newLine int64
	inHunk := false
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" {
			d.Binary = true
			d.Lines = nil
			return d
		}
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			oldLine, newLine = parseHunkHeader(line)
			d.Lines = append(d.Lines, &protocol.DiffLine{Op: "@", Text: line})
			continue
		}
		if !inHunk {
			continue
		}
		if len(d.Lines) >= gitstat.MaxDiffLines {
			d.Truncated = true
			break
		}
		if line == "" {
			continue
		}
		switch line[0] {
		case '+':
			d.Lines = append(d.Lines, &protocol.DiffLine{Op: "+", Text: line[1:], New: newLine})
			newLine++
		case '-':
			d.Lines = append(d.Lines, &protocol.DiffLine{Op: "-", Text: line[1:], Old: oldLine})
			oldLine++
		case ' ':
			d.Lines = append(d.Lines, &protocol.DiffLine{Op: " ", Text: line[1:], Old: oldLine, New: newLine})
			oldLine++
			newLine++
		}
	}
	return d
}

func parseHunkHeader(hdr string) (oldStart, newStart int64) {
	// Format: @@ -oldStart,oldCount +newStart,newCount @@
	rest := strings.TrimPrefix(hdr, "@@ ")
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return 1, 1
	}
	parseRange := func(tok string) int64 {
		tok = strings.TrimLeft(tok, "-+")
		start, _, _ := strings.Cut(tok, ",")
		n, err := strconv.ParseInt(start, 10, 64)
		if err != nil || n < 1 {
			return 1
		}
		return n
	}
	return parseRange(fields[0]), parseRange(fields[1])
}

// GitLog lists recent commits on the remote SSH host.
func GitLog(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.GitLog) (*protocol.GitHistory, error) {
	root := ExpandRemotePath(req.GetRoot(), remoteHome)
	limit := int(req.GetMax())
	if limit <= 0 {
		limit = 100
	}
	script := fmt.Sprintf(`%s; _r=%s
_cur="$(git -C "$_r" --no-optional-locks rev-parse HEAD 2>/dev/null || true)"
printf 'CUR=%%s\n' "$_cur"
git -C "$_r" --no-optional-locks log -n %d --format='%%H%%x1f%%P%%x1f%%an%%x1f%%ct%%x1f%%s%%x1f%%D' 2>/dev/null || true`, remotePathPrelude, ShellQuote(root), limit+1)

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return nil, err
	}
	hist := &protocol.GitHistory{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if cur, ok := strings.CutPrefix(line, "CUR="); ok {
			hist.Current = cur
			hist.Fingerprint = cur
			continue
		}
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 6 {
			continue
		}
		if len(hist.Commits) >= limit {
			hist.More = true
			break
		}
		ts, _ := strconv.ParseInt(parts[3], 10, 64)
		var parents []string
		if parts[1] != "" {
			parents = strings.Fields(parts[1])
		}
		var refs []string
		if parts[5] != "" {
			for _, ref := range strings.Split(parts[5], ",") {
				ref = strings.TrimSpace(ref)
				ref = strings.TrimPrefix(ref, "HEAD -> ")
				if ref != "" && ref != "HEAD" {
					refs = append(refs, ref)
				}
			}
		}
		c := &protocol.GitCommit{
			Hash:    parts[0],
			Parents: parents,
			Author:  parts[2],
			Subject: parts[4],
			Refs:    refs,
		}
		if ts > 0 {
			c.Time = timestamppb.New(time.Unix(ts, 0))
		}
		hist.Commits = append(hist.Commits, c)
	}
	if req.GetKnown() != "" && req.GetKnown() == hist.Fingerprint {
		return &protocol.GitHistory{Fingerprint: hist.Fingerprint, Current: hist.Current, Unchanged: true}, nil
	}
	return hist, nil
}

// GitShow inspects a single commit on the remote SSH host.
func GitShow(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.GitShow) (*protocol.GitCommitInfo, error) {
	if err := rejectLeadingDash("commit hash", req.GetHash()); err != nil {
		return nil, err
	}
	root := ExpandRemotePath(req.GetRoot(), remoteHome)
	script := fmt.Sprintf(`%s; _r=%s; _h=%s
git -C "$_r" --no-optional-locks show -s --format='%%H%%x1f%%P%%x1f%%an%%x1f%%ae%%x1f%%ct%%x1f%%s%%x1f%%b' "$_h" --
printf '\n===FILES===\n'
git -C "$_r" --no-optional-locks diff-tree --root --no-commit-id --name-status -r "$_h" --`, remotePathPrelude, ShellQuote(root), ShellQuote(req.GetHash()))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return nil, err
	}
	metaPart, filesPart, _ := strings.Cut(string(out), "\n===FILES===\n")
	parts := strings.Split(strings.TrimRight(metaPart, "\r\n"), "\x1f")
	info := &protocol.GitCommitInfo{Commit: &protocol.GitCommit{Hash: req.GetHash()}}
	if len(parts) >= 6 {
		info.Commit.Hash = parts[0]
		if parts[1] != "" {
			info.Commit.Parents = strings.Fields(parts[1])
		}
		info.Commit.Author = parts[2]
		info.Email = parts[3]
		if ts, err := strconv.ParseInt(parts[4], 10, 64); err == nil && ts > 0 {
			info.Commit.Time = timestamppb.New(time.Unix(ts, 0))
		}
		info.Commit.Subject = parts[5]
		if len(parts) >= 7 {
			info.Body = strings.TrimSpace(parts[6])
		}
	}
	for _, line := range strings.Split(filesPart, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		gf := &protocol.GitFile{Path: f[1], Status: protocol.GitModified}
		switch {
		case strings.HasPrefix(f[0], "A"):
			gf.Status = protocol.GitAdded
		case strings.HasPrefix(f[0], "D"):
			gf.Status = protocol.GitDeleted
		case strings.HasPrefix(f[0], "R") && len(f) >= 3:
			gf.Status = protocol.GitRenamed
			gf.From = f[1]
			gf.Path = f[2]
		}
		info.Files = append(info.Files, gf)
	}
	return info, nil
}

// GitRun executes a git operation (commit, push, pull, fetch, checkout, branch,
// stash, discard, clone, etc.) on the remote SSH host.
func GitRun(ctx context.Context, h config.SSHHost, controlPath, remoteHome string, req *protocol.GitRun) (*protocol.GitResult, error) {
	root := ExpandRemotePath(req.GetRoot(), remoteHome)
	var cmd string
	switch req.GetOp() {
	case protocol.GitOpCommit:
		var b strings.Builder
		b.WriteString(remotePathPrelude + "; set -e; ")
		if len(req.GetPaths()) > 0 {
			args := append([]string{"git", "-C", root, "add", "--"}, req.GetPaths()...)
			b.WriteString(ShellJoin(args) + "; ")
		} else {
			fmt.Fprintf(&b, "git -C %s add -A; ", ShellQuote(root))
		}
		cargs := []string{"git", "-C", root, "commit"}
		if req.GetAmend() {
			cargs = append(cargs, "--amend")
		}
		cargs = append(cargs, "-m", req.GetMessage())
		b.WriteString(ShellJoin(cargs))
		cmd = b.String()
	case protocol.GitOpPush:
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "push"})
	case protocol.GitOpPull:
		args := []string{"git", "-C", root, "pull"}
		if req.GetRebase() {
			args = append(args, "--rebase")
		} else {
			args = append(args, "--ff-only")
		}
		cmd = remotePathPrelude + "; " + ShellJoin(args)
	case protocol.GitOpFetch:
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "fetch", "--all", "--prune"})
	case protocol.GitOpCheckout:
		if err := rejectLeadingDash("branch", req.GetBranch()); err != nil {
			return nil, err
		}
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "checkout", req.GetBranch(), "--"})
	case protocol.GitOpBranch:
		if err := rejectLeadingDash("branch", req.GetBranch()); err != nil {
			return nil, err
		}
		if err := rejectLeadingDash("commit", req.GetCommit()); err != nil {
			return nil, err
		}
		args := []string{"git", "-C", root, "checkout", "-b", req.GetBranch()}
		if req.GetCommit() != "" {
			args = append(args, req.GetCommit())
		}
		cmd = remotePathPrelude + "; " + ShellJoin(args)
	case protocol.GitOpStash:
		args := []string{"git", "-C", root, "stash", "push", "-u"}
		if req.GetMessage() != "" {
			args = append(args, "-m", req.GetMessage())
		}
		cmd = remotePathPrelude + "; " + ShellJoin(args)
	case protocol.GitOpStashApply:
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "stash", "apply", fmt.Sprintf("stash@{%d}", req.GetStash())})
	case protocol.GitOpStashPop:
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "stash", "pop", fmt.Sprintf("stash@{%d}", req.GetStash())})
	case protocol.GitOpStashDrop:
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "stash", "drop", fmt.Sprintf("stash@{%d}", req.GetStash())})
	case protocol.GitOpDiscardFiles:
		args := append([]string{"git", "-C", root, "checkout", "--"}, req.GetPaths()...)
		cmd = remotePathPrelude + "; " + ShellJoin(args)
	case protocol.GitOpDeleteBranch:
		if err := rejectLeadingDash("branch", req.GetBranch()); err != nil {
			return nil, err
		}
		flag := "-d"
		if req.GetForce() {
			flag = "-D"
		}
		cmd = remotePathPrelude + "; " + ShellJoin([]string{"git", "-C", root, "branch", flag, "--", req.GetBranch()})
	case protocol.GitOpClone:
		if err := rejectLeadingDash("clone URL", req.GetMessage()); err != nil {
			return nil, err
		}
		cmd = fmt.Sprintf(`%s; mkdir -p -- "$(dirname -- %s)" && git clone -- %s %s`, remotePathPrelude, ShellQuote(root), ShellQuote(req.GetMessage()), ShellQuote(root))
	default:
		return nil, fmt.Errorf("git op %q is not supported in direct SSH mode (install makod on the remote host for full git operations)", req.GetOp())
	}

	out, err := runSSH(ctx, h, controlPath, cmd)
	if err != nil {
		return nil, err
	}
	return &protocol.GitResult{Output: strings.TrimSpace(string(out))}, nil
}
