package transcript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// OpenLogs lists the logs each process tree has open for writing, keyed by
// the pid at the tree's root. One listing covers every tree, so watching many
// sessions spawns no more processes than watching one.
func OpenLogs(ctx context.Context, roots []int) (map[int][]string, error) {
	tree := map[int]int{} // a pid to the root of its tree
	for _, root := range roots {
		tree[root] = root
	}
	if out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output(); err == nil {
		tree = descendants(roots, string(out))
	}
	logs := map[int][]string{}
	if runtime.GOOS == "linux" {
		for pid, root := range tree {
			logs[root] = append(logs[root], procLogs(pid)...)
		}
		return logs, nil
	}
	ids := make([]string, 0, len(tree))
	for pid := range tree {
		ids = append(ids, strconv.Itoa(pid))
	}
	out, err := exec.CommandContext(ctx, "lsof", "-w", "-a", "-p", strings.Join(ids, ","), "-Fpfan").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	for pid, paths := range writablePaths(string(out)) {
		if root, ok := tree[pid]; ok {
			logs[root] = append(logs[root], paths...)
		}
	}
	return logs, nil
}

// procLogs is the logs pid has open for writing, read from /proc.
func procLogs(pid int) []string {
	var paths []string
	fds, _ := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", pid))
	for _, fd := range fds {
		target, err := os.Readlink(fd)
		if err != nil || !strings.HasSuffix(target, ".jsonl") {
			continue
		}
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", pid, filepath.Base(fd)))
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "flags:" {
				flags, _ := strconv.ParseUint(fields[1], 8, 64)
				if flags&3 != 0 {
					paths = append(paths, target)
				}
			}
		}
	}
	return paths
}

// Identify picks, among the logs a process tree has open for writing, the one
// conversation of project. It never guesses from a project's newest file.
func Identify(agent, project string, paths []string) (string, string, error) {
	a, ok := Lookup(agent)
	if !ok {
		return "", "", errUnsupported
	}
	var foundID, foundPath string
	for _, path := range paths {
		if !strings.HasSuffix(path, ".jsonl") {
			continue
		}
		id, ok := logProject(path, a, project)
		if !ok {
			continue
		}
		if foundID != "" && foundID != id {
			return "", "", errors.New("multiple writable conversations")
		}
		foundID, foundPath = id, path
	}
	return foundID, foundPath, nil
}

// writablePaths reads lsof's field output: the files each process has open
// for writing.
func writablePaths(output string) map[int][]string {
	paths := map[int][]string{}
	pid := 0
	writable := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "f"):
			writable = false
		case strings.HasPrefix(line, "a"):
			writable = line == "aw" || line == "au"
		case writable && strings.HasPrefix(line, "n"):
			paths[pid] = append(paths[pid], line[1:])
		}
	}
	return paths
}

// descendants maps every process under roots, the roots included, to the
// root it runs under.
func descendants(roots []int, output string) map[int]int {
	children := map[int][]int{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		parent, _ := strconv.Atoi(fields[1])
		children[parent] = append(children[parent], pid)
	}
	tree := map[int]int{}
	for _, root := range roots {
		queue := []int{root}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			if _, seen := tree[pid]; seen {
				continue
			}
			tree[pid] = root
			queue = append(queue, children[pid]...)
		}
	}
	return tree
}
