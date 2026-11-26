package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// host carries out a plugin's calls of the Host API. The dispatcher has
// checked the permission each takes before any method here runs; what is
// checked here is what a permission alone does not say, as the host of a
// URL, a key's size or a setting's name. Its methods run on the plugin's
// goroutine, inside the call into its JavaScript.
type host struct{ p *plugin }

var _ hostapi.Handler = (*host)(nil)

const maxLogLine = 4 << 10

func (h *host) LogWrite(_ context.Context, req *hostapi.WriteRequest) (*hostapi.WriteResponse, error) {
	if !h.p.logRate.take() {
		return &hostapi.WriteResponse{}, nil // the rest of the minute's lines are dropped
	}
	level := map[string]slog.Level{"debug": slog.LevelDebug, "": slog.LevelInfo, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "error": slog.LevelError}
	l, ok := level[req.GetLevel()]
	if !ok {
		return nil, fmt.Errorf("level %q is not debug, info, warn or error", req.GetLevel())
	}
	msg := req.GetMessage()
	if len(msg) > maxLogLine {
		msg = msg[:maxLogLine]
	}
	clean := strings.ToValidUTF8(msg, "?")
	h.p.recordLog(req.GetLevel(), clean)
	h.p.m.cfg.Log.Log(context.Background(), l, "plugin: "+clean, "plugin", h.p.id)
	return &hostapi.WriteResponse{}, nil
}

func (h *host) decl(key string) (SettingDecl, error) {
	v := h.p.current()
	if v != nil {
		for _, s := range v.man.Settings {
			if s.Key == key {
				return s, nil
			}
		}
	}
	return SettingDecl{}, fmt.Errorf("the manifest declares no setting %q", key)
}

func (h *host) SettingsGetSetting(_ context.Context, req *hostapi.GetSettingRequest) (*hostapi.GetSettingResponse, error) {
	d, err := h.decl(req.GetKey())
	if err != nil {
		return nil, err
	}
	set, err := h.p.settings.forHostScoped(d, req.GetScope())
	if err != nil {
		return nil, err
	}
	return &hostapi.GetSettingResponse{Setting: set}, nil
}

func (h *host) SettingsListSettings(_ context.Context, req *hostapi.ListSettingsRequest) (*hostapi.ListSettingsResponse, error) {
	scopeKey := req.GetScope()
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil, err
	}
	out := &hostapi.ListSettingsResponse{}
	if v := h.p.current(); v != nil {
		for _, s := range v.man.Settings {
			set, err := h.p.settings.forHostScoped(s, scopeKey)
			if err != nil {
				return nil, err
			}
			out.Settings = append(out.Settings, set)
		}
	}
	return out, nil
}

func (h *host) StorageGetItem(_ context.Context, req *hostapi.GetItemRequest) (*hostapi.GetItemResponse, error) {
	v, ok, err := h.p.store.getScoped(req.GetScope(), req.GetKey())
	if err != nil {
		return nil, err
	}
	return &hostapi.GetItemResponse{Found: ok, Value: v}, nil
}

func (h *host) StorageSetItem(_ context.Context, req *hostapi.SetItemRequest) (*hostapi.SetItemResponse, error) {
	used, err := h.p.store.setScoped(req.GetScope(), req.GetKey(), req.GetValue())
	if err != nil {
		return nil, err
	}
	return &hostapi.SetItemResponse{Used: uint32(used), Quota: uint32(h.p.m.lim.Storage)}, nil
}

func (h *host) StorageDeleteItem(_ context.Context, req *hostapi.DeleteItemRequest) (*hostapi.DeleteItemResponse, error) {
	found, err := h.p.store.delScoped(req.GetScope(), req.GetKey())
	if err != nil {
		return nil, err
	}
	return &hostapi.DeleteItemResponse{Found: found}, nil
}

func (h *host) StorageListKeys(_ context.Context, req *hostapi.ListKeysRequest) (*hostapi.ListKeysResponse, error) {
	keys, err := h.p.store.keysScoped(req.GetScope(), req.GetPrefix())
	if err != nil {
		return nil, err
	}
	return &hostapi.ListKeysResponse{Keys: keys}, nil
}

// session is a session as a plugin sees it.
func session(s *protocol.SessionInfo) *hostapi.Session {
	return &hostapi.Session{Id: s.GetId(), Name: s.GetName(), Agent: s.GetAgent(), Project: s.GetProject(),
		State: s.GetState(), Status: s.GetStatus(), NodeId: s.GetNodeId()}
}

