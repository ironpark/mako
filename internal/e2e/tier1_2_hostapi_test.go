//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

// ---------------------------------------------------------------------------
// File-local helpers (prefixed with hostTest to avoid package e2e collisions)
// ---------------------------------------------------------------------------

func hostTestManifest(id string, perms []string, extra string) string {
	quoted := make([]string, len(perms))
	for i, p := range perms {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return fmt.Sprintf(`id = %q
version = "0.1.0"
publisher = "e2e-test"
api = 1
entry = "main.js"
permissions = [%s]

[[contributes.block]]
id = "ui"
title = "Host API Test"

[[contributes.command]]
id = "go"
title = "Run Host API Test"
%s`, id, strings.Join(quoted, ", "), extra)
}

func hostTestInstallAndWatch(t *testing.T, r *PluginRig, id string, perms []string, js string) *TreeWatcher {
	t.Helper()
	r.InstallJS(t, id, hostTestManifest(id, perms, ""), js)
	if len(perms) > 0 {
		r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
			return p.GetState() == protocol.PluginWaiting
		})
		r.Grant(t, id)
	}
	return r.WatchTree(t, "plugin:"+id+"/ui", "", plugin.View{})
}

func hostTestDaemonInstallAndWatch(t *testing.T, r *DaemonRig, id string, perms []string, js string) *TreeWatcher {
	t.Helper()
	r.InstallPlugin(t, id, hostTestManifest(id, perms, ""), js)
	if len(perms) > 0 {
		r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
			return p.GetState() == protocol.PluginWaiting
		})
		r.Grant(t, id)
	}
	return r.WatchBlockView(t, "plugin:"+id+"/ui", "", "en", false)
}

func hostTestNewP2PMeshPair(t *testing.T, nameA, nameB string) (*DaemonRig, *DaemonRig) {
	t.Helper()
	rigA := NewDaemonRig(t, DaemonRigOpts{
		Name:      nameA,
		EnableP2P: true,
	})
	rigB := NewDaemonRig(t, DaemonRigOpts{
		Name:      nameB,
		EnableP2P: true,
		Peers:     []string{rigA.PeerAddr()},
	})
	rigA.LetIn(t, rigB)
	rigB.LetIn(t, rigA)

	idA := rigA.Daemon.Self().GetId()
	idB := rigB.Daemon.Self().GetId()

	WaitFor(t, defaultWaitTimeout, "bidirectional trusted P2P link", func() bool {
		aSeesB := false
		for _, n := range rigA.Nodes(t) {
			if n.GetId() == idB && n.GetTrusted() && n.GetTrustsUs() && n.GetLink() == protocol.LinkUp {
				aSeesB = true
				break
			}
		}
		bSeesA := false
		for _, n := range rigB.Nodes(t) {
			if n.GetId() == idA && n.GetTrusted() && n.GetTrustsUs() && n.GetLink() == protocol.LinkUp {
				bSeesA = true
				break
			}
		}
		return aSeesB && bSeesA
	})
	return rigA, rigB
}

func hostTestWaitDaemonSession(t *testing.T, r *DaemonRig, sessionID string) *protocol.SessionInfo {
	t.Helper()
	var found *protocol.SessionInfo
	WaitFor(t, defaultWaitTimeout, fmt.Sprintf("session %s on daemon %s", sessionID, r.Daemon.Self().GetName()), func() bool {
		for _, s := range r.ListSessions(t) {
			if s.GetId() == sessionID {
				found = s
				return true
			}
		}
		return false
	})
	return found
}

func hostTestWriteFakeClaude(t *testing.T) func(*config.Config) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "claude")
	body := "#!/bin/sh\ncase \"$1\" in plugin|--help|--version|-h|-v) exit 0;; esac\nsleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}
	return func(cfg *config.Config) {
		cfg.Agents = map[string]agentspec.Agent{
			"claude": {Command: []string{script}},
		}
	}
}

func hostTestProtoField(msg proto.Message, names ...string) (protoreflect.FieldDescriptor, protoreflect.Value, bool) {
	if msg == nil {
		return nil, protoreflect.Value{}, false
	}
	ref := msg.ProtoReflect()
	fields := ref.Descriptor().Fields()
	for _, name := range names {
		if fd := fields.ByName(protoreflect.Name(name)); fd != nil {
			return fd, ref.Get(fd), true
		}
	}
	return nil, protoreflect.Value{}, false
}

func hostTestProtoString(msg proto.Message, names ...string) (string, bool) {
	fd, val, ok := hostTestProtoField(msg, names...)
	if !ok || fd.Kind() != protoreflect.StringKind {
		return "", false
	}
	return val.String(), true
}

func hostTestProtoBool(msg proto.Message, names ...string) (bool, bool) {
	fd, val, ok := hostTestProtoField(msg, names...)
	if !ok || fd.Kind() != protoreflect.BoolKind {
		return false, false
	}
	return val.Bool(), true
}

func hostTestProtoFloat(msg proto.Message, names ...string) (float64, bool) {
	fd, val, ok := hostTestProtoField(msg, names...)
	if !ok {
		return 0, false
	}
	switch fd.Kind() {
	case protoreflect.DoubleKind, protoreflect.FloatKind:
		return val.Float(), true
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return float64(val.Int()), true
	default:
		return 0, false
	}
}

func hostTestProtoBadges(msg proto.Message) ([]string, bool) {
	fd, val, ok := hostTestProtoField(msg, "badges")
	if !ok || !fd.IsList() {
		return nil, false
	}
	list := val.List()
	out := make([]string, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		elem := list.Get(i)
		switch fd.Kind() {
		case protoreflect.StringKind:
			out = append(out, elem.String())
		case protoreflect.MessageKind:
			m := elem.Message()
			if tfd := m.Descriptor().Fields().ByName("text"); tfd != nil {
				out = append(out, m.Get(tfd).String())
			}
		}
	}
	return out, true
}

func hostTestStructStringField(v any, names ...string) (string, bool) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return "", false
	}
	for _, name := range names {
		f := rv.FieldByName(name)
		if f.IsValid() && f.Kind() == reflect.String {
			return f.String(), true
		}
	}
	return "", false
}

func hostTestFindGraphNode(g *protocol.UiNode, id string) *protocol.UiGraphNode {
	if g == nil {
		return nil
	}
	for _, gn := range g.GetGraphNodes() {
		if gn.GetId() == id {
			return gn
		}
	}
	return nil
}

func hostTestHasGraphEdge(g *protocol.UiNode, from, to string) bool {
	if g == nil {
		return false
	}
	for _, ge := range g.GetGraphEdges() {
		if ge.GetFrom() == from && ge.GetTo() == to {
			return true
		}
	}
	return false
}

