package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

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
		root, rel, err := trustTarget(r.GetPath())
		if err != nil {
			return nil, err
		}
		data, err := repotrust.Read(root, rel)
		if err != nil {
			return nil, err
		}
		sum := repotrust.Hash(data)
		if r.GetOp() == protocol.RepoTrustAllow {
			if r.GetSha256() == "" {
				return nil, errors.New("trusting a file needs the SHA-256 of the content shown")
			}
			if !strings.EqualFold(r.GetSha256(), sum) {
				return nil, fmt.Errorf("%s changed since it was shown; read it again before trusting it", r.GetPath())
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
		return trustedFiles(recs), err
	case protocol.RepoTrustList:
		recs, err := store.List()
		return trustedFiles(recs), err
	}
	return nil, fmt.Errorf("unknown repository trust operation %q", r.GetOp())
}

// trustTarget is the repository a file is of, by the root of the git work
// tree it is in, and the file's path from that root.
func trustTarget(path string) (root, rel string, err error) {
	if err := absolute(path); err != nil {
		return "", "", err
	}
	path = filepath.Clean(path)
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", "", err
	}
	root = gitWorkTreeTop(dir)
	if root == "" {
		return "", "", fmt.Errorf("%s is not in a git work tree", path)
	}
	rel, err = filepath.Rel(root, filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		return "", "", err
	}
	return root, rel, nil
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
