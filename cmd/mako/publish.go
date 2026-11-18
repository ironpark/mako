package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/testhooks"
)

// A publisher's tools, which need no daemon: they make a publisher key,
// pack and sign a plugin's folder, check a package, and list one in a
// registry's index. The private key stays where keygen wrote it; nothing
// here reads the node's own keys.

// publishOps are the plugins commands that run without a daemon.
var publishOps = map[string]bool{"keygen": true, "pack": true, "sign": true, "verify": true, "index": true}

func publish(op string, args []string) error {
	switch op {
	case "keygen":
		if len(args) != 1 {
			return errors.New("plugins keygen takes the file to write the private key to")
		}
		pub, err := pack.Keygen(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("The private key is in %s: keep it on this machine, readable by you alone.\n", args[0])
		fmt.Printf("public key   %s\nfingerprint  %s\n", pub, pack.Fingerprint(pub))
		return nil
	case "pack", "sign":
		fs := flag.NewFlagSet("plugins "+op, flag.ContinueOnError)
		keyFile := fs.String("key", "", "the publisher's private key, from plugins keygen")
		out := fs.String("o", "", "the package to write (default <id>-<version>.makoplugin here)")
		in, err := parseInterleaved(fs, args)
		if err != nil {
			return err
		}
		if len(in) != 1 || *keyFile == "" {
			return fmt.Errorf("plugins %s takes a %s and -key", op, map[string]string{"pack": "folder", "sign": "package"}[op])
		}
		priv, err := pack.LoadKey(*keyFile)
		if err != nil {
			return err
		}
		var data []byte
		var pkg *pack.Package
		if op == "pack" {
			files, err := pack.ReadFolder(in[0])
			if err != nil {
				return err
			}
			data, pkg, err = pack.Build(files, priv)
			if err != nil {
				return err
			}
		} else {
			raw, err := readPackage(in[0])
			if err != nil {
				return err
			}
			if data, pkg, err = pack.Resign(raw, priv); err != nil {
				return err
			}
		}
		path := *out
		if path == "" {
			path = pkg.Manifest.ID + "-" + pkg.Manifest.Version + ".makoplugin"
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		return describe(path, data, pkg)
	case "verify":
		fs := flag.NewFlagSet("plugins verify", flag.ContinueOnError)
		want := fs.String("key", "", "the publisher key it must be signed by")
		in, err := parseInterleaved(fs, args)
		if err != nil {
			return err
		}
		if len(in) != 1 {
			return errors.New("plugins verify takes a package")
		}
		data, err := readPackage(in[0])
		if err != nil {
			return err
		}
		pkg, err := pack.Open(data)
		if err != nil {
			return err
		}
		if *want != "" {
			if _, err := pack.ParseKey(*want); err != nil {
				return fmt.Errorf("-key: %w", err)
			}
			if !pack.SameKey(pkg.Key, *want) {
				return fmt.Errorf("signed by %s (%s), not by %s", pkg.Key, pack.Fingerprint(pkg.Key), *want)
			}
		}
		return describe(in[0], data, pkg)
	case "index":
		return indexPackage(args)
	}
	return fmt.Errorf("unknown plugins command %q", op)
}

// indexPackage lists a package in a registry's index, in a checkout of
// the registry's repository: the index is made if there is none, and
// written as the registry keeps it, for the change to be reviewed and
// pushed.
func indexPackage(args []string) error {
	fs := flag.NewFlagSet("plugins index", flag.ContinueOnError)
	url := fs.String("url", "", "where the package is downloaded from")
	name := fs.String("name", "", "the plugin's name, as the store shows it")
	description := fs.String("description", "", "a line saying what it does")
	homepage := fs.String("homepage", "", "its homepage")
	registryName := fs.String("registry-name", "", "the registry's name, for a new index")
	in, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(in) != 2 || *url == "" {
		return errors.New("plugins index takes the registry's folder, a package and -url")
	}
	loopback := testhooks.RegistryLoopback()
	data, err := readPackage(in[1])
	if err != nil {
		return err
	}
	pkg, err := pack.Open(data)
	if err != nil {
		return err
	}
	path := filepath.Join(in[0], registry.IndexName)
	idx := &registry.Index{Format: registry.Format, Name: *registryName}
	if b, err := os.ReadFile(path); err == nil {
		if idx, err = registry.Parse(b, loopback); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	man := pkg.Manifest
	e := idx.Find(man.ID)
	if e == nil {
		e = &registry.Entry{ID: man.ID, Publisher: man.Publisher, Key: pkg.Key}
		idx.Plugins = append(idx.Plugins, e)
	}
	if !pack.SameKey(e.Key, pkg.Key) {
		return fmt.Errorf("the index lists %s under the key %s; this package is signed by %s, which no node that installed it takes as an update",
			man.ID, pack.Fingerprint(e.Key), pack.Fingerprint(pkg.Key))
	}
	for _, v := range e.Versions {
		if v.Version == man.Version {
			return fmt.Errorf("the index lists %s %s already: a version once listed is not changed", man.ID, man.Version)
		}
	}
	for _, set := range []struct {
		to   *string
		from string
	}{{&e.Name, *name}, {&e.Description, *description}, {&e.Homepage, *homepage}} {
		if set.from != "" {
			*set.to = set.from
		}
	}
	e.Publisher = man.Publisher
	sum := sha256.Sum256(data)
	perms := man.Permissions
	if perms == nil {
		perms = []string{}
	}
	e.Versions = append(e.Versions, &registry.Version{Version: man.Version, API: man.API, URL: *url,
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Permissions: perms})
	out, err := idx.Marshal(loopback)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s now lists %s %s\n", path, man.ID, man.Version)
	return nil
}

func readPackage(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, pack.MaxPackageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > pack.MaxPackageBytes {
		return nil, fmt.Errorf("%s is over %d MiB", path, pack.MaxPackageBytes>>20)
	}
	return b, nil
}

// describe prints what a package checked is.
func describe(path string, data []byte, pkg *pack.Package) error {
	sum := sha256.Sum256(data)
	fmt.Printf("%s\n  plugin       %s %s, by %s (as it says)\n  permissions  %v\n  key          %s\n  fingerprint  %s\n  contents     %s\n  sha256       %s\n  size         %d\n",
		path, pkg.Manifest.ID, pkg.Manifest.Version, pkg.Manifest.Publisher, pkg.Manifest.Permissions, pkg.Key,
		pack.Fingerprint(pkg.Key), pkg.Digest, hex.EncodeToString(sum[:]), len(data))
	return nil
}

// parseInterleaved parses flags given before, between or after the
// arguments, and returns the arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}
