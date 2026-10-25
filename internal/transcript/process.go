package transcript

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ProcessLog identifies the log actually open for writing by this process or
// its launcher children. It never guesses from a project's newest file.
func ProcessLog(ctx context.Context, pid int, agent, project string) (string, string, error) {
	pids := []int{pid}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output()
	if err == nil {
		pids = descendants(pid, string(out))
	}
	var paths []string
	if runtime.GOOS == "linux" {
		for _, p := range pids {
			fds, _ := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", p))
			for _, fd := range fds {
				target, err := os.Readlink(fd)
				if err != nil || !strings.HasSuffix(target, ".jsonl") {
					continue
				}
				data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", p, filepath.Base(fd)))
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
		}
	} else {
		ids := make([]string, len(pids))
		for i, p := range pids {
			ids[i] = strconv.Itoa(p)
		}
		out, err := exec.CommandContext(ctx, "lsof", "-w", "-a", "-p", strings.Join(ids, ","), "-Ffan").Output()
		if err != nil && len(out) == 0 {
			return "", "", err
		}
		paths = writablePaths(string(out))
	}
	var foundID, foundPath string
	for _, path := range paths {
		if !strings.HasSuffix(path, ".jsonl") {
			continue
		}
		id, ok := logProject(path, agent, project)
		if !ok {
			continue
		}
		if foundID != "" && foundID != id {
			return "", "", fmt.Errorf("multiple writable conversations for process %d", pid)
		}
		foundID, foundPath = id, path
	}
	return foundID, foundPath, nil
}

func writablePaths(output string) []string {
	var paths []string
	writable := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "f") {
			writable = false
		}
		if strings.HasPrefix(line, "a") {
			writable = line == "aw" || line == "au"
		}
		if writable && strings.HasPrefix(line, "n") {
			paths = append(paths, line[1:])
		}
	}
	return paths
}

func descendants(root int, output string) []int {
	parents := map[int]int{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		parent, _ := strconv.Atoi(fields[1])
		parents[pid] = parent
	}
	result := []int{root}
	seen := map[int]bool{root: true}
	for i := 0; i < len(result); i++ {
		for pid, parent := range parents {
			if parent == result[i] && !seen[pid] {
				seen[pid] = true
				result = append(result, pid)
			}
		}
	}
	return result
}
