package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	gitReadsPerMin   = 120
	gitRunsPerMin    = 30
	forgeReadsPerMin = 30
	maxGitWatches    = 16
	maxGitHistory    = 500
	maxGitOutput     = 64 << 10
	gitRunTimeout    = 2 * time.Minute
)

// gitWatchDebounce coalesces rapid filesystem notifications before reloading a
// watched block instance. It is a var so unit tests can use a short window.
var gitWatchDebounce = 300 * time.Millisecond

// allowedGitOps lists every mutating operation GitService.Run accepts.
// protocol.GitOpDiscard ("discard_hunk") is intentionally omitted (handled by
// the native diff tab).
var allowedGitOps = map[string]bool{
	protocol.GitOpCommit:       true,
	protocol.GitOpPush:         true,
	protocol.GitOpPull:         true,
	protocol.GitOpFetch:        true,
	protocol.GitOpCheckout:     true,
	protocol.GitOpBranch:       true,
	protocol.GitOpMerge:        true,
	protocol.GitOpPR:           true,
	protocol.GitOpRemoveWT:     true,
	protocol.GitOpDiscardFiles: true,
	protocol.GitOpIgnore:       true,
	protocol.GitOpStash:        true,
	protocol.GitOpStashApply:   true,
	protocol.GitOpStashPop:     true,
	protocol.GitOpStashDrop:    true,
	protocol.GitOpTrack:        true,
	protocol.GitOpDeleteBranch: true,
	protocol.GitOpMergeBranch:  true,
	protocol.GitOpRebase:       true,
	protocol.GitOpDetach:       true,
	protocol.GitOpCherryPick:   true,
	protocol.GitOpRevert:       true,
	protocol.GitOpTag:          true,
	protocol.GitOpContinue:     true,
	protocol.GitOpAbort:        true,
}

type gitWatchKey struct {
	instanceKey string
	node        string
	path        string
}

type gitWatch struct {
	cancel context.CancelFunc
	root   string
}

type gitDoneEvent struct {
	Job      string `json:"job"`
	ScopeKey string `json:"scopeKey"`
	Op       string `json:"op"`
	OK       bool   `json:"ok"`
	Output   string `json:"output"`
	Error    string `json:"error"`
}

func canonicalNode(node string) string {
	if node == "" {
		return protocol.LocalScopeNode
	}
	return node
}

func gitHostNode(node string) string {
	if node == protocol.LocalScopeNode {
		return ""
	}
	return node
}

func sameGitNode(a, b string) bool {
	return canonicalNode(a) == canonicalNode(b)
}

