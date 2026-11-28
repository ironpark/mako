package gitstat

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// openRepo opens the repository at dir, as git.PlainOpenWithOptions does
// with EnableDotGitCommonDir and DetectDotGit set to detectDotGit,
// but reads its config as configAt does: go-git refuses a config whole
// for one branch whose upstream is not a branch, as gh pr checkout sets
// (refs/pull/n/head), and every read of the repository with it, where git
// reads the rest.
func openRepo(dir string, detectDotGit bool) (*git.Repository, error) {
	if dir == "" {
		return nil, git.ErrRepositoryNotExists
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	gitDir, top := findGitDir(dir)
	if gitDir == "" {
		return nil, git.ErrRepositoryNotExists
	}
	if !detectDotGit && filepath.Clean(top) != filepath.Clean(dir) {
		return nil, git.ErrRepositoryNotExists
	}
	if _, err := os.Stat(gitDir); err != nil {
		if os.IsNotExist(err) {
			return nil, git.ErrRepositoryNotExists
		}
		return nil, err
	}
	dot := osfs.New(gitDir)
	fs := dot
	common := commonDir(gitDir)
	if filepath.Clean(common) != filepath.Clean(gitDir) {
		if _, err := os.Stat(common); err != nil {
			if os.IsNotExist(err) {
				return nil, git.ErrRepositoryIncomplete
			}
			return nil, err
		}
		fs = dotgit.NewRepositoryFilesystem(dot, osfs.New(common))
	}
	s := &tolerantStorage{Storage: filesystem.NewStorage(fs, cache.NewObjectLRUDefault()), common: common}
	return git.Open(s, osfs.New(top))
}

// tolerantStorage is a repository's storage whose config is read as
// configAt reads it, cached for the lifetime of the open.
type tolerantStorage struct {
	*filesystem.Storage
	common string

	once sync.Once
	cfg  *config.Config
	err  error
}

func (s *tolerantStorage) Config() (*config.Config, error) {
	s.once.Do(func() {
		s.cfg, s.err = s.Storage.Config()
		if s.err != nil {
			if tolerant := configAt(s.common); tolerant != nil {
				s.cfg, s.err = tolerant, nil
			}
		}
	})
	return s.cfg, s.err
}
