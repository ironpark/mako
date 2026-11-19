package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/pkg/fsutil"
	"google.golang.org/protobuf/proto"
)

func (d *Daemon) handleListAgentModels(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeAgentModels, func(r *protocol.ListAgentModels) (*protocol.AgentModels, error) {
		name := r.GetAgent()
		if name == "" {
			return &protocol.AgentModels{}, nil
		}
		configured := d.agents()
		entry, driverOK := agents.Lookup(name)
		if !driverOK {
			return &protocol.AgentModels{}, nil
		}
		callEnv := agentspec.IsolatedHomeEnvForAgent(name, d.isolateHome(), configured)
		if catalog, ok := entry.Driver.(agent.ModelCatalog); ok {
			models, err := catalog.Catalog(ctx, callEnv, r.GetRefresh())
			if err != nil {
				return nil, err
			}
			return &protocol.AgentModels{Models: toProtoModels(models)}, nil
		}
		if m, ok := entry.Driver.(agent.Models); ok {
			efforts := m.Efforts()
			var out []*protocol.AgentModel
			for _, id := range m.Models() {
				out = append(out, &protocol.AgentModel{Id: id, Name: id, Efforts: slices.Clone(efforts)})
			}
			return &protocol.AgentModels{Models: out}, nil
		}
		return &protocol.AgentModels{}, nil
	})
}

func toProtoModels(in []agent.ModelInfo) []*protocol.AgentModel {
	out := make([]*protocol.AgentModel, len(in))
	for i, m := range in {
		out[i] = &protocol.AgentModel{
			Id:       m.ID,
			Name:     m.Name,
			Efforts:  slices.Clone(m.Efforts),
			Overflow: m.Overflow,
		}
	}
	return out
}

func (d *Daemon) handleInspectAgentHome(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeAgentHomeInfo, func(r *protocol.InspectAgentHome) (*protocol.AgentHomeInfo, error) {
		name, isolate := r.GetAgent(), r.GetIsolate()
		if name == "" {
			return d.inspectGlobalHome(isolate)
		}
		if !agentspec.SupportsHomeIsolation(name) {
			return &protocol.AgentHomeInfo{Supported: false}, nil
		}
		fromDir, err := agentspec.AgentDataDir(name, !isolate)
		if err != nil {
			return nil, err
		}
		toDir, err := agentspec.AgentDataDir(name, isolate)
		if err != nil {
			return nil, err
		}
		d.sessionMu.Lock()
		running := d.runningCountLocked(name)
		saved := d.savedCountLocked(name)
		d.sessionMu.Unlock()
		return &protocol.AgentHomeInfo{
			Supported:       true,
			FromDir:         fromDir,
			ToDir:           toDir,
			RunningSessions: int32(running),
			SavedSessions:   int32(saved),
			HasConfig:       hasAgentConfig(name, fromDir),
		}, nil
	})
}

func (d *Daemon) inspectGlobalHome(isolate bool) (*protocol.AgentHomeInfo, error) {
	isoBase, err := agentspec.IsolatedHomeBase()
	if err != nil {
		return nil, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	fromDir, toDir := userHome, isoBase
	if !isolate {
		fromDir, toDir = isoBase, userHome
	}
	targets := agentspec.IsolatableAgents()
	d.sessionMu.Lock()
	running, saved := 0, 0
	for _, name := range targets {
		running += d.runningCountLocked(name)
		saved += d.savedCountLocked(name)
	}
	d.sessionMu.Unlock()

	hasConfig := false
	for _, name := range targets {
		if dir, err := agentspec.AgentDataDir(name, !isolate); err == nil && hasAgentConfig(name, dir) {
			hasConfig = true
			break
		}
	}
	return &protocol.AgentHomeInfo{
		Supported:       len(targets) > 0,
		FromDir:         fromDir,
		ToDir:           toDir,
		RunningSessions: int32(running),
		SavedSessions:   int32(saved),
		HasConfig:       hasConfig,
	}, nil
}

func (d *Daemon) handleMigrateAgentHome(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeAgentHomeMigrated, func(r *protocol.MigrateAgentHome) (*protocol.AgentHomeMigrated, error) {
		name, isolate := r.GetAgent(), r.GetIsolate()
		if name == "" {
			return d.migrateGlobalHome(isolate, r.GetCopyConfig(), r.GetMoveSessions())
		}
		if !agentspec.SupportsHomeIsolation(name) {
			return nil, fmt.Errorf("agent %q does not support home isolation", name)
		}
		d.sessionMu.Lock()
		defer d.sessionMu.Unlock()
		if running := d.runningCountLocked(name); running > 0 {
			return nil, fmt.Errorf("stop %d running %s session(s) before changing its home directory", running, name)
		}
		copied, moved, err := d.migrateOneAgentLocked(name, isolate, r.GetCopyConfig(), r.GetMoveSessions())
		if err != nil {
			return nil, err
		}
		return &protocol.AgentHomeMigrated{MovedSessions: int32(moved), CopiedConfig: copied}, nil
	})
}

