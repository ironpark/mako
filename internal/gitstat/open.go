package gitstat

import (
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// openRepo opens the repository of the work tree dir is in, as
// git.PlainOpenWithOptions does with DetectDotGit and EnableDotGitCommonDir,
// but reads its config as configAt does: go-git refuses a config whole
// for one branch whose upstream is not a branch, as gh pr checkout sets
// (refs/pull/n/head), and every read of the repository with it, where git
// reads the rest.
func openRepo(dir string) (*git.Repository, error) {
	gitDir, top := findGitDir(dir)
	if gitDir == "" {
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
	if common := commonDir(gitDir); filepath.Clean(common) != filepath.Clean(gitDir) {
		fs = dotgit.NewRepositoryFilesystem(dot, osfs.New(common))
	}
	s := &tolerantStorage{Storage: filesystem.NewStorage(fs, cache.NewObjectLRUDefault()), common: commonDir(gitDir)}
	return git.Open(s, osfs.New(top))
}

// tolerantStorage is a repository's storage whose config is read as
// configAt reads it.
type tolerantStorage struct {
	*filesystem.Storage
	common string
}

func (s *tolerantStorage) Config() (*config.Config, error) {
	cfg, err := s.Storage.Config()
	if err == nil {
		return cfg, nil
	}
	if tolerant := configAt(s.common); tolerant != nil {
		return tolerant, nil
	}
	return nil, err
}
