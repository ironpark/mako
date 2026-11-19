package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

var errDecisionPeer = errors.New("a node's provider keys are its own front-ends' alone")

func (d *Daemon) initDecisionStore(dir string, kc keychain.Keychain, note string) {
	if dir == "" {
		if cfgDir, err := config.Dir(); err == nil {
			dir = cfgDir
		}
	}
	d.decisionsMu.Lock()
	defer d.decisionsMu.Unlock()
	if d.decisions == nil {
		d.decisions = decision.NewStore(dir, kc, note)
	}
}

func (d *Daemon) decisionStore() *decision.Store {
	d.decisionsMu.Lock()
	defer d.decisionsMu.Unlock()
	if d.decisions == nil {
		dir := d.dir
		if dir == "" {
			if cfgDir, err := config.Dir(); err == nil {
				dir = cfgDir
			}
		}
		kc, why := keychain.System()
		note := ""
		if why != nil {
			note = why.Error()
		}
		d.decisions = decision.NewStore(dir, kc, note)
	}
	return d.decisions
}

func (d *Daemon) providersSnapshot() *protocol.Providers {
	store := d.decisionStore()
	cfg := d.decisionConfig()
	statuses := store.Vault.ListStatuses(cfg.Models)
	infos := make([]*protocol.ProviderInfo, len(statuses))
	for i, s := range statuses {
		infos[i] = &protocol.ProviderInfo{
			Id:           s.ID,
			Set:          s.Set,
			EnvSet:       s.EnvSet,
			DefaultModel: s.DefaultModel,
		}
	}
	secrets, note := store.Vault.SecretStore()
	return &protocol.Providers{
		Providers:       infos,
		Secrets:         secrets,
		SecretsNote:     note,
		DefaultProvider: cfg.Provider,
		DefaultModel:    cfg.Model,
	}
}

func (d *Daemon) handleListProviders(_ context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errDecisionPeer
	}
	return stream.Send(protocol.TypeProviders, env.GetId(), d.providersSnapshot())
}

func (d *Daemon) handleProviderAction(_ context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errDecisionPeer
	}
	req, err := protocol.Body[*protocol.ProviderAction](env)
	if err != nil {
		return err
	}
	store := d.decisionStore()
	switch strings.ToLower(strings.TrimSpace(req.GetOp())) {
	case "set_key":
		if err := store.Vault.Set(req.GetProvider(), req.GetValue()); err != nil {
			return err
		}
	case "clear_key":
		if err := store.Vault.Delete(req.GetProvider()); err != nil {
			return err
		}
	case "set_defaults":
		prov := ""
		if raw := strings.TrimSpace(req.GetDefaultProvider()); raw != "" {
			prov = decision.NormalizeProvider(raw)
			if prov == "" {
				return fmt.Errorf("unknown provider %q", raw)
			}
		}
		next := d.decisionConfig()
		next.Provider = prov
		next.Model = strings.TrimSpace(req.GetDefaultModel())
		if pRaw := strings.TrimSpace(req.GetProvider()); pRaw != "" {
			p := decision.NormalizeProvider(pRaw)
			if p == "" {
				return fmt.Errorf("unknown provider %q", pRaw)
			}
			if val := strings.TrimSpace(req.GetValue()); val != "" {
				if next.Models == nil {
					next.Models = map[string]string{}
				}
				next.Models[p] = val
			} else if next.Models != nil {
				delete(next.Models, p)
			}
		}
		if err := d.setDecisionConfig(next); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown provider action %q", req.GetOp())
	}
	return stream.Send(protocol.TypeProviders, env.GetId(), d.providersSnapshot())
}

func (d *Daemon) handleRunDecision(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errDecisionPeer
	}
	req, err := protocol.Body[*protocol.RunDecision](env)
	if err != nil {
		return err
	}
	out, err := d.decisionStore().Evaluate(ctx, []byte(req.GetRequestJson()), d.decisionConfig())
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypeDecisionResult, env.GetId(), &protocol.DecisionResult{
		ResponseJson: string(out),
	})
}