func (h *host) SessionsListSessions(context.Context, *hostapi.ListSessionsRequest) (*hostapi.ListSessionsResponse, error) {
	out := &hostapi.ListSessionsResponse{}
	for _, s := range h.p.m.cfg.Sessions() {
		out.Sessions = append(out.Sessions, session(s))
	}
	return out, nil
}

func (h *host) SessionsGetSession(_ context.Context, req *hostapi.GetSessionRequest) (*hostapi.GetSessionResponse, error) {
	for _, s := range h.p.m.cfg.Sessions() {
		if s.GetId() == req.GetId() {
			return &hostapi.GetSessionResponse{Found: true, Session: session(s)}, nil
		}
	}
	return &hostapi.GetSessionResponse{}, nil
}

func (h *host) HttpFetch(ctx context.Context, req *hostapi.FetchRequest) (*hostapi.FetchResponse, error) {
	return h.p.m.net.fetch(ctx, h.p.id, pluginGrants{p: h.p}, h.p.netRate, req)
}

// The window's commands a plugin may run.
const (
	cmdOpenURL           = "mako.openUrl"
	cmdToast             = "mako.toast"
	cmdCopyText          = "mako.copyText"
	cmdOpenDiff          = "mako.openDiff"
	cmdOpenFile          = "mako.openFile"
	cmdOpenForge         = "mako.openForge"
	cmdCreatePullRequest = "mako.createPullRequest"
	cmdOpenTab           = "mako.openTab"
	cmdCloseTab          = "mako.closeTab"

	maxCopyText      = 64 << 10
	maxPluginIntents = 8
)

