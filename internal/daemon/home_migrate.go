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
	"maps"
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
		j := &migrationJournal{}
		defer j.cleanup()
		previous := make(map[string]savedSession, len(d.saved))
		maps.Copy(previous, d.saved)
		liveUpdated := map[*session.Session]string{}
		rollback := func(err error) error {
			d.saved = previous
			for s, oldPath := range liveUpdated {
				s.SetConversation(s.Info().GetConversationId(), oldPath)
			}
			if rbErr := j.rollback(); rbErr != nil {
				return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
			}
			return err
		}
		copied, moved, err := d.migrateOneAgentLocked(j, liveUpdated, name, isolate, r.GetCopyConfig(), r.GetMoveSessions())
		if err != nil {
			return nil, rollback(err)
		}
		if r.GetMoveSessions() {
			if err := d.saveSessionsLocked(); err != nil {
				return nil, rollback(err)
			}
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
	j := &migrationJournal{}
	defer j.cleanup()
	if isolate {
		base, err := agentspec.IsolatedHomeBase()
		if err != nil {
			return nil, err
		}
		if err := j.mkdirAll(base, 0o700); err != nil {
			return nil, err
		}
	}
	previous := make(map[string]savedSession, len(d.saved))
	maps.Copy(previous, d.saved)
	liveUpdated := map[*session.Session]string{}
	rollback := func(err error) error {
		d.saved = previous
		for s, oldPath := range liveUpdated {
			s.SetConversation(s.Info().GetConversationId(), oldPath)
		}
		if rbErr := j.rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}

	copiedAny := false
	movedTotal := 0
	anyMoveSess := false
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
		if hasSess {
			anyMoveSess = true
		}
		copied, moved, err := d.migrateOneAgentLocked(j, liveUpdated, name, isolate, hasCfg, hasSess)
		if err != nil {
			return nil, rollback(fmt.Errorf("%s: %w", name, err))
		}
		if copied {
			copiedAny = true
		}
		movedTotal += moved
	}
	if anyMoveSess {
		if err := d.saveSessionsLocked(); err != nil {
			return nil, rollback(err)
		}
	}
	return &protocol.AgentHomeMigrated{MovedSessions: int32(movedTotal), CopiedConfig: copiedAny}, nil
}