func (d *Daemon) migrateGlobalHome(isolate, copyCfg, moveSess bool) (*protocol.AgentHomeMigrated, error) {
	targets := agentspec.IsolatableAgents()
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()

	running := 0
	for _, name := range targets {
		running += d.runningCountLocked(name)
	}
	if running > 0 {
		return nil, fmt.Errorf("stop %d running agent session(s) before changing Mako home", running)
	}
	if isolate {
		base, err := agentspec.IsolatedHomeBase()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(base, 0o700); err != nil {
			return nil, err
		}
	}

	copiedAny := false
	movedTotal := 0
	for _, name := range targets {
		fromDir, err := agentspec.AgentDataDir(name, !isolate)
		if err != nil {
			continue
		}
		hasCfg := copyCfg && hasAgentConfig(name, fromDir)
		hasSess := moveSess && d.savedCountLocked(name) > 0
		if !hasCfg && !hasSess {
			continue
		}
		copied, moved, err := d.migrateOneAgentLocked(name, isolate, hasCfg, hasSess)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if copied {
			copiedAny = true
		}
		movedTotal += moved
	}
	return &protocol.AgentHomeMigrated{MovedSessions: int32(movedTotal), CopiedConfig: copiedAny}, nil
}

func (d *Daemon) migrateOneAgentLocked(name string, isolate, copyCfg, moveSess bool) (bool, int, error) {
	fromDir, err := agentspec.AgentDataDir(name, !isolate)
	if err != nil {
		return false, 0, err
	}
	toDir, err := agentspec.AgentDataDir(name, isolate)
	if err != nil {
		return false, 0, err
	}
	if name == "agy" && isolate {
		isoRoot, err := agentspec.IsolatedHomeDir(name)
		if err != nil {
			return false, 0, err
		}
		if err := agentspec.EnsureAgyIsolatedHome(isoRoot); err != nil {
			return false, 0, err
		}
	} else {
		if err := os.MkdirAll(toDir, 0o700); err != nil {
			return false, 0, err
		}
	}
	copied := false
	if copyCfg {
		copied, err = copyAgentConfig(name, fromDir, toDir)
		if err != nil {
			return false, 0, fmt.Errorf("copy config: %w", err)
		}
	}
	moved := 0
	if moveSess {
		moved, err = d.moveAgentSessionsLocked(name, fromDir, toDir)
		if err != nil {
			return copied, moved, fmt.Errorf("move sessions: %w", err)
		}
	}
	return copied, moved, nil
}

func (d *Daemon) runningCountLocked(agentName string) int {
	n := 0
	for _, info := range d.sessions.list() {
		if info.GetAgent() == agentName && info.GetState() == protocol.StateRunning {
			n++
		}
	}
	return n
}

func (d *Daemon) savedCountLocked(agentName string) int {
	n := 0
	for _, entry := range d.saved {
		if entry.Info.GetAgent() == agentName {
			n++
		}
	}
	return n
}

func agentConfigEntries(name string) []string {
	switch name {
	case "claude":
		return []string{
			"settings.json",
			"settings.local.json",
			".credentials.json",
			"CLAUDE.md",
			"commands",
			"agents",
			"skills",
			filepath.Join("cache", "model-catalog"),
		}
	case "codex":
		return []string{
			"config.toml",
			"auth.json",
			"AGENTS.md",
			"rules",
			"models_cache.json",
		}
	case "agy":
		return []string{"settings.json"}
	default:
		return nil
	}
}