func (h *host) CommandsRunCommand(_ context.Context, req *hostapi.RunCommandRequest) (*hostapi.RunCommandResponse, error) {
	switch req.GetId() {
	case cmdOpenURL:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s opens a link only in answer to the user's tap or command", cmdOpenURL)
		}
		u, err := url.Parse(req.GetArg())
		if err != nil || !slices.Contains([]string{"http", "https"}, u.Scheme) || u.Host == "" || u.User != nil || len(req.GetArg()) > 2048 {
			return nil, fmt.Errorf("%s opens an http or https link, without a user in it", cmdOpenURL)
		}
		h.p.openURL = u.String()
		return &hostapi.RunCommandResponse{}, nil
	case cmdToast:
		msg := strings.TrimSpace(protocol.Printable(req.GetArg()))
		if len([]rune(msg)) > 240 {
			msg = string([]rune(msg)[:240])
		}
		h.p.toast = msg
		return &hostapi.RunCommandResponse{}, nil
	case cmdCopyText:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s copies text only in answer to the user's tap or command", cmdCopyText)
		}
		arg := req.GetArg()
		if len(arg) > maxCopyText || !utf8.ValidString(arg) {
			return nil, fmt.Errorf("%s takes UTF-8 text of at most %d KiB", cmdCopyText, maxCopyText>>10)
		}
		h.p.copyText = arg
		return &hostapi.RunCommandResponse{}, nil
	case cmdOpenDiff:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s runs only in answer to the user's tap or command", cmdOpenDiff)
		}
		if len(h.p.intents) >= maxPluginIntents {
			return nil, fmt.Errorf("at most %d window intents may be queued per gesture", maxPluginIntents)
		}
		var a struct {
			Node    string `json:"node"`
			Folder  string `json:"folder"`
			Repo    string `json:"repo"`
			Path    string `json:"path"`
			From    string `json:"from"`
			Status  string `json:"status"`
			Added   int64  `json:"added"`
			Removed int64  `json:"removed"`
			Binary  bool   `json:"binary"`
			Commit  string `json:"commit"`
			Base    string `json:"base"`
			Preview bool   `json:"preview"`
		}
		if err := json.Unmarshal([]byte(req.GetArg()), &a); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON argument: %w", cmdOpenDiff, err)
		}
		if a.Folder == "" {
			if node, folder, ok := h.activeFolder(); ok {
				if a.Node == "" {
					a.Node = node
				}
				a.Folder = folder
			}
		}
		if err := h.allowIntentPath(a.Node, a.Folder); err != nil {
			return nil, fmt.Errorf("%s: %w", cmdOpenDiff, err)
		}
		if err := checkWorktreeRelPath(a.Path, false); err != nil {
			return nil, fmt.Errorf("%s: path: %w", cmdOpenDiff, err)
		}
		if err := checkWorktreeRelPath(a.From, true); err != nil {
			return nil, fmt.Errorf("%s: from: %w", cmdOpenDiff, err)
		}
		if a.Repo != "" {
			if err := h.allowIntentPath(a.Node, a.Repo); err != nil {
				return nil, fmt.Errorf("%s: repo %q is not allowed", cmdOpenDiff, a.Repo)
			}
		}
		h.p.intents = append(h.p.intents, &protocol.PluginOpen{
			Kind:    "diff",
			Node:    intentNode(a.Node),
			Folder:  a.Folder,
			Repo:    a.Repo,
			Path:    a.Path,
			From:    a.From,
			Status:  a.Status,
			Added:   a.Added,
			Removed: a.Removed,
			Binary:  a.Binary,
			Commit:  a.Commit,
			Base:    a.Base,
			Preview: a.Preview,
		})
		return &hostapi.RunCommandResponse{}, nil
	case cmdOpenFile:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s runs only in answer to the user's tap or command", cmdOpenFile)
		}
		if len(h.p.intents) >= maxPluginIntents {
			return nil, fmt.Errorf("at most %d window intents may be queued per gesture", maxPluginIntents)
		}
		var a struct {
			Node string `json:"node"`
			Path string `json:"path"`
			Line int32  `json:"line"`
		}
		if err := json.Unmarshal([]byte(req.GetArg()), &a); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON argument: %w", cmdOpenFile, err)
		}
		if a.Line < 0 {
			return nil, fmt.Errorf("%s: line must be >= 0", cmdOpenFile)
		}
		if a.Node == "" {
			if node, folder, ok := h.activeFolder(); ok && pathWithinOrEqual(a.Path, folder) {
				a.Node = node
			}
		}
		if err := h.allowIntentPath(a.Node, a.Path); err != nil {
			return nil, fmt.Errorf("%s: %w", cmdOpenFile, err)
		}
		h.p.intents = append(h.p.intents, &protocol.PluginOpen{
			Kind: "file",
			Node: intentNode(a.Node),
			Path: a.Path,
			Line: a.Line,
		})
		return &hostapi.RunCommandResponse{}, nil
	case cmdOpenForge:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s runs only in answer to the user's tap or command", cmdOpenForge)
		}
		if len(h.p.intents) >= maxPluginIntents {
			return nil, fmt.Errorf("at most %d window intents may be queued per gesture", maxPluginIntents)
		}
		var a struct {
			Node   string `json:"node"`
			Folder string `json:"folder"`
			Kind   string `json:"kind"`
			Number int64  `json:"number"`
		}
		if err := json.Unmarshal([]byte(req.GetArg()), &a); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON argument: %w", cmdOpenForge, err)
		}
		if a.Kind != "pr" && a.Kind != "runs" {
			return nil, fmt.Errorf("%s: kind %q must be \"pr\" or \"runs\"", cmdOpenForge, a.Kind)
		}
		if a.Number < 0 {
			return nil, fmt.Errorf("%s: number must be >= 0", cmdOpenForge)
		}
		if a.Folder == "" {
			if node, folder, ok := h.activeFolder(); ok {
				if a.Node == "" {
					a.Node = node
				}
				a.Folder = folder
			}
		}
		if err := h.allowIntentPath(a.Node, a.Folder); err != nil {
			return nil, fmt.Errorf("%s: %w", cmdOpenForge, err)
		}
		h.p.intents = append(h.p.intents, &protocol.PluginOpen{
			Kind:   a.Kind,
			Node:   intentNode(a.Node),
			Folder: a.Folder,
			Number: a.Number,
		})
		return &hostapi.RunCommandResponse{}, nil
	case cmdCreatePullRequest:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s runs only in answer to the user's tap or command", cmdCreatePullRequest)
		}
		if len(h.p.intents) >= maxPluginIntents {
			return nil, fmt.Errorf("at most %d window intents may be queued per gesture", maxPluginIntents)
		}
		var a struct {
			Node   string `json:"node"`
			Folder string `json:"folder"`
		}
		if err := json.Unmarshal([]byte(req.GetArg()), &a); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON argument: %w", cmdCreatePullRequest, err)
		}
		if a.Folder == "" {
			if node, folder, ok := h.activeFolder(); ok {
				if a.Node == "" {
					a.Node = node
				}
				a.Folder = folder
			}
		}
		if err := h.allowIntentPath(a.Node, a.Folder); err != nil {
			return nil, fmt.Errorf("%s: %w", cmdCreatePullRequest, err)
		}
		h.p.intents = append(h.p.intents, &protocol.PluginOpen{
			Kind:   "create_pr",
			Node:   intentNode(a.Node),
			Folder: a.Folder,
		})
		return &hostapi.RunCommandResponse{}, nil
	case cmdOpenTab:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s runs only in answer to the user's tap or command", cmdOpenTab)
		}
		if len(h.p.intents) >= maxPluginIntents {
			return nil, fmt.Errorf("at most %d window intents may be queued per gesture", maxPluginIntents)
		}
		var a struct {
			Block   string          `json:"block"`
			Key     string          `json:"key"`
			Title   string          `json:"title"`
			Icon    string          `json:"icon"`
			Params  json.RawMessage `json:"params"`
			Scope   string          `json:"scope"`
			Preview bool            `json:"preview"`
		}
		if err := json.Unmarshal([]byte(req.GetArg()), &a); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON argument: %w", cmdOpenTab, err)
		}
		decl, ok := h.tabBlockDecl(a.Block)
		if !ok {
			return nil, fmt.Errorf("%s: block %q is not a tab-slotted block of plugin %s", cmdOpenTab, a.Block, h.p.id)
		}
		key := strings.TrimSpace(a.Key)
		if key == "" {
			key = "default"
		}
		if err := validateInstanceID(key); err != nil {
			return nil, fmt.Errorf("%s: %w", cmdOpenTab, err)
		}
		if a.Title != "" {
			if err := checkText("tab title", a.Title, maxTitle, true); err != nil {
				return nil, fmt.Errorf("%s: %w", cmdOpenTab, err)
			}
		}
		icon := strings.TrimSpace(a.Icon)
		if icon != "" && (len(icon) > 48 || !iconNameRe.MatchString(icon)) {
			return nil, fmt.Errorf("%s: icon %q is not an icon's name", cmdOpenTab, icon)
		}
		if icon == "" {
			icon = decl.Icon
		}
		if a.Scope != "" {
			if _, _, err := parseScopeKey(a.Scope); err != nil {
				return nil, fmt.Errorf("%s: %w", cmdOpenTab, err)
			}
		}
		var rawParams string
		if len(a.Params) > 0 && string(a.Params) != "null" {
			var str string
			if err := json.Unmarshal(a.Params, &str); err == nil {
				rawParams = str
			} else {
				rawParams = string(a.Params)
			}
		}
		paramsStr, err := validateTabParams(rawParams)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cmdOpenTab, err)
		}
		title := a.Title
		if title == "" {
			title = decl.Title
		}
		h.p.intents = append(h.p.intents, &protocol.PluginOpen{
			Kind:    "tab",
			Block:   blockName(h.p.id, decl.ID),
			Key:     key,
			Title:   title,
			Icon:    icon,
			Params:  paramsStr,
			Scope:   a.Scope,
			Preview: a.Preview,
		})
		return &hostapi.RunCommandResponse{}, nil
	case cmdCloseTab:
		if !h.p.gesture {
			return nil, fmt.Errorf("%s runs only in answer to the user's tap or command", cmdCloseTab)
		}
		if len(h.p.intents) >= maxPluginIntents {
			return nil, fmt.Errorf("at most %d window intents may be queued per gesture", maxPluginIntents)
		}
		var a struct {
			Block string `json:"block"`
			Key   string `json:"key"`
		}
		if err := json.Unmarshal([]byte(req.GetArg()), &a); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON argument: %w", cmdCloseTab, err)
		}
		decl, ok := h.tabBlockDecl(a.Block)
		if !ok {
			return nil, fmt.Errorf("%s: block %q is not a tab-slotted block of plugin %s", cmdCloseTab, a.Block, h.p.id)
		}
		key := strings.TrimSpace(a.Key)
		if key == "" {
			key = "default"
		}
		if err := validateInstanceID(key); err != nil {
			return nil, fmt.Errorf("%s: %w", cmdCloseTab, err)
		}
		h.p.intents = append(h.p.intents, &protocol.PluginOpen{
			Kind:  "close_tab",
			Block: blockName(h.p.id, decl.ID),
			Key:   key,
		})
		return &hostapi.RunCommandResponse{}, nil
	}
	return nil, fmt.Errorf("command %q is not one a plugin may run", req.GetId())
}

