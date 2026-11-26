// Agent Map — multi-machine interactive topology map for projects, agents, and subagents.
//
// Demonstrates Mako's sidebar surface (`slots = ["sidebar"]`), Host API mesh
// & enriched session metadata (`mako.mesh.listNodes`, `mako.mesh.listSessions`,
// `parentSessionId`, `branch`, `profile`, `approvalTool`, `startedAt`, `lastActivityAt`),
// M1 `ui.graph` rich cards/minimap/layouts/hulls/live motion, drag-drop position
// persistence via `ctx.storage`, double-tap session opening, and right-click
// context menu actions.

let mako;

const I18N = {
  en: {
    "title": "Agent Map",
    "view.map": "Topology Map",
    "view.projects": "Projects",
    "view.table": "Sessions",
    "filter.all": "All",
    "filter.running": "Running",
    "filter.working": "Working",
    "filter.waiting": "Waiting",
    "layout.force": "Force",
    "layout.hierarchy": "Hierarchy",
    "layout.radial": "Radial",
    "search.placeholder": "Filter machines, projects, branches, or agents…",
    "kpi.projects": "Projects",
    "kpi.projectsDelta": "{active} active · {machines} nodes",
    "kpi.running": "Running",
    "kpi.runningDelta": "{total} total",
    "kpi.working": "Working",
    "kpi.workingDelta": "busy right now",
    "kpi.waiting": "Waiting",
    "kpi.waitingDelta": "needs attention",
    "map.caption": "Drag canvas to pan · Drag node to pin · Double-click or right-click any agent for actions",
    "map.emptyTitle": "No matching sessions",
    "map.emptyDesc": "Start an agent in any project or clear the current filter to populate the topology map.",
    "map.clearFilter": "Clear filters",
    "machine.local": "local",
    "machine.self": "this node",
    "machine.peer": "mesh peer",
    "machine.linkUp": "link up",
    "machine.linkDown": "link down",
    "machine.linkLinking": "linking",
    "ctx.openSession": "Open session",
    "ctx.sendMessage": "Send message",
    "ctx.runSubagent": "Run subagent",
    "ctx.killSession": "Kill session",
    "dialog.sendTitle": "Send Message to Agent",
    "dialog.sendPlaceholder": "Type instruction for this agent…",
    "dialog.sendSubmit": "Send",
    "dialog.subagentTitle": "Spawn Subagent",
    "dialog.subagentPlaceholder": "Task prompt for the child subagent…",
    "dialog.subagentSubmit": "Start Subagent",
    "dialog.cancel": "Cancel",
    "toast.sent": "Message delivered to session.",
    "toast.subagentAsked": "Subagent start requested.",
    "toast.killed": "Session stopped.",
    "toast.actionUnavailable": "Action applies to an agent session.",
    "toast.refreshed": "Agent map refreshed ({count} sessions, {nodes} nodes).",
    "inspector.title": "Inspector",
    "inspector.overviewTitle": "Multi-Machine Workspace Overview",
    "inspector.overviewHint": "Click a machine, project, or agent card to inspect metadata, drag nodes to save positions, or double-click an agent to jump straight to its session.",
    "inspector.projectSummary": "{running} running · {working} working · {waiting} waiting",
    "inspector.machinesCard": "Mesh Nodes",
    "inspector.agentsInProject": "Agents in this project ({count})",
    "inspector.recentMessages": "Recent inter-agent messages ({count})",
    "inspector.noMessages": "No inter-agent messages captured yet.",
    "inspector.jump": "Jump to session",
    "inspector.copyId": "Copy ID",
    "inspector.copied": "Copied session ID.",
    "inspector.agent": "Agent",
    "inspector.profile": "Profile",
    "inspector.branch": "Branch",
    "inspector.approval": "Approval",
    "inspector.state": "State",
    "inspector.status": "Status",
    "inspector.project": "Project",
    "inspector.node": "Machine",
    "inspector.parent": "Parent Session",
    "inspector.started": "Started",
    "inspector.lastActivity": "Last Activity",
    "inspector.os": "Platform",
    "inspector.link": "Mesh Link",
    "inspector.sessionId": "Session ID",
    "inspector.open": "Open",
    "inspector.focusParent": "Focus parent",
    "projects.emptyTitle": "No projects with sessions",
    "projects.emptyDesc": "Sessions grouped by project folder appear here automatically.",
    "table.colName": "Session",
    "table.colProject": "Project",
    "table.colAgent": "Agent",
    "table.colState": "State",
    "table.colStatus": "Status",
    "table.colAction": "Action",
    "status.working": "working",
    "status.waiting": "waiting",
    "status.done": "done",
    "status.idle": "idle",
    "state.running": "running",
    "state.starting": "starting",
    "state.stopped": "saved",
    "state.exited": "exited",
    "noFolder": "(No folder)",
  },
  ko: {
    "title": "에이전트 맵",
    "view.map": "토폴로지 맵",
    "view.projects": "프로젝트 카드",
    "view.table": "세션 목록",
    "filter.all": "전체",
    "filter.running": "실행 중",
    "filter.working": "작업 중",
    "filter.waiting": "대기 중",
    "layout.force": "포스",
    "layout.hierarchy": "계층형",
    "layout.radial": "방사형",
    "search.placeholder": "머신, 프로젝트, 브랜치, 에이전트 검색…",
    "kpi.projects": "프로젝트",
    "kpi.projectsDelta": "활성 {active}개 · 노드 {machines}대",
    "kpi.running": "실행 중",
    "kpi.runningDelta": "전체 {total}개",
    "kpi.working": "작업 중",
    "kpi.workingDelta": "현재 응답 생성",
    "kpi.waiting": "입력 대기",
    "kpi.waitingDelta": "승인/확인 필요",
    "map.caption": "배경 드래그로 이동 · 노드 드래그로 위치 저장 · 더블클릭으로 세션 열기 · 우클릭 메뉴",
    "map.emptyTitle": "표시할 세션이 없습니다",
    "map.emptyDesc": "프로젝트에서 에이전트를 실행하거나 필터를 초기화하면 전체 맵이 표시됩니다.",
    "map.clearFilter": "필터 초기화",
    "machine.local": "로컬",
    "machine.self": "현재 노드",
    "machine.peer": "메시 피어",
    "machine.linkUp": "연결됨",
    "machine.linkDown": "연결 끊김",
    "machine.linkLinking": "연결 중",
    "ctx.openSession": "세션 열기",
    "ctx.sendMessage": "메시지 보내기",
    "ctx.runSubagent": "서브에이전트 실행",
    "ctx.killSession": "세션 종료",
    "dialog.sendTitle": "에이전트에게 메시지 보내기",
    "dialog.sendPlaceholder": "전달할 지시 내용을 입력하세요…",
    "dialog.sendSubmit": "보내기",
    "dialog.subagentTitle": "서브에이전트 실행",
    "dialog.subagentPlaceholder": "서브에이전트 작업 프롬프트를 입력하세요…",
    "dialog.subagentSubmit": "실행",
    "dialog.cancel": "취소",
    "toast.sent": "메시지를 전달했습니다.",
    "toast.subagentAsked": "서브에이전트 실행을 요청했습니다.",
    "toast.killed": "세션을 종료했습니다.",
    "toast.actionUnavailable": "에이전트 세션 노드에서 실행할 수 있습니다.",
    "toast.refreshed": "에이전트 맵을 새로고침했습니다 ({count}개 세션, {nodes}대 노드).",
    "inspector.title": "인스펙터",
    "inspector.overviewTitle": "멀티 머신 워크스페이스 요약",
    "inspector.overviewHint": "머신·프로젝트·에이전트 카드를 클릭해 상세를 확인하고, 드래그로 위치를 저장하거나 더블클릭으로 세션에 바로 진입하세요.",
    "inspector.projectSummary": "실행 {running} · 작업 {working} · 대기 {waiting}",
    "inspector.machinesCard": "메시 노드",
    "inspector.agentsInProject": "이 프로젝트의 에이전트 ({count})",
    "inspector.recentMessages": "최근 에이전트 간 메시지 ({count})",
    "inspector.noMessages": "아직 감지된 에이전트 간 메시지가 없습니다.",
    "inspector.jump": "세션으로 이동",
    "inspector.copyId": "ID 복사",
    "inspector.copied": "세션 ID를 복사했습니다.",
    "inspector.agent": "에이전트",
    "inspector.profile": "프로필",
    "inspector.branch": "브랜치",
    "inspector.approval": "승인 대기 도구",
    "inspector.state": "프로세스",
    "inspector.status": "상태",
    "inspector.project": "프로젝트",
    "inspector.node": "머신",
    "inspector.parent": "부모 세션",
    "inspector.started": "시작 시각",
    "inspector.lastActivity": "최근 활동",
    "inspector.os": "플랫폼",
    "inspector.link": "메시 링크",
    "inspector.sessionId": "세션 ID",
    "inspector.open": "이동",
    "inspector.focusParent": "부모 포커스",
    "projects.emptyTitle": "세션이 있는 프로젝트가 없습니다",
    "projects.emptyDesc": "에이전트가 실행되면 프로젝트 폴더별로 자동 그룹화됩니다.",
    "table.colName": "세션",
    "table.colProject": "프로젝트",
    "table.colAgent": "에이전트",
    "table.colState": "프로세스",
    "table.colStatus": "상태",
    "table.colAction": "이동",
    "status.working": "작업 중",
    "status.waiting": "대기 중",
    "status.done": "완료",
    "status.idle": "유휴",
    "state.running": "실행 중",
    "state.starting": "시작 중",
    "state.stopped": "저장됨",
    "state.exited": "종료됨",
    "noFolder": "(폴더 없음)",
  },
};