func hasAgentConfig(name, fromDir string) bool {
	if fromDir == "" {
		return false
	}
	for _, rel := range agentConfigEntries(name) {
		if _, err := os.Stat(filepath.Join(fromDir, rel)); err == nil {
			return true
		}
	}
	if name == "agy" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(fromDir), "config", "hooks.json")); err == nil {
			return true
		}
		entries, err := os.ReadDir(fromDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
					return true
				}
			}
		}
		return false
	}
	if len(agentConfigEntries(name)) == 0 {
		entries, err := os.ReadDir(fromDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && isConfigExt(e.Name()) {
					return true
				}
			}
		}
	}
	return false
}

func isConfigExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".json" || ext == ".toml" || ext == ".yaml" || ext == ".yml" || ext == ".md"
}

func copyAgentConfig(name, fromDir, toDir string) (bool, error) {
	if fromDir == "" || filepath.Clean(fromDir) == filepath.Clean(toDir) {
		return false, nil
	}
	copied := false
	entries := agentConfigEntries(name)
	for _, rel := range entries {
		ok, err := copyPathIfMissing(filepath.Join(fromDir, rel), filepath.Join(toDir, rel))
		if err != nil {
			return copied, err
		}
		if ok {
			copied = true
		}
	}
	if name == "agy" {
		if dirEntries, err := os.ReadDir(fromDir); err == nil {
			for _, e := range dirEntries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
					ok, err := copyPathIfMissing(filepath.Join(fromDir, e.Name()), filepath.Join(toDir, e.Name()))
					if err != nil {
						return copied, err
					}
					if ok {
						copied = true
					}
				}
			}
		}
		srcHooks := filepath.Join(filepath.Dir(fromDir), "config", "hooks.json")
		dstHooks := filepath.Join(filepath.Dir(toDir), "config", "hooks.json")
		ok, err := copyPathIfMissing(srcHooks, dstHooks)
		if err != nil {
			return copied, err
		}
		if ok {
			copied = true
		}
		return copied, nil
	}
	if len(entries) == 0 {
		if dirEntries, err := os.ReadDir(fromDir); err == nil {
			for _, e := range dirEntries {
				if !e.IsDir() && isConfigExt(e.Name()) {
					ok, err := copyPathIfMissing(filepath.Join(fromDir, e.Name()), filepath.Join(toDir, e.Name()))
					if err != nil {
						return copied, err
					}
					if ok {
						copied = true
					}
				}
			}
		}
	}
	return copied, nil
}

func (d *Daemon) moveAgentSessionsLocked(agentName, fromDir, toDir string) (int, error) {
	if filepath.Clean(fromDir) == filepath.Clean(toDir) {
		return 0, nil
	}
	fromClean := filepath.Clean(fromDir)
	moved := 0
	agyConvIDs := map[string]bool{}
	previous := make(map[string]savedSession, len(d.saved))
	for id, entry := range d.saved {
		previous[id] = entry
	}
	liveUpdated := map[*session.Session]string{}

	for id, entry := range d.saved {
		if entry.Info.GetAgent() != agentName {
			continue
		}
		convID := entry.Info.GetConversationId()
		srcPath := entry.TranscriptPath
		if srcPath == "" && convID != "" {
			srcPath = locateTranscriptUnder(agentName, fromClean, convID)
		}

		var newPath string
		didMove := false

		if agentName == "agy" {
			if convID == "" && srcPath != "" && withinDir(fromClean, srcPath) {
				convID = agyConvIDFromPath(fromClean, srcPath)
			}
			if convID != "" {
				srcBrain := filepath.Join(fromClean, "brain", convID)
				dstBrain := filepath.Join(toDir, "brain", convID)
				if _, err := os.Stat(srcBrain); err == nil {
					if err := movePath(srcBrain, dstBrain); err != nil {
						return moved, err
					}
					didMove = true
				}
				if didMove || (srcPath != "" && withinDir(fromClean, srcPath)) {
					agyConvIDs[convID] = true
				}
				if srcPath != "" && withinDir(fromClean, srcPath) {
					rel, _ := filepath.Rel(fromClean, srcPath)
					newPath = filepath.Join(toDir, rel)
				}
			}
		} else if srcPath != "" && withinDir(fromClean, srcPath) {
			rel, err := filepath.Rel(fromClean, srcPath)
			if err == nil {
				dstPath := filepath.Join(toDir, rel)
				if _, err := os.Stat(srcPath); err == nil {
					if err := movePath(srcPath, dstPath); err != nil {
						return moved, err
					}
					didMove = true
					newPath = dstPath
					if agentName == "claude" {
						// Move sibling <convID>/ subagents directory if present.
						subDir := strings.TrimSuffix(srcPath, ".jsonl")
						if info, err := os.Stat(subDir); err == nil && info.IsDir() {
							dstSubDir := strings.TrimSuffix(dstPath, ".jsonl")
							if err := movePath(subDir, dstSubDir); err != nil {
								return moved, err
							}
						}
					}
				}
			}
		}

		if newPath != "" && newPath != entry.TranscriptPath {
			next := entry
			next.Info = proto.CloneOf(entry.Info)
			next.TranscriptPath = newPath
			d.saved[id] = next
			if s, ok := d.sessions.get(id); ok {
				liveUpdated[s] = entry.TranscriptPath
				s.SetConversation(next.Info.GetConversationId(), newPath)
			}
		}
		if didMove {
			moved++
		}
	}

	if agentName == "agy" && len(agyConvIDs) > 0 {
		if err := migrateAgyHistory(filepath.Join(fromClean, "history.jsonl"), filepath.Join(toDir, "history.jsonl"), agyConvIDs); err != nil {
			return moved, err
		}
	}

	if err := d.saveSessionsLocked(); err != nil {
		d.saved = previous
		for s, oldPath := range liveUpdated {
			s.SetConversation(s.Info().GetConversationId(), oldPath)
		}
		return moved, err
	}
	return moved, nil
}