func hostTestFindContextMenu(root *protocol.UiNode, key string) *protocol.UiNode {
	if key != "" {
		if n := FindNodeByKey(root, key); n != nil && n.GetKind() == "menu" {
			return n
		}
	}
	for _, m := range FindAllNodesByKind(root, "menu") {
		if m.GetVariant() == "context" {
			return m
		}
	}
	return nil
}

// ===========================================================================
// TIER 1: F2.1 — Session Host API Enrichment (>= 5 tests)
// ===========================================================================

func TestHostAPI_T1_F2_1_ParentSessionAndNodeExposed(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:              "child-sess-1",
				Name:            "Subagent Worker",
				Agent:           "claude",
				Project:         "/work/mako",
				State:           protocol.StateRunning,
				Status:          protocol.AgentWorking,
				NodeId:          "node-beta",
				ParentSessionId: "parent-sess-0",
				ParentNodeId:    "node-alpha",
			},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "pending" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const one = await mako.sessions.getSession({ id: "child-sess-1" });
        const a = (list.sessions || [])[0] || {};
        const b = one.session || {};
        s.out = JSON.stringify({
          listParent: a.parentSessionId,
          listParentNode: a.parentNodeId,
          listNode: a.nodeId,
          getParent: b.parentSessionId,
          getParentNode: b.parentNodeId,
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f21-parent", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"listParent":"parent-sess-0"`) &&
			HasTreeText(n, `"listParentNode":"node-alpha"`) &&
			HasTreeText(n, `"listNode":"node-beta"`) &&
			HasTreeText(n, `"getParent":"parent-sess-0"`) &&
			HasTreeText(n, `"getParentNode":"node-alpha"`)
	})
}

func TestHostAPI_T1_F2_1_BranchExposed(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:      "s-branch",
				Name:    "Branch Runner",
				Agent:   "codex",
				Project: "/work/repo",
				State:   protocol.StateRunning,
				Status:  protocol.AgentIdle,
				Branch:  "feat/hostapi-v2",
			},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const one = await mako.sessions.getSession({ id: "s-branch" });
        s.out = list.sessions[0].branch + "|" + one.session.branch;
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f21-branch", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "feat/hostapi-v2|feat/hostapi-v2")
	})
}

func TestHostAPI_T1_F2_1_ProfileExposed(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:      "s-prof",
				Name:    "Profiled Agent",
				Agent:   "claude",
				Profile: "architect",
				Project: "/work/repo",
				State:   protocol.StateRunning,
				Status:  protocol.AgentWorking,
			},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const one = await mako.sessions.getSession({ id: "s-prof" });
        s.out = list.sessions[0].profile + "|" + one.session.profile;
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f21-profile", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "architect|architect")
	})
}

func TestHostAPI_T1_F2_1_ApprovalToolExposed(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:      "s-appr",
				Name:    "Waiting Session",
				Agent:   "claude",
				Project: "/work/repo",
				State:   protocol.StateRunning,
				Status:  protocol.AgentWaiting,
				Approval: &protocol.Approval{
					Id:     "appr-1",
					Tool:   "Bash",
					Detail: "rm -rf build",
				},
			},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const one = await mako.sessions.getSession({ id: "s-appr" });
        s.out = list.sessions[0].approvalTool + "|" + one.session.approvalTool;
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f21-approval", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "Bash|Bash")
	})
}

func TestHostAPI_T1_F2_1_TimestampsUnixMsExposed(t *testing.T) {
	tStart := time.UnixMilli(1700000000123)
	tStatus := time.UnixMilli(1700000005000)
	tOutput := time.UnixMilli(1700000010000)
	tMsg := time.UnixMilli(1700000025456)

	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:          "s-ts",
				Name:        "Timed Session",
				Agent:       "claude",
				Project:     "/work/repo",
				State:       protocol.StateRunning,
				Status:      protocol.AgentWorking,
				StartedAt:   timestamppb.New(tStart),
				StatusAt:    timestamppb.New(tStatus),
				LastOutput:  timestamppb.New(tOutput),
				LastMessage: timestamppb.New(tMsg),
			},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const one = await mako.sessions.getSession({ id: "s-ts" });
        const a = list.sessions[0];
        const b = one.session;
        s.out = JSON.stringify({
          typesOk: typeof a.startedAt === "number" && typeof a.lastActivityAt === "number" &&
                   typeof b.startedAt === "number" && typeof b.lastActivityAt === "number",
          listStarted: a.startedAt,
          listLast: a.lastActivityAt,
          getStarted: b.startedAt,
          getLast: b.lastActivityAt,
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f21-ts", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"typesOk":true`) &&
			HasTreeText(n, `"listStarted":1700000000123`) &&
			HasTreeText(n, `"listLast":1700000025456`) &&
			HasTreeText(n, `"getStarted":1700000000123`) &&
			HasTreeText(n, `"getLast":1700000025456`)
	})
}

func TestHostAPI_T1_F2_1_LastActivityDerivedFromLatestTimestamp(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:         "s-out",
				Agent:      "claude",
				State:      protocol.StateRunning,
				StartedAt:  timestamppb.New(time.UnixMilli(1700000001000)),
				StatusAt:   timestamppb.New(time.UnixMilli(1700000002000)),
				LastOutput: timestamppb.New(time.UnixMilli(1700000009000)),
			},
			{
				Id:        "s-status",
				Agent:     "claude",
				State:     protocol.StateRunning,
				StartedAt: timestamppb.New(time.UnixMilli(1700000001000)),
				StatusAt:  timestamppb.New(time.UnixMilli(1700000007000)),
			},
			{
				Id:        "s-start-only",
				Agent:     "claude",
				State:     protocol.StateRunning,
				StartedAt: timestamppb.New(time.UnixMilli(1700000003000)),
			},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.sessions.listSessions();
        const byId = Object.fromEntries((res.sessions || []).map((x) => [x.id, x.lastActivityAt]));
        s.out = JSON.stringify(byId);
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f21-max-ts", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"s-out":1700000009000`) &&
			HasTreeText(n, `"s-status":1700000007000`) &&
			HasTreeText(n, `"s-start-only":1700000003000`)
	})
}

// ===========================================================================
// TIER 1: F2.2 — mesh Host API Exposure (>= 5 tests)
// ===========================================================================

func TestHostAPI_T1_F2_2_ListNodesSelfMetadata(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{
		Name:      "mac-studio-e2e",
		EnableP2P: false,
	})
	self := rig.Daemon.Self()

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listNodes();
        const nodes = res.nodes || [];
        const me = nodes.find((n) => n.self === true) || {};
        s.out = JSON.stringify({
          count: nodes.length,
          id: me.id,
          name: me.name,
          os: me.os,
          arch: me.arch,
          self: me.self,
          hasProjectsArr: Array.isArray(me.projects),
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "f22-self", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, fmt.Sprintf(`"id":%q`, self.GetId())) &&
			HasTreeText(n, `"name":"mac-studio-e2e"`) &&
			HasTreeText(n, fmt.Sprintf(`"os":%q`, runtime.GOOS)) &&
			HasTreeText(n, fmt.Sprintf(`"arch":%q`, runtime.GOARCH)) &&
			HasTreeText(n, `"self":true`) &&
			HasTreeText(n, `"hasProjectsArr":true`)
	})
}