func (d *Daemon) migrateOneAgentLocked(j *migrationJournal, liveUpdated map[*session.Session]string, name string, isolate, copyCfg, moveSess bool) (bool, int, error) {
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
		if err := j.ensureAgyIsolatedHome(isoRoot); err != nil {
			return false, 0, err
		}
	} else {
		if err := j.mkdirAll(toDir, 0o700); err != nil {
			return false, 0, err
		}
	}
	copied := false
	if copyCfg {
		copied, err = copyAgentConfig(j, name, fromDir, toDir)
		if err != nil {
			return false, 0, fmt.Errorf("copy config: %w", err)
		}
	}
	moved := 0
	if moveSess {
		moved, err = d.moveAgentSessionsLocked(j, liveUpdated, name, fromDir, toDir)
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

func copyAgentConfig(j *migrationJournal, name, fromDir, toDir string) (bool, error) {
	if fromDir == "" || filepath.Clean(fromDir) == filepath.Clean(toDir) {
		return false, nil
	}
	copied := false
	entries := agentConfigEntries(name)
	for _, rel := range entries {
		ok, err := copyPathIfMissing(j, filepath.Join(fromDir, rel), filepath.Join(toDir, rel))
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
					ok, err := copyPathIfMissing(j, filepath.Join(fromDir, e.Name()), filepath.Join(toDir, e.Name()))
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
		ok, err := copyPathIfMissing(j, srcHooks, dstHooks)
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
					ok, err := copyPathIfMissing(j, filepath.Join(fromDir, e.Name()), filepath.Join(toDir, e.Name()))
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

type journalKind uint8

const (
	journalMoved journalKind = iota + 1
	journalReplacedTarget
	journalRemovedDir
	journalCreatedDir
	journalCreatedFile
	journalSnapshotFile
)

type journalEntry struct {
	kind    journalKind
	src     string
	dst     string
	path    string
	backup  string
	mode    fs.FileMode
	existed bool
	data    []byte
}

type migrationJournal struct {
	entries  []journalEntry
	backedUp map[string]bool
}

func (j *migrationJournal) record(e journalEntry) {
	if j == nil {
		return
	}
	j.entries = append(j.entries, e)
}

func (j *migrationJournal) mkdirAll(dir string, perm fs.FileMode) error {
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return nil
	}
	if fi, err := os.Lstat(dir); err == nil {
		if fi.IsDir() {
			return nil
		}
		return os.Mkdir(dir, perm)
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := j.mkdirAll(parent, perm); err != nil {
			return err
		}
	}
	err := os.Mkdir(dir, perm)
	if err == nil {
		j.record(journalEntry{kind: journalCreatedDir, path: dir})
		return nil
	}
	if os.IsExist(err) {
		if fi, statErr := os.Lstat(dir); statErr == nil && fi.IsDir() {
			return nil
		}
	}
	return err
}

func (j *migrationJournal) ensureAgyIsolatedHome(isoRoot string) error {
	isoRoot = filepath.Clean(isoRoot)
	if err := j.mkdirAll(filepath.Dir(isoRoot), 0o700); err != nil {
		return err
	}
	_, isoStatErr := os.Lstat(isoRoot)
	isoExisted := isoStatErr == nil
	preTop := map[string]bool{}
	if isoExisted {
		if entries, err := os.ReadDir(isoRoot); err == nil {
			for _, e := range entries {
				preTop[e.Name()] = true
			}
		}
	}
	geminiSubs := []string{
		".gemini",
		filepath.Join(".gemini", "antigravity-cli"),
		filepath.Join(".gemini", "config"),
		filepath.Join(".gemini", "config", "skills"),
	}
	preGemini := make(map[string]bool, len(geminiSubs))
	for _, sub := range geminiSubs {
		_, err := os.Lstat(filepath.Join(isoRoot, sub))
		preGemini[sub] = err == nil
	}
	skillsDir := filepath.Join(isoRoot, ".gemini", "config", "skills")
	preSkillPaths := map[string]bool{}
	if preGemini[filepath.Join(".gemini", "config", "skills")] {
		_ = filepath.WalkDir(skillsDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			preSkillPaths[p] = true
			if !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
				_ = j.snapshotFile(p)
			}
			return nil
		})
	}

	ensureErr := agentspec.EnsureAgyIsolatedHome(isoRoot)

	if !isoExisted {
		if _, err := os.Lstat(isoRoot); err == nil {
			j.record(journalEntry{kind: journalCreatedDir, path: isoRoot})
		}
	}
	for _, sub := range geminiSubs {
		if !preGemini[sub] {
			p := filepath.Join(isoRoot, sub)
			if _, err := os.Lstat(p); err == nil {
				j.record(journalEntry{kind: journalCreatedDir, path: p})
			}
		}
	}
	if _, err := os.Lstat(skillsDir); err == nil {
		_ = filepath.WalkDir(skillsDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || p == skillsDir || preSkillPaths[p] {
				return nil
			}
			if d.IsDir() {
				j.record(journalEntry{kind: journalCreatedDir, path: p})
			} else {
				j.record(journalEntry{kind: journalCreatedFile, path: p})
			}
			return nil
		})
	}
	if entries, err := os.ReadDir(isoRoot); err == nil {
		for _, e := range entries {
			if e.Name() == ".gemini" || preTop[e.Name()] {
				continue
			}
			p := filepath.Join(isoRoot, e.Name())
			if e.IsDir() {
				j.record(journalEntry{kind: journalCreatedDir, path: p})
			} else {
				j.record(journalEntry{kind: journalCreatedFile, path: p})
			}
		}
	}
	return ensureErr
}

func (j *migrationJournal) move(src, dst string) error {
	return movePathJournaled(j, src, dst)
}

func (j *migrationJournal) snapshotFile(path string) error {
	if j == nil || j.backedUp[path] {
		return nil
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			if j.backedUp == nil {
				j.backedUp = map[string]bool{}
			}
			j.backedUp[path] = true
			j.record(journalEntry{kind: journalSnapshotFile, path: path, existed: false})
			return nil
		}
		return statErr
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if j.backedUp == nil {
		j.backedUp = map[string]bool{}
	}
	j.backedUp[path] = true
	j.record(journalEntry{
		kind:    journalSnapshotFile,
		path:    path,
		existed: true,
		data:    data,
		mode:    info.Mode().Perm(),
	})
	return nil
}

func (j *migrationJournal) cleanup() {
	if j == nil {
		return
	}
	for i := range j.entries {
		if j.entries[i].kind == journalReplacedTarget && j.entries[i].backup != "" {
			_ = os.Remove(j.entries[i].backup)
			j.entries[i].backup = ""
		}
	}
}

