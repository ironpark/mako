package agent

import (
	"bufio"
	"cmp"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Command is a slash command an agent takes at the start of its prompt.
type Command struct {
	Name        string // without its slash: compact, frontend:review
	Description string
	Args        string // the arguments it takes, as its hint says them
	// Source is where it comes from: "" for the agent's own, else
	// "user", "project", or the plugin's name.
	Source string
}

// Commands is an agent whose prompt takes slash commands.
type Commands interface {
	// Commands are those it takes in a session in project, with the
	// session's environment env: its own, the user's and the project's,
	// and its plugins'.
	Commands(project string, env map[string]string) []Command
}

// maxCommandFile bounds what is read of a command's file for its front
// matter and first line.
const maxCommandFile = 16 << 10

// CommandDir reads the commands of dir, a Markdown file each, named by
// their paths under dir with folders and file joined by colons, after
// prefix: frontend/review.md is prefix+"frontend:review".
func CommandDir(dir, prefix, source string) []Command {
	var out []Command
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(dir, strings.TrimSuffix(path, ".md"))
		meta, first, _ := frontMatter(path)
		out = append(out, Command{
			Name:        prefix + strings.ReplaceAll(filepath.ToSlash(rel), "/", ":"),
			Description: cmp.Or(meta["description"], first),
			Args:        meta["argument-hint"],
			Source:      source,
		})
		return nil
	})
	return out
}

// SkillDir reads the skills of dir, a folder each with its SKILL.md, named
// by the skill's own name, or its folder's, after prefix. A skill marked
// not to be invoked by the user is left out.
func SkillDir(dir, prefix, source string) []Command {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Command
	for _, e := range entries {
		meta, first, ok := frontMatter(filepath.Join(dir, e.Name(), "SKILL.md"))
		if !ok || meta["user-invocable"] == "false" {
			continue
		}
		out = append(out, Command{
			Name:        prefix + cmp.Or(meta["name"], e.Name()),
			Description: cmp.Or(meta["description"], first),
			Args:        meta["argument-hint"],
			Source:      source,
		})
	}
	return out
}

// frontMatter reads the YAML front matter of the Markdown file at path as
// plain keys and values, which is all a command's has, and the first line
// of text after it; ok is false for a file it cannot open.
func frontMatter(path string) (meta map[string]string, first string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxCommandFile))
	meta = map[string]string{}
	in := false
	for i := 0; sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case i == 0 && line == "---":
			in = true
		case in && line == "---":
			in = false
		case in:
			k, v, ok := strings.Cut(line, ":")
			if ok && !strings.HasPrefix(k, " ") {
				meta[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		case line != "":
			return meta, OneLine(strings.TrimLeft(line, "# "), 200), true
		}
	}
	return meta, "", true
}