const POSITIONS_KEY = "node-positions";
const MAX_GRAPH_NODES = 120;
const MAX_GRAPH_EDGES = 400;
const MAX_MESSAGES = 16;
const KNOWN_AGENT_ICONS = new Set(["agy", "claude", "codex", "omo", "omp", "opencode", "pi"]);

function utf8ByteLen(cp) {
  if (cp <= 0x7f) return 1;
  if (cp <= 0x7ff) return 2;
  if (cp <= 0xffff) return 3;
  return 4;
}

function sanitizeInline(s) {
  return String(s ?? "")
    .replace(/[\u200b-\u200d\ufeff]/g, "")
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/g, " ");
}

function sanitizeText(s, maxBytes) {
  const clean = String(s ?? "")
    .replace(/[\u200b-\u200d\ufeff]/g, "")
    .replace(/[\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!clean) return "";
  if (maxBytes === undefined) return clean;
  if (maxBytes <= 0) return "";
  let totalBytes = 0;
  let exceeded = false;
  for (const ch of clean) {
    const b = utf8ByteLen(ch.codePointAt(0));
    if (totalBytes + b > maxBytes) {
      exceeded = true;
      break;
    }
    totalBytes += b;
  }
  if (!exceeded) return clean;
  const ellipsis = maxBytes >= 3 ? "…" : "";
  const budget = maxBytes >= 3 ? maxBytes - 3 : maxBytes;
  let out = "";
  let used = 0;
  for (const ch of clean) {
    const b = utf8ByteLen(ch.codePointAt(0));
    if (used + b > budget) break;
    out += ch;
    used += b;
  }
  return out + ellipsis;
}

function safeKeyPart(s, fallback) {
  const clean = sanitizeText(s, 80).replace(/[/#]/g, "-");
  return clean || fallback;
}

function parsePositions(raw) {
  if (!raw || typeof raw !== "string") return {};
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out = {};
    for (const [id, pt] of Object.entries(parsed)) {
      if (!id || id.length > 128 || !pt || typeof pt !== "object") continue;
      const x = Number(pt.x);
      const y = Number(pt.y);
      if (Number.isFinite(x) && Number.isFinite(y) && Math.abs(x) <= 1e6 && Math.abs(y) <= 1e6) {
        out[id] = { x, y };
      }
    }
    return out;
  } catch {
    return {};
  }
}

function mergeSessions(localList, meshList) {
  const byId = new Map();
  const ingest = (list) => {
    for (const s of (Array.isArray(list) ? list : [])) {
      if (!s || !s.id) continue;
      const prev = byId.get(s.id);
      if (!prev) {
        byId.set(s.id, { ...s });
        continue;
      }
      byId.set(s.id, {
        ...prev,
        ...s,
        name: s.name || prev.name || "",
        agent: s.agent || prev.agent || "",
        project: s.project || prev.project || "",
        state: s.state || prev.state || "",
        status: s.status || prev.status || "",
        nodeId: s.nodeId || prev.nodeId || "",
        parentSessionId: s.parentSessionId || prev.parentSessionId || "",
        parentNodeId: s.parentNodeId || prev.parentNodeId || "",
        branch: s.branch || prev.branch || "",
        profile: s.profile || prev.profile || "",
        approvalTool: s.approvalTool || prev.approvalTool || "",
        startedAt: (Number(s.startedAt) > 0 ? Number(s.startedAt) : Number(prev.startedAt)) || 0,
        lastActivityAt: Math.max(Number(s.lastActivityAt) || 0, Number(prev.lastActivityAt) || 0),
      });
    }
  };
  ingest(meshList);
  ingest(localList);
  return [...byId.values()];
}

function projectBaseName(folder) {
  const norm = String(folder || "").replace(/\\/g, "/").replace(/\/+$/, "");
  if (!norm) return "";
  const parts = norm.split("/").filter(Boolean);
  return parts[parts.length - 1] || norm;
}

function projectDisplayNames(folders) {
  const unique = [...new Set(folders)];
  const byBase = new Map();
  for (const f of unique) {
    const b = projectBaseName(f);
    if (!byBase.has(b)) byBase.set(b, []);
    byBase.get(b).push(f);
  }
  const labels = new Map();
  for (const f of unique) {
    if (!f) {
      labels.set(f, "");
      continue;
    }
    const base = projectBaseName(f);
    let rawLabel;
    if ((byBase.get(base) || []).length > 1) {
      const parts = f.replace(/\\/g, "/").replace(/\/+$/, "").split("/").filter(Boolean);
      rawLabel = parts.slice(-2).join("/");
    } else {
      rawLabel = base;
    }
    labels.set(f, sanitizeText(rawLabel, 64));
  }
  return labels;
}

function isRunning(s) {
  return s.state === "running" || s.state === "starting";
}

function isWaitingSession(s) {
  return Boolean(s && (s.status === "waiting" || (s.approvalTool && String(s.approvalTool).trim() !== "")));
}

function sessionTone(s) {
  if (isWaitingSession(s) && isRunning(s)) return "warning";
  if (s && s.approvalTool && String(s.approvalTool).trim() !== "") return "warning";
  if (!isRunning(s)) return "muted";
  switch (s.status) {
    case "waiting":
      return "warning";
    case "working":
      return "accent";
    case "done":
      return "success";
    default:
      return "success";
  }
}

function graphStatus(s) {
  if (isWaitingSession(s)) return "waiting";
  if (!isRunning(s)) return s.state === "exited" ? "stopped" : "done";
  switch (s.status) {
    case "working":
    case "waiting":
    case "idle":
    case "done":
      return s.status;
    default:
      return "idle";
  }
}

function agentIcon(agent, waiting) {
  if (waiting) return "shield-alert";
  const a = String(agent || "").trim().toLowerCase();
  if (KNOWN_AGENT_ICONS.has(a)) return `agent-${a}`;
  if (a === "shell" || a === "bash" || a === "zsh") return "terminal";
  return "bot";
}

function osIcon(os) {
  switch (String(os || "").toLowerCase()) {
    case "darwin":
      return "os-macos";
    case "linux":
      return "os-linux";
    case "windows":
      return "os-windows";
    default:
      return "server";
  }
}

function sessionLabel(s) {
  const raw = sanitizeText(s.name, 40);
  if (raw) return raw;
  const agent = sanitizeText(s.agent || "agent", 24) || "agent";
  const tail = sanitizeText(String(s.id || "").slice(0, 6), 12);
  return sanitizeText(tail ? `${agent} · ${tail}` : agent, 40) || "agent";
}

function stateText(ctx, s) {
  return sanitizeText(ctx.t(`state.${s.state || "stopped"}`), 32) || "stopped";
}

function statusText(ctx, s) {
  if (!isRunning(s)) {
    return stateText(ctx, s);
  }
  return sanitizeText(ctx.t(`status.${s.status || "idle"}`), 32) || "idle";
}

function buildSessionBadges(s) {
  const badges = [];
  const branch = sanitizeText(s.branch || "", 26);
  if (branch) {
    badges.push({ text: branch, icon: "git-branch", tone: "muted", variant: "soft" });
  }
  const profile = sanitizeText(s.profile || "", 24);
  if (profile) {
    badges.push({ text: profile, tone: "accent", variant: "soft" });
  }
  const approval = sanitizeText(s.approvalTool || "", 24);
  if (approval) {
    badges.push({ text: approval, icon: "shield-alert", tone: "warning", variant: "soft" });
  }
  return badges;
}

function formatTs(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0) return "";
  try {
    const d = new Date(n);
    if (Number.isNaN(d.getTime())) return "";
    return d.toISOString().replace("T", " ").slice(0, 19);
  } catch {
    return "";
  }
}

function filterSessions(sessions, filter, query, nodeNameById) {
  const q = String(query || "").trim().toLowerCase();
  return sessions.filter((s) => {
    if (filter === "running" && !isRunning(s)) return false;
    if (filter === "working" && !(isRunning(s) && s.status === "working")) return false;
    if (filter === "waiting" && !(isRunning(s) && isWaitingSession(s))) return false;
    if (q) {
      const mName = nodeNameById.get(s.nodeId || "") || "";
      const hay = `${s.name || ""} ${s.agent || ""} ${s.project || ""} ${s.id || ""} ${s.branch || ""} ${s.profile || ""} ${s.approvalTool || ""} ${s.nodeId || ""} ${mName} ${s.status || ""} ${s.state || ""}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
}

function groupByProject(sessions, noFolderLabel) {
  const safeNoFolder = sanitizeText(noFolderLabel, 64) || "(No folder)";
  const names = projectDisplayNames(sessions.map((s) => s.project || ""));
  const map = new Map();
  for (const s of sessions) {
    const rawKey = s.project || "";
    if (!map.has(rawKey)) {
      map.set(rawKey, {
        key: sanitizeText(rawKey, 256),
        name: names.get(rawKey) || sanitizeText(rawKey, 64) || safeNoFolder,
        sessions: [],
        running: 0,
        working: 0,
        waiting: 0,
      });
    }
    const grp = map.get(rawKey);
    grp.sessions.push(s);
    if (isRunning(s)) {
      grp.running += 1;
      if (s.status === "working") grp.working += 1;
      if (isWaitingSession(s)) grp.waiting += 1;
    }
  }
  return [...map.values()].sort((a, b) => (
    (b.working - a.working) ||
    (b.waiting - a.waiting) ||
    (b.running - a.running) ||
    a.name.localeCompare(b.name)
  ));
}

function buildMachines(nodes, sessions, ctx) {
  const byId = new Map();
  const usedKeys = new Set();
  const allocKey = (raw) => {
    const base = safeKeyPart(raw, "local");
    let k = base;
    let i = 1;
    while (usedKeys.has(k)) {
      k = `${base}-${i++}`;
    }
    usedKeys.add(k);
    return k;
  };
  for (const n of (Array.isArray(nodes) ? nodes : [])) {
    if (!n || !n.id) continue;
    byId.set(n.id, {
      id: n.id,
      keyId: allocKey(n.id),
      name: sanitizeText(n.name, 48) || sanitizeText(n.id, 48) || "local",
      os: sanitizeText(n.os, 24),
      arch: sanitizeText(n.arch, 24),
      link: sanitizeText(n.link, 16) || (n.self ? "up" : ""),
      self: Boolean(n.self),
      trusted: Boolean(n.trusted),
      projects: Array.isArray(n.projects) ? n.projects : [],
      sessions: [],
    });
  }
  for (const s of sessions) {
    const mid = (s.nodeId || "").trim() || "local";
    if (!byId.has(mid)) {
      byId.set(mid, {
        id: mid,
        keyId: allocKey(mid),
        name: sanitizeText(mid === "local" ? ctx.t("machine.local") : mid, 48) || "local",
        os: "",
        arch: "",
        link: "up",
        self: mid === "local",
        trusted: true,
        projects: [],
        sessions: [],
      });
    }
    byId.get(mid).sessions.push(s);
  }
  return [...byId.values()]
    .filter((m) => m.sessions.length > 0)
    .sort((a, b) => (Number(b.self) - Number(a.self)) || a.name.localeCompare(b.name));
}

function withSavedPosition(gn, positions) {
  const p = positions && positions[gn.id];
  if (p && Number.isFinite(p.x) && Number.isFinite(p.y)) {
    return { ...gn, x: p.x, y: p.y, hasPos: true, pinned: true };
  }
  return gn;
}

async function fetchSnapshot() {
  const localRes = await mako.sessions.listSessions();
  const localSessions = Array.isArray(localRes && localRes.sessions) ? localRes.sessions : [];
  let nodes = [];
  let meshSessions = [];
  if (mako.mesh && typeof mako.mesh.listNodes === "function") {
    try {
      const nRes = await mako.mesh.listNodes();
      nodes = Array.isArray(nRes && nRes.nodes) ? nRes.nodes : [];
    } catch {
      nodes = [];
    }
  }
  if (mako.mesh && typeof mako.mesh.listSessions === "function") {
    try {
      const msRes = await mako.mesh.listSessions();
      meshSessions = Array.isArray(msRes && msRes.sessions) ? msRes.sessions : [];
    } catch {
      meshSessions = [];
    }
  }
  return {
    sessions: mergeSessions(localSessions, meshSessions),
    nodes,
  };
}

function sessionIdFromNodeArg(arg) {
  const raw = String(arg || "").trim();
  if (!raw) return "";
  if (raw.startsWith("sess:")) return raw.slice(5);
  return "";
}

export default {
  async activate(m) {
    mako = m;
    mako.i18n.register(I18N);

    try {
      await mako.sessions.subscribe({
        events: ["sessionStarted", "sessionEnded", "sessionSlept", "sessionStatus", "agentMessage"],
      });
    } catch {
      // Permission may not be granted yet; load() will prompt/refresh once granted.
    }

    mako.blocks.register("map", {
      state: {
        sessions: [],
        nodes: [],
        messages: [],
        view: "map",
        filter: "all",
        layout: "force",
        query: "",
        selectedNode: "",
        positions: {},
        positionsLoaded: false,
        modal: null,
        modalText: "",
      },
      refresh: 15,

      async load(state, ctx) {
        if (!state.positionsLoaded && ctx && ctx.storage) {
          try {
            const item = await ctx.storage.getItem(POSITIONS_KEY);
            if (item && item.found && item.value) {
              state.positions = parsePositions(item.value);
            }
          } catch {
            state.positions = {};
          }
          state.positionsLoaded = true;
        }
        const snap = await fetchSnapshot();
        state.sessions = snap.sessions;
        state.nodes = snap.nodes;
      },

      render(state, ui, ctx) {
        const all = Array.isArray(state.sessions) ? state.sessions : [];
        const nodes = Array.isArray(state.nodes) ? state.nodes : [];
        const nodeNameById = new Map(nodes.map((n) => [n.id, n.name || n.id]));
        const allMachines = buildMachines(nodes, all, ctx);
        const allGroups = groupByProject(all, ctx.t("noFolder"));
        const activeProjects = allGroups.filter((g) => g.running > 0).length;
        const totalRunning = all.filter(isRunning).length;
        const totalWorking = all.filter((s) => isRunning(s) && s.status === "working").length;
        const totalWaiting = all.filter((s) => isRunning(s) && isWaitingSession(s)).length;

        const filtered = filterSessions(all, state.filter, state.query, nodeNameById);
        const groups = groupByProject(filtered, ctx.t("noFolder"));

        const viewOptions = [ctx.t("view.map"), ctx.t("view.projects"), ctx.t("view.table")];
        const viewKeyToLabel = {
          map: viewOptions[0],
          projects: viewOptions[1],
          table: viewOptions[2],
        };
        const currentViewLabel = viewKeyToLabel[state.view] || viewOptions[0];

        const filterOptions = [
          { id: "all", label: ctx.t("filter.all") },
          { id: "running", label: ctx.t("filter.running") },
          { id: "working", label: ctx.t("filter.working") },
          { id: "waiting", label: ctx.t("filter.waiting") },
        ];
        const currentFilterLabel = (filterOptions.find((f) => f.id === state.filter) || filterOptions[0]).label;

        const layoutOptions = [
          { id: "force", label: ctx.t("layout.force") },
          { id: "hierarchy", label: ctx.t("layout.hierarchy") },
          { id: "radial", label: ctx.t("layout.radial") },
        ];
        const currentLayoutLabel = (layoutOptions.find((l) => l.id === state.layout) || layoutOptions[0]).label;

        const toolbar = ui.toolbar([
          ui.tabs(currentViewLabel, viewOptions, {
            key: "view-tabs",
            variant: "pills",
            onChange: ["setView"],
          }),
          ui.select(currentFilterLabel, filterOptions.map((f) => f.label), {
            key: "filter-select",
            onChange: ["setFilter"],
          }),
          ui.select(currentLayoutLabel, layoutOptions.map((l) => l.label), {
            key: "layout-select",
            onChange: ["setLayout"],
          }),
          ui.input(sanitizeInline(state.query), {
            key: "search-input",
            variant: "search",
            placeholder: ctx.t("search.placeholder"),
            grow: 1,
            onChange: ["setQuery"],
          }),
          ui.button("", {
            key: "btn-refresh",
            icon: "refresh-cw",
            variant: "ghost",
            size: "small",
            onTap: ["refresh"],
          }),
        ], { key: "tb" });

        const kpiStrip = ui.grid(4, [
          ui.stat(ctx.t("kpi.projects"), String(allGroups.length), {
            key: "kpi-projects",
            placeholder: ctx.t("kpi.projectsDelta", { active: activeProjects, machines: allMachines.length }),
            icon: "folder",
            tone: "accent",
            onTap: ["setFilterKey", "all"],
          }),
          ui.stat(ctx.t("kpi.running"), String(totalRunning), {
            key: "kpi-running",
            placeholder: ctx.t("kpi.runningDelta", { total: all.length }),
            icon: "rocket",
            tone: "success",
            onTap: ["setFilterKey", "running"],
          }),
          ui.stat(ctx.t("kpi.working"), String(totalWorking), {
            key: "kpi-working",
            placeholder: ctx.t("kpi.workingDelta"),
            icon: "sparkles",
            tone: "accent",
            onTap: ["setFilterKey", "working"],
          }),
          ui.stat(ctx.t("kpi.waiting"), String(totalWaiting), {
            key: "kpi-waiting",
            placeholder: ctx.t("kpi.waitingDelta"),
            icon: "shield-alert",
            tone: totalWaiting > 0 ? "warning" : "muted",
            onTap: ["setFilterKey", "waiting"],
          }),
        ], { key: "kpi-grid", gap: 10 });

        let mainSection;
        let showContextMenu = false;

        if (filtered.length === 0) {
          mainSection = ui.empty(ctx.t("map.emptyTitle"), [
            ui.button(ctx.t("map.clearFilter"), {
              key: "btn-clear-filter",
              variant: "outline",
              size: "small",
              onTap: ["clearFilters"],
            }),
          ], {
            key: "empty-map",
            icon: "network",
            tone: "muted",
            placeholder: ctx.t("map.emptyDesc"),
          });
        } else if (state.view === "projects") {
          const cards = groups.map((grp, gi) => {
            const projTitle = sanitizeText(grp.name, 64) || sanitizeText(ctx.t("noFolder"), 64) || "project";
            const projPath = sanitizeText(grp.key, 256) || sanitizeText(ctx.t("noFolder"), 64) || "(No folder)";
            const rows = grp.sessions.map((s) => {
              const waiting = isWaitingSession(s);
              const rowMeta = [
                sanitizeText(s.agent, 24) || "agent",
                statusText(ctx, s),
                sanitizeText(s.branch, 32),
                sanitizeText(s.nodeId, 32),
              ].filter(Boolean).join(" · ");
              return ui.row([
                ui.icon(agentIcon(s.agent, waiting), { tone: sessionTone(s), size: "small" }),
                ui.column([
                  ui.text(sessionLabel(s), { weight: "medium" }),
                  ui.text(
                    sanitizeText(rowMeta, 120) || "agent",
                    { size: "small", tone: "muted" },
                  ),
                ], { grow: 1, gap: 1 }),
                ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
                ui.button(ctx.t("inspector.open"), {
                  key: `open-${s.id}`,
                  icon: "arrow-right",
                  variant: "ghost",
                  size: "small",
                  onTap: ["openSession", s.id],
                }),
              ], { key: `pcard-s-${s.id}`, gap: 8 });
            });
            return ui.card([
              ui.column([
                ui.text(projPath, { variant: "mono", size: "small", tone: "muted" }),
                ui.divider(),
                ...rows,
              ], { gap: 8 }),
            ], {
              key: `pcard-${gi}`,
              text: projTitle,
              icon: "folder",
              badges: [
                { text: `${grp.running}/${grp.sessions.length}`, tone: grp.running > 0 ? "success" : "muted" },
                ...(grp.waiting > 0 ? [{ text: `${grp.waiting} ${ctx.t("status.waiting")}`, tone: "warning" }] : []),
              ],
            });
          });
          mainSection = ui.scroll([
            ui.grid(2, cards, { key: "projects-grid", gap: 12 }),
          ], { key: "projects-scroll", grow: 1 });
        } else if (state.view === "table") {
          const names = projectDisplayNames(filtered.map((s) => s.project || ""));
          const tableRows = filtered.map((s) => {
            const projDisplay = names.get(s.project || "") || sanitizeText(s.project || "", 64) || sanitizeText(ctx.t("noFolder"), 64) || "(No folder)";
            return ui.row([
              ui.text(sessionLabel(s), { weight: "medium" }),
              ui.text(projDisplay, { tone: "muted", size: "small" }),
              ui.chip(sanitizeText(s.agent, 24) || "agent", { variant: "soft" }),
              ui.text(stateText(ctx, s), { size: "small", tone: isRunning(s) ? "success" : "muted" }),
              ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
              ui.button(ctx.t("inspector.open"), {
                key: `tbl-open-${s.id}`,
                icon: "arrow-right",
                variant: "outline",
                size: "small",
                onTap: ["openSession", s.id],
              }),
            ], { key: `tbl-row-${s.id}` });
          });
          mainSection = ui.scroll([
            ui.table(
              [
                ctx.t("table.colName"),
                ctx.t("table.colProject"),
                ctx.t("table.colAgent"),
                ctx.t("table.colState"),
                ctx.t("table.colStatus"),
                ctx.t("table.colAction"),
              ],
              tableRows,
              { key: "sessions-table", variant: "striped" },
            ),
          ], { key: "table-scroll", grow: 1 });
        } else {
          // Multi-machine Topology Graph: Machine -> Project -> Agent -> Subagent
          showContextMenu = true;
          const graphNodes = [];
          const graphEdges = [];
          const machineNodeMeta = new Map();
          const projectNodeMeta = new Map();
          const sessionNodeMeta = new Map();
          const visibleSessionIds = new Set();
          const machines = buildMachines(nodes, filtered, ctx);
          const sessionMap = new Map(all.map((s) => [s.id, s]));

          const pendingSessions = [];

          machines.forEach((m, mi) => {
            if (graphNodes.length >= MAX_GRAPH_NODES) return;
            const machId = `mach:${m.keyId}`;
            const mLabel = sanitizeText(m.name, 48) || sanitizeText(m.id, 48) || "local";
            const mGroup = sanitizeText(m.name, 64) || sanitizeText(m.id, 64) || "local";
            const mSubParts = [];
            if (m.os) mSubParts.push(m.arch ? `${m.os}/${m.arch}` : m.os);
            mSubParts.push(m.self ? ctx.t("machine.self") : ctx.t("machine.peer"));
            const mBadges = [];
            const osBadge = sanitizeText(m.os, 20);
            if (osBadge) mBadges.push({ text: osBadge, tone: "muted", variant: "soft" });
            const linkBadge = sanitizeText(m.link, 16);
            if (linkBadge) {
              mBadges.push({
                text: linkBadge,
                tone: m.link === "up" ? "success" : (m.link === "down" ? "danger" : "warning"),
                variant: "soft",
              });
            }
            machineNodeMeta.set(machId, m);
            graphNodes.push(withSavedPosition({
              id: machId,
              label: mLabel,
              group: mGroup,
              shape: "card",
              icon: osIcon(m.os),
              status: m.link === "down" ? "error" : "idle",
              tone: m.link === "down" ? "danger" : (m.self ? "accent" : "success"),
              subtitle: sanitizeText(mSubParts.join(" · "), 120),
              badges: mBadges,
              weight: 2.6,
            }, state.positions));

            const projGroups = groupByProject(m.sessions, ctx.t("noFolder"));
            projGroups.forEach((grp, pi) => {
              if (graphNodes.length >= MAX_GRAPH_NODES) return;
              const projId = `proj:${mi}:${pi}`;
              const projLabel = sanitizeText(grp.name, 48) || sanitizeText(ctx.t("noFolder"), 48) || "project";
              const clusterGroup = sanitizeText(`${mLabel} · ${projLabel}`, 64);
              projectNodeMeta.set(projId, { project: grp, machine: m });
              graphNodes.push(withSavedPosition({
                id: projId,
                label: projLabel,
                group: clusterGroup,
                shape: "card",
                icon: "folder",
                status: grp.waiting > 0 ? "waiting" : (grp.working > 0 ? "working" : (grp.running > 0 ? "idle" : "done")),
                tone: grp.waiting > 0 ? "warning" : (grp.running > 0 ? "accent" : "muted"),
                subtitle: sanitizeText(`${grp.running}/${grp.sessions.length} ${ctx.t("state.running")}`, 100),
                badges: [
                  { text: `${grp.running}/${grp.sessions.length}`, tone: grp.running > 0 ? "success" : "muted", variant: "soft" },
                  ...(grp.waiting > 0 ? [{ text: `${grp.waiting} ${ctx.t("status.waiting")}`, tone: "warning", variant: "soft" }] : []),
                ],
                weight: 2.0,
              }, state.positions));
              if (graphEdges.length < MAX_GRAPH_EDGES) {
                graphEdges.push({
                  from: machId,
                  to: projId,
                  kind: "machine",
                  tone: m.link === "down" ? "danger" : "muted",
                });
              }

              for (const s of grp.sessions) {
                if (graphNodes.length >= MAX_GRAPH_NODES) break;
                const sid = `sess:${s.id}`;
                const waiting = isWaitingSession(s);
                const tone = sessionTone(s);
                visibleSessionIds.add(s.id);
                sessionNodeMeta.set(sid, { session: s, project: grp, machine: m });
                const subParts = [sanitizeText(s.agent, 24) || "agent", statusText(ctx, s)];
                const cleanBranch = sanitizeText(s.branch, 32);
                if (cleanBranch) subParts.push(cleanBranch);
                graphNodes.push(withSavedPosition({
                  id: sid,
                  label: sessionLabel(s),
                  group: clusterGroup,
                  shape: "card",
                  icon: agentIcon(s.agent, waiting),
                  status: graphStatus(s),
                  tone,
                  subtitle: sanitizeText(subParts.join(" · "), 120),
                  badges: buildSessionBadges(s),
                  weight: isRunning(s) ? (s.status === "working" ? 1.5 : 1.25) : 0.9,
                }, state.positions));
                pendingSessions.push({ session: s, sid, projId, tone });
              }
            });
          });

          for (const item of pendingSessions) {
            if (graphEdges.length >= MAX_GRAPH_EDGES) break;
            const s = item.session;
            const parentId = s.parentSessionId && s.parentSessionId !== s.id && visibleSessionIds.has(s.parentSessionId)
              ? s.parentSessionId
              : "";
            if (parentId) {
              graphEdges.push({
                from: `sess:${parentId}`,
                to: item.sid,
                kind: "parent",
                tone: isRunning(s) ? "accent" : "muted",
                active: isRunning(s),
                dashed: !isRunning(s),
              });
            } else {
              graphEdges.push({
                from: item.projId,
                to: item.sid,
                kind: "project",
                tone: item.tone,
                dashed: !isRunning(s),
              });
            }
          }

          // Live inter-agent communication edges captured via on.agentMessage.
          const msgEdgeSeen = new Set();
          for (const msg of (state.messages || [])) {
            if (graphEdges.length >= MAX_GRAPH_EDGES) break;
            if (!msg.from || !msg.to || msg.from === msg.to) continue;
            if (!visibleSessionIds.has(msg.from) || !visibleSessionIds.has(msg.to)) continue;
            const edgeKey = `${msg.from}->${msg.to}`;
            if (msgEdgeSeen.has(edgeKey)) continue;
            msgEdgeSeen.add(edgeKey);
            graphEdges.push({
              from: `sess:${msg.from}`,
              to: `sess:${msg.to}`,
              label: "msg",
              kind: "agentMessage",
              active: true,
              tone: "accent",
              dashed: true,
            });
          }

          const selectedId = (
            sessionNodeMeta.has(state.selectedNode) ||
            projectNodeMeta.has(state.selectedNode) ||
            machineNodeMeta.has(state.selectedNode)
          ) ? state.selectedNode : "";

          const graphPane = ui.column([
            ui.graph(graphNodes, graphEdges, {
              key: "agent-topology",
              variant: "cards",
              layout: state.layout || "force",
              minimap: true,
              fill: true,
              grow: 1,
              value: selectedId,
              contextMenu: "node-menu",
              caption: ctx.t("map.caption"),
              onChange: ["selectNode"],
              onDoubleTap: ["openGraphNode"],
              onDrop: ["saveNodePosition"],
            }),
          ], { key: "graph-pane", grow: 1, gap: 8 });

          // Inspector content on right side of split
          let inspectorBody;
          if (selectedId && sessionNodeMeta.has(selectedId)) {
            const { session: s, project: grp, machine: m } = sessionNodeMeta.get(selectedId);
            const waiting = isWaitingSession(s);
            const relMsgs = (state.messages || []).filter((msg) => msg.from === s.id || msg.to === s.id).slice(-5).reverse();
            const startedText = formatTs(s.startedAt);
            const activityText = formatTs(s.lastActivityAt);
            const hasParent = Boolean(s.parentSessionId && sessionMap.has(s.parentSessionId));
            const agentText = sanitizeText(s.agent, 24) || "agent";
            const projNameText = sanitizeText(grp.name, 64) || sanitizeText(ctx.t("noFolder"), 64) || "project";
            const projPathText = sanitizeText(s.project, 256) || sanitizeText(ctx.t("noFolder"), 64) || "(No folder)";
            const nodeText = sanitizeText(`${m.name}${m.os ? ` (${m.os})` : ""}`, 80) || "local";
            const branchText = sanitizeText(s.branch, 48);
            const profileText = sanitizeText(s.profile, 48);
            const approvalText = sanitizeText(s.approvalTool, 48);
            const parentText = sanitizeText(s.parentSessionId, 64);
            const sidText = sanitizeText(s.id, 64) || "session";

            inspectorBody = ui.column([
              ui.card([
                ui.column([
                  ui.row([
                    ui.chip(agentText, { icon: agentIcon(s.agent, waiting), variant: "soft", tone: "accent" }),
                    ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
                    ui.chip(stateText(ctx, s), { variant: "outline" }),
                  ], { gap: 6 }),
                  ui.divider(),
                  ui.row([
                    ui.text(ctx.t("inspector.project"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(projNameText, { size: "small", weight: "medium" }),
                  ]),
                  ui.text(projPathText, { variant: "mono", size: "small", tone: "muted" }),
                  ui.row([
                    ui.text(ctx.t("inspector.node"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(nodeText, { size: "small", weight: "medium" }),
                  ]),
                  ...(branchText ? [ui.row([
                    ui.text(ctx.t("inspector.branch"), { size: "small", tone: "muted", grow: 1 }),
                    ui.chip(branchText, { icon: "git-branch", variant: "soft", tone: "muted" }),
                  ])] : []),
                  ...(profileText ? [ui.row([
                    ui.text(ctx.t("inspector.profile"), { size: "small", tone: "muted", grow: 1 }),
                    ui.chip(profileText, { variant: "soft", tone: "accent" }),
                  ])] : []),
                  ...(approvalText ? [ui.row([
                    ui.text(ctx.t("inspector.approval"), { size: "small", tone: "muted", grow: 1 }),
                    ui.pill(approvalText, { icon: "shield-alert", tone: "warning" }),
                  ])] : []),
                  ...(parentText ? [ui.row([
                    ui.text(ctx.t("inspector.parent"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(parentText, { variant: "mono", size: "small", tone: "accent" }),
                  ])] : []),
                  ...(startedText ? [ui.row([
                    ui.text(ctx.t("inspector.started"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(startedText, { variant: "mono", size: "small", tone: "muted" }),
                  ])] : []),
                  ...(activityText ? [ui.row([
                    ui.text(ctx.t("inspector.lastActivity"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(activityText, { variant: "mono", size: "small", tone: "muted" }),
                  ])] : []),
                  ui.row([
                    ui.text(ctx.t("inspector.sessionId"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(sidText, { variant: "mono", size: "small" }),
                  ]),
                  ui.divider(),
                  ui.row([
                    ui.button(ctx.t("inspector.jump"), {
                      key: "btn-jump-session",
                      icon: "arrow-right",
                      variant: "primary",
                      grow: 1,
                      onTap: ["openSession", s.id],
                    }),
                    ui.button(ctx.t("inspector.copyId"), {
                      key: "btn-copy-sid",
                      icon: "copy",
                      variant: "outline",
                      onTap: ["copySessionId", s.id],
                    }),
                  ], { gap: 8 }),
                  ...(hasParent ? [ui.button(ctx.t("inspector.focusParent"), {
                    key: "btn-focus-parent",
                    icon: "pin",
                    variant: "ghost",
                    size: "small",
                    onTap: ["selectNode", `sess:${s.parentSessionId}`],
                  })] : []),
                ], { gap: 8 }),
              ], { key: "insp-sess-card", text: sessionLabel(s), icon: agentIcon(s.agent, waiting) }),
              ...(relMsgs.length > 0 ? [ui.card(
                relMsgs.map((msg, i) => ui.column([
                  ui.text(`${sanitizeText(msg.from, 16).slice(0, 6) || "?"} → ${sanitizeText(msg.to, 16).slice(0, 6) || "?"}`, { variant: "mono", size: "small", tone: "accent" }),
                  ui.text(sanitizeText(msg.text, 120), { size: "small" }),
                ], { key: `smsg-${i}`, gap: 2 })),
                { key: "insp-sess-msgs", text: ctx.t("inspector.recentMessages", { count: relMsgs.length }), icon: "message-square" },
              )] : []),
            ], { key: "insp-session", gap: 10 });
          } else if (selectedId && projectNodeMeta.has(selectedId)) {
            const { project: grp, machine: m } = projectNodeMeta.get(selectedId);
            const projNameText = sanitizeText(grp.name, 64) || sanitizeText(ctx.t("noFolder"), 64) || "project";
            const projSubText = sanitizeText(`${m.name} · ${sanitizeText(grp.key, 200) || ctx.t("noFolder")}`, 240) || "project";
            inspectorBody = ui.column([
              ui.card([
                ui.column([
                  ui.text(projSubText, { variant: "mono", size: "small", tone: "muted" }),
                  ui.text(ctx.t("inspector.projectSummary", {
                    running: grp.running,
                    working: grp.working,
                    waiting: grp.waiting,
                  }), { size: "small", tone: "accent" }),
                  ui.divider(),
                  ...grp.sessions.map((s) => ui.row([
                    ui.column([
                      ui.text(sessionLabel(s), { weight: "medium" }),
                      ui.text(sanitizeText(`${sanitizeText(s.agent, 24) || "agent"} · ${statusText(ctx, s)}`, 64) || "agent", { size: "small", tone: "muted" }),
                    ], { grow: 1, gap: 1 }),
                    ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
                    ui.button(ctx.t("inspector.open"), {
                      key: `pjump-${s.id}`,
                      icon: "arrow-right",
                      variant: "ghost",
                      size: "small",
                      onTap: ["openSession", s.id],
                    }),
                  ], { key: `prow-${s.id}`, gap: 6 })),
                ], { gap: 8 }),
              ], {
                key: "insp-proj-card",
                text: projNameText,
                icon: "folder",
                badges: [{ text: String(grp.sessions.length), tone: "accent" }],
              }),
            ], { key: "insp-project", gap: 10 });
          } else if (selectedId && machineNodeMeta.has(selectedId)) {
            const m = machineNodeMeta.get(selectedId);
            const machTitle = sanitizeText(m.name, 48) || "local";
            const machPlatform = sanitizeText([m.os || "unknown", m.arch].filter(Boolean).join(" / "), 64) || "unknown";
            const machLink = sanitizeText(m.link, 16) || "up";
            inspectorBody = ui.column([
              ui.card([
                ui.column([
                  ui.row([
                    ui.text(ctx.t("inspector.os"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(machPlatform, { variant: "mono", size: "small" }),
                  ]),
                  ui.row([
                    ui.text(ctx.t("inspector.link"), { size: "small", tone: "muted", grow: 1 }),
                    ui.pill(machLink, { tone: m.link === "down" ? "danger" : "success" }),
                  ]),
                  ui.divider(),
                  ...m.sessions.map((s) => ui.row([
                    ui.column([
                      ui.text(sessionLabel(s), { weight: "medium" }),
                      ui.text(sanitizeText(`${sanitizeText(s.agent, 24) || "agent"} · ${statusText(ctx, s)}`, 64) || "agent", { size: "small", tone: "muted" }),
                    ], { grow: 1, gap: 1 }),
                    ui.button(ctx.t("inspector.open"), {
                      key: `mjump-${s.id}`,
                      icon: "arrow-right",
                      variant: "ghost",
                      size: "small",
                      onTap: ["openSession", s.id],
                    }),
                  ], { key: `mrow-${s.id}`, gap: 6 })),
                ], { gap: 8 }),
              ], { key: "insp-mach-card", text: machTitle, icon: osIcon(m.os) }),
            ], { key: "insp-machine", gap: 10 });
          } else {
            const msgList = (state.messages || []).slice(-6).reverse();
            inspectorBody = ui.column([
              ui.alert(ctx.t("inspector.overviewTitle"), {
                key: "insp-hint",
                variant: "soft",
                tone: "accent",
                icon: "network",
                placeholder: ctx.t("inspector.overviewHint"),
              }),
              ui.card(
                machines.map((m) => ui.row([
                  ui.icon(osIcon(m.os), { tone: m.self ? "accent" : "success", size: "small" }),
                  ui.text(sanitizeText(m.name, 48) || "local", { weight: "medium", grow: 1 }),
                  ui.pill(String(m.sessions.length), { tone: "accent" }),
                  ui.button("", {
                    key: `focus-m-${m.keyId}`,
                    icon: "radar",
                    variant: "ghost",
                    size: "small",
                    onTap: ["selectNode", `mach:${m.keyId}`],
                  }),
                ], { key: `ov-m-node-${m.keyId}`, gap: 6 })),
                { key: "insp-machines-card", text: ctx.t("inspector.machinesCard"), icon: "server" },
              ),
              ui.card(
                groups.map((grp, gi) => ui.row([
                  ui.icon("folder", { tone: grp.running > 0 ? "accent" : "muted", size: "small" }),
                  ui.text(sanitizeText(grp.name, 64) || sanitizeText(ctx.t("noFolder"), 64) || "project", { weight: "medium", grow: 1 }),
                  ui.pill(`${grp.running}/${grp.sessions.length}`, { tone: grp.running > 0 ? "success" : "muted" }),
                ], { key: `ov-p-${gi}`, gap: 6 })),
                { key: "insp-projects-card", text: ctx.t("kpi.projects"), icon: "folder" },
              ),
              ui.card(
                msgList.length > 0
                  ? msgList.map((msg, i) => ui.column([
                      ui.text(`${sanitizeText(msg.from, 16).slice(0, 6) || "?"} → ${sanitizeText(msg.to, 16).slice(0, 6) || "?"}`, { variant: "mono", size: "small", tone: "accent" }),
                      ui.text(sanitizeText(msg.text, 100), { size: "small", tone: "muted" }),
                    ], { key: `ov-m-${i}`, gap: 2 }))
                  : [ui.text(ctx.t("inspector.noMessages"), { size: "small", tone: "muted" })],
                { key: "insp-msgs-card", text: ctx.t("inspector.recentMessages", { count: msgList.length }), icon: "message-square" },
              ),
            ], { key: "insp-overview", gap: 10 });
          }

          const inspectorPane = ui.scroll([inspectorBody], { key: "inspector-scroll" });
          mainSection = ui.split(graphPane, inspectorPane, {
            key: "map-split",
            direction: "horizontal",
            ratio: 0.68,
            grow: 1,
          });
        }

        const rootChildren = [toolbar, kpiStrip, mainSection];

        if (showContextMenu) {
          rootChildren.push(ui.contextMenu("node-menu", [
            ui.menuItem(ctx.t("ctx.openSession"), {
              key: "ctx-open",
              icon: "external-link",
              onTap: ["ctxOpenSession"],
            }),
            ui.menuItem(ctx.t("ctx.sendMessage"), {
              key: "ctx-send",
              icon: "message-square",
              onTap: ["ctxSendMessage"],
            }),
            ui.menuItem(ctx.t("ctx.runSubagent"), {
              key: "ctx-subagent",
              icon: "sparkles",
              onTap: ["ctxRunSubagent"],
            }),
            ui.divider(),
            ui.menuItem(ctx.t("ctx.killSession"), {
              key: "ctx-kill",
              icon: "trash-2",
              tone: "danger",
              onTap: ["ctxKillSession"],
            }),
          ]));
        }

        if (state.modal && state.modal.kind === "send") {
          rootChildren.push(ui.dialog(ctx.t("dialog.sendTitle"), [
            ui.textarea(sanitizeInline(state.modalText), {
              key: "dlg-input",
              placeholder: ctx.t("dialog.sendPlaceholder"),
              onChange: ["setModalText"],
            }),
            ui.row([
              ui.button(ctx.t("dialog.cancel"), {
                key: "dlg-cancel",
                variant: "outline",
                onTap: ["closeModal"],
              }),
              ui.button(ctx.t("dialog.sendSubmit"), {
                key: "dlg-submit",
                variant: "primary",
                icon: "send-horizontal",
                onTap: ["submitModal"],
              }),
            ], { gap: 8 }),
          ], { key: "action-dialog", onChange: ["closeModal"] }));
        } else if (state.modal && state.modal.kind === "subagent") {
          rootChildren.push(ui.dialog(ctx.t("dialog.subagentTitle"), [
            ui.textarea(sanitizeInline(state.modalText), {
              key: "dlg-input",
              placeholder: ctx.t("dialog.subagentPlaceholder"),
              onChange: ["setModalText"],
            }),
            ui.row([
              ui.button(ctx.t("dialog.cancel"), {
                key: "dlg-cancel",
                variant: "outline",
                onTap: ["closeModal"],
              }),
              ui.button(ctx.t("dialog.subagentSubmit"), {
                key: "dlg-submit",
                variant: "primary",
                icon: "sparkles",
                onTap: ["submitModal"],
              }),
            ], { gap: 8 }),
          ], { key: "action-dialog", onChange: ["closeModal"] }));
        }

        return ui.column(rootChildren, { fill: true, gap: 12 });
      },

      on: {
        setView(state, label) {
          const ctx = mako.blocks.context("map");
          if (label === ctx.t("view.projects")) state.view = "projects";
          else if (label === ctx.t("view.table")) state.view = "table";
          else state.view = "map";
        },
        setFilter(state, label) {
          const ctx = mako.blocks.context("map");
          if (label === ctx.t("filter.running")) state.filter = "running";
          else if (label === ctx.t("filter.working")) state.filter = "working";
          else if (label === ctx.t("filter.waiting")) state.filter = "waiting";
          else state.filter = "all";
        },
        setFilterKey(state, key) {
          state.filter = key || "all";
        },
        setLayout(state, label) {
          const ctx = mako.blocks.context("map");
          if (label === ctx.t("layout.hierarchy") || label === "hierarchy") state.layout = "hierarchy";
          else if (label === ctx.t("layout.radial") || label === "radial") state.layout = "radial";
          else state.layout = "force";
        },
        setQuery(state, val) {
          state.query = sanitizeInline(val);
        },
        clearFilters(state) {
          state.filter = "all";
          state.query = "";
          state.selectedNode = "";
        },
        selectNode(state, nodeId) {
          state.selectedNode = String(nodeId ?? "");
        },
        async saveNodePosition(state, pos) {
          const ctx = mako.blocks.context("map");
          if (!pos || typeof pos.id !== "string" || !pos.id) return;
          const x = Number(pos.x);
          const y = Number(pos.y);
          if (!Number.isFinite(x) || !Number.isFinite(y) || Math.abs(x) > 1e6 || Math.abs(y) > 1e6) return;
          state.positions = {
            ...(state.positions || {}),
            [pos.id]: { x, y },
          };
          if (ctx && ctx.storage) {
            try {
              await ctx.storage.setItem(POSITIONS_KEY, JSON.stringify(state.positions));
            } catch {
              // Storage quota or permission unavailable; keep in-memory position.
            }
          }
        },
        async openGraphNode(state, nodeId) {
          const ctx = mako.blocks.context("map");
          const sid = sessionIdFromNodeArg(nodeId);
          if (!sid) {
            state.selectedNode = String(nodeId ?? "");
            return;
          }
          const sess = (state.sessions || []).find((s) => s.id === sid);
          await ctx.ui.openSession({ sessionId: sid, node: (sess && sess.nodeId) || "" });
        },
        async ctxOpenSession(state, arg) {
          const ctx = mako.blocks.context("map");
          const sid = sessionIdFromNodeArg(arg);
          if (!sid) {
            state.selectedNode = String(arg ?? "");
            return;
          }
          const sess = (state.sessions || []).find((s) => s.id === sid);
          await ctx.ui.openSession({ sessionId: sid, node: (sess && sess.nodeId) || "" });
        },
        async ctxSendMessage(state, arg) {
          const ctx = mako.blocks.context("map");
          const sid = sessionIdFromNodeArg(arg);
          if (!sid) {
            await ctx.ui.toast(ctx.t("toast.actionUnavailable"));
            return;
          }
          state.modal = { kind: "send", sessionId: sid };
          state.modalText = "";
        },
        async ctxRunSubagent(state, arg) {
          const ctx = mako.blocks.context("map");
          const sid = sessionIdFromNodeArg(arg);
          if (!sid) {
            await ctx.ui.toast(ctx.t("toast.actionUnavailable"));
            return;
          }
          const sess = (state.sessions || []).find((s) => s.id === sid);
          state.modal = {
            kind: "subagent",
            parentSessionId: sid,
            agent: (sess && sess.agent && sess.agent !== "shell") ? sess.agent : "claude",
            root: (sess && sess.project) || "",
          };
          state.modalText = "";
        },
        async ctxKillSession(_state, arg) {
          const ctx = mako.blocks.context("map");
          const sid = sessionIdFromNodeArg(arg);
          if (!sid) {
            await ctx.ui.toast(ctx.t("toast.actionUnavailable"));
            return;
          }
          try {
            await mako.messages.stopAgent({ sessionId: sid });
            await ctx.ui.toast(ctx.t("toast.killed"));
            mako.blocks.reload("map");
          } catch (err) {
            await ctx.ui.toast(String((err && err.message) || err || "stop failed"));
          }
        },
        setModalText(state, val) {
          state.modalText = sanitizeInline(val);
        },
        closeModal(state) {
          state.modal = null;
          state.modalText = "";
        },
        async submitModal(state) {
          const ctx = mako.blocks.context("map");
          if (!state.modal) return;
          const text = String(state.modalText || "").trim();
          if (!text) return;
          const modal = state.modal;
          try {
            if (modal.kind === "send") {
              await mako.messages.sendMessage({ sessionId: modal.sessionId, text });
              state.modal = null;
              state.modalText = "";
              await ctx.ui.toast(ctx.t("toast.sent"));
            } else if (modal.kind === "subagent") {
              const targetRoot = modal.root || "";
              const normPath = (v) => String(v || "").replace(/\\/g, "/").replace(/\/+$/, "");
              const normTarget = normPath(targetRoot);
              let project = "";
              let root = targetRoot;
              const pr = await mako.files.listProjects();
              const projects = Array.isArray(pr && pr.projects) ? pr.projects : [];
              // 1. Exact match on root path.
              for (const p of projects) {
                const roots = Array.isArray(p && p.roots) ? p.roots : [];
                for (const r of roots) {
                  if (r === targetRoot || (normTarget && normPath(r) === normTarget)) {
                    project = p.id;
                    root = r;
                    break;
                  }
                }
                if (project) break;
              }
              // 2. Match if targetRoot is inside one of a project's registered roots (longest root wins).
              if (!project && normTarget) {
                let bestLen = -1;
                for (const p of projects) {
                  const roots = Array.isArray(p && p.roots) ? p.roots : [];
                  for (const r of roots) {
                    const nr = normPath(r);
                    if (!nr) continue;
                    const prefix = nr === "/" ? "/" : `${nr}/`;
                    if (normTarget.startsWith(prefix) && nr.length > bestLen) {
                      bestLen = nr.length;
                      project = p.id;
                      root = r;
                    }
                  }
                }
              }
              // 3. Fallback to first project with roots, setting BOTH project and root cleanly.
              if (!project) {
                for (const p of projects) {
                  const roots = Array.isArray(p && p.roots) ? p.roots : [];
                  if (roots.length > 0) {
                    project = p.id;
                    root = roots[0];
                    break;
                  }
                }
              }
              await mako.messages.startAgent({
                agent: modal.agent || "claude",
                project,
                root,
                prompt: text,
                parentSessionId: modal.parentSessionId,
              });
              state.modal = null;
              state.modalText = "";
              await ctx.ui.toast(ctx.t("toast.subagentAsked"));
            }
          } catch (err) {
            await ctx.ui.toast(String((err && err.message) || err || "action failed"));
          }
        },
        async refresh(state) {
          const ctx = mako.blocks.context("map");
          const snap = await fetchSnapshot();
          state.sessions = snap.sessions;
          state.nodes = snap.nodes;
          await ctx.ui.toast(ctx.t("toast.refreshed", { count: state.sessions.length, nodes: state.nodes.length }));
        },
        async openSession(state, sessionId) {
          const ctx = mako.blocks.context("map");
          if (!sessionId) return;
          const sess = (state.sessions || []).find((s) => s.id === sessionId);
          await ctx.ui.openSession({ sessionId, node: (sess && sess.nodeId) || "" });
        },
        async copySessionId(_state, sessionId) {
          const ctx = mako.blocks.context("map");
          if (!sessionId) return;
          await ctx.ui.copy(sessionId);
          await ctx.ui.toast(ctx.t("inspector.copied"));
        },
      },
    });

    mako.commands.register("refresh-map", async (ctx) => {
      const state = mako.blocks.state("map");
      const snap = await fetchSnapshot();
      state.sessions = snap.sessions;
      state.nodes = snap.nodes;
      mako.blocks.reload("map");
      if (ctx) {
        await ctx.ui.toast(ctx.t("toast.refreshed", { count: state.sessions.length, nodes: state.nodes.length }));
      }
    });
  },

  on: {
    sessionStarted() {
      mako.blocks.reload("map");
    },
    sessionEnded() {
      mako.blocks.reload("map");
    },
    sessionSlept() {
      mako.blocks.reload("map");
    },
    sessionStatus() {
      mako.blocks.reload("map");
    },
    agentMessage(ev) {
      const state = mako.blocks.state("map");
      const msg = ev && ev.message;
      if (msg && msg.fromSessionId && msg.toSessionId) {
        state.messages = [
          ...(state.messages || []).slice(-(MAX_MESSAGES - 1)),
          { from: msg.fromSessionId, to: msg.toSessionId, text: msg.text || "" },
        ];
      }
      mako.blocks.reload("map");
    },
  },
};