func TestHostAPI_T1_F2_2_TwoNodeP2PPeerVisibility(t *testing.T) {
	rigA, rigB := hostTestNewP2PMeshPair(t, "alpha-mac", "beta-linux")
	idB := rigB.Daemon.Self().GetId()

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listNodes();
        const nodes = res.nodes || [];
        const peer = nodes.find((n) => n.id === ` + fmt.Sprintf("%q", idB) + `) || {};
        s.out = JSON.stringify({
          total: nodes.length,
          id: peer.id,
          name: peer.name,
          self: peer.self,
          trusted: peer.trusted,
          link: peer.link,
          os: peer.os,
          arch: peer.arch,
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rigA, "f22-peer", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, fmt.Sprintf(`"id":%q`, idB)) &&
			HasTreeText(n, `"name":"beta-linux"`) &&
			HasTreeText(n, `"self":false`) &&
			HasTreeText(n, `"trusted":true`) &&
			HasTreeText(n, `"link":"up"`)
	})
}

func TestHostAPI_T1_F2_2_MeshListSessionsLocalAndRemote(t *testing.T) {
	rigA, rigB := hostTestNewP2PMeshPair(t, "mesh-a", "mesh-b")
	sessLocal := rigA.OpenShellSession(t, "")
	sessRemote := rigB.OpenShellSession(t, "")

	hostTestWaitDaemonSession(t, rigA, sessLocal.GetId())
	hostTestWaitDaemonSession(t, rigA, sessRemote.GetId())

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listSessions();
        const byId = Object.fromEntries((res.sessions || []).map((x) => [x.id, x.nodeId]));
        s.out = JSON.stringify(byId);
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rigA, "f22-sessions", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, fmt.Sprintf(`%q:%q`, sessLocal.GetId(), rigA.Daemon.Self().GetId())) &&
			HasTreeText(n, fmt.Sprintf(`%q:%q`, sessRemote.GetId(), rigB.Daemon.Self().GetId()))
	})
}

func TestHostAPI_T1_F2_2_SessionsReadPermitsMeshRead(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "perm-node", EnableP2P: false})
	rig.OpenShellSession(t, "")

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const n = await mako.mesh.listNodes();
        const ms = await mako.mesh.listSessions();
        s.out = ` + "`ok:${(n.nodes || []).length}:${(ms.sessions || []).length}`" + `;
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "f22-perm", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "ok:1:1")
	})
}

func TestHostAPI_T1_F2_2_NodeProjectsMatchSetProjects(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "proj-node", EnableP2P: false})
	projDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	if _, err := rig.Client.SetProjects(ctx, &protocol.SetProjects{
		Projects: []*protocol.ProjectScope{
			{Id: "proj-mako", Name: "Mako Core", Paths: []string{projDir}},
			{Id: "proj-docs", Name: "Mako Docs", Paths: []string{t.TempDir()}},
		},
	}); err != nil {
		t.Fatalf("SetProjects: %v", err)
	}

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listNodes();
        const me = (res.nodes || []).find((n) => n.self) || {};
        s.out = JSON.stringify(me.projects || []);
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "f22-proj", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"id":"proj-mako"`) &&
			HasTreeText(n, `"name":"Mako Core"`) &&
			HasTreeText(n, `"id":"proj-docs"`) &&
			HasTreeText(n, `"name":"Mako Docs"`)
	})
}

func TestHostAPI_T1_F2_2_PeerProjectsPropagatedOverMesh(t *testing.T) {
	rigA, rigB := hostTestNewP2PMeshPair(t, "proj-alpha", "proj-beta")
	remoteDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	if _, err := rigB.Client.SetProjects(ctx, &protocol.SetProjects{
		Projects: []*protocol.ProjectScope{
			{Id: "proj-remote", Name: "Remote Engine", Paths: []string{remoteDir}},
		},
	}); err != nil {
		t.Fatalf("rigB SetProjects: %v", err)
	}

	idB := rigB.Daemon.Self().GetId()
	WaitFor(t, defaultWaitTimeout, "peer projects propagated to rigA", func() bool {
		for _, n := range rigA.Nodes(t) {
			if n.GetId() == idB {
				for _, p := range n.GetProjects() {
					if p.GetId() == "proj-remote" {
						return true
					}
				}
			}
		}
		return false
	})

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listNodes();
        const peer = (res.nodes || []).find((n) => n.id === ` + fmt.Sprintf("%q", idB) + `) || {};
        s.out = JSON.stringify(peer.projects || []);
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rigA, "f22-peer-proj", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"id":"proj-remote"`) && HasTreeText(n, `"name":"Remote Engine"`)
	})
}

// ===========================================================================
// TIER 1: F2.3 — Host Session Action Support (>= 5 tests)
// ===========================================================================