func locateTranscriptUnder(agentName, root, convID string) string {
	switch agentName {
	case "claude":
		matches, _ := filepath.Glob(filepath.Join(root, "projects", "*", convID+".jsonl"))
		if len(matches) > 0 {
			return matches[0]
		}
	case "codex":
		matches, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*-"+convID+".jsonl"))
		if len(matches) > 0 {
			return matches[0]
		}
	case "agy":
		p := filepath.Join(root, "brain", convID, ".system_generated", "logs", "transcript_full.jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func agyConvIDFromPath(root, p string) string {
	rel, err := filepath.Rel(filepath.Join(root, "brain"), p)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) > 0 && agent.ValidID.MatchString(parts[0]) {
		return parts[0]
	}
	return ""
}

func migrateAgyHistory(srcHistory, dstHistory string, convIDs map[string]bool) error {
	data, err := os.ReadFile(srcHistory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var kept, moved [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := bytes.Clone(sc.Bytes())
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec struct {
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal(line, &rec) == nil && convIDs[rec.ConversationID] {
			moved = append(moved, line)
		} else {
			kept = append(kept, line)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(moved) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dstHistory), 0o700); err != nil {
		return err
	}
	var dstExisting []byte
	if b, err := os.ReadFile(dstHistory); err == nil {
		dstExisting = b
	} else if !os.IsNotExist(err) {
		return err
	}
	var dstBuf bytes.Buffer
	dstBuf.Write(dstExisting)
	if len(dstExisting) > 0 && dstExisting[len(dstExisting)-1] != '\n' {
		dstBuf.WriteByte('\n')
	}
	for _, line := range moved {
		dstBuf.Write(line)
		dstBuf.WriteByte('\n')
	}
	if err := fsutil.Write(dstHistory, dstBuf.Bytes()); err != nil {
		return err
	}

	var srcBuf bytes.Buffer
	for _, line := range kept {
		srcBuf.Write(line)
		srcBuf.WriteByte('\n')
	}
	return fsutil.Write(srcHistory, srcBuf.Bytes())
}

func withinDir(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyPathIfMissing(src, dst string) (bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		if _, err := os.Lstat(dst); err == nil {
			return false, nil
		}
		if err := copyMigrateFile(src, dst, info.Mode().Perm()); err != nil {
			return false, err
		}
		return true, nil
	}
	copied := false
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyMigrateFile(p, target, fi.Mode().Perm()); err != nil {
			return err
		}
		copied = true
		return nil
	})
	return copied, err
}

func copyMigrateFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dst)
		return errors.Join(copyErr, closeErr)
	}
	return nil
}

func movePath(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if err := copyFileOverwrite(src, dst, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Remove(src)
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFileOverwrite(p, target, fi.Mode().Perm())
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyFileOverwrite(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