func (j *migrationJournal) rollback() error {
	if j == nil {
		return nil
	}
	var errs []error
	for i := len(j.entries) - 1; i >= 0; i-- {
		e := j.entries[i]
		switch e.kind {
		case journalSnapshotFile:
			if !e.existed {
				if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, err)
				}
				continue
			}
			if err := fsutil.Write(e.path, e.data); err != nil {
				errs = append(errs, err)
			} else if e.mode != 0 {
				if err := os.Chmod(e.path, e.mode); err != nil {
					errs = append(errs, err)
				}
			}
		case journalCreatedFile:
			if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		case journalCreatedDir:
			if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		case journalRemovedDir:
			if err := os.MkdirAll(e.path, e.mode); err != nil {
				errs = append(errs, err)
			} else if err := os.Chmod(e.path, e.mode); err != nil {
				errs = append(errs, err)
			}
		case journalMoved:
			if err := movePathRaw(e.dst, e.src); err != nil {
				errs = append(errs, err)
			}
		case journalReplacedTarget:
			if e.backup != "" {
				if err := os.Rename(e.backup, e.dst); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) moveAgentSessionsLocked(j *migrationJournal, liveUpdated map[*session.Session]string, agentName, fromDir, toDir string) (int, error) {
	if filepath.Clean(fromDir) == filepath.Clean(toDir) {
		return 0, nil
	}
	fromClean := filepath.Clean(fromDir)
	moved := 0
	agyConvIDs := map[string]bool{}

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
					if err := j.move(srcBrain, dstBrain); err != nil {
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
					if err := j.move(srcPath, dstPath); err != nil {
						return moved, err
					}
					didMove = true
					newPath = dstPath
					if agentName == "claude" {
						// Move sibling <convID>/ subagents directory if present.
						subDir := strings.TrimSuffix(srcPath, ".jsonl")
						if info, err := os.Stat(subDir); err == nil && info.IsDir() {
							dstSubDir := strings.TrimSuffix(dstPath, ".jsonl")
							if err := j.move(subDir, dstSubDir); err != nil {
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
				if _, seen := liveUpdated[s]; !seen {
					liveUpdated[s] = entry.TranscriptPath
				}
				s.SetConversation(next.Info.GetConversationId(), newPath)
			}
		}
		if didMove {
			moved++
		}
	}

	if agentName == "agy" && len(agyConvIDs) > 0 {
		if err := migrateAgyHistory(j, filepath.Join(fromClean, "history.jsonl"), filepath.Join(toDir, "history.jsonl"), agyConvIDs); err != nil {
			return moved, err
		}
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

func migrateAgyHistory(j *migrationJournal, srcHistory, dstHistory string, convIDs map[string]bool) error {
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

	if err := j.mkdirAll(filepath.Dir(dstHistory), 0o700); err != nil {
		return err
	}
	if err := j.snapshotFile(srcHistory); err != nil {
		return err
	}
	if err := j.snapshotFile(dstHistory); err != nil {
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

func copyPathIfMissing(j *migrationJournal, src, dst string) (bool, error) {
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
		if err := copyMigrateFile(j, src, dst, info.Mode().Perm()); err != nil {
			return false, err
		}
		return true, nil
	}
	copied := false
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
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
			return j.mkdirAll(target, 0o755)
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := copyMigrateFile(j, p, target, fi.Mode().Perm()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		copied = true
		return nil
	})
	return copied, err
}

func copyMigrateFile(j *migrationJournal, src, dst string, mode fs.FileMode) error {
	if err := j.mkdirAll(filepath.Dir(dst), 0o755); err != nil {
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
	j.record(journalEntry{kind: journalCreatedFile, path: dst})
	return nil
}

func stageExistingTarget(dst string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".mako-bak-*")
	if err != nil {
		return "", err
	}
	bak := tmp.Name()
	_ = tmp.Close()
	if err := os.Rename(dst, bak); err != nil {
		_ = os.Remove(bak)
		return "", err
	}
	return bak, nil
}

func movePathJournaled(j *migrationJournal, src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if src == dst {
		return nil
	}
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if dstInfo, err := os.Lstat(dst); err == nil {
		if srcInfo.IsDir() && dstInfo.IsDir() {
			entries, err := os.ReadDir(src)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := movePathJournaled(j, filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
					return err
				}
			}
			if err := os.Remove(src); err != nil {
				return err
			}
			j.record(journalEntry{kind: journalRemovedDir, path: src, mode: srcInfo.Mode().Perm()})
			return nil
		}
		if srcInfo.IsDir() != dstInfo.IsDir() {
			// Return the platform ENOTDIR/EISDIR error without touching dst.
			return os.Rename(src, dst)
		}
		bak, err := stageExistingTarget(dst)
		if err != nil {
			return err
		}
		if err := movePathRaw(src, dst); err != nil {
			_ = os.Rename(bak, dst)
			return err
		}
		j.record(journalEntry{kind: journalReplacedTarget, dst: dst, backup: bak})
		j.record(journalEntry{kind: journalMoved, src: src, dst: dst})
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := j.mkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := movePathRaw(src, dst); err != nil {
		return err
	}
	j.record(journalEntry{kind: journalMoved, src: src, dst: dst})
	return nil
}

func movePathRaw(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(link, dst); err != nil {
			return err
		}
		return os.Remove(src)
	}
	if !info.IsDir() {
		if err := copyFileOverwrite(src, dst, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Remove(src)
	}
	_, dstStatErr := os.Lstat(dst)
	dstExisted := dstStatErr == nil
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
		if !dstExisted {
			_ = os.RemoveAll(dst)
		}
		return err
	}
	return os.RemoveAll(src)
}

func copyFileOverwrite(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".mako-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	keep = true
	return nil
}