func TestHostAPI_T1_F2_3_StartAgentRecordsParentSessionIdInPluginRig(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "sess-parent-1", Name: "Coordinator", Agent: "claude", Project: "/proj", State: protocol.StateRunning},
		},
		Projects: []plugin.ProjectRoots{
			{ID: "p1", Name: "Proj", Roots: []string{"/proj"}},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { status: "idle" },
      render: (s, ui) => ui.column([
        ui.text(s.status),
        ui.button("Spawn", { key: "spawn", onTap: ["spawn"] }),
      ]),
      on: {
        async spawn(s) {
          const resp = await mako.messages.startAgent({
            agent: "claude",
            project: "p1",
            root: "/proj",
            prompt: "analyze failing tests",
            name: "Sub Worker",
            parentSessionId: "sess-parent-1",
          });
          s.status = "asked:" + resp.requestId;
        },
      },
    });
  },
  on: {
    agentStarted(ev) {
      mako.blocks.state("ui").status = "started:" + ev.session.id;
      mako.blocks.reload("ui");
    },
  },
};`
	w := hostTestInstallAndWatch(t, r, "f23-spawn", []string{"sessions:write", "sessions:read"}, js)
	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "spawn") != nil
	})
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-spawn/ui",
		Node:  FindNodeByKey(tree, "spawn").GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("spawn tap: %v", err)
	}
	info := r.WaitPlugin(t, "f23-spawn", func(p *protocol.PluginInfo) bool {
		return p.GetAgentAsk() != nil
	})
	if _, err := r.Act(t, &protocol.PluginAction{
		PluginId: "f23-spawn",
		Op:       protocol.PluginAllowAgent,
		Key:      info.GetAgentAsk().GetId(),
		Value:    protocol.AgentOnce,
	}); err != nil {
		t.Fatalf("allow agent: %v", err)
	}
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "started:e2e-agent-1")
	})

	started := r.Agents.Started()
	if len(started) != 1 {
		t.Fatalf("expected 1 started agent, got %+v", started)
	}
	parentID, ok := hostTestStructStringField(started[0].Req, "ParentSessionID", "ParentSessionId")
	if !ok || parentID != "sess-parent-1" {
		t.Fatalf("expected AgentStart.ParentSessionID = %q, got (%q, ok=%v)", "sess-parent-1", parentID, ok)
	}
}

func TestHostAPI_T1_F2_3_StartAgentRecordsParentSessionIdInDaemon(t *testing.T) {
	configure := hostTestWriteFakeClaude(t)
	rig := NewDaemonRig(t, DaemonRigOpts{
		Name:      "spawn-daemon",
		EnableP2P: false,
		Configure: configure,
	})
	projDir := t.TempDir()
	parent := rig.OpenShellSession(t, projDir)

	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	if _, err := rig.Client.SetProjects(ctx, &protocol.SetProjects{
		Projects: []*protocol.ProjectScope{
			{Id: "p-sub", Name: "Sub Proj", Paths: []string{projDir}},
		},
	}); err != nil {
		t.Fatalf("SetProjects: %v", err)
	}

	js := fmt.Sprintf(`let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { msg: "ready" },
      render: (s, ui) => ui.column([
        ui.text(s.msg),
        ui.button("Spawn", { key: "spawn", onTap: ["spawn"] }),
      ]),
      on: {
        async spawn(s) {
          const r = await mako.messages.startAgent({
            agent: "claude",
            project: "p-sub",
            root: %q,
            prompt: "inspect subagent",
            name: "Child Agent",
            parentSessionId: %q,
          });
          s.msg = "pending:" + r.requestId;
        },
      },
    });
  },
  on: {
    agentStarted(ev) {
      mako.blocks.state("ui").msg = "child:" + ev.session.id;
      mako.blocks.reload("ui");
    },
  },
};`, projDir, parent.GetId())

	w := hostTestDaemonInstallAndWatch(t, rig, "f23-dspawn", []string{"sessions:write", "sessions:read"}, js)
	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "spawn") != nil
	})
	if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-dspawn/ui",
		Node:  FindNodeByKey(tree, "spawn").GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("spawn tap: %v", err)
	}
	info := rig.WaitPlugin(t, "f23-dspawn", func(p *protocol.PluginInfo) bool {
		return p.GetAgentAsk() != nil
	})
	if _, err := rig.Client.PluginAction(ctx, &protocol.PluginAction{
		PluginId: "f23-dspawn",
		Op:       protocol.PluginAllowAgent,
		Key:      info.GetAgentAsk().GetId(),
		Value:    protocol.AgentOnce,
	}); err != nil {
		t.Fatalf("allow agent: %v", err)
	}
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "child:")
	})

	var child *protocol.SessionInfo
	WaitFor(t, defaultWaitTimeout, "child session listed with parent_session_id", func() bool {
		for _, s := range rig.ListSessions(t) {
			if s.GetName() == "Child Agent" {
				child = s
				return true
			}
		}
		return false
	})
	t.Cleanup(func() {
		kctx, kcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer kcancel()
		_ = rig.Client.Kill(kctx, &protocol.CloseSession{SessionId: child.GetId()})
	})
	if child.GetParentSessionId() != parent.GetId() {
		t.Fatalf("daemon child session ParentSessionId = %q, want %q", child.GetParentSessionId(), parent.GetId())
	}
}

func TestHostAPI_T1_F2_3_SendMessageDeliversText(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "target-s1", Name: "Target", Agent: "claude", State: protocol.StateRunning},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      render: (s, ui) => ui.column([
        ui.text(s.out),
        ui.button("Send", { key: "send", onTap: ["send"] }),
      ]),
      on: {
        async send(s) {
          const res = await mako.messages.sendMessage({ sessionId: "target-s1", text: "run go test ./..." });
          s.out = "sent:" + res.id;
        },
      },
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f23-send", []string{"sessions:write"}, js)
	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "send") != nil
	})
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-send/ui",
		Node:  FindNodeByKey(tree, "send").GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("send tap: %v", err)
	}
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "sent:1")
	})
	sent := r.Agents.Sent()
	if len(sent) != 1 || sent[0].Session != "target-s1" || sent[0].Text != "run go test ./..." {
		t.Fatalf("unexpected sent messages: %+v", sent)
	}
}

func TestHostAPI_T1_F2_3_StopAgentStopsPluginOwnedSession(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Projects: []plugin.ProjectRoots{
			{ID: "p1", Name: "Proj", Roots: []string{"/proj"}},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { sid: "", status: "init" },
      render: (s, ui) => ui.column([
        ui.text(s.status),
        ui.button("Start", { key: "start", onTap: ["start"] }),
        ui.button("Stop", { key: "stop", onTap: ["stop"] }),
      ]),
      on: {
        async start() {
          await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/proj", prompt: "work" });
        },
        async stop(s) {
          await mako.messages.stopAgent({ sessionId: s.sid });
          s.status = "stopped:" + s.sid;
        },
      },
    });
  },
  on: {
    agentStarted(ev) {
      const st = mako.blocks.state("ui");
      st.sid = ev.session.id;
      st.status = "running:" + ev.session.id;
      mako.blocks.reload("ui");
    },
  },
};`
	w := hostTestInstallAndWatch(t, r, "f23-stop", []string{"sessions:write"}, js)
	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "start") != nil
	})
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-stop/ui",
		Node:  FindNodeByKey(tree, "start").GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("start tap: %v", err)
	}
	info := r.WaitPlugin(t, "f23-stop", func(p *protocol.PluginInfo) bool {
		return p.GetAgentAsk() != nil
	})
	if _, err := r.Act(t, &protocol.PluginAction{
		PluginId: "f23-stop",
		Op:       protocol.PluginAllowAgent,
		Key:      info.GetAgentAsk().GetId(),
		Value:    protocol.AgentOnce,
	}); err != nil {
		t.Fatalf("allow agent: %v", err)
	}
	tree = w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "running:e2e-agent-1")
	})
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-stop/ui",
		Node:  FindNodeByKey(tree, "stop").GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("stop tap: %v", err)
	}
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "stopped:e2e-agent-1")
	})
	stopped := r.Agents.Stopped()
	if len(stopped) != 1 || stopped[0].Session != "e2e-agent-1" || r.Agents.Running("f23-stop") != 0 {
		t.Fatalf("expected e2e-agent-1 stopped and running=0, got stopped=%+v running=%d", stopped, r.Agents.Running("f23-stop"))
	}
}

