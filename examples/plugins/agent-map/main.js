// Agent Map — live central-canvas view of running projects and all agents.
//
// Demonstrates Mako's left-sidebar navigation menu -> central content area
// plugin surface (`slots = ["sidebar"]`), live session subscriptions,
// interactive force-directed graph (`ui.graph`), split inspector, and
// one-click session navigation via `ctx.ui.openSession(...)`.

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
    "search.placeholder": "Filter projects or agents…",
    "kpi.projects": "Projects",
    "kpi.projectsDelta": "{active} active",
    "kpi.running": "Running",
    "kpi.runningDelta": "{total} total",
    "kpi.working": "Working",
    "kpi.workingDelta": "busy right now",
    "kpi.waiting": "Waiting",
    "kpi.waitingDelta": "needs attention",
    "map.caption": "Drag to pan · ⌘/Ctrl + scroll to zoom · Click any project or agent node to inspect",
    "map.emptyTitle": "No matching sessions",
    "map.emptyDesc": "Start an agent in any project or clear the current filter to populate the topology map.",
    "map.clearFilter": "Clear filters",
    "inspector.title": "Inspector",
    "inspector.overviewTitle": "Workspace Overview",
    "inspector.overviewHint": "Select a project hub or agent node on the map to inspect details and jump straight to its session.",
    "inspector.projectSummary": "{running} running · {working} working · {waiting} waiting",
    "inspector.agentsInProject": "Agents in this project ({count})",
    "inspector.recentMessages": "Recent inter-agent messages ({count})",
    "inspector.noMessages": "No inter-agent messages captured yet.",
    "inspector.jump": "Jump to session",
    "inspector.copyId": "Copy ID",
    "inspector.copied": "Copied session ID.",
    "inspector.agent": "Agent",
    "inspector.state": "State",
    "inspector.status": "Status",
    "inspector.project": "Project",
    "inspector.node": "Node",
    "inspector.sessionId": "Session ID",
    "inspector.open": "Open",
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
    "toast.refreshed": "Agent map refreshed ({count} sessions).",
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
    "search.placeholder": "프로젝트 또는 에이전트 검색…",
    "kpi.projects": "프로젝트",
    "kpi.projectsDelta": "활성 {active}개",
    "kpi.running": "실행 중",
    "kpi.runningDelta": "전체 {total}개",
    "kpi.working": "작업 중",
    "kpi.workingDelta": "현재 응답 생성",
    "kpi.waiting": "입력 대기",
    "kpi.waitingDelta": "확인 필요",
    "map.caption": "드래그로 이동 · ⌘/Ctrl + 스크롤로 확대/축소 · 노드를 클릭해 상세 확인 및 세션 이동",
    "map.emptyTitle": "표시할 세션이 없습니다",
    "map.emptyDesc": "프로젝트에서 에이전트를 실행하거나 필터를 초기화하면 전체 맵이 표시됩니다.",
    "map.clearFilter": "필터 초기화",
    "inspector.title": "인스펙터",
    "inspector.overviewTitle": "워크스페이스 요약",
    "inspector.overviewHint": "맵에서 프로젝트 허브나 에이전트 노드를 클릭하면 상세 정보를 보고 바로 세션으로 이동할 수 있습니다.",
    "inspector.projectSummary": "실행 {running} · 작업 {working} · 대기 {waiting}",
    "inspector.agentsInProject": "이 프로젝트의 에이전트 ({count})",
    "inspector.recentMessages": "최근 에이전트 간 메시지 ({count})",
    "inspector.noMessages": "아직 감지된 에이전트 간 메시지가 없습니다.",
    "inspector.jump": "세션으로 이동",
    "inspector.copyId": "ID 복사",
    "inspector.copied": "세션 ID를 복사했습니다.",
    "inspector.agent": "에이전트",
    "inspector.state": "프로세스",
    "inspector.status": "상태",
    "inspector.project": "프로젝트",
    "inspector.node": "노드",
    "inspector.sessionId": "세션 ID",
    "inspector.open": "이동",
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
    "toast.refreshed": "에이전트 맵을 새로고침했습니다 ({count}개 세션).",
    "noFolder": "(폴더 없음)",
  },
};

const MAX_GRAPH_SESSIONS = 80;
const MAX_MESSAGES = 16;

function sanitizeText(s, maxLen) {
  const clean = String(s || "").replace(/[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/g, " ").trim();
  if (clean.length <= maxLen) return clean;
  return clean.slice(0, Math.max(1, maxLen - 1)) + "…";
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
    if ((byBase.get(base) || []).length > 1) {
      const parts = f.replace(/\\/g, "/").replace(/\/+$/, "").split("/").filter(Boolean);
      labels.set(f, parts.slice(-2).join("/"));
    } else {
      labels.set(f, base);
    }
  }
  return labels;
}