func (h *host) tabBlockDecl(blockID string) (BlockDecl, bool) {
	v := h.p.current()
	if v == nil {
		return BlockDecl{}, false
	}
	for _, b := range v.man.Blocks {
		if b.ID == blockID && slices.Contains(b.Slots, "tab") {
			return b, true
		}
	}
	return BlockDecl{}, false
}

func (h *host) activeFolder() (node, folder string, ok bool) {
	if h.p.activeInstance == "" {
		return "", "", false
	}
	_, scopeKey := splitInstanceKey(h.p.activeInstance)
	return protocol.ParseFolderScope(scopeKey)
}

func (h *host) allowIntentPath(node, p string) error {
	if p == "" || strings.ContainsRune(p, 0) || !utf8.ValidString(p) {
		return errors.New("path must be a non-empty valid UTF-8 path")
	}
	if hasDotDotSegment(p) {
		return fmt.Errorf("path %q must not contain \"..\" segments", p)
	}
	if instNode, instFolder, ok := h.activeFolder(); ok && sameGitNode(node, instNode) && pathWithinOrEqual(p, instFolder) {
		return nil
	}
	if h.p.hasWatchedFolderContaining(node, p) {
		return nil
	}
	if gh := h.p.m.cfg.Git; gh != nil && gh.Allowed(GitTarget{Node: gitHostNode(node), Path: p}) {
		return nil
	}
	return fmt.Errorf("path %q is not within an allowed or watched folder", p)
}