func hasDotDotSegment(p string) bool {
	for seg := range strings.SplitSeq(strings.ReplaceAll(p, "\\", "/"), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func cleanSlashPath(p string) string {
	s := strings.ReplaceAll(p, "\\", "/")
	s = path.Clean(s)
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	return s
}

func pathWithinOrEqual(p, base string) bool {
	if p == "" || base == "" || hasDotDotSegment(p) || hasDotDotSegment(base) {
		return false
	}
	cp, cb := cleanSlashPath(p), cleanSlashPath(base)
	if cp == cb {
		return true
	}
	if cb == "/" {
		return strings.HasPrefix(cp, "/")
	}
	return strings.HasPrefix(cp, cb+"/")
}

func (p *plugin) hasWatchedFolder(node, folder string) bool {
	wantFolder := cleanSlashPath(folder)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, in := range p.inst {
		if in.subs <= 0 {
			continue
		}
		n, f, ok := protocol.ParseFolderScope(in.scopeKey)
		if ok && sameGitNode(n, node) && cleanSlashPath(f) == wantFolder {
			return true
		}
	}
	return false
}

func (p *plugin) hasWatchedFolderContaining(node, targetPath string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, in := range p.inst {
		if in.subs <= 0 {
			continue
		}
		n, f, ok := protocol.ParseFolderScope(in.scopeKey)
		if ok && sameGitNode(n, node) && pathWithinOrEqual(targetPath, f) {
			return true
		}
	}
	return false
}

// resolveGitTarget validates and authorizes a GitTarget. A scope target must
// be a canonical folder scope matching an actively watched folder instance of
// this plugin; an explicit node/path target must be permitted by gh.Allowed.
func (h *host) resolveGitTarget(target *hostapi.GitTarget, gh GitHost) (GitTarget, string, error) {
	if target == nil {
		return GitTarget{}, "", errors.New("missing git target")
	}
	if scope := target.GetScope(); scope != "" {
		if _, _, err := parseScopeKey(scope); err != nil {
			return GitTarget{}, "", err
		}
		node, folder, ok := protocol.ParseFolderScope(scope)
		if !ok {
			return GitTarget{}, "", fmt.Errorf("git target scope %q is not a folder scope", scope)
		}
		if hasDotDotSegment(folder) {
			return GitTarget{}, "", fmt.Errorf("git target folder %q must not contain \"..\"", folder)
		}
		if !h.p.hasWatchedFolder(node, folder) {
			return GitTarget{}, "", fmt.Errorf("git target scope %q is not an actively watched folder of this plugin", scope)
		}
		return GitTarget{Node: gitHostNode(node), Path: folder}, scope, nil
	}
	node, folder := target.GetNode(), target.GetPath()
	if folder == "" || strings.ContainsRune(folder, 0) || !utf8.ValidString(folder) {
		return GitTarget{}, "", errors.New("git target requires scope or a valid path")
	}
	if hasDotDotSegment(folder) {
		return GitTarget{}, "", fmt.Errorf("git target path %q must not contain \"..\"", folder)
	}
	gt := GitTarget{Node: gitHostNode(node), Path: folder}
	if gh == nil || !gh.Allowed(gt) {
		return GitTarget{}, "", fmt.Errorf("git target %q on node %q is not allowed", folder, canonicalNode(node))
	}
	return gt, protocol.FolderScope(canonicalNode(node), folder), nil
}

func clampInt32(n int64) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}

func formatProtoTime(ts *timestamppb.Timestamp) string {
	if ts == nil || !ts.IsValid() {
		return ""
	}
	t := ts.AsTime()
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toHostFile(f *protocol.GitFile) *hostapi.GitFileEntry {
	if f == nil {
		return nil
	}
	return &hostapi.GitFileEntry{
		Path:    f.GetPath(),
		From:    f.GetFrom(),
		Status:  f.GetStatus(),
		Added:   clampInt32(f.GetAdded()),
		Removed: clampInt32(f.GetRemoved()),
		Binary:  f.GetBinary(),
	}
}

func toHostCommit(c *protocol.GitCommit) *hostapi.GitCommitEntry {
	if c == nil {
		return nil
	}
	return &hostapi.GitCommitEntry{
		Hash:    c.GetHash(),
		Parents: slices.Clone(c.GetParents()),
		Author:  c.GetAuthor(),
		Time:    formatProtoTime(c.GetTime()),
		Subject: c.GetSubject(),
		Refs:    slices.Clone(c.GetRefs()),
	}
}

func (h *host) GitStatus(ctx context.Context, req *hostapi.GitServiceStatusRequest) (*hostapi.GitServiceStatusResponse, error) {
	gh := h.p.m.cfg.Git
	if gh == nil {
		return nil, ErrGitUnavailable
	}
	if !h.p.gitReadRate.take() {
		return nil, fmt.Errorf("the plugin called git reads more than %d times a minute", gitReadsPerMin)
	}
	t, _, err := h.resolveGitTarget(req.GetTarget(), gh)
	if err != nil {
		return nil, err
	}
	ch, err := gh.Status(ctx, t, req.GetBase())
	if err != nil {
		return nil, err
	}
	resp := &hostapi.GitServiceStatusResponse{
		Root:           ch.GetRoot(),
		Node:           canonicalNode(t.Node),
		Branch:         ch.GetBranch(),
		NotRepo:        ch.GetNotRepo(),
		Upstream:       ch.GetUpstream(),
		Ahead:          clampInt32(ch.GetAhead()),
		Behind:         clampInt32(ch.GetBehind()),
		Branches:       slices.Clone(ch.GetBranches()),
		RemoteBranches: slices.Clone(ch.GetRemoteBranches()),
		Operation:      ch.GetOperation(),
		Base:           req.GetBase(),
	}
	for _, f := range ch.GetFiles() {
		resp.Files = append(resp.Files, toHostFile(f))
	}
	for _, st := range ch.GetStashes() {
		resp.Stashes = append(resp.Stashes, &hostapi.GitStashEntry{
			Index:   clampInt32(st.GetIndex()),
			Message: st.GetMessage(),
			Time:    formatProtoTime(st.GetTime()),
		})
	}
	if wt := ch.GetWorktree(); wt != nil {
		resp.Worktree = &hostapi.GitWorktreeInfo{
			Repo:   wt.GetRepo(),
			Path:   wt.GetPath(),
			Branch: wt.GetBranch(),
			Base:   wt.GetBase(),
			Start:  wt.GetStart(),
		}
	}
	return resp, nil
}

func (h *host) GitHistory(ctx context.Context, req *hostapi.GitServiceHistoryRequest) (*hostapi.GitServiceHistoryResponse, error) {
	gh := h.p.m.cfg.Git
	if gh == nil {
		return nil, ErrGitUnavailable
	}
	if !h.p.gitReadRate.take() {
		return nil, fmt.Errorf("the plugin called git reads more than %d times a minute", gitReadsPerMin)
	}
	t, _, err := h.resolveGitTarget(req.GetTarget(), gh)
	if err != nil {
		return nil, err
	}
	maxCommits := int(req.GetMax())
	if maxCommits <= 0 || maxCommits > maxGitHistory {
		maxCommits = maxGitHistory
	}
	hist, err := gh.Log(ctx, t, maxCommits, req.GetKnown())
	if err != nil {
		return nil, err
	}
	resp := &hostapi.GitServiceHistoryResponse{
		Current:     hist.GetCurrent(),
		More:        hist.GetMore(),
		Fingerprint: hist.GetFingerprint(),
		Unchanged:   hist.GetUnchanged(),
	}
	for _, c := range hist.GetCommits() {
		resp.Commits = append(resp.Commits, toHostCommit(c))
	}
	return resp, nil
}

func (h *host) GitShowCommit(ctx context.Context, req *hostapi.GitServiceShowCommitRequest) (*hostapi.GitServiceShowCommitResponse, error) {
	gh := h.p.m.cfg.Git
	if gh == nil {
		return nil, ErrGitUnavailable
	}
	if !h.p.gitReadRate.take() {
		return nil, fmt.Errorf("the plugin called git reads more than %d times a minute", gitReadsPerMin)
	}
	t, _, err := h.resolveGitTarget(req.GetTarget(), gh)
	if err != nil {
		return nil, err
	}
	if req.GetHash() == "" {
		return nil, errors.New("commit hash is required")
	}
	info, err := gh.Show(ctx, t, req.GetHash())
	if err != nil {
		return nil, err
	}
	resp := &hostapi.GitServiceShowCommitResponse{
		Commit: toHostCommit(info.GetCommit()),
		Email:  info.GetEmail(),
		Body:   info.GetBody(),
	}
	for _, f := range info.GetFiles() {
		resp.Files = append(resp.Files, toHostFile(f))
	}
	return resp, nil
}

func (h *host) GitWatch(_ context.Context, req *hostapi.GitServiceWatchRequest) (*hostapi.GitServiceWatchResponse, error) {
	gh := h.p.m.cfg.Git
	if gh == nil {
		return nil, ErrGitUnavailable
	}
	instKey := h.p.activeInstance
	if instKey == "" {
		return nil, errors.New("git.watch must be called from a block instance")
	}
	t, _, err := h.resolveGitTarget(req.GetTarget(), gh)
	if err != nil {
		return nil, err
	}
	key := gitWatchKey{
		instanceKey: instKey,
		node:        canonicalNode(t.Node),
		path:        cleanSlashPath(t.Path),
	}

	h.p.gitWatchMu.Lock()
	if existing, ok := h.p.gitWatches[key]; ok {
		root := existing.root
		h.p.gitWatchMu.Unlock()
		return &hostapi.GitServiceWatchResponse{Root: root}, nil
	}
	if len(h.p.gitWatches) >= maxGitWatches {
		h.p.gitWatchMu.Unlock()
		return nil, fmt.Errorf("the plugin watches %d repositories, its most", maxGitWatches)
	}
	if !h.p.gitReadRate.take() {
		h.p.gitWatchMu.Unlock()
		return nil, fmt.Errorf("the plugin called git reads more than %d times a minute", gitReadsPerMin)
	}
	wctx, cancel := context.WithCancel(h.p.m.ctx)
	w := &gitWatch{cancel: cancel, root: t.Path}
	h.p.gitWatches[key] = w
	h.p.gitWatchMu.Unlock()

	go h.p.runGitWatch(wctx, key, t, w)
	return &hostapi.GitServiceWatchResponse{Root: w.root}, nil
}

func (h *host) GitUnwatch(_ context.Context, req *hostapi.GitServiceUnwatchRequest) (*hostapi.GitServiceUnwatchResponse, error) {
	instKey := h.p.activeInstance
	if instKey == "" {
		return nil, errors.New("git.unwatch must be called from a block instance")
	}
	target := req.GetTarget()
	if target == nil {
		return nil, errors.New("missing git target")
	}
	var node, folder string
	if scope := target.GetScope(); scope != "" {
		n, f, ok := protocol.ParseFolderScope(scope)
		if !ok {
			return nil, fmt.Errorf("git target scope %q is not a folder scope", scope)
		}
		node, folder = n, f
	} else {
		node, folder = target.GetNode(), target.GetPath()
		if folder == "" {
			return nil, errors.New("git target requires scope or path")
		}
	}
	key := gitWatchKey{
		instanceKey: instKey,
		node:        canonicalNode(node),
		path:        cleanSlashPath(folder),
	}
	h.p.gitWatchMu.Lock()
	if w, ok := h.p.gitWatches[key]; ok {
		delete(h.p.gitWatches, key)
		w.cancel()
	}
	h.p.gitWatchMu.Unlock()
	return &hostapi.GitServiceUnwatchResponse{}, nil
}

func (h *host) GitRun(_ context.Context, req *hostapi.GitServiceRunRequest) (*hostapi.GitServiceRunResponse, error) {
	gh := h.p.m.cfg.Git
	if gh == nil {
		return nil, ErrGitUnavailable
	}
	if !h.p.gesture {
		return nil, errors.New("git.run runs only in answer to a user gesture")
	}
	if !h.p.gitRunRate.take() {
		return nil, fmt.Errorf("the plugin ran git operations more than %d times a minute", gitRunsPerMin)
	}
	op := req.GetOp()
	if !allowedGitOps[op] {
		return nil, fmt.Errorf("git operation %q is not allowed", op)
	}
	t, scopeKey, err := h.resolveGitTarget(req.GetTarget(), gh)
	if err != nil {
		return nil, err
	}
	runReq := &protocol.GitRun{
		NodeId:  t.Node,
		Root:    t.Path,
		Op:      op,
		Message: req.GetMessage(),
		Paths:   slices.Clone(req.GetPaths()),
		Amend:   req.GetAmend(),
		Branch:  req.GetBranch(),
		Commit:  req.GetCommit(),
		Stash:   int64(req.GetStash()),
		Rebase:  req.GetRebase(),
		Force:   req.GetForce(),
		Tag:     req.GetTag(),
	}
	jobID := "g" + nonce()
	jobCtx, cancel := context.WithTimeout(h.p.m.ctx, gitRunTimeout)
	h.p.pendingMu.Lock()
	h.p.gitJobs[jobID] = cancel
	h.p.pendingMu.Unlock()

	go h.p.runGitJob(jobID, scopeKey, t, runReq, req.GetWorktree(), jobCtx, cancel)
	return &hostapi.GitServiceRunResponse{Job: jobID}, nil
}

func (h *host) ForgeBranchStatus(ctx context.Context, req *hostapi.ForgeServiceBranchStatusRequest) (*hostapi.ForgeServiceBranchStatusResponse, error) {
	fh := h.p.m.cfg.Forge
	if fh == nil {
		return nil, ErrForgeUnavailable
	}
	if !h.p.forgeReadRate.take() {
		return nil, fmt.Errorf("the plugin called forge more than %d times a minute", forgeReadsPerMin)
	}
	t, _, err := h.resolveGitTarget(req.GetTarget(), h.p.m.cfg.Git)
	if err != nil {
		return nil, err
	}
	fb, err := fh.BranchStatus(ctx, t, req.GetBranch())
	if err != nil {
		return nil, err
	}
	if fb == nil {
		return &hostapi.ForgeServiceBranchStatusResponse{}, nil
	}
	return &hostapi.ForgeServiceBranchStatusResponse{
		Forge:     fb.Forge,
		HasPr:     fb.HasPR,
		PrNumber:  fb.PRNumber,
		PrTitle:   fb.PRTitle,
		PrState:   fb.PRState,
		PrChecks:  fb.PRChecks,
		Runs:      fb.Runs,
		RunsState: fb.RunsState,
		RunNumber: fb.RunNumber,
		RunHead:   fb.RunHead,
	}, nil
}

func (p *plugin) delGitJob(id string) {
	p.pendingMu.Lock()
	delete(p.gitJobs, id)
	p.pendingMu.Unlock()
}

func (p *plugin) runGitJob(jobID, scopeKey string, t GitTarget, runReq *protocol.GitRun, worktree bool, ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		cancel()
		p.delGitJob(jobID)
	}()
	select {
	case <-p.done:
		return
	default:
	}
	out, err := p.m.cfg.Git.Run(ctx, t, runReq, worktree)
	trimmed, _ := cut(out, maxGitOutput)
	ev := gitDoneEvent{
		Job:      jobID,
		ScopeKey: scopeKey,
		Op:       runReq.GetOp(),
		OK:       err == nil,
		Output:   trimmed,
	}
	if err != nil {
		ev.Error = err.Error()
	}
	select {
	case p.jobs <- func() { p.deliverGitDone(ev) }:
	case <-p.done:
	case <-p.m.ctx.Done():
	}
}

func (p *plugin) deliverGitDone(ev gitDoneEvent) {
	if !p.hasGrant(hostapi.PermGitWrite) || p.ensure() != nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if _, err := p.invoke("gitDone", false, string(b)); err != nil {
		p.m.cfg.Log.Debug("plugin's gitDone handler failed", "plugin", p.id, "op", ev.Op, "err", err)
	}
	p.afterCall(ev.ScopeKey)
}

func (p *plugin) runGitWatch(ctx context.Context, key gitWatchKey, t GitTarget, w *gitWatch) {
	var mu sync.Mutex
	var timer *time.Timer
	trigger := func() {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if timer == nil {
			timer = time.AfterFunc(gitWatchDebounce, func() {
				if ctx.Err() != nil {
					return
				}
				p.scheduleInstanceReload(key.instanceKey)
			})
		} else {
			timer.Reset(gitWatchDebounce)
		}
	}
	defer func() {
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		p.gitWatchMu.Lock()
		if cur, ok := p.gitWatches[key]; ok && cur == w {
			delete(p.gitWatches, key)
		}
		p.gitWatchMu.Unlock()
	}()
	if err := p.m.cfg.Git.Watch(ctx, t, trigger); err != nil && ctx.Err() == nil {
		p.m.cfg.Log.Warn("plugin git watch stopped", "plugin", p.id, "path", t.Path, "err", err)
	}
}

func (p *plugin) scheduleInstanceReload(instKey string) {
	p.mu.Lock()
	in, ok := p.inst[instKey]
	if !ok || in.subs <= 0 {
		p.mu.Unlock()
		return
	}
	if in.loading || in.dirty {
		in.dirty = true
		p.mu.Unlock()
		return
	}
	in.dirty = true
	p.mu.Unlock()
	if !p.post(func() { p.reloadInstance(instKey) }) {
		p.mu.Lock()
		if cur, ok := p.inst[instKey]; ok && cur == in {
			cur.dirty = false
		}
		p.mu.Unlock()
	}
}

func (p *plugin) reloadInstance(instKey string) {
	p.mu.Lock()
	in, ok := p.inst[instKey]
	if !ok || in.subs <= 0 || !in.dirty {
		p.mu.Unlock()
		return
	}
	in.dirty = false
	p.mu.Unlock()
	if err := p.ensure(); err != nil {
		return
	}
	p.load(in)
	if p.rt != nil {
		p.render(instKey)
	}
}

func (p *plugin) stopGitWatchesForInstance(instKey string) bool {
	p.gitWatchMu.Lock()
	var cancels []context.CancelFunc
	for k, w := range p.gitWatches {
		if k.instanceKey == instKey {
			cancels = append(cancels, w.cancel)
			delete(p.gitWatches, k)
		}
	}
	p.gitWatchMu.Unlock()
	for _, c := range cancels {
		c()
	}
	return len(cancels) > 0
}

func (p *plugin) stopAllGitWatches() {
	p.gitWatchMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(p.gitWatches))
	for k, w := range p.gitWatches {
		cancels = append(cancels, w.cancel)
		delete(p.gitWatches, k)
	}
	p.gitWatchMu.Unlock()
	for _, c := range cancels {
		c()
	}
}
