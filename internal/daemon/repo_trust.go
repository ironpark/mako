package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
	"github.com/ironpark/mako/internal/transport"
)

// The command files of repositories the user trusts (internal/repotrust):
// a front-end shows one, and trusts it as the user read it, through the
// daemon of the node the repository is on, which alone keeps them.

func (d *Daemon) handleRepoTrust(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeRepoTrustInfo, func(r *protocol.RepoTrust) (*protocol.RepoTrustInfo, error) {
		if d.isDirectSSH(r.GetNodeId()) {
			return nil, errors.New("a direct SSH host runs no repository's commands, and keeps no trust in them")
		}
		return d.repoTrustLocal(r)
	})
}

// repoTrustLocal answers r with this node's store.
func (d *Daemon) repoTrustLocal(r *protocol.RepoTrust) (*protocol.RepoTrustInfo, error) {
	store := d.repoTrust
	if store == nil {
		return nil, errors.New("this daemon has no configuration directory to keep trust in")
	}
	switch r.GetOp() {
	case protocol.RepoTrustShow, protocol.RepoTrustAllow:
		root, rel, top, err := trustTarget(r.GetPath())
		if err != nil {
			return nil, err
		}
		data, err := repotrust.Read(root, rel)
		if errors.Is(err, fs.ErrNotExist) && top != root {
			return nil, fmt.Errorf("%s is not in %s, the main work tree of its repository, which mako reads it from for every worktree", rel, root)
		}
		if err != nil {
			return nil, err
		}
		sum := repotrust.Hash(data)
		if r.GetOp() == protocol.RepoTrustAllow {
			if r.GetSha256() == "" {
				return nil, errors.New("trusting a file needs the SHA-256 of the content shown")
			}
			if !strings.EqualFold(r.GetSha256(), sum) {
				return nil, &protocol.CodedError{Code: protocol.CodeChanged,
					Message: fmt.Sprintf("%s changed since it was shown; read it again before trusting it", r.GetPath())}
			}
			rec, err := store.Trust(root, rel, sum)
			if err != nil {
				return nil, err
			}
			return &protocol.RepoTrustInfo{Files: []*protocol.TrustedFile{trustedFile(rec)}}, nil
		}
		real, p, err := repotrust.Key(root, rel)
		if err != nil {
			return nil, err
		}
		f := &protocol.TrustedFile{Root: real, Path: p, Sha256: sum, Content: data}
		if rec, ok := store.Lookup(root, rel); ok && rec.SHA256 == sum {
			f.Trusted, f.TrustedAt = true, timestamppb.New(rec.TrustedAt)
		}
		return &protocol.RepoTrustInfo{Files: []*protocol.TrustedFile{f}}, nil
	case protocol.RepoTrustRevoke:
		recs, err := store.Revoke(r.GetPath())
		if err != nil {
			return nil, err
		}
		// A linked worktree's file, or the worktree, stands for the main
		// work tree's, as it does to show and trust.
		if main := mainWorkTreePath(r.GetPath()); main != "" {
			more, err := store.Revoke(main)
			if err != nil {
				return nil, err
			}
			recs = append(recs, more...)
		}
		return trustedFiles(recs), nil
	case protocol.RepoTrustList:
		recs, err := store.List()
		return trustedFiles(recs), err
	}
	return nil, fmt.Errorf("unknown repository trust operation %q", r.GetOp())
}

// trustTarget is the repository a file is of, by its main work tree
// (gitstat.Repo), and the file's path from the top of the work tree it is
// in, which top is. What runs a repository's commands reads its command
// files from its main work tree, for a session in any worktree of it, and
// checks their trust there. So a file of a linked worktree is taken for
// the same file of the main work tree, the one read, shown and trusted.
// A folder whose .git points into a repository that does not list it as
// a worktree is its own repository.
func trustTarget(path string) (root, rel, top string, err error) {
	if err := absolute(path); err != nil {
		return "", "", "", err
	}
	path = filepath.Clean(path)
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", "", "", err
	}
	top, root = gitstat.WorkTree(dir)
	if top == "" {
		return "", "", "", fmt.Errorf("%s is not in a git work tree", path)
	}
	rel, err = filepath.Rel(top, filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		return "", "", "", err
	}
	return root, rel, top, nil
}

// mainWorkTreePath is path, a linked worktree or a file or folder in one,
// as the same of the main work tree of its repository; "" for a path that
// is not in a linked worktree.
func mainWorkTreePath(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	top, root := gitstat.WorkTree(real)
	if top == "" || top == root {
		return ""
	}
	rel, err := filepath.Rel(top, real)
	if err != nil {
		return ""
	}
	return filepath.Join(root, rel)
}

func trustedFile(r repotrust.Record) *protocol.TrustedFile {
	return &protocol.TrustedFile{Root: r.Root, Path: r.Path, Sha256: r.SHA256, Trusted: true, TrustedAt: timestamppb.New(r.TrustedAt)}
}

func trustedFiles(recs []repotrust.Record) *protocol.RepoTrustInfo {
	out := &protocol.RepoTrustInfo{}
	for _, r := range recs {
		out.Files = append(out.Files, trustedFile(r))
	}
	return out
}