function isRunning(s) {
  return s.state === "running" || s.state === "starting";
}

function sessionTone(s) {
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

function sessionLabel(s) {
  const raw = (s.name || "").trim();
  if (raw) return sanitizeText(raw, 40);
  const agent = (s.agent || "agent").trim();
  const tail = String(s.id || "").slice(0, 6);
  return sanitizeText(tail ? `${agent} · ${tail}` : agent, 40);
}

function statusText(ctx, s) {
  if (!isRunning(s)) {
    return ctx.t(`state.${s.state || "stopped"}`);
  }
  return ctx.t(`status.${s.status || "idle"}`);
}

function filterSessions(sessions, filter, query) {
  const q = String(query || "").trim().toLowerCase();
  return sessions.filter((s) => {
    if (filter === "running" && !isRunning(s)) return false;
    if (filter === "working" && !(isRunning(s) && s.status === "working")) return false;
    if (filter === "waiting" && !(isRunning(s) && s.status === "waiting")) return false;
    if (q) {
      const hay = `${s.name || ""} ${s.agent || ""} ${s.project || ""} ${s.id || ""} ${s.status || ""} ${s.state || ""}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
}

function groupByProject(sessions, noFolderLabel) {
  const names = projectDisplayNames(sessions.map((s) => s.project || ""));
  const map = new Map();
  for (const s of sessions) {
    const key = s.project || "";
    if (!map.has(key)) {
      map.set(key, {
        key,
        name: names.get(key) || noFolderLabel,
        sessions: [],
        running: 0,
        working: 0,
        waiting: 0,
      });
    }
    const grp = map.get(key);
    grp.sessions.push(s);
    if (isRunning(s)) {
      grp.running += 1;
      if (s.status === "working") grp.working += 1;
      if (s.status === "waiting") grp.waiting += 1;
    }
  }
  return [...map.values()].sort((a, b) => (
    (b.working - a.working) ||
    (b.waiting - a.waiting) ||
    (b.running - a.running) ||
    a.name.localeCompare(b.name)
  ));
}

function makeProjectNodeId(index) {
  return `proj:${index}`;
}

export default {
  async activate(mako) {
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
        messages: [],
        view: "map",
        filter: "all",
        query: "",
        selectedNode: "",
      },
      refresh: 15,

      async load(state) {
        const res = await mako.sessions.listSessions();
        state.sessions = Array.isArray(res.sessions) ? res.sessions : [];
      },

      render(state, ui, ctx) {
        const all = Array.isArray(state.sessions) ? state.sessions : [];
        const allGroups = groupByProject(all, ctx.t("noFolder"));
        const activeProjects = allGroups.filter((g) => g.running > 0).length;
        const totalRunning = all.filter(isRunning).length;
        const totalWorking = all.filter((s) => isRunning(s) && s.status === "working").length;
        const totalWaiting = all.filter((s) => isRunning(s) && s.status === "waiting").length;

        const filtered = filterSessions(all, state.filter, state.query);
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
          ui.input(state.query, {
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
            placeholder: ctx.t("kpi.projectsDelta", { active: activeProjects }),
            icon: "folder-kanban",
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
            icon: "bell",
            tone: totalWaiting > 0 ? "warning" : "muted",
            onTap: ["setFilterKey", "waiting"],
          }),
        ], { key: "kpi-grid", gap: 10 });

        let mainSection;

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
            const rows = grp.sessions.map((s) => ui.row([
              ui.icon("bot", { tone: sessionTone(s), size: "small" }),
              ui.column([
                ui.text(sessionLabel(s), { weight: "medium" }),
                ui.text(`${s.agent || "agent"} · ${statusText(ctx, s)}`, { size: "small", tone: "muted" }),
              ], { grow: 1, gap: 1 }),
              ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
              ui.button(ctx.t("inspector.open"), {
                key: `open-${s.id}`,
                icon: "arrow-right",
                variant: "ghost",
                size: "small",
                onTap: ["openSession", s.id],
              }),
            ], { key: `pcard-s-${s.id}`, gap: 8 }));
            return ui.card([
              ui.column([
                ui.text(grp.key || ctx.t("noFolder"), { variant: "mono", size: "small", tone: "muted" }),
                ui.divider(),
                ...rows,
              ], { gap: 8 }),
            ], {
              key: `pcard-${gi}`,
              text: grp.name,
              icon: "folder-kanban",
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
          const tableRows = filtered.map((s) => ui.row([
            ui.text(sessionLabel(s), { weight: "medium" }),
            ui.text(names.get(s.project || "") || ctx.t("noFolder"), { tone: "muted", size: "small" }),
            ui.chip(s.agent || "agent", { variant: "soft" }),
            ui.text(ctx.t(`state.${s.state || "stopped"}`), { size: "small", tone: isRunning(s) ? "success" : "muted" }),
            ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
            ui.button(ctx.t("inspector.open"), {
              key: `tbl-open-${s.id}`,
              icon: "arrow-right",
              variant: "outline",
              size: "small",
              onTap: ["openSession", s.id],
            }),
          ], { key: `tbl-row-${s.id}` }));
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
          // Interactive Topology Graph + Inspector split
          const graphNodes = [];
          const graphEdges = [];
          const projectNodeMeta = new Map();
          const sessionNodeMeta = new Map();
          const visibleSessionIds = new Set();

          let sessionCount = 0;
          groups.forEach((grp, gi) => {
            if (sessionCount >= MAX_GRAPH_SESSIONS) return;
            const pid = makeProjectNodeId(gi);
            const groupTag = sanitizeText(grp.name || `p${gi}`, 32);
            projectNodeMeta.set(pid, grp);
            graphNodes.push({
              id: pid,
              label: sanitizeText(grp.name, 48),
              group: groupTag,
              tone: grp.waiting > 0 ? "warning" : (grp.running > 0 ? "accent" : "muted"),
              subtitle: sanitizeText(`${grp.running}/${grp.sessions.length} ${ctx.t("state.running")}`, 60),
              weight: 2.2,
            });
            for (const s of grp.sessions) {
              if (sessionCount >= MAX_GRAPH_SESSIONS) break;
              sessionCount += 1;
              const sid = `sess:${s.id}`;
              visibleSessionIds.add(s.id);
              sessionNodeMeta.set(sid, { session: s, project: grp });
              graphNodes.push({
                id: sid,
                label: sessionLabel(s),
                group: groupTag,
                tone: sessionTone(s),
                subtitle: sanitizeText(`${s.agent || "agent"} · ${statusText(ctx, s)}`, 60),
                weight: isRunning(s) ? (s.status === "working" ? 1.5 : 1.25) : 0.85,
              });
              graphEdges.push({
                from: pid,
                to: sid,
                tone: sessionTone(s),
                dashed: !isRunning(s),
              });
            }
          });

          // Live inter-agent communication edges captured via on.agentMessage.
          const msgEdgeSeen = new Set();
          for (const msg of (state.messages || [])) {
            if (!msg.from || !msg.to || msg.from === msg.to) continue;
            if (!visibleSessionIds.has(msg.from) || !visibleSessionIds.has(msg.to)) continue;
            const edgeKey = `${msg.from}->${msg.to}`;
            if (msgEdgeSeen.has(edgeKey)) continue;
            msgEdgeSeen.add(edgeKey);
            graphEdges.push({
              from: `sess:${msg.from}`,
              to: `sess:${msg.to}`,
              label: "msg",
              tone: "accent",
              dashed: true,
            });
          }

          const selectedId = (projectNodeMeta.has(state.selectedNode) || sessionNodeMeta.has(state.selectedNode))
            ? state.selectedNode
            : "";

          const graphPane = ui.column([
            ui.graph(graphNodes, graphEdges, {
              key: "agent-topology",
              value: selectedId,
              maxHeight: 620,
              caption: ctx.t("map.caption"),
              onChange: ["selectNode"],
            }),
          ], { key: "graph-pane", gap: 8 });

          // Inspector content on right side of split
          let inspectorBody;
          if (selectedId && sessionNodeMeta.has(selectedId)) {
            const { session: s, project: grp } = sessionNodeMeta.get(selectedId);
            const relMsgs = (state.messages || []).filter((m) => m.from === s.id || m.to === s.id).slice(-5).reverse();
            inspectorBody = ui.column([
              ui.card([
                ui.column([
                  ui.row([
                    ui.chip(s.agent || "agent", { icon: "bot", variant: "soft", tone: "accent" }),
                    ui.pill(statusText(ctx, s), { tone: sessionTone(s) }),
                    ui.chip(ctx.t(`state.${s.state || "stopped"}`), { variant: "outline" }),
                  ], { gap: 6 }),
                  ui.divider(),
                  ui.row([
                    ui.text(ctx.t("inspector.project"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(grp.name, { size: "small", weight: "medium" }),
                  ]),
                  ui.text(s.project || ctx.t("noFolder"), { variant: "mono", size: "small", tone: "muted" }),
                  ui.row([
                    ui.text(ctx.t("inspector.sessionId"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(s.id, { variant: "mono", size: "small" }),
                  ]),
                  ...(s.nodeId ? [ui.row([
                    ui.text(ctx.t("inspector.node"), { size: "small", tone: "muted", grow: 1 }),
                    ui.text(s.nodeId, { variant: "mono", size: "small", tone: "muted" }),
                  ])] : []),
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
                ], { gap: 8 }),
              ], { key: "insp-sess-card", text: sessionLabel(s), icon: "bot" }),
              ...(relMsgs.length > 0 ? [ui.card(
                relMsgs.map((m, i) => ui.column([
                  ui.text(`${m.from.slice(0, 6)} → ${m.to.slice(0, 6)}`, { variant: "mono", size: "small", tone: "accent" }),
                  ui.text(sanitizeText(m.text, 120), { size: "small" }),
                ], { key: `smsg-${i}`, gap: 2 })),
                { key: "insp-sess-msgs", text: ctx.t("inspector.recentMessages", { count: relMsgs.length }), icon: "message-square" },
              )] : []),
            ], { key: "insp-session", gap: 10 });
          } else if (selectedId && projectNodeMeta.has(selectedId)) {
            const grp = projectNodeMeta.get(selectedId);
            inspectorBody = ui.column([
              ui.card([
                ui.column([
                  ui.text(grp.key || ctx.t("noFolder"), { variant: "mono", size: "small", tone: "muted" }),
                  ui.text(ctx.t("inspector.projectSummary", {
                    running: grp.running,
                    working: grp.working,
                    waiting: grp.waiting,
                  }), { size: "small", tone: "accent" }),
                  ui.divider(),
                  ...grp.sessions.map((s) => ui.row([
                    ui.column([
                      ui.text(sessionLabel(s), { weight: "medium" }),
                      ui.text(`${s.agent || "agent"} · ${statusText(ctx, s)}`, { size: "small", tone: "muted" }),
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
                text: grp.name,
                icon: "folder-kanban",
                badges: [{ text: String(grp.sessions.length), tone: "accent" }],
              }),
            ], { key: "insp-project", gap: 10 });
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
                groups.map((grp, gi) => ui.row([
                  ui.icon("folder-kanban", { tone: grp.running > 0 ? "accent" : "muted", size: "small" }),
                  ui.text(grp.name, { weight: "medium", grow: 1 }),
                  ui.pill(`${grp.running}/${grp.sessions.length}`, { tone: grp.running > 0 ? "success" : "muted" }),
                  ui.button("", {
                    key: `focus-p-${gi}`,
                    icon: "crosshair",
                    variant: "ghost",
                    size: "small",
                    onTap: ["selectNode", makeProjectNodeId(gi)],
                  }),
                ], { key: `ov-p-${gi}`, gap: 6 })),
                { key: "insp-projects-card", text: ctx.t("kpi.projects"), icon: "folder-kanban" },
              ),
              ui.card(
                msgList.length > 0
                  ? msgList.map((m, i) => ui.column([
                      ui.text(`${m.from.slice(0, 6)} → ${m.to.slice(0, 6)}`, { variant: "mono", size: "small", tone: "accent" }),
                      ui.text(sanitizeText(m.text, 100), { size: "small", tone: "muted" }),
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
            ratio: 0.66,
            grow: 1,
          });
        }

        return ui.column([
          toolbar,
          kpiStrip,
          mainSection,
        ], { fill: true, gap: 12 });
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
        setQuery(state, val) {
          state.query = String(val ?? "");
        },
        clearFilters(state) {
          state.filter = "all";
          state.query = "";
          state.selectedNode = "";
        },
        selectNode(state, nodeId) {
          state.selectedNode = String(nodeId ?? "");
        },
        async refresh(state) {
          const ctx = mako.blocks.context("map");
          const res = await mako.sessions.listSessions();
          state.sessions = Array.isArray(res.sessions) ? res.sessions : [];
          await ctx.ui.toast(ctx.t("toast.refreshed", { count: state.sessions.length }));
        },
        async openSession(_state, sessionId) {
          const ctx = mako.blocks.context("map");
          if (!sessionId) return;
          await ctx.ui.openSession(sessionId);
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
      const res = await mako.sessions.listSessions();
      state.sessions = Array.isArray(res.sessions) ? res.sessions : [];
      mako.blocks.reload("map");
      if (ctx) {
        await ctx.ui.toast(ctx.t("toast.refreshed", { count: state.sessions.length }));
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