func TestHostAPI_T1_F2_3_OpenSessionProducesPluginOpenIntent(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-open-1", Name: "Main Session", Agent: "claude", NodeId: "e2e-node", State: protocol.StateRunning},
		},
	})
	js := `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: {},
      render: (s, ui) => ui.column([ui.button("Open", { key: "open", onTap: ["open", "s-open-1"] })]),
      on: {
        async open(_s, sid) {
          const ctx = m.blocks.context("ui");
          await ctx.ui.openSession(sid);
        },
      },
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f23-open", []string{"sessions:read"}, js)
	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "open") != nil
	})
	done, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-open/ui",
		Node:  FindNodeByKey(tree, "open").GetId(),
		Kind:  protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("open tap: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "session" || done.GetOpen()[0].GetKey() != "s-open-1" {
		t.Fatalf("unexpected open intents: %+v", done.GetOpen())
	}
}

func TestHostAPI_T1_F2_3_OpenSessionRemoteNodeTargeting(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "remote-s-99", Name: "Linux Worker", Agent: "codex", NodeId: "linux-worker-2", State: protocol.StateRunning},
		},
	})
	js := `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: {},
      render: (s, ui) => ui.column([ui.button("Open Remote", { key: "open-rem", onTap: ["openRemote"] })]),
      on: {
        async openRemote() {
          const ctx = m.blocks.context("ui");
          await ctx.ui.openSession({ sessionId: "remote-s-99", node: "linux-worker-2" });
        },
      },
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "f23-open-rem", []string{"sessions:read"}, js)
	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "open-rem") != nil
	})
	done, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:f23-open-rem/ui",
		Node:  FindNodeByKey(tree, "open-rem").GetId(),
		Kind:  protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("open remote tap: %v", err)
	}
	if len(done.GetOpen()) != 1 ||
		done.GetOpen()[0].GetKind() != "session" ||
		done.GetOpen()[0].GetKey() != "remote-s-99" ||
		done.GetOpen()[0].GetNode() != "linux-worker-2" {
		t.Fatalf("unexpected remote openSession intent: %+v", done.GetOpen())
	}
}

// ===========================================================================
// TIER 1: F2.4 — agent-map Plugin Update (>= 5 tests)
// ===========================================================================

func TestHostAPI_T1_F2_4_AgentMapRendersRichCardsAndMinimap(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-1", Name: "Architect", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac-studio"},
			{Id: "s-2", Name: "Reviewer", Agent: "codex", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentIdle, NodeId: "mac-studio"},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-1") != nil
	})
	g := FindNodeByKind(tree, "graph")
	if g.GetVariant() != "cards" {
		t.Fatalf("expected agent-map ui.graph variant %q, got %q", "cards", g.GetVariant())
	}
	minimap, ok := hostTestProtoBool(g, "minimap")
	if !ok || !minimap {
		t.Fatalf("expected agent-map ui.graph minimap=true, got (%v, ok=%v)", minimap, ok)
	}
	sn := hostTestFindGraphNode(g, "sess:s-1")
	shape, ok := hostTestProtoString(sn, "shape")
	if !ok || shape != "card" {
		t.Fatalf("expected session graph node shape=%q, got (%q, ok=%v)", "card", shape, ok)
	}
}

func TestHostAPI_T1_F2_4_AgentMapMultiMachineTopologyAndClusterHulls(t *testing.T) {
	rigA, rigB := hostTestNewP2PMeshPair(t, "mac-studio", "linux-builder")
	dirA := t.TempDir()
	dirB := t.TempDir()
	sA := rigA.OpenShellSession(t, dirA)
	sB := rigB.OpenShellSession(t, dirB)
	hostTestWaitDaemonSession(t, rigA, sA.GetId())
	hostTestWaitDaemonSession(t, rigA, sB.GetId())

	rigA.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	rigA.Grant(t, "agent-map")
	w := rigA.WatchBlockView(t, "plugin:agent-map/map", "", "en", false)

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		if g == nil {
			return false
		}
		return hostTestFindGraphNode(g, "sess:"+sA.GetId()) != nil &&
			hostTestFindGraphNode(g, "sess:"+sB.GetId()) != nil
	})
	g := FindNodeByKind(tree, "graph")
	// Verify cluster hulls (non-empty groups) and hierarchy edges exist for multi-machine topology.
	groups := map[string]bool{}
	for _, gn := range g.GetGraphNodes() {
		if gn.GetGroup() != "" {
			groups[gn.GetGroup()] = true
		}
	}
	if len(groups) < 2 {
		t.Fatalf("expected >= 2 cluster hull groups across two machines/projects, got %v", groups)
	}
	var sawMachineA, sawMachineB bool
	for _, gn := range g.GetGraphNodes() {
		lbl := strings.ToLower(gn.GetLabel() + " " + gn.GetId() + " " + gn.GetGroup())
		if strings.Contains(lbl, "mac-studio") || strings.Contains(lbl, strings.ToLower(rigA.Daemon.Self().GetId())) {
			sawMachineA = true
		}
		if strings.Contains(lbl, "linux-builder") || strings.Contains(lbl, strings.ToLower(rigB.Daemon.Self().GetId())) {
			sawMachineB = true
		}
	}
	if !sawMachineA || !sawMachineB {
		t.Fatalf("expected both machine names/ids in multi-machine topology (mac=%v, linux=%v), nodes=%+v", sawMachineA, sawMachineB, g.GetGraphNodes())
	}
	if len(g.GetGraphEdges()) < 2 {
		t.Fatalf("expected hierarchy edges across machines/projects/sessions, got %+v", g.GetGraphEdges())
	}
}

func TestHostAPI_T1_F2_4_AgentMapParentSubagentEdges(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-parent", Name: "Lead Agent", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac"},
			{Id: "s-child", Name: "Subagent 1", Agent: "codex", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac", ParentSessionId: "s-parent"},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-child") != nil
	})
	g := FindNodeByKind(tree, "graph")
	if !hostTestHasGraphEdge(g, "sess:s-parent", "sess:s-child") {
		t.Fatalf("expected parent->subagent edge sess:s-parent -> sess:s-child, edges=%+v", g.GetGraphEdges())
	}
}