func checkWorktreeRelPath(p string, allowEmpty bool) error {
	if p == "" {
		if allowEmpty {
			return nil
		}
		return errors.New("path is empty")
	}
	if len(p) > maxRelPath || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") || path.IsAbs(p) || hasDotDotSegment(p) {
		return fmt.Errorf("%q is not a valid relative worktree path", p)
	}
	return nil
}

func intentNode(node string) string {
	if node == protocol.LocalScopeNode {
		return ""
	}
	return node
}

// secretKey is the vault's name of a secret the plugin names, checked.
func (h *host) secretKey(name string) (string, error) {
	if !h.p.secRate.take() {
		return "", fmt.Errorf("the plugin called secrets more than %d times a minute", secretsPerMinute)
	}
	if !secretName.MatchString(name) {
		return "", fmt.Errorf("a secret's name is letters, digits and ._-, starting with a letter or digit, at most 64")
	}
	return vaultSecret + name, nil
}

func (h *host) SecretsGetSecret(_ context.Context, req *hostapi.GetSecretRequest) (*hostapi.GetSecretResponse, error) {
	key, err := h.secretKey(req.GetName())
	if err != nil {
		return nil, err
	}
	v, ok, err := h.p.vault.get(key)
	if err != nil {
		return nil, err
	}
	return &hostapi.GetSecretResponse{Found: ok, Value: v}, nil
}

