package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"

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
	return h.p.m.net.fetch(ctx, h.p.id, h.p.grants(), h.p.netRate, req)
}

// The window's commands a plugin may run.
const (
	cmdOpenURL = "mako.openUrl"
	cmdToast   = "mako.toast"
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
	}
	return nil, fmt.Errorf("command %q is not one a plugin may run", req.GetId())
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