func TestHostAPI_T1_F2_4_AgentMapBranchProfileAndApprovalBeacon(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:       "s-appr",
				Name:     "Deploy Agent",
				Agent:    "claude",
				Profile:  "reviewer",
				Branch:   "feat/mesh-v2",
				Project:  "/work/mako",
				State:    protocol.StateRunning,
				Status:   protocol.AgentWaiting,
				NodeId:   "mac",
				Approval: &protocol.Approval{Id: "ap-1", Tool: "Write"},
			},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-appr") != nil
	})
	g := FindNodeByKind(tree, "graph")
	sn := hostTestFindGraphNode(g, "sess:s-appr")

	if sn.GetTone() != "warning" {
		t.Fatalf("expected waiting approval session tone=%q, got %q", "warning", sn.GetTone())
	}
	status, ok := hostTestProtoString(sn, "status")
	if !ok || status != "waiting" {
		t.Fatalf("expected waiting session graph node status=%q for warning beacon, got (%q, ok=%v)", "waiting", status, ok)
	}
	badges, hasBadges := hostTestProtoBadges(sn)
	joined := sn.GetSubtitle() + " " + strings.Join(badges, " ")
	if !hasBadges || len(badges) == 0 {
		t.Fatalf("expected non-empty badges on enriched session graph node, got badges=%v (hasField=%v)", badges, hasBadges)
	}
	for _, want := range []string{"feat/mesh-v2", "reviewer", "Write"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in session node subtitle/badges (%q), badges=%v", want, joined, badges)
		}
	}
}

func TestHostAPI_T1_F2_4_AgentMapDragDropPersistsAndDoubleTapOpensSession(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-drag-1", Name: "Draggable Agent", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac"},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-drag-1") != nil
	})
	g := FindNodeByKind(tree, "graph")

	// Drop node at new coordinates -> agent-map persists to ctx.storage.
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  g.GetId(),
		Kind:  protocol.UiDrop,
		Value: `{"id":"sess:s-drag-1","x":320,"y":180}`,
	}); err != nil {
		t.Fatalf("graph drop event: %v", err)
	}

	// Verify coordinates reflected on graph node, and survive plugin reload via storage.
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		ng := FindNodeByKind(n, "graph")
		sn := hostTestFindGraphNode(ng, "sess:s-drag-1")
		x, okX := hostTestProtoFloat(sn, "x")
		y, okY := hostTestProtoFloat(sn, "y")
		return okX && okY && x == 320 && y == 180
	})

	// Double-tap node -> emits PluginOpen{Kind: "session", Key: "s-drag-1"}.
	done, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  g.GetId(),
		Kind:  protocol.UiDoubleTap,
		Value: "sess:s-drag-1",
	})
	if err != nil {
		t.Fatalf("graph double_tap event: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "session" || done.GetOpen()[0].GetKey() != "s-drag-1" {
		t.Fatalf("expected double_tap openSession intent for s-drag-1, got %+v", done.GetOpen())
	}
}

func TestHostAPI_T1_F2_4_AgentMapContextMenuActions(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-ctx-1", Name: "Context Agent", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentIdle, NodeId: "mac"},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-ctx-1") != nil
	})
	g := FindNodeByKind(tree, "graph")
	menu := hostTestFindContextMenu(tree, g.GetContextMenu())
	if menu == nil {
		t.Fatalf("expected context menu node in agent-map tree (graph contextMenu=%q)", g.GetContextMenu())
	}
	items := FindAllNodesByKind(menu, "menu_item")
	if len(items) == 0 {
		t.Fatalf("expected menu_item children in context menu %q", menu.GetId())
	}

	var openItem *protocol.UiNode
	for _, it := range items {
		low := strings.ToLower(it.GetId() + " " + it.GetText())
		if strings.Contains(low, "open") || strings.Contains(it.GetText(), "열기") {
			openItem = it
			break
		}
	}
	if openItem == nil {
		t.Fatalf("expected an open-session item in context menu, items=%+v", items)
	}
	done, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  openItem.GetId(),
		Kind:  protocol.UiTap,
		Value: "sess:s-ctx-1",
	})
	if err != nil {
		t.Fatalf("context menu tap: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "session" || done.GetOpen()[0].GetKey() != "s-ctx-1" {
		t.Fatalf("expected context menu openSession intent for s-ctx-1, got %+v", done.GetOpen())
	}
}

// ===========================================================================
// TIER 2: F2.1 — Session Host API Boundary & Negative (>= 5 tests)
// ===========================================================================

func TestHostAPI_T2_F2_1_NoApprovalYieldsEmptyApprovalTool(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-no-appr", Name: "Plain", Agent: "shell", State: protocol.StateRunning, Approval: nil},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const one = await mako.sessions.getSession({ id: "s-no-appr" });
        s.out = JSON.stringify({
          listEmpty: list.sessions[0].approvalTool === "",
          getEmpty: one.session.approvalTool === "",
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f21-appr", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"listEmpty":true`) && HasTreeText(n, `"getEmpty":true`)
	})
}

func TestHostAPI_T2_F2_1_RootSessionHasEmptyParentSessionId(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-root", Name: "Root", Agent: "claude", State: protocol.StateRunning},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const x = list.sessions[0];
        s.out = JSON.stringify({
          parentEmpty: x.parentSessionId === "",
          parentNodeEmpty: x.parentNodeId === "",
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f21-root", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"parentEmpty":true`) && HasTreeText(n, `"parentNodeEmpty":true`)
	})
}

func TestHostAPI_T2_F2_1_ZeroTimestampsReturnZeroWithoutNaN(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-zero-ts", Name: "UnsetTimes", Agent: "shell", State: protocol.StateStopped},
		},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const x = list.sessions[0];
        s.out = JSON.stringify({
          startedZero: x.startedAt === 0 && !Number.isNaN(x.startedAt),
          lastZero: x.lastActivityAt === 0 && !Number.isNaN(x.lastActivityAt),
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f21-zerots", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"startedZero":true`) && HasTreeText(n, `"lastZero":true`)
	})
}

func TestHostAPI_T2_F2_1_GetSessionMissingHandledCleanly(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        try {
          const res = await mako.sessions.getSession({ id: "missing-session-id" });
          s.out = (!res.found && !res.session) ? "clean-missing" : "unexpected-found";
        } catch (e) {
          s.out = "clean-err:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f21-missing", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "clean-missing") || HasTreeText(n, "clean-err:")
	})
}

func TestHostAPI_T2_F2_1_DeniedWithoutSessionsReadPermission(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{{Id: "s1", State: protocol.StateRunning}},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        try {
          await mako.sessions.listSessions();
          s.out = "unexpected-allow";
        } catch (e) {
          s.out = "denied:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f21-deny", []string{"storage"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "denied:") && HasTreeText(n, "sessions:read")
	})
}

// ===========================================================================
// TIER 2: F2.2 — mesh Host API Boundary & Negative (>= 5 tests)
// ===========================================================================

func TestHostAPI_T2_F2_2_DeniedWithoutSessionsReadPermission(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "t2-mesh-deny", EnableP2P: false})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        try {
          await mako.mesh.listNodes();
          s.out = "unexpected-allow";
        } catch (e) {
          s.out = "denied:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "t2-f22-deny", []string{"storage"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "denied:") && HasTreeText(n, "not granted")
	})
}