func (h *host) SecretsSetSecret(_ context.Context, req *hostapi.SetSecretRequest) (*hostapi.SetSecretResponse, error) {
	key, err := h.secretKey(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := checkSecret("a secret", req.GetValue()); err != nil {
		return nil, err
	}
	if !h.p.vault.has(key) && len(h.p.vault.list(vaultSecret)) >= maxSecrets {
		return nil, fmt.Errorf("the plugin keeps %d secrets, its most", maxSecrets)
	}
	if err := h.p.vault.set(key, req.GetValue()); err != nil {
		return nil, err
	}
	return &hostapi.SetSecretResponse{}, nil
}

func (h *host) SecretsDeleteSecret(_ context.Context, req *hostapi.DeleteSecretRequest) (*hostapi.DeleteSecretResponse, error) {
	key, err := h.secretKey(req.GetName())
	if err != nil {
		return nil, err
	}
	found, err := h.p.vault.del(key)
	if err != nil {
		return nil, err
	}
	return &hostapi.DeleteSecretResponse{Found: found}, nil
}

func (h *host) SecretsListSecrets(context.Context, *hostapi.ListSecretsRequest) (*hostapi.ListSecretsResponse, error) {
	if !h.p.secRate.take() {
		return nil, fmt.Errorf("the plugin called secrets more than %d times a minute", secretsPerMinute)
	}
	return &hostapi.ListSecretsResponse{Names: h.p.vault.list(vaultSecret)}, nil
}

func (h *host) checkDBRate() error {
	if !h.p.dbRate.take() {
		return fmt.Errorf("the plugin called db more than %d times a minute", dbOpsPerMinute)
	}
	return nil
}

func (h *host) DbQuery(ctx context.Context, req *hostapi.QueryRequest) (*hostapi.QueryResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	return h.p.db.query(ctx, req.GetScope(), req.GetSql(), req.GetParamsJson())
}

func (h *host) DbExec(ctx context.Context, req *hostapi.ExecRequest) (*hostapi.ExecResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	return h.p.db.exec(ctx, req.GetScope(), req.GetSql(), req.GetParamsJson())
}

func (h *host) DbBatch(ctx context.Context, req *hostapi.BatchRequest) (*hostapi.BatchResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	return h.p.db.batch(ctx, req.GetScope(), req.GetStatements())
}

func (h *host) DbStats(ctx context.Context, req *hostapi.StatsRequest) (*hostapi.StatsResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	return h.p.db.stats(ctx, req.GetScope())
}

func (h *host) DbSyncEnable(ctx context.Context, req *hostapi.SyncEnableRequest) (*hostapi.SyncEnableResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	if err := h.p.db.syncEnable(ctx, req.GetScope(), req.GetTable()); err != nil {
		return nil, err
	}
	return &hostapi.SyncEnableResponse{Table: req.GetTable()}, nil
}

func (h *host) DbSyncPut(ctx context.Context, req *hostapi.SyncPutRequest) (*hostapi.SyncPutResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	ts, err := h.p.db.syncPut(ctx, req.GetScope(), req.GetTable(), req.GetId(), req.GetDocJson())
	if err != nil {
		return nil, err
	}
	return &hostapi.SyncPutResponse{UpdatedAt: float64(ts)}, nil
}

func (h *host) DbSyncDelete(ctx context.Context, req *hostapi.SyncDeleteRequest) (*hostapi.SyncDeleteResponse, error) {
	if err := h.checkDBRate(); err != nil {
		return nil, err
	}
	del, err := h.p.db.syncDelete(ctx, req.GetScope(), req.GetTable(), req.GetId())
	if err != nil {
		return nil, err
	}
	return &hostapi.SyncDeleteResponse{Deleted: del}, nil
}

func (h *host) checkDatafsRate() error {
	if !h.p.datafsRate.take() {
		return fmt.Errorf("the plugin called datafs more than %d times a minute", datafsOpsPerMinute)
	}
	return nil
}

func (h *host) DatafsReadData(_ context.Context, req *hostapi.ReadDataRequest) (*hostapi.ReadDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	return h.p.datafs.read(req.GetScope(), req.GetPath(), req.GetOffset(), req.GetLength())
}

func (h *host) DatafsWriteData(_ context.Context, req *hostapi.WriteDataRequest) (*hostapi.WriteDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	return h.p.datafs.write(req.GetScope(), req.GetPath(), req.GetText(), req.GetData(), req.GetMakeDirs())
}

func (h *host) DatafsListData(_ context.Context, req *hostapi.ListDataRequest) (*hostapi.ListDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	return h.p.datafs.list(req.GetScope(), req.GetPath(), req.GetRecursive())
}

func (h *host) DatafsStatData(_ context.Context, req *hostapi.StatDataRequest) (*hostapi.StatDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	return h.p.datafs.stat(req.GetScope(), req.GetPath())
}

func (h *host) DatafsMkdirData(_ context.Context, req *hostapi.MkdirDataRequest) (*hostapi.MkdirDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	if err := h.p.datafs.mkdir(req.GetScope(), req.GetPath()); err != nil {
		return nil, err
	}
	return &hostapi.MkdirDataResponse{}, nil
}

func (h *host) DatafsDeleteData(_ context.Context, req *hostapi.DeleteDataRequest) (*hostapi.DeleteDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	del, err := h.p.datafs.remove(req.GetScope(), req.GetPath(), req.GetRecursive())
	if err != nil {
		return nil, err
	}
	return &hostapi.DeleteDataResponse{Deleted: del}, nil
}

func (h *host) DatafsMoveData(_ context.Context, req *hostapi.MoveDataRequest) (*hostapi.MoveDataResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	if err := h.p.datafs.move(req.GetScope(), req.GetFromPath(), req.GetToPath(), req.GetMakeDirs()); err != nil {
		return nil, err
	}
	return &hostapi.MoveDataResponse{}, nil
}

func (h *host) DatafsDataStats(_ context.Context, req *hostapi.DataStatsRequest) (*hostapi.DataStatsResponse, error) {
	if err := h.checkDatafsRate(); err != nil {
		return nil, err
	}
	return h.p.datafs.stats(req.GetScope())
}