func TestHostAPI_T2_F2_2_UntrustedPeerExcludedFromMeshSessions(t *testing.T) {
	rigA := NewDaemonRig(t, DaemonRigOpts{Name: "untrusted-a", EnableP2P: true})
	rigB := NewDaemonRig(t, DaemonRigOpts{Name: "untrusted-b", EnableP2P: true, Peers: []string{rigA.PeerAddr()}})
	// Intentionally do NOT call LetIn on either side.
	remoteSess := rigB.OpenShellSession(t, "")
	hostTestWaitDaemonSession(t, rigB, remoteSess.GetId())

	idB := rigB.Daemon.Self().GetId()
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const nodesRes = await mako.mesh.listNodes();
        const sessRes = await mako.mesh.listSessions();
        const peer = (nodesRes.nodes || []).find((n) => n.id === ` + fmt.Sprintf("%q", idB) + `);
        const leaked = (sessRes.sessions || []).some((x) => x.id === ` + fmt.Sprintf("%q", remoteSess.GetId()) + `);
        s.out = JSON.stringify({
          leaked,
          peerTrusted: peer ? Boolean(peer.trusted) : false,
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rigA, "t2-f22-untrusted", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"leaked":false`) && HasTreeText(n, `"peerTrusted":false`)
	})
}

func TestHostAPI_T2_F2_2_ZeroProjectsReturnsEmptyArray(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "no-proj-node", EnableP2P: false})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listNodes();
        const me = (res.nodes || []).find((n) => n.self) || {};
        s.out = JSON.stringify({
          isArr: Array.isArray(me.projects),
          len: Array.isArray(me.projects) ? me.projects.length : -1,
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "t2-f22-noproj", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"isArr":true`) && HasTreeText(n, `"len":0`)
	})
}

func TestHostAPI_T2_F2_2_ZeroPeersMeshListSessionsReturnsLocalOnly(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "solo-node", EnableP2P: false})
	localSess := rig.OpenShellSession(t, "")
	hostTestWaitDaemonSession(t, rig, localSess.GetId())

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const nodesRes = await mako.mesh.listNodes();
        const sessRes = await mako.mesh.listSessions();
        s.out = JSON.stringify({
          nodeCount: (nodesRes.nodes || []).length,
          sessCount: (sessRes.sessions || []).length,
          firstId: ((sessRes.sessions || [])[0] || {}).id || "",
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "t2-f22-solo", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"nodeCount":1`) &&
			HasTreeText(n, `"sessCount":1`) &&
			HasTreeText(n, fmt.Sprintf(`"firstId":%q`, localSess.GetId()))
	})
}

func TestHostAPI_T2_F2_2_RevokedPeerUpdatesListNodes(t *testing.T) {
	rigA, rigB := hostTestNewP2PMeshPair(t, "revoke-a", "revoke-b")
	idB := rigB.Daemon.Self().GetId()

	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	bAccess, err := rigB.Client.Access(ctx, &protocol.Access{})
	if err != nil {
		t.Fatalf("rigB Access: %v", err)
	}
	if _, err := rigA.Client.Access(ctx, &protocol.Access{
		Op:  "revoke",
		Key: bAccess.GetKey().GetFingerprint(),
	}); err != nil {
		t.Fatalf("rigA revoke peer key: %v", err)
	}

	WaitFor(t, defaultWaitTimeout, "revoked peer loses trusted status", func() bool {
		for _, n := range rigA.Nodes(t) {
			if n.GetId() == idB {
				return !n.GetTrusted()
			}
		}
		return true
	})

	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const res = await mako.mesh.listNodes();
        const peer = (res.nodes || []).find((n) => n.id === ` + fmt.Sprintf("%q", idB) + `);
        s.out = peer ? JSON.stringify({ trusted: Boolean(peer.trusted) }) : "removed";
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rigA, "t2-f22-revoke", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"trusted":false`) || HasTreeText(n, "removed")
	})
}

// ===========================================================================
// TIER 2: F2.3 — Host Session Action Boundary & Negative (>= 5 tests)
// ===========================================================================

func TestHostAPI_T2_F2_3_StartAgentDeniedWithoutSessionsWrite(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Projects: []plugin.ProjectRoots{{ID: "p1", Name: "Proj", Roots: []string{"/proj"}}},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        try {
          await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/proj", prompt: "hi" });
          s.out = "unexpected-allow";
        } catch (e) {
          s.out = "denied:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f23-deny-start", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "denied:") && HasTreeText(n, "sessions:write")
	})
	if len(r.Agents.Started()) != 0 {
		t.Fatalf("expected 0 started agents, got %+v", r.Agents.Started())
	}
}

func TestHostAPI_T2_F2_3_SendMessageEmptyAndOversizedRejected(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{{Id: "s1", State: protocol.StateRunning}},
	})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        let e1 = "", e2 = "";
        try {
          await mako.messages.sendMessage({ sessionId: "s1", text: "   \t\n  " });
        } catch (e) { e1 = e.message; }
        try {
          await mako.messages.sendMessage({ sessionId: "s1", text: "x".repeat(32 * 1024 + 1) });
        } catch (e) { e2 = e.message; }
        s.out = JSON.stringify({ e1, e2 });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f23-bounds", []string{"sessions:write"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, `"e1":"a message is text`) && HasTreeText(n, `"e2":"a message is text`)
	})
	if len(r.Agents.Sent()) != 0 {
		t.Fatalf("expected 0 sent messages, got %+v", r.Agents.Sent())
	}
}

func TestHostAPI_T2_F2_3_SendMessageNonexistentSessionFailsCleanly(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "msg-missing-sess", EnableP2P: false})
	js := `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        try {
          await mako.messages.sendMessage({ sessionId: "no-such-session", text: "ping" });
          s.out = "unexpected-ok";
        } catch (e) {
          s.out = "err:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestDaemonInstallAndWatch(t, rig, "t2-f23-missing-sess", []string{"sessions:write"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "err:") && HasTreeText(n, "no session")
	})
}

func TestHostAPI_T2_F2_3_OpenSessionWithoutUserGestureRejected(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{{Id: "s1", State: protocol.StateRunning}},
	})
	js := `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s, ctx) {
        try {
          await ctx.ui.openSession("s1");
          s.out = "unexpected-ok";
        } catch (e) {
          s.out = "err:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`
	w := hostTestInstallAndWatch(t, r, "t2-f23-gesture", []string{"sessions:read"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "err:") && HasTreeText(n, "only in answer to")
	})
}

func TestHostAPI_T2_F2_3_StopAgentUnauthorizedSessionRejected(t *testing.T) {
	rig := NewDaemonRig(t, DaemonRigOpts{Name: "stop-unauth", EnableP2P: false})
	userSess := rig.OpenShellSession(t, "")
	hostTestWaitDaemonSession(t, rig, userSess.GetId())

	js := fmt.Sprintf(`let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        try {
          await mako.messages.stopAgent({ sessionId: %q });
          s.out = "unexpected-stopped";
        } catch (e) {
          s.out = "rejected:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`, userSess.GetId())
	w := hostTestDaemonInstallAndWatch(t, rig, "t2-f23-stop-unauth", []string{"sessions:write"}, js)
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "rejected:") && HasTreeText(n, "only the agents it started")
	})

	// Confirm the user-owned shell session is still alive on the daemon.
	stillAlive := false
	for _, s := range rig.ListSessions(t) {
		if s.GetId() == userSess.GetId() && s.GetState() != protocol.StateExited {
			stillAlive = true
			break
		}
	}
	if !stillAlive {
		t.Fatalf("expected user session %q to remain running after unauthorized stopAgent", userSess.GetId())
	}
}

// ===========================================================================
// TIER 2: F2.4 — agent-map Plugin Boundary & Negative (>= 5 tests)
// ===========================================================================

func TestHostAPI_T2_F2_4_AgentMapZeroSessionsGracefulEmptyState(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{Sessions: nil})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "empty") != nil || FindNodeByKind(n, "graph") != nil
	})
	if err := w.Err(); err != nil {
		t.Fatalf("unexpected block error with 0 sessions: %v", err)
	}
	if FindNodeByKind(tree, "empty") == nil {
		t.Fatalf("expected ui.empty state when 0 sessions exist, tree texts=%v", TreeTexts(tree))
	}
}

func TestHostAPI_T2_F2_4_AgentMapOrphanParentSessionIdSurvives(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:              "s-orphan",
				Name:            "Orphan Subagent",
				Agent:           "claude",
				Project:         "/work/mako",
				State:           protocol.StateRunning,
				Status:          protocol.AgentWorking,
				NodeId:          "mac",
				ParentSessionId: "ended-parent-404",
			},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-orphan") != nil
	})
	if err := w.Err(); err != nil {
		t.Fatalf("unexpected error with orphan parentSessionId: %v", err)
	}
	g := FindNodeByKind(tree, "graph")
	for _, ge := range g.GetGraphEdges() {
		if ge.GetFrom() == "sess:ended-parent-404" || ge.GetTo() == "sess:ended-parent-404" {
			t.Fatalf("dangling edge to nonexistent parent sess:ended-parent-404 must not exist: %+v", ge)
		}
	}
}

func TestHostAPI_T2_F2_4_AgentMapMissingBranchAndProfileOmitsEmptyBadges(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:       "s-minimal",
				Name:     "Minimal Agent",
				Agent:    "shell",
				Project:  "/work/mako",
				State:    protocol.StateRunning,
				Status:   protocol.AgentIdle,
				NodeId:   "mac",
				Branch:   "",
				Profile:  "",
				Approval: nil,
			},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-minimal") != nil
	})
	sn := hostTestFindGraphNode(FindNodeByKind(tree, "graph"), "sess:s-minimal")
	if badges, ok := hostTestProtoBadges(sn); ok {
		for _, b := range badges {
			if strings.TrimSpace(b) == "" {
				t.Fatalf("expected empty branch/profile/approval badges to be omitted, got %v", badges)
			}
		}
	}
}

func TestHostAPI_T2_F2_4_AgentMapSearchMatchingZeroNodesRendersSafely(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-search-1", Name: "Alpha", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac"},
			{Id: "s-search-2", Name: "Beta", Agent: "codex", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentIdle, NodeId: "mac"},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil && FindNodeByKey(n, "search-input") != nil
	})
	searchInput := FindNodeByKey(tree, "search-input")
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  searchInput.GetId(),
		Kind:  protocol.UiChange,
		Value: "zzz-no-matching-node-999",
	}); err != nil {
		t.Fatalf("search change: %v", err)
	}
	emptyTree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "empty") != nil && FindNodeByKey(n, "btn-clear-filter") != nil
	})
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  FindNodeByKey(emptyTree, "btn-clear-filter").GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("clear filter tap: %v", err)
	}
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-search-1") != nil
	})
}

func TestHostAPI_T2_F2_4_AgentMapCorruptedSavedPositionsHandled(t *testing.T) {
	r := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-corrupt", Name: "Resilient Node", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac"},
		},
	})
	r.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r.Grant(t, "agent-map")
	w := r.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})

	tree := w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && hostTestFindGraphNode(g, "sess:s-corrupt") != nil
	})
	g := FindNodeByKind(tree, "graph")

	// Trigger a real drag-drop write to discover any storage key agent-map uses.
	if _, err := r.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  g.GetId(),
		Kind:  protocol.UiDrop,
		Value: `{"id":"sess:s-corrupt","x":111,"y":222}`,
	}); err != nil {
		t.Fatalf("drop event: %v", err)
	}
	w.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		sn := hostTestFindGraphNode(FindNodeByKind(n, "graph"), "sess:s-corrupt")
		x, okX := hostTestProtoFloat(sn, "x")
		return okX && x == 111
	})

	// Seed a fresh PluginRig with corrupted saved position JSON under the discovered key(s).
	storagePath := filepath.Join(r.DataDir, "agent-map", "storage.json")
	rawStorage, err := os.ReadFile(storagePath)
	if err != nil {
		t.Fatalf("read storage.json after drop: %v", err)
	}
	kv := map[string]string{}
	if err := json.Unmarshal(rawStorage, &kv); err != nil {
		t.Fatalf("unmarshal storage.json: %v", err)
	}
	for k := range kv {
		kv[k] = "{corrupt-json-not-parseable"
	}
	if len(kv) == 0 {
		kv["positions"] = "{corrupt-json-not-parseable"
	}
	corruptBytes, _ := json.Marshal(kv)

	r2 := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-corrupt", Name: "Resilient Node", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac"},
		},
	})
	if err := os.MkdirAll(filepath.Join(r2.DataDir, "agent-map"), 0o755); err != nil {
		t.Fatalf("mkdir agent-map data dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r2.DataDir, "agent-map", "storage.json"), corruptBytes, 0o644); err != nil {
		t.Fatalf("write corrupted storage.json: %v", err)
	}
	r2.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	r2.Grant(t, "agent-map")
	w2 := r2.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})
	w2.WaitFor(t, defaultWaitTimeout, func(n *protocol.UiNode) bool {
		ng := FindNodeByKind(n, "graph")
		return ng != nil && hostTestFindGraphNode(ng, "sess:s-corrupt") != nil
	})
	if err := w2.Err(); err != nil {
		t.Fatalf("agent-map crashed with corrupted saved positions: %v", err)
	}
}
