// ui-workbench — Revision 49 demo plugin showcasing:
// - Left sidebar plugin page (slots = ["sidebar"], chrome = "toolbar")
// - Multi-instance custom workspace tabs (slots = ["tab"], ctx.tab, openTab/closeTab)
// - All new & enhanced Revision 49 UI components:
//   diff (unified/split), alert (soft/outline/solid), empty, stat, radio, slider,
//   kbd, skeleton, spacer, code (syntax highlight + copy), input (search/password),
//   textarea (mono), table (sortable/striped/compact), card (header), progress (ring/spinner),
//   tabs (pills/underline), text (heading/mono/caption), plus EN/KO localization.

let mako;

const PRESETS = [
  {
    id: "diff-auth",
    section: "diff",
    titleKey: "preset.diffAuth.title",
    subtitleKey: "preset.diffAuth.sub",
    icon: "file-diff",
    badge: "+24 -6",
    tone: "accent",
    diffFrom: "internal/auth/token.go (before)",
    diffTo: "internal/auth/token.go (after)",
    language: "go",
    diffLines: [
      { op: "@", text: "@@ -42,11 +42,18 @@ func VerifyBearer(ctx context.Context, raw string) (*Claims, error) {", oldLine: 42, newLine: 42 },
      { op: " ", text: "\ttok := strings.TrimPrefix(raw, \"Bearer \")", oldLine: 43, newLine: 43 },
      { op: "-", text: "\tif tok == \"\" {", oldLine: 44, newLine: 0 },
      { op: "-", text: "\t\treturn nil, ErrUnauthorized", oldLine: 45, newLine: 0 },
      { op: "+", text: "\tif tok == raw || strings.TrimSpace(tok) == \"\" {", oldLine: 0, newLine: 44 },
      { op: "+", text: "\t\treturn nil, fmt.Errorf(\"%w: missing bearer prefix\", ErrUnauthorized)", oldLine: 0, newLine: 45 },
      { op: " ", text: "\t}", oldLine: 46, newLine: 46 },
      { op: " ", text: "\tclaims, err := parseJWT(tok)", oldLine: 47, newLine: 47 },
      { op: " ", text: "\tif err != nil {", oldLine: 48, newLine: 48 },
      { op: "-", text: "\t\treturn nil, err", oldLine: 49, newLine: 0 },
      { op: "+", text: "\t\treturn nil, fmt.Errorf(\"verify token: %w\", err)", oldLine: 0, newLine: 49 },
      { op: " ", text: "\t}", oldLine: 50, newLine: 50 },
      { op: "+", text: "\tif claims.ExpiresAt.Before(time.Now().Add(-clockSkew)) {", oldLine: 0, newLine: 51 },
      { op: "+", text: "\t\treturn nil, ErrTokenExpired", oldLine: 0, newLine: 52 },
      { op: "+", text: "\t}", oldLine: 0, newLine: 53 },
      { op: " ", text: "\treturn claims, nil", oldLine: 54, newLine: 54 },
      { op: " ", text: "}", oldLine: 55, newLine: 55 },
    ],
    snippet: `// Strict Bearer prefix + clock skew check
func VerifyBearer(ctx context.Context, raw string) (*Claims, error) {
\ttok := strings.TrimPrefix(raw, "Bearer ")
\tif tok == raw || strings.TrimSpace(tok) == "" {
\t\treturn nil, fmt.Errorf("%w: missing bearer prefix", ErrUnauthorized)
\t}
\treturn verifyExpiry(tok, 30*time.Second)
}`,
  },
  {
    id: "studio-ui",
    section: "components",
    titleKey: "preset.studio.title",
    subtitleKey: "preset.studio.sub",
    icon: "sliders-horizontal",
    badge: "Rev 49",
    tone: "success",
  },
  {
    id: "diff-wasm",
    section: "diff",
    titleKey: "preset.diffWasm.title",
    subtitleKey: "preset.diffWasm.sub",
    icon: "code-xml",
    badge: "+12 -3",
    tone: "warning",
    diffFrom: "plugin/wasm_runtime.go (v1)",
    diffTo: "plugin/wasm_runtime.go (v2)",
    language: "go",
    diffLines: [
      { op: "@", text: "@@ -168,8 +168,14 @@ func startWasmCoreRuntime(...) (*wasmCoreRuntime, error) {", oldLine: 168, newLine: 168 },
      { op: " ", text: "\tstopAfter := armWasmBudget(ctx, engine, w.store)", oldLine: 169, newLine: 169 },
      { op: " ", text: "\tinst, err := w.linker.Instantiate(w.store, mod)", oldLine: 170, newLine: 170 },
      { op: "+", text: "\tif err == nil {", oldLine: 0, newLine: 171 },
      { op: "+", text: "\t\tif initFn := inst.GetFunc(w.store, \"_initialize\"); initFn != nil {", oldLine: 0, newLine: 172 },
      { op: "+", text: "\t\t\t_, err = initFn.Call(w.store)", oldLine: 0, newLine: 173 },
      { op: "+", text: "\t\t}", oldLine: 0, newLine: 174 },
      { op: "+", text: "\t}", oldLine: 0, newLine: 175 },
      { op: " ", text: "\tstopAfter()", oldLine: 171, newLine: 176 },
    ],
    snippet: `// WASI reactor initialization
if initFn := inst.GetFunc(w.store, "_initialize"); initFn != nil {
\t_, err = initFn.Call(w.store)
}`,
  },
  {
    id: "instance-notes",
    section: "instance",
    titleKey: "preset.instance.title",
    subtitleKey: "preset.instance.sub",
    icon: "layers",
    badge: "Multi-tab",
    tone: "muted",
  },
  {
    id: "canvas-lab",
    section: "canvas",
    titleKey: "preset.canvas.title",
    subtitleKey: "preset.canvas.sub",
    icon: "palette",
    badge: "2D Vector",
    tone: "accent",
  },
  {
    id: "timeline-lab",
    section: "timeline",
    titleKey: "preset.timeline.title",
    subtitleKey: "preset.timeline.sub",
    icon: "clock",
    badge: "Spans",
    tone: "success",
  },
  {
    id: "charts-lab",
    section: "charts",
    titleKey: "preset.charts.title",
    subtitleKey: "preset.charts.sub",
    icon: "sparkles",
    badge: "3 Charts",
    tone: "warning",
  },
  {
    id: "graph-lab",
    section: "graph",
    titleKey: "preset.graph.title",
    subtitleKey: "preset.graph.sub",
    icon: "network",
    badge: "Cards",
    tone: "accent",
  },
];

const BENCH_ROWS = [
  { name: "ui.diff", category: "Display", rev: 49, latency: 1.4, status: "Ready" },
  { name: "ui.slider", category: "Input", rev: 49, latency: 0.6, status: "Ready" },
  { name: "ui.stat", category: "Metric", rev: 49, latency: 0.4, status: "Ready" },
  { name: "ui.alert", category: "Feedback", rev: 49, latency: 0.5, status: "Ready" },
  { name: "ui.skeleton", category: "Loading", rev: 49, latency: 0.3, status: "Ready" },
  { name: "ui.code", category: "Display", rev: 49, latency: 2.1, status: "Enhanced" },
  { name: "ui.table", category: "Data", rev: 49, latency: 1.1, status: "Enhanced" },
];

const I18N = {
  en: {
    "sidebar.title": "Workbench",
    "sidebar.search": "Filter demos…",
    "sidebar.stat.demos": "Demos",
    "sidebar.stat.demosDelta": "8 presets",
    "sidebar.stat.comments": "Notes",
    "sidebar.stat.commentsDelta": "Saved locally",
    "sidebar.coverage": "Rev 49 + M1/M3 UI",
    "sidebar.openPreview": "Click to preview tab, pin icon to keep open",
    "sidebar.emptyTitle": "No matching demos",
    "sidebar.emptyDesc": "Try another keyword or clear the search filter.",
    "sidebar.clearSearch": "Clear search",
    "sidebar.newTab": "Open new tab",
    "sidebar.reset": "Reset demo data",
    "preset.diffAuth.title": "Auth Token Diff Review",
    "preset.diffAuth.sub": "Unified/Split diff + inline line notes",
    "preset.studio.title": "UI Component Studio",
    "preset.studio.sub": "Slider, ring, stat, sortable table, skeleton",
    "preset.diffWasm.title": "Wasm Reactor Diff",
    "preset.diffWasm.sub": "Second diff tab instance (multi-tab)",
    "preset.instance.title": "Multi-Instance Tab Lab",
    "preset.instance.sub": "Inspect ctx.tab key, params & closeTab",
    "preset.canvas.title": "2D Vector Canvas Lab",
    "preset.canvas.sub": "Theme-token ops, pan/zoom & hitId",
    "preset.timeline.title": "Execution Timeline Lab",
    "preset.timeline.sub": "Grouped lanes, agent turns & CI spans",
    "preset.charts.title": "Interactive Charts Lab",
    "preset.charts.sub": "Area, stacked-bar, heatmap + onChange",
    "preset.graph.title": "Interactive Graph Lab",
    "preset.graph.sub": "Cards, cluster hulls, layouts & minimap",
    "tab.diff": "Diff Review",
    "tab.components": "Component Studio",
    "tab.canvas": "Canvas",
    "tab.timeline": "Timeline",
    "tab.charts": "Charts",
    "tab.graph": "Graph",
    "tab.instance": "Tab Instance",
    "tab.spawn": "New Tab",
    "tab.close": "Close",
    "diff.alertTitle": "Interactive Revision 49 Diff Component",
    "diff.alertBody": "Switch between Unified and Split views, or click any line below to attach a persistent review note.",
    "diff.modeUnified": "Unified",
    "diff.modeSplit": "Split",
    "diff.selectedLine": "Line {line} selected",
    "diff.noLine": "Click any diff line to attach a review comment.",
    "diff.notePlaceholder": "Write a review comment for this line…",
    "diff.saveNote": "Save comment",
    "diff.clearNotes": "Clear comments",
    "diff.savedNotes": "Saved line comments ({count})",
    "diff.snippetTitle": "Highlighted Code Block",
    "studio.kpi.throughput": "Throughput",
    "studio.kpi.latency": "Render p95",
    "studio.kpi.health": "Sandbox",
    "studio.controlsTitle": "Interactive Controls",
    "studio.sliderLabel": "Target completion ({pct}%)",
    "studio.passwordPlaceholder": "Demo secret token (masked)",
    "studio.pill.overview": "Overview",
    "studio.pill.alerts": "Alerts",
    "studio.pill.skeleton": "Skeleton & Empty",
    "studio.tableTitle": "Sortable Component Matrix",
    "studio.tableStyle": "Table style",
    "studio.skeletonToggle": "Preview loading skeletons",
    "studio.emptyTitle": "Empty State Component",
    "studio.emptyDesc": "Use ui.empty when a filter or workspace scope has no items yet.",
    "studio.emptyAction": "Show toast",
    "studio.alertSoft": "Soft informational callout with action.",
    "studio.alertOutline": "Outline warning callout for attention.",
    "studio.alertSolid": "Solid accent callout for primary highlights.",
    "inst.cardTitle": "Active Tab Metadata (ctx.tab)",
    "inst.noteLabel": "Per-tab persistent scratchpad",
    "inst.notePlaceholder": "Notes typed here are saved per tab key in plugin storage…",
    "inst.save": "Save scratchpad",
    "inst.copyKey": "Copy tab key",
    "inst.openSibling": "Open sibling tab",
    "inst.closeSelf": "Close this tab",
    "toast.saved": "Saved to plugin storage.",
    "toast.reset": "Demo data reset.",
    "toast.copied": "Copied tab key to clipboard.",
    "toast.emptyPing": "Action triggered from ui.empty!",
    "canvas.title": "Sandboxed 2D Vector Canvas",
    "canvas.caption": "Drag to pan, scroll/buttons to zoom, click any region to inspect its hitId.",
    "canvas.reset": "Clear hit",
    "canvas.inspectorTitle": "Canvas Hit Inspector",
    "canvas.selectedHit": "Selected hitId: {hit}",
    "canvas.emptyHint": "Click any shape, icon, or Bezier edge on the canvas to inspect its hitId.",
    "timeline.title": "Agent & CI Execution Timeline",
    "timeline.caption": "Grouped lanes across orchestrator turns, parallel subagents, and CI checks.",
    "timeline.reset": "Clear span",
    "timeline.inspectorTitle": "Timeline Span Inspector",
    "timeline.selectedSpan": "{label} ({id}) · lane={lane} · kind={kind} · {start}–{end} ({dur})",
    "timeline.emptyHint": "Click any span bar on the timeline to inspect its timing and metadata.",
    "charts.title": "Area, Stacked-Bar & Heatmap Charts",
    "charts.reset": "Clear point",
    "charts.areaTitle": "Throughput vs Cache Hits (Area)",
    "charts.stackedTitle": "Milestone Gate Outcomes (Stacked Bar)",
    "charts.heatmapTitle": "Subsystem Activity Matrix (Heatmap)",
    "charts.inspectorTitle": "Chart Selection Inspector",
    "charts.emptyHint": "Click any point, bar, or heatmap cell to inspect its selection payload.",
    "graph.title": "Interactive Topology Graph",
    "graph.caption": "Drag nodes to pin positions in storage, switch layouts, double-tap or right-click.",
    "graph.resetPos": "Reset pins",
    "graph.ctxInspect": "Inspect node",
    "graph.inspectorTitle": "Graph Node Inspector",
    "graph.selectedNode": "Selected node: {node}",
    "graph.lastDoubleTap": "Last double-tap: {node}",
    "graph.pinnedCount": "Pinned nodes in storage: {count}",
    "graph.emptyHint": "Select, double-tap, or drag a node to inspect selection and storage persistence.",
    "graph.toastDoubleTap": "Focused graph node {node}",
    "graph.toastContext": "Inspected {node} from context menu",
  },
  ko: {
    "sidebar.title": "워크벤치",
    "sidebar.search": "데모 검색…",
    "sidebar.stat.demos": "데모",
    "sidebar.stat.demosDelta": "8개 프리셋",
    "sidebar.stat.comments": "메모",
    "sidebar.stat.commentsDelta": "로컬 저장됨",
    "sidebar.coverage": "Rev 49 + M1/M3 UI",
    "sidebar.openPreview": "클릭 시 프리뷰 탭, 핀 클릭 시 고정 탭으로 열기",
    "sidebar.emptyTitle": "일치하는 데모 없음",
    "sidebar.emptyDesc": "다른 키워드를 입력하거나 검색 필터를 지워 보세요.",
    "sidebar.clearSearch": "검색 지우기",
    "sidebar.newTab": "새 탭 열기",
    "sidebar.reset": "데모 데이터 초기화",
    "preset.diffAuth.title": "인증 토큰 Diff 리뷰",
    "preset.diffAuth.sub": "Unified/Split Diff + 라인별 코멘트",
    "preset.studio.title": "UI 컴포넌트 스튜디오",
    "preset.studio.sub": "슬라이더, 링, 스탯, 정렬 테이블, 스켈레톤",
    "preset.diffWasm.title": "Wasm 리액터 Diff",
    "preset.diffWasm.sub": "두 번째 Diff 탭 인스턴스 (멀티 탭)",
    "preset.instance.title": "멀티 인스턴스 탭 랩",
    "preset.instance.sub": "ctx.tab 키/파라미터 및 closeTab 확인",
    "preset.canvas.title": "2D 벡터 캔버스 랩",
    "preset.canvas.sub": "테마 토큰 명령, 팬/줌 및 hitId 검사",
    "preset.timeline.title": "실행 타임라인 랩",
    "preset.timeline.sub": "그룹 레인, 에이전트 턴 및 CI 스팬",
    "preset.charts.title": "인터랙티브 차트 랩",
    "preset.charts.sub": "Area, Stacked-Bar, Heatmap + onChange",
    "preset.graph.title": "인터랙티브 그래프 랩",
    "preset.graph.sub": "카드 노드, 클러스터 Hull, 레이아웃 및 미니맵",
    "tab.diff": "Diff 리뷰",
    "tab.components": "컴포넌트 스튜디오",
    "tab.canvas": "캔버스",
    "tab.timeline": "타임라인",
    "tab.charts": "차트",
    "tab.graph": "그래프",
    "tab.instance": "탭 인스턴스",
    "tab.spawn": "새 탭",
    "tab.close": "닫기",
    "diff.alertTitle": "인터랙티브 Revision 49 Diff 컴포넌트",
    "diff.alertBody": "Unified/Split 보기를 전환하거나 아래 Diff 라인을 클릭해 리뷰 코멘트를 남길 수 있습니다.",
    "diff.modeUnified": "Unified",
    "diff.modeSplit": "Split",
    "diff.selectedLine": "{line}번째 줄 선택됨",
    "diff.noLine": "Diff 줄을 클릭하면 리뷰 코멘트를 작성할 수 있습니다.",
    "diff.notePlaceholder": "선택한 줄에 대한 리뷰 코멘트를 입력하세요…",
    "diff.saveNote": "코멘트 저장",
    "diff.clearNotes": "코멘트 비우기",
    "diff.savedNotes": "저장된 라인 코멘트 ({count})",
    "diff.snippetTitle": "구문 강조 코드 블록",
    "studio.kpi.throughput": "처리량",
    "studio.kpi.latency": "렌더 p95",
    "studio.kpi.health": "샌드박스",
    "studio.controlsTitle": "인터랙티브 컨트롤",
    "studio.sliderLabel": "목표 진행률 ({pct}%)",
    "studio.passwordPlaceholder": "데모 시크릿 토큰 (마스킹)",
    "studio.pill.overview": "개요",
    "studio.pill.alerts": "알림 배너",
    "studio.pill.skeleton": "스켈레톤 & 빈 상태",
    "studio.tableTitle": "정렬 가능한 컴포넌트 테이블",
    "studio.tableStyle": "테이블 스타일",
    "studio.skeletonToggle": "로딩 스켈레톤 미리보기",
    "studio.emptyTitle": "Empty State 컴포넌트",
    "studio.emptyDesc": "필터 결과나 항목이 없을 때 ui.empty로 안내와 액션을 표시합니다.",
    "studio.emptyAction": "토스트 띄우기",
    "studio.alertSoft": "액션 버튼이 포함된 Soft 안내 배너입니다.",
    "studio.alertOutline": "주의가 필요한 항목을 알리는 Outline 경고 배너입니다.",
    "studio.alertSolid": "핵심 상태를 강조하는 Solid 액센트 배너입니다.",
    "inst.cardTitle": "현재 탭 메타데이터 (ctx.tab)",
    "inst.noteLabel": "탭 인스턴스별 영구 메모",
    "inst.notePlaceholder": "여기에 입력한 메모는 탭 key별로 플러그인 스토리지에 저장됩니다…",
    "inst.save": "메모 저장",
    "inst.copyKey": "탭 키 복사",
    "inst.openSibling": "새 인스턴스 탭 열기",
    "inst.closeSelf": "현재 탭 닫기",
    "toast.saved": "플러그인 스토리지에 저장했습니다.",
    "toast.reset": "데모 데이터를 초기화했습니다.",
    "toast.copied": "탭 키를 클립보드에 복사했습니다.",
    "toast.emptyPing": "ui.empty 버튼이 정상 동작했습니다!",
    "canvas.title": "샌드박스 2D 벡터 캔버스",
    "canvas.caption": "드래그로 팬, 버튼/휠로 줌, 도형이나 연결선을 클릭해 hitId를 확인하세요.",
    "canvas.reset": "선택 지우기",
    "canvas.inspectorTitle": "캔버스 히트 인스펙터",
    "canvas.selectedHit": "선택된 hitId: {hit}",
    "canvas.emptyHint": "캔버스의 도형, 아이콘 또는 Bézier 연결선을 클릭하면 hitId가 표시됩니다.",
    "timeline.title": "에이전트 및 CI 실행 타임라인",
    "timeline.caption": "오케스트레이터 턴, 병렬 서브에이전트, CI 검증 스팬을 시간축으로 표시합니다.",
    "timeline.reset": "선택 지우기",
    "timeline.inspectorTitle": "타임라인 스팬 인스펙터",
    "timeline.selectedSpan": "{label} ({id}) · 레인={lane} · 종류={kind} · {start}–{end} ({dur})",
    "timeline.emptyHint": "타임라인의 스팬 막대를 클릭하면 상세 메타데이터와 소요 시간이 표시됩니다.",
    "charts.title": "Area · Stacked-Bar · Heatmap 차트",
    "charts.reset": "선택 지우기",
    "charts.areaTitle": "처리량 vs 캐시 적중 (Area)",
    "charts.stackedTitle": "마일스톤 게이트 결과 (Stacked Bar)",
    "charts.heatmapTitle": "서브시스템 활동 매트릭스 (Heatmap)",
    "charts.inspectorTitle": "차트 선택 인스펙터",
    "charts.emptyHint": "데이터 포인트, 막대 또는 히트맵 셀을 클릭하면 선택 페이로드가 표시됩니다.",
    "graph.title": "인터랙티브 토폴로지 그래프",
    "graph.caption": "노드 드래그로 좌표 저장, 레이아웃 전환, 더블클릭 및 우클릭 컨텍스트 메뉴를 지원합니다.",
    "graph.resetPos": "핀 초기화",
    "graph.ctxInspect": "노드 검사",
    "graph.inspectorTitle": "그래프 노드 인스펙터",
    "graph.selectedNode": "선택된 노드: {node}",
    "graph.lastDoubleTap": "최근 더블탭: {node}",
    "graph.pinnedCount": "스토리지에 저장된 핀 좌표: {count}개",
    "graph.emptyHint": "노드를 클릭, 더블탭 또는 드래그하면 선택 상태와 좌표 저장이 반영됩니다.",
    "graph.toastDoubleTap": "그래프 노드 포커스: {node}",
    "graph.toastContext": "컨텍스트 메뉴에서 검사함: {node}",
  },
};

const MAX_GRAPH_COORD = 1e6;

function utf8ByteLen(cp) {
  if (cp <= 0x7f) return 1;
  if (cp <= 0x7ff) return 2;
  if (cp <= 0xffff) return 3;
  return 4;
}

function truncateUtf8(str, maxBytes) {
  if (!str || maxBytes <= 0) return "";
  let total = 0;
  let out = "";
  for (const ch of str) {
    const b = utf8ByteLen(ch.codePointAt(0));
    if (total + b > maxBytes) break;
    total += b;
    out += ch;
  }
  return out;
}

function sanitizeInlineText(s, maxBytes = 4096) {
  const clean = String(s ?? "")
    .replace(/[\u200b-\u200d\ufeff]/g, "")
    .replace(/[\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/g, " ");
  return truncateUtf8(clean, maxBytes);
}

function sanitizeMultilineText(s, maxBytes = 4096) {
  const clean = String(s ?? "")
    .replace(/[\u200b-\u200d\ufeff]/g, "")
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/g, " ");
  return truncateUtf8(clean, maxBytes);
}

function sanitizeKey(s) {
  const clean = String(s ?? "")
    .replace(/[/#\u200b-\u200d\ufeff\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/g, "")
    .trim();
  return truncateUtf8(clean, 100);
}

function isValidGraphPos(v) {
  return Boolean(
    v &&
    typeof v === "object" &&
    !Array.isArray(v) &&
    typeof v.x === "number" &&
    typeof v.y === "number" &&
    Number.isFinite(v.x) &&
    Number.isFinite(v.y) &&
    Math.abs(v.x) <= MAX_GRAPH_COORD &&
    Math.abs(v.y) <= MAX_GRAPH_COORD
  );
}

function maxCommentSeq(comments) {
  let maxSeq = 0;
  for (const c of comments) {
    if (!c || typeof c.id !== "string") continue;
    const m = /^c([1-9]\d*)$/.exec(c.id);
    if (m) {
      const n = Number(m[1]);
      if (Number.isSafeInteger(n) && n > maxSeq) {
        maxSeq = n;
      }
    }
  }
  return maxSeq;
}

function normalizeComments(parsed) {
  if (!Array.isArray(parsed)) return [];
  const out = [];
  const seen = new Set(["comment-draft"]);
  let fallbackSeq = 1;
  for (const c of parsed) {
    if (!c || typeof c !== "object" || Array.isArray(c)) continue;
    if (typeof c.line !== "number" || !Number.isFinite(c.line)) continue;
    const line = Math.floor(c.line);
    if (line < 1 || !Number.isSafeInteger(line)) continue;
    if (typeof c.text !== "string") continue;
    const text = sanitizeInlineText(c.text, 4096);
    let id = typeof c.id === "string" ? sanitizeKey(c.id) : "";
    if (!id) {
      while (seen.has(`c${fallbackSeq}`)) fallbackSeq += 1;
      id = `c${fallbackSeq}`;
      fallbackSeq += 1;
    } else if (seen.has(id)) {
      let suffix = 2;
      while (seen.has(`${id}-${suffix}`)) suffix += 1;
      id = `${id}-${suffix}`;
    }
    seen.add(id);
    out.push({ id, line, text });
  }
  return out;
}

function findPreset(id) {
  return PRESETS.find((p) => p.id === id) || PRESETS[0];
}

async function loadSavedComments(ctx, key) {
  try {
    const res = await ctx.storage.getItem(`comments:${key}`);
    if (res.found && res.value) {
      const parsed = JSON.parse(res.value);
      return normalizeComments(parsed);
    }
  } catch {}
  return [];
}

async function countAllNotes(ctx) {
  try {
    const res = await ctx.storage.listKeys("comments:");
    const keys = Array.isArray(res.keys) ? res.keys : [];
    let total = 0;
    for (const k of keys) {
      const item = await ctx.storage.getItem(k);
      if (item.found && item.value) {
        try {
          const arr = JSON.parse(item.value);
          total += normalizeComments(arr).length;
        } catch {}
      }
    }
    return total;
  } catch {
    return 0;
  }
}

function sortedBenchRows(sortExpr) {
  const [col, dir] = String(sortExpr || "Component:asc").split(":");
  const mul = dir === "desc" ? -1 : 1;
  const rows = [...BENCH_ROWS];
  rows.sort((a, b) => {
    switch (col) {
      case "Category":
        return a.category.localeCompare(b.category) * mul;
      case "Latency (ms)":
        return (a.latency - b.latency) * mul;
      case "Status":
        return a.status.localeCompare(b.status) * mul;
      default:
        return a.name.localeCompare(b.name) * mul;
    }
  });
  return rows;
}

export default {
  activate(m) {
    mako = m;
    mako.i18n.register(I18N);

    // 1. Left Sidebar Page block (slots = ["sidebar", "left", "right"])
    mako.blocks.register("explorer", {
      state: {
        query: "",
        totalNotes: 0,
        tabCounter: 1,
      },
      async load(s, ctx) {
        s.totalNotes = await countAllNotes(ctx);
      },
      render(s, ui, ctx) {
        const t = ctx.t;
        const q = (s.query || "").trim().toLowerCase();
        const filtered = PRESETS.filter((p) => {
          if (!q) return true;
          const hay = `${t(p.titleKey)} ${t(p.subtitleKey)} ${p.id} ${p.badge}`.toLowerCase();
          return hay.includes(q);
        });

        const toolbar = ui.toolbar([
          ui.text(t("sidebar.title"), { weight: "semibold", grow: 1 }),
          ui.button("", {
            key: "tb-new",
            icon: "plus",
            variant: "ghost",
            size: "small",
            placeholder: t("sidebar.newTab"),
            onTap: ["openCustomInstance"],
          }),
          ui.button("", {
            key: "tb-refresh",
            icon: "refresh-cw",
            variant: "ghost",
            size: "small",
            onTap: ["refresh"],
          }),
          ui.button("", {
            key: "tb-reset",
            icon: "trash-2",
            variant: "ghost",
            size: "small",
            placeholder: t("sidebar.reset"),
            onTap: ["resetAll"],
          }),
        ], { key: "tb" });

        const stats = ui.grid(2, [
          ui.stat(t("sidebar.stat.demos"), String(PRESETS.length), {
            key: "st-demos",
            icon: "flask-conical",
            tone: "accent",
            size: "small",
            placeholder: t("sidebar.stat.demosDelta"),
            onTap: ["openPreset", "studio-ui", false],
          }),
          ui.stat(t("sidebar.stat.comments"), String(s.totalNotes), {
            key: "st-notes",
            icon: "file-diff",
            tone: s.totalNotes > 0 ? "success" : "muted",
            size: "small",
            placeholder: t("sidebar.stat.commentsDelta"),
            onTap: ["openPreset", "diff-auth", false],
          }),
        ], { key: "stats", gap: 6 });

        const coverage = ui.card([
          ui.row([
            ui.progress(1.0, { key: "ring", variant: "ring", tone: "accent" }),
            ui.column([
              ui.text(t("sidebar.coverage"), { size: "small", weight: "medium" }),
              ui.text("Canvas · Timeline · Charts · Graph", { variant: "caption", tone: "muted" }),
            ], { grow: 1, gap: 2 }),
            ui.pill("100%", { tone: "success", variant: "soft" }),
          ], { gap: 8 }),
        ], { key: "cov", variant: "soft" });

        const search = ui.input(s.query, {
          key: "search",
          variant: "search",
          icon: "search",
          placeholder: t("sidebar.search"),
          onChange: ["setQuery"],
          onSubmit: ["setQuery"],
        });

        let listContent;
        if (filtered.length === 0) {
          listContent = ui.empty(t("sidebar.emptyTitle"), [
            ui.button(t("sidebar.clearSearch"), {
              key: "clear-search",
              variant: "outline",
              size: "small",
              onTap: ["setQuery", ""],
            }),
          ], {
            key: "empty-list",
            icon: "search",
            tone: "muted",
            placeholder: t("sidebar.emptyDesc"),
          });
        } else {
          listContent = ui.list(filtered, (p) => ui.card([
            ui.row([
              ui.icon(p.icon, { tone: p.tone }),
              ui.column([
                ui.text(t(p.titleKey), { weight: "medium", size: "small" }),
                ui.text(t(p.subtitleKey), { variant: "caption", tone: "muted" }),
              ], { grow: 1, gap: 2 }),
              ui.button("", {
                key: `pin-${p.id}`,
                icon: "pin",
                variant: "ghost",
                size: "small",
                onTap: ["openPreset", p.id, false],
              }),
            ], { gap: 8 }),
          ], {
            key: `item-${p.id}`,
            variant: "outline",
            badges: [{ text: p.badge, tone: p.tone, variant: "soft" }],
            onTap: ["openPreset", p.id, true],
          }), { key: "preset-list", gap: 6 });
        }

        return ui.column([
          toolbar,
          ui.scroll([
            ui.column([
              stats,
              coverage,
              search,
              ui.text(t("sidebar.openPreview"), { variant: "caption", tone: "faint" }),
              listContent,
            ], { gap: 10 }),
          ], { key: "sb-scroll", grow: 1 }),
        ], { fill: true, gap: 8 });
      },
      on: {
        setQuery(s, val) {
          s.query = String(val || "");
        },
        async refresh(s) {
          const ctx = mako.blocks.context("explorer");
          s.totalNotes = await countAllNotes(ctx);
        },
        async openPreset(s, presetId, preview) {
          const ctx = mako.blocks.context("explorer");
          const p = findPreset(presetId);
          await ctx.ui.openTab({
            block: "viewer",
            key: p.id,
            title: ctx.t(p.titleKey),
            icon: p.icon,
            params: { preset: p.id, section: p.section },
            preview: Boolean(preview),
          });
        },
        async openCustomInstance(s) {
          const ctx = mako.blocks.context("explorer");
          s.tabCounter = (s.tabCounter || 1) + 1;
          const key = `lab-${s.tabCounter}`;
          await ctx.ui.openTab({
            block: "viewer",
            key,
            title: `Lab #${s.tabCounter}`,
            icon: "sparkles",
            params: { preset: "instance-notes", section: "instance", seq: s.tabCounter },
            preview: false,
          });
        },
        async resetAll(s) {
          const ctx = mako.blocks.context("explorer");
          const keys = await ctx.storage.listKeys("");
          for (const k of keys.keys || []) {
            await ctx.storage.deleteItem(k);
          }
          s.query = "";
          s.totalNotes = 0;
          await ctx.ui.toast(ctx.t("toast.reset"));
        },
      },
    });

    // 2. Multi-Instance Custom Workspace Tab block (slots = ["tab"])
    const SECTION_IDS = ["diff", "components", "canvas", "timeline", "charts", "graph", "instance"];
    mako.blocks.register("viewer", {
      state: {
        initialized: false,
        section: "diff",
        presetId: "diff-auth",
        diffMode: "unified",
        alertDismissed: false,
        selectedLine: -1,
        commentDraft: "",
        commentSeq: 0,
        comments: [],
        sliderVal: 70,
        secretInput: "mk_live_demo_9f3a",
        studioSubTab: "Overview",
        tableSort: "Component:asc",
        tableStyle: "striped",
        showSkeletons: false,
        kpiClicks: 0,
        scratchpad: "",
        canvasScene: "architecture",
        canvasSelected: "",
        timelineSelected: "",
        chartSelected: "",
        graphLayout: "force",
        graphSelected: "",
        graphLastDoubleTap: "",
        graphPositions: {},
      },
      async load(s, ctx) {
        const params = (ctx.tab && ctx.tab.params) || {};
        if (!s.initialized) {
          s.initialized = true;
          if (params.preset) s.presetId = String(params.preset);
          if (params.section) s.section = String(params.section);
          const dm = await ctx.settings.getSetting("default_diff_mode");
          if (dm.setting && dm.setting.text) s.diffMode = dm.setting.text === "split" ? "split" : "unified";
          const ts = await ctx.settings.getSetting("default_table_style");
          if (ts.setting && ts.setting.text) s.tableStyle = ts.setting.text === "compact" ? "compact" : "striped";
        }
        if (!SECTION_IDS.includes(s.section)) {
          s.section = "diff";
        }
        const tabKey = (ctx.tab && ctx.tab.key) || "default";
        s.comments = await loadSavedComments(ctx, `${tabKey}:${s.presetId}`);
        s.commentSeq = Math.max(s.commentSeq || 0, maxCommentSeq(s.comments));
        const note = await ctx.storage.getItem(`scratch:${tabKey}`);
        s.scratchpad = (note.found && typeof note.value === "string") ? sanitizeMultilineText(note.value, 4096) : "";
        let loadedPositions = {};
        try {
          const gp = await ctx.storage.getItem(`graph-pos:${tabKey}`);
          if (gp.found && gp.value) {
            const parsed = JSON.parse(gp.value);
            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
              for (const [k, v] of Object.entries(parsed)) {
                if (k && isValidGraphPos(v)) {
                  loadedPositions[String(k)] = { x: v.x, y: v.y };
                }
              }
            }
          }
        } catch {}
        s.graphPositions = loadedPositions;
      },
      render(s, ui, ctx) {
        const t = ctx.t;
        const tabKey = (ctx.tab && ctx.tab.key) || "default";
        const tabParams = (ctx.tab && ctx.tab.params) || {};
        const preset = findPreset(s.presetId);

        const sectionLabels = [
          t("tab.diff"),
          t("tab.components"),
          t("tab.canvas"),
          t("tab.timeline"),
          t("tab.charts"),
          t("tab.graph"),
          t("tab.instance"),
        ];
        const curIdx = Math.max(0, SECTION_IDS.indexOf(s.section));
        const curSectionLabel = sectionLabels[curIdx];

        const header = ui.toolbar([
          ui.chip(`tab:${sanitizeInlineText(tabKey, 128) || "default"}`, { icon: "layers", variant: "soft", tone: "accent" }),
          ui.spacer(),
          ui.kbd(["⌘", "K"]),
          ui.button(t("tab.spawn"), {
            key: "btn-spawn",
            icon: "plus",
            variant: "outline",
            size: "small",
            onTap: ["spawnSibling"],
          }),
          ui.button(t("tab.close"), {
            key: "btn-close",
            icon: "x",
            variant: "ghost",
            size: "small",
            onTap: ["closeSelf"],
          }),
        ], { key: "viewer-tb" });

        const mainTabs = ui.tabs(curSectionLabel, sectionLabels, {
          key: "main-tabs",
          variant: "underline",
          onChange: ["pickSection"],
        });

        let body;
        if (s.section === "diff") {
          body = renderDiffSection(s, ui, ctx, preset);
        } else if (s.section === "components") {
          body = renderStudioSection(s, ui, ctx);
        } else if (s.section === "canvas") {
          body = renderCanvasSection(s, ui, ctx);
        } else if (s.section === "timeline") {
          body = renderTimelineSection(s, ui, ctx);
        } else if (s.section === "charts") {
          body = renderChartsSection(s, ui, ctx);
        } else if (s.section === "graph") {
          body = renderGraphSection(s, ui, ctx);
        } else {
          body = renderInstanceSection(s, ui, ctx, tabKey, tabParams);
        }

        return ui.column([
          header,
          mainTabs,
          ui.scroll([body], { key: "viewer-scroll", grow: 1 }),
        ], { fill: true, gap: 10 });
      },
      on: {
        pickSection(s, label) {
          const ctx = mako.blocks.context("viewer");
          const t = ctx.t;
          if (label === t("tab.components")) s.section = "components";
          else if (label === t("tab.canvas")) s.section = "canvas";
          else if (label === t("tab.timeline")) s.section = "timeline";
          else if (label === t("tab.charts")) s.section = "charts";
          else if (label === t("tab.graph")) s.section = "graph";
          else if (label === t("tab.instance")) s.section = "instance";
          else s.section = "diff";
        },
        setDiffMode(s, mode) {
          s.diffMode = mode === "Split" ? "split" : "unified";
        },
        dismissAlert(s) {
          s.alertDismissed = true;
        },
        restoreAlert(s) {
          s.alertDismissed = false;
        },
        selectLine(s, idx) {
          const n = Number(idx);
          s.selectedLine = Number.isFinite(n) ? n : -1;
        },
        setCommentDraft(s, val) {
          s.commentDraft = sanitizeInlineText(val, 4096);
        },
        async saveComment(s) {
          const ctx = mako.blocks.context("viewer");
          const vals = mako.blocks.values("viewer");
          const text = sanitizeInlineText(vals["comment-draft"] ?? s.commentDraft ?? "", 4096).trim();
          if (!text) return;
          const lineNo = s.selectedLine >= 0 ? s.selectedLine + 1 : 1;
          const existing = Array.isArray(s.comments) ? s.comments : [];
          const used = new Set(existing.map((c) => (c && typeof c.id === "string") ? c.id : "").filter(Boolean));
          let seq = Math.max(s.commentSeq || 0, maxCommentSeq(existing)) + 1;
          while (used.has(`c${seq}`)) {
            seq += 1;
          }
          s.commentSeq = seq;
          s.comments = [...existing, { id: `c${seq}`, line: lineNo, text }];
          s.commentDraft = "";
          const tabKey = (ctx.tab && ctx.tab.key) || "default";
          await ctx.storage.setItem(`comments:${tabKey}:${s.presetId}`, JSON.stringify(s.comments));
          mako.blocks.reload("explorer", "");
          await ctx.ui.toast(ctx.t("toast.saved"));
        },
        async clearComments(s) {
          const ctx = mako.blocks.context("viewer");
          s.comments = [];
          const tabKey = (ctx.tab && ctx.tab.key) || "default";
          await ctx.storage.deleteItem(`comments:${tabKey}:${s.presetId}`);
          mako.blocks.reload("explorer", "");
        },
        setSlider(s, val) {
          const n = Number(val);
          if (Number.isFinite(n)) s.sliderVal = Math.max(0, Math.min(100, Math.round(n)));
        },
        setSecret(s, val) {
          s.secretInput = String(val || "");
        },
        pickStudioSubTab(s, val) {
          s.studioSubTab = String(val || "Overview");
        },
        sortTable(s, expr) {
          s.tableSort = String(expr || "Component:asc");
        },
        setTableStyle(s, style) {
          s.tableStyle = style === "compact" ? "compact" : "striped";
        },
        toggleSkeletons(s, checked) {
          s.showSkeletons = Boolean(checked);
        },
        bumpKpi(s) {
          s.kpiClicks = (s.kpiClicks || 0) + 1;
        },
        async pingEmpty() {
          const ctx = mako.blocks.context("viewer");
          await ctx.ui.toast(ctx.t("toast.emptyPing"));
        },
        setScratchpad(s, val) {
          s.scratchpad = sanitizeMultilineText(val, 4096);
        },
        async saveScratchpad(s) {
          const ctx = mako.blocks.context("viewer");
          const vals = mako.blocks.values("viewer");
          const text = sanitizeMultilineText(vals["scratch-input"] ?? s.scratchpad ?? "", 4096);
          s.scratchpad = text;
          const tabKey = (ctx.tab && ctx.tab.key) || "default";
          await ctx.storage.setItem(`scratch:${tabKey}`, text);
          await ctx.ui.toast(ctx.t("toast.saved"));
        },
        async copyTabKey() {
          const ctx = mako.blocks.context("viewer");
          const tabKey = (ctx.tab && ctx.tab.key) || "default";
          await ctx.ui.copy(tabKey);
          await ctx.ui.toast(ctx.t("toast.copied"));
        },
        async spawnSibling() {
          const ctx = mako.blocks.context("viewer");
          const key = `sibling-${Date.now() % 10000}`;
          await ctx.ui.openTab({
            block: "viewer",
            key,
            title: `Instance ${key}`,
            icon: "layers",
            params: { preset: "instance-notes", section: "instance", parent: ctx.tab ? ctx.tab.key : "" },
            preview: false,
          });
        },
        async closeSelf() {
          const ctx = mako.blocks.context("viewer");
          await ctx.ui.closeTab({ block: "viewer" });
        },
        setCanvasScene(s, scene) {
          s.canvasScene = scene === "pipeline" ? "pipeline" : "architecture";
        },
        selectCanvasHit(s, hitId) {
          s.canvasSelected = typeof hitId === "string" ? sanitizeInlineText(hitId, 128) : "";
        },
        resetCanvasSelection(s) {
          s.canvasSelected = "";
        },
        selectTimelineSpan(s, spanId) {
          s.timelineSelected = typeof spanId === "string" ? sanitizeInlineText(spanId, 128) : "";
        },
        resetTimelineSelection(s) {
          s.timelineSelected = "";
        },
        selectChartDatum(s, chartKind, rawPayload) {
          const kind = sanitizeInlineText(chartKind || "chart", 32) || "chart";
          const raw = typeof rawPayload === "string"
            ? rawPayload
            : (rawPayload === undefined || rawPayload === null ? "" : String(rawPayload));
          try {
            const obj = JSON.parse(raw);
            if (obj && typeof obj === "object" && !Array.isArray(obj)) {
              if (typeof obj.row === "number" && Number.isFinite(obj.row) && typeof obj.col === "number" && Number.isFinite(obj.col)) {
                const ser = typeof obj.series === "string" && obj.series ? `${sanitizeInlineText(obj.series, 64)} / ` : "";
                const lbl = typeof obj.label === "string" && obj.label ? sanitizeInlineText(obj.label, 64) : `col ${obj.col}`;
                const val = (typeof obj.value === "number" && Number.isFinite(obj.value)) ? ` = ${obj.value}` : "";
                s.chartSelected = sanitizeInlineText(`${kind}: ${ser}${lbl} [${obj.row},${obj.col}]${val}`, 256);
                return;
              }
              if (typeof obj.index === "number" && Number.isFinite(obj.index)) {
                const lbl = typeof obj.label === "string" && obj.label ? ` (${sanitizeInlineText(obj.label, 64)})` : "";
                s.chartSelected = sanitizeInlineText(`${kind}: index #${obj.index}${lbl}`, 256);
                return;
              }
            }
          } catch {}
          const safeRaw = sanitizeInlineText(raw, 180);
          s.chartSelected = safeRaw ? `${kind}: ${safeRaw}` : "";
        },
        resetChartSelection(s) {
          s.chartSelected = "";
        },
        setGraphLayout(s, mode) {
          const m = String(mode || "");
          s.graphLayout = (m === "hierarchy" || m === "radial") ? m : "force";
        },
        selectGraphNode(s, id) {
          s.graphSelected = typeof id === "string" ? sanitizeInlineText(id, 128) : "";
        },
        async doubleTapGraphNode(s, id) {
          const ctx = mako.blocks.context("viewer");
          const nid = typeof id === "string" ? sanitizeInlineText(id, 128) : "";
          s.graphSelected = nid;
          s.graphLastDoubleTap = nid;
          if (nid) {
            await ctx.ui.toast(ctx.t("graph.toastDoubleTap", { node: nid }));
          }
        },
        async dropGraphNode(s, pos) {
          if (!isValidGraphPos(pos) || typeof pos.id !== "string" || !pos.id) return;
          const ctx = mako.blocks.context("viewer");
          const tabKey = (ctx.tab && ctx.tab.key) || "default";
          const next = { ...(s.graphPositions || {}), [pos.id]: { x: pos.x, y: pos.y } };
          s.graphPositions = next;
          await ctx.storage.setItem(`graph-pos:${tabKey}`, JSON.stringify(next));
        },
        async resetGraphPositions(s) {
          const ctx = mako.blocks.context("viewer");
          const tabKey = (ctx.tab && ctx.tab.key) || "default";
          s.graphPositions = {};
          s.graphLastDoubleTap = "";
          s.graphSelected = "";
          await ctx.storage.deleteItem(`graph-pos:${tabKey}`);
        },
        async inspectGraphNodeFromMenu(s, arg) {
          const ctx = mako.blocks.context("viewer");
          const nid = typeof arg === "string" ? sanitizeInlineText(arg, 128) : "";
          s.graphSelected = nid;
          if (nid) {
            await ctx.ui.toast(ctx.t("graph.toastContext", { node: nid }));
          }
        },
      },
    });

    mako.commands.register("open-diff", async (ctx) => {
      const p = findPreset("diff-auth");
      await ctx.ui.openTab({
        block: "viewer",
        key: p.id,
        title: ctx.t(p.titleKey),
        icon: p.icon,
        params: { preset: p.id, section: "diff" },
        preview: false,
      });
    });

    mako.commands.register("open-studio", async (ctx) => {
      const p = findPreset("studio-ui");
      await ctx.ui.openTab({
        block: "viewer",
        key: p.id,
        title: ctx.t(p.titleKey),
        icon: p.icon,
        params: { preset: p.id, section: "components" },
        preview: false,
      });
    });

    for (const [cmdId, presetId] of [
      ["open-canvas", "canvas-lab"],
      ["open-timeline", "timeline-lab"],
      ["open-charts", "charts-lab"],
      ["open-graph", "graph-lab"],
    ]) {
      mako.commands.register(cmdId, async (ctx) => {
        const p = findPreset(presetId);
        await ctx.ui.openTab({
          block: "viewer",
          key: p.id,
          title: ctx.t(p.titleKey),
          icon: p.icon,
          params: { preset: p.id, section: p.section },
          preview: false,
        });
      });
    }
  },
};

function renderDiffSection(s, ui, ctx, preset) {
  const t = ctx.t;
  const diffPreset = preset.diffLines ? preset : PRESETS[0];
  const children = [];

  if (!s.alertDismissed) {
    children.push(ui.alert(t("diff.alertTitle"), {
      key: "diff-alert",
      variant: "soft",
      tone: "accent",
      icon: "info",
      placeholder: t("diff.alertBody"),
      onChange: ["dismissAlert"],
    }));
  }

  const modeRadio = ui.radio(
    s.diffMode === "split" ? t("diff.modeSplit") : t("diff.modeUnified"),
    [t("diff.modeUnified"), t("diff.modeSplit")],
    {
      key: "diff-mode",
      direction: "horizontal",
      onChange: ["setDiffMode"],
    },
  );

  children.push(ui.row([
    ui.text(t(diffPreset.titleKey), { variant: "heading" }),
    ui.spacer(),
    modeRadio,
  ], { key: "diff-head", gap: 8 }));

  children.push(ui.diff(diffPreset.diffLines, {
    key: "diff-view",
    variant: s.diffMode === "split" ? "split" : "unified",
    diffFrom: diffPreset.diffFrom,
    diffTo: diffPreset.diffTo,
    language: diffPreset.language || "go",
    maxHeight: 320,
    onChange: ["selectLine"],
  }));

  const lineInfo = s.selectedLine >= 0
    ? t("diff.selectedLine", { line: s.selectedLine + 1 })
    : t("diff.noLine");

  const commentRows = [];
  if (Array.isArray(s.comments) && s.comments.length > 0) {
    const seenRowKeys = new Set(["comment-draft"]);
    for (let i = 0; i < s.comments.length; i++) {
      const c = s.comments[i];
      if (!c || typeof c !== "object" || Array.isArray(c) || typeof c.text !== "string") continue;
      const line = (typeof c.line === "number" && Number.isFinite(c.line) && Math.floor(c.line) >= 1)
        ? Math.floor(c.line)
        : 1;
      let rowKey = typeof c.id === "string" ? sanitizeKey(c.id) : "";
      if (!rowKey) rowKey = `c-row-${i + 1}`;
      if (seenRowKeys.has(rowKey)) {
        let suffix = 2;
        while (seenRowKeys.has(`${rowKey}-${suffix}`)) suffix += 1;
        rowKey = `${rowKey}-${suffix}`;
      }
      seenRowKeys.add(rowKey);
      commentRows.push(ui.row([
        ui.chip(`L${line}`, { tone: "accent", variant: "soft" }),
        ui.text(sanitizeInlineText(c.text, 4096), { size: "small", grow: 1 }),
      ], { key: rowKey, gap: 8 }));
    }
  }

  children.push(ui.card([
    ui.column([
      ui.row([
        ui.text(lineInfo, { size: "small", tone: s.selectedLine >= 0 ? "accent" : "muted" }),
        ui.spacer(),
        ui.kbd(["⌘", "Enter"]),
      ], { gap: 6 }),
      ui.textarea(sanitizeInlineText(s.commentDraft, 4096), {
        key: "comment-draft",
        variant: "mono",
        size: "small",
        placeholder: t("diff.notePlaceholder"),
        onChange: ["setCommentDraft"],
        onSubmit: ["saveComment"],
      }),
      ui.row([
        ui.button(t("diff.saveNote"), {
          key: "btn-save-comment",
          icon: "check",
          variant: "primary",
          size: "small",
          onTap: ["saveComment"],
        }),
        ...(commentRows.length > 0 ? [ui.button(t("diff.clearNotes"), {
          key: "btn-clear-comments",
          icon: "trash-2",
          variant: "ghost",
          size: "small",
          onTap: ["clearComments"],
        })] : []),
      ], { gap: 8 }),
      ...(commentRows.length > 0 ? [
        ui.divider(),
        ui.text(t("diff.savedNotes", { count: commentRows.length }), { variant: "caption", tone: "muted" }),
        ...commentRows,
      ] : []),
    ], { gap: 8 }),
  ], {
    key: "diff-comments-card",
    text: t("diff.savedNotes", { count: commentRows.length }),
    icon: "file-diff",
  }));

  if (diffPreset.snippet) {
    children.push(ui.card([
      ui.code(diffPreset.snippet, "go", { key: "diff-code", maxHeight: 180 }),
    ], {
      key: "diff-code-card",
      text: t("diff.snippetTitle"),
      icon: "file-code",
      badges: [{ text: "go", variant: "mono", tone: "muted" }],
    }));
  }

  return ui.column(children, { key: "diff-sec", gap: 12 });
}

function renderStudioSection(s, ui, ctx) {
  const t = ctx.t;
  const pillOpts = [t("studio.pill.overview"), t("studio.pill.alerts"), t("studio.pill.skeleton")];
  const curPill = pillOpts.includes(s.studioSubTab) ? s.studioSubTab : pillOpts[0];

  const kpis = ui.grid(3, [
    ui.stat(t("studio.kpi.throughput"), `${42 + s.kpiClicks}/s`, {
      key: "kpi-1",
      icon: "rocket",
      tone: "accent",
      placeholder: "+12% vs baseline",
      badges: [{ text: "live", tone: "success", variant: "soft" }],
      onTap: ["bumpKpi"],
    }),
    ui.stat(t("studio.kpi.latency"), "1.4 ms", {
      key: "kpi-2",
      icon: "clock",
      tone: "success",
      placeholder: "60 fps budget",
      onTap: ["bumpKpi"],
    }),
    ui.stat(t("studio.kpi.health"), "32 MiB", {
      key: "kpi-3",
      icon: "shield-alert",
      tone: "warning",
      placeholder: "QuickJS / Wasm cap",
      onTap: ["bumpKpi"],
    }),
  ], { key: "studio-kpis", gap: 10 });

  const subTabs = ui.tabs(curPill, pillOpts, {
    key: "studio-pills",
    variant: "pills",
    onChange: ["pickStudioSubTab"],
  });

  let panel;
  if (curPill === pillOpts[1]) {
    panel = ui.column([
      ui.alert("Soft Alert", [
        ui.button("OK", { key: "al-ok", size: "small", variant: "outline", onTap: ["pingEmpty"] }),
      ], { key: "al-soft", variant: "soft", tone: "accent", icon: "info", placeholder: t("studio.alertSoft") }),
      ui.alert("Outline Warning", {
        key: "al-outline",
        variant: "outline",
        tone: "warning",
        icon: "shield-alert",
        placeholder: t("studio.alertOutline"),
      }),
      ui.alert("Solid Highlight", {
        key: "al-solid",
        variant: "solid",
        tone: "success",
        icon: "circle-check",
        placeholder: t("studio.alertSolid"),
      }),
    ], { key: "alerts-panel", gap: 10 });
  } else if (curPill === pillOpts[2]) {
    panel = ui.column([
      ui.toggle(s.showSkeletons, t("studio.skeletonToggle"), {
        key: "sk-toggle",
        onChange: ["toggleSkeletons"],
      }),
      ...(s.showSkeletons ? [
        ui.card([
          ui.row([
            ui.skeleton({ key: "sk-c", variant: "circle", size: "large" }),
            ui.column([
              ui.skeleton({ key: "sk-t1", variant: "text" }),
              ui.skeleton({ key: "sk-t2", variant: "text", size: "small" }),
            ], { grow: 1, gap: 6 }),
          ], { gap: 10 }),
          ui.skeleton({ key: "sk-r", variant: "rect", maxHeight: 72 }),
        ], { key: "sk-card", text: "Skeleton preview", icon: "sparkles" }),
      ] : [
        ui.empty(t("studio.emptyTitle"), [
          ui.button(t("studio.emptyAction"), {
            key: "empty-act",
            icon: "sparkles",
            variant: "primary",
            size: "small",
            onTap: ["pingEmpty"],
          }),
        ], {
          key: "studio-empty",
          icon: "flask-conical",
          tone: "accent",
          placeholder: t("studio.emptyDesc"),
        }),
      ]),
    ], { key: "skel-panel", gap: 10 });
  } else {
    const share = Math.max(0, Math.min(1, s.sliderVal / 100));
    const rows = sortedBenchRows(s.tableSort).map((r) => ui.row([
      ui.text(r.name, { variant: "mono", size: "small" }),
      ui.text(r.category, { size: "small", tone: "muted" }),
      ui.text(`${r.latency.toFixed(1)}`, { variant: "mono", size: "small" }),
      ui.pill(r.status, { tone: r.status === "Ready" ? "success" : "accent", variant: "soft" }),
    ], { key: `tr-${r.name}` }));

    panel = ui.column([
      ui.card([
        ui.column([
          ui.slider(String(s.sliderVal), {
            key: "demo-slider",
            min: 0,
            max: 100,
            step: 5,
            text: t("studio.sliderLabel", { pct: s.sliderVal }),
            onChange: ["setSlider"],
          }),
          ui.row([
            ui.progress(share, { key: "prog-bar", tone: "accent", grow: 1 }),
            ui.progress(share, { key: "prog-ring", variant: "ring", tone: "success" }),
            ui.progress({ key: "prog-spin", variant: "spinner" }),
          ], { gap: 10 }),
          ui.input(s.secretInput, {
            key: "demo-secret",
            variant: "password",
            icon: "lock",
            placeholder: t("studio.passwordPlaceholder"),
            onChange: ["setSecret"],
          }),
        ], { gap: 10 }),
      ], {
        key: "controls-card",
        text: t("studio.controlsTitle"),
        icon: "sliders-horizontal",
        badges: [{ text: `${s.sliderVal}%`, tone: "accent", variant: "mono" }],
      }),
      ui.card([
        ui.column([
          ui.row([
            ui.text(t("studio.tableStyle"), { variant: "caption", tone: "muted" }),
            ui.spacer(),
            ui.radio(s.tableStyle === "compact" ? "compact" : "striped", ["striped", "compact"], {
              key: "tbl-style",
              direction: "horizontal",
              onChange: ["setTableStyle"],
            }),
          ], { gap: 8 }),
          ui.table(["Component", "Category", "Latency (ms)", "Status"], rows, {
            key: "comp-table",
            variant: s.tableStyle === "compact" ? "compact" : "striped",
            value: s.tableSort,
            onChange: ["sortTable"],
          }),
        ], { gap: 8 }),
      ], {
        key: "table-card",
        text: t("studio.tableTitle"),
        icon: "layers",
        badges: [{ text: s.tableSort, variant: "mono", tone: "muted" }],
      }),
    ], { key: "overview-panel", gap: 12 });
  }

  return ui.column([kpis, subTabs, panel], { key: "studio-sec", gap: 12 });
}

function renderInstanceSection(s, ui, ctx, tabKey, tabParams) {
  const t = ctx.t;
  const metaCode = JSON.stringify({
    tab: { key: tabKey, params: tabParams },
    locale: ctx.locale,
    scope: ctx.scope,
  }, null, 2);

  return ui.column([
    ui.card([
      ui.code(metaCode, "json", { key: "inst-meta-code", maxHeight: 160 }),
    ], {
      key: "inst-meta-card",
      text: t("inst.cardTitle"),
      icon: "layers",
      badges: [{ text: sanitizeInlineText(tabKey, 32) || "default", variant: "mono", tone: "accent" }],
    }),
    ui.card([
      ui.column([
        ui.textarea(sanitizeMultilineText(s.scratchpad, 4096), {
          key: "scratch-input",
          variant: "mono",
          placeholder: t("inst.notePlaceholder"),
          onChange: ["setScratchpad"],
          onSubmit: ["saveScratchpad"],
        }),
        ui.row([
          ui.button(t("inst.save"), { key: "inst-save", icon: "check", variant: "primary", size: "small", onTap: ["saveScratchpad"] }),
          ui.button(t("inst.copyKey"), { key: "inst-copy", icon: "copy", variant: "outline", size: "small", onTap: ["copyTabKey"] }),
          ui.spacer(),
          ui.button(t("inst.openSibling"), { key: "inst-sibling", icon: "plus", variant: "outline", size: "small", onTap: ["spawnSibling"] }),
          ui.button(t("inst.closeSelf"), { key: "inst-close", icon: "x", variant: "danger", size: "small", onTap: ["closeSelf"] }),
        ], { gap: 8 }),
      ], { gap: 10 }),
    ], {
      key: "inst-note-card",
      text: t("inst.noteLabel"),
      icon: "file-code",
    }),
  ], { key: "inst-sec", gap: 12 });
}

function buildCanvasCommands(scene) {
  if (scene === "pipeline") {
    return [
      { op: "roundRect", x: 16, y: 16, w: 648, h: 248, r: 12, fillTone: "surface", strokeTone: "border", strokeWidth: 1.5, alpha: 0.9 },
      { op: "line", x1: 32, y1: 62, x2: 648, y2: 62, strokeTone: "faint", strokeWidth: 1, alpha: 0.7 },
      { op: "text", x: 32, y: 34, text: "CI / Build Sandbox Pipeline", size: "large", tone: "ink" },
      { op: "roundRect", x: 40, y: 92, w: 152, h: 96, r: 10, fillTone: "surfaceAlt", strokeTone: "accent", strokeWidth: 2, hitId: "stage-plan" },
      { op: "icon", x: 56, y: 112, icon: "layers", size: 18, tone: "accent", hitId: "stage-plan" },
      { op: "text", x: 82, y: 114, text: "Plan", size: "medium", tone: "text", hitId: "stage-plan" },
      { op: "text", x: 56, y: 148, text: "Token validation", size: "small", tone: "muted", hitId: "stage-plan" },
      { op: "roundRect", x: 264, y: 92, w: 152, h: 96, r: 10, fillTone: "surfaceAlt", strokeTone: "warning", strokeWidth: 2, hitId: "stage-build" },
      { op: "icon", x: 280, y: 112, icon: "terminal", size: 18, tone: "warning", hitId: "stage-build" },
      { op: "text", x: 306, y: 114, text: "Compile", size: "medium", tone: "ink", hitId: "stage-build" },
      { op: "text", x: 280, y: 148, text: "Wasm + QuickJS", size: "small", tone: "muted", hitId: "stage-build" },
      { op: "roundRect", x: 488, y: 92, w: 152, h: 96, r: 10, fillTone: "surfaceAlt", strokeTone: "success", strokeWidth: 2, hitId: "stage-verify" },
      { op: "icon", x: 504, y: 112, icon: "circle-check", size: 18, tone: "success", hitId: "stage-verify" },
      { op: "text", x: 530, y: 114, text: "Verify", size: "medium", tone: "ink", hitId: "stage-verify" },
      { op: "text", x: 504, y: 148, text: "Parity & tests", size: "small", tone: "muted", hitId: "stage-verify" },
      { op: "bezier", x1: 192, y1: 140, cx1: 224, cy1: 100, cx2: 232, cy2: 180, x2: 264, y2: 140, strokeTone: "accent", strokeWidth: 2.2, hitId: "edge-deploy" },
      { op: "bezier", x1: 416, y1: 140, cx1: 448, cy1: 180, cx2: 456, cy2: 100, x2: 488, y2: 140, strokeTone: "success", strokeWidth: 2.2, hitId: "edge-deploy" },
      { op: "circle", x: 586, y: 222, r: 18, fillTone: "danger", strokeTone: "border", strokeWidth: 1.5, alpha: 0.25, hitId: "badge-status" },
      { op: "icon", x: 578, y: 214, icon: "shield-alert", size: 16, tone: "danger", hitId: "badge-status" },
      { op: "text", x: 40, y: 220, text: "12 theme tokens enforced (zero raw hex)", size: "small", tone: "" },
    ];
  }
  return [
    { op: "roundRect", x: 16, y: 16, w: 648, h: 248, r: 12, fillTone: "surface", strokeTone: "border", strokeWidth: 1.5, alpha: 0.92 },
    { op: "line", x1: 32, y1: 62, x2: 648, y2: 62, strokeTone: "faint", strokeWidth: 1, alpha: 0.7 },
    { op: "text", x: 32, y: 34, text: "Mako Plugin Runtime Architecture", size: "large", tone: "ink" },
    { op: "roundRect", x: 38, y: 90, w: 164, h: 102, r: 10, fillTone: "surfaceAlt", strokeTone: "accent", strokeWidth: 2, hitId: "node-gateway" },
    { op: "icon", x: 54, y: 108, icon: "server", size: 18, tone: "accent", hitId: "node-gateway" },
    { op: "text", x: 80, y: 110, text: "Host Gateway", size: "medium", tone: "text", hitId: "node-gateway" },
    { op: "text", x: 54, y: 146, text: "uiblock + RPC", size: "small", tone: "muted", hitId: "node-gateway" },
    { op: "roundRect", x: 262, y: 90, w: 164, h: 102, r: 10, fillTone: "surfaceAlt", strokeTone: "success", strokeWidth: 2, hitId: "node-runtime" },
    { op: "icon", x: 278, y: 108, icon: "sparkles", size: 18, tone: "success", hitId: "node-runtime" },
    { op: "text", x: 304, y: 110, text: "QuickJS / Wasm", size: "medium", tone: "ink", hitId: "node-runtime" },
    { op: "text", x: 278, y: 146, text: "Sandboxed render", size: "small", tone: "muted", hitId: "node-runtime" },
    { op: "roundRect", x: 486, y: 90, w: 158, h: 102, r: 10, fillTone: "surfaceAlt", strokeTone: "warning", strokeWidth: 2, hitId: "node-store" },
    { op: "icon", x: 502, y: 108, icon: "database", size: 18, tone: "warning", hitId: "node-store" },
    { op: "text", x: 528, y: 110, text: "Scoped Store", size: "medium", tone: "ink", hitId: "node-store" },
    { op: "text", x: 502, y: 146, text: "KV + SQLite", size: "small", tone: "muted", hitId: "node-store" },
    { op: "bezier", x1: 202, y1: 140, cx1: 228, cy1: 96, cx2: 236, cy2: 184, x2: 262, y2: 140, strokeTone: "accent", strokeWidth: 2.2, hitId: "edge-bus" },
    { op: "bezier", x1: 426, y1: 140, cx1: 452, cy1: 184, cx2: 460, cy2: 96, x2: 486, y2: 140, strokeTone: "warning", strokeWidth: 2.2, hitId: "edge-bus" },
    { op: "circle", x: 586, y: 224, r: 18, fillTone: "success", strokeTone: "border", strokeWidth: 1.5, alpha: 0.25, hitId: "badge-live" },
    { op: "icon", x: 578, y: 216, icon: "rocket", size: 16, tone: "success", hitId: "badge-live" },
    { op: "circle", x: 536, y: 224, r: 14, fillTone: "danger", strokeTone: "danger", strokeWidth: 1, alpha: 0.2, hitId: "badge-live" },
    { op: "text", x: 38, y: 222, text: "6 vector ops · 12 theme tokens · hitId regions", size: "small", tone: "" },
  ];
}

function renderCanvasSection(s, ui, ctx) {
  const t = ctx.t;
  const scene = s.canvasScene === "pipeline" ? "pipeline" : "architecture";
  const cmds = buildCanvasCommands(scene);
  const selectedHit = typeof s.canvasSelected === "string" ? sanitizeInlineText(s.canvasSelected, 128) : "";

  return ui.column([
    ui.row([
      ui.text(t("canvas.title"), { variant: "heading" }),
      ui.spacer(),
      ui.radio(scene, ["architecture", "pipeline"], {
        key: "canvas-scene",
        direction: "horizontal",
        onChange: ["setCanvasScene"],
      }),
      ui.button(t("canvas.reset"), {
        key: "canvas-reset",
        icon: "refresh-cw",
        variant: "outline",
        size: "small",
        onTap: ["resetCanvasSelection"],
      }),
    ], { key: "canvas-head", gap: 8 }),
    ui.canvas(cmds, {
      key: "demo-canvas",
      caption: t("canvas.caption"),
      width: 680,
      height: 280,
      maxHeight: 360,
      pan: true,
      zoom: true,
      onChange: ["selectCanvasHit"],
    }),
    ui.card([
      ui.row([
        ui.icon("palette", { tone: selectedHit ? "accent" : "muted" }),
        ui.text(
          selectedHit ? t("canvas.selectedHit", { hit: selectedHit }) : t("canvas.emptyHint"),
          { size: "small", tone: selectedHit ? "accent" : "muted", grow: 1 },
        ),
        ...(selectedHit ? [ui.pill(selectedHit, { tone: "accent", variant: "soft" })] : []),
      ], { gap: 8 }),
    ], {
      key: "canvas-inspector",
      text: t("canvas.inspectorTitle"),
      icon: "palette",
    }),
  ], { key: "canvas-sec", gap: 12 });
}

const TIMELINE_LANES = [
  {
    id: "lane-orchestrator",
    label: "Orchestrator",
    spans: [
      { id: "sp-plan", startMs: 0, endMs: 1400, label: "Plan", subtitle: "scope + DAG", kind: "turn", tone: "accent" },
      { id: "sp-impl", startMs: 3700, endMs: 7100, label: "Implement", subtitle: "M4a plugin + tests", kind: "turn", tone: "accent" },
      { id: "sp-handoff", startMs: 9650, endMs: 10400, label: "Handoff", subtitle: "report ready", kind: "turn", tone: "success" },
    ],
  },
  {
    id: "lane-subagents",
    label: "Subagents",
    spans: [
      { id: "sp-explore-1", startMs: 1450, endMs: 3600, label: "Explorer 1", subtitle: "ui-workbench map", kind: "subagent", tone: "success" },
      { id: "sp-explore-2", startMs: 1500, endMs: 3200, label: "Explorer 2", subtitle: "AppSupport diff", kind: "subagent", tone: "warning" },
    ],
  },
  {
    id: "lane-ci",
    label: "CI & Vet",
    spans: [
      { id: "sp-vet", startMs: 7200, endMs: 8900, label: "go vet", subtitle: "static check", kind: "ci", tone: "muted" },
      { id: "sp-test", startMs: 7200, endMs: 9600, label: "go test", subtitle: "plugin + uiblock + gui", kind: "ci", tone: "danger" },
    ],
  },
];

function findTimelineSpanInfo(spanId) {
  for (const lane of TIMELINE_LANES) {
    for (const sp of lane.spans) {
      if (sp.id === spanId) {
        return { lane: lane.label, span: sp };
      }
    }
  }
  return null;
}

function renderTimelineSection(s, ui, ctx) {
  const t = ctx.t;
  const selectedId = typeof s.timelineSelected === "string" ? s.timelineSelected : "";
  const info = selectedId ? findTimelineSpanInfo(selectedId) : null;
  const inspText = info
    ? t("timeline.selectedSpan", {
        label: info.span.label,
        id: info.span.id,
        lane: info.lane,
        kind: info.span.kind || "span",
        start: `${info.span.startMs}ms`,
        end: `${info.span.endMs}ms`,
        dur: `${info.span.endMs - info.span.startMs}ms`,
      })
    : t("timeline.emptyHint");

  return ui.column([
    ui.row([
      ui.text(t("timeline.title"), { variant: "heading" }),
      ui.spacer(),
      ui.button(t("timeline.reset"), {
        key: "timeline-reset",
        icon: "refresh-cw",
        variant: "outline",
        size: "small",
        onTap: ["resetTimelineSelection"],
      }),
    ], { key: "timeline-head", gap: 8 }),
    ui.timeline(TIMELINE_LANES, {
      key: "demo-timeline",
      caption: t("timeline.caption"),
      maxHeight: 280,
      grow: 1,
      onChange: ["selectTimelineSpan"],
    }),
    ui.card([
      ui.row([
        ui.icon("clock", { tone: info ? (info.span.tone || "accent") : "muted" }),
        ui.text(inspText, { size: "small", tone: info ? "accent" : "muted", grow: 1 }),
        ...(info ? [ui.pill(info.span.id, { tone: info.span.tone || "accent", variant: "soft" })] : []),
      ], { gap: 8 }),
    ], {
      key: "timeline-inspector",
      text: t("timeline.inspectorTitle"),
      icon: "clock",
    }),
  ], { key: "timeline-sec", gap: 12 });
}

function renderChartsSection(s, ui, ctx) {
  const t = ctx.t;
  const selectedChart = typeof s.chartSelected === "string" ? sanitizeInlineText(s.chartSelected, 256) : "";
  return ui.column([
    ui.row([
      ui.text(t("charts.title"), { variant: "heading" }),
      ui.spacer(),
      ui.button(t("charts.reset"), {
        key: "chart-reset",
        icon: "refresh-cw",
        variant: "outline",
        size: "small",
        onTap: ["resetChartSelection"],
      }),
    ], { key: "charts-head", gap: 8 }),
    ui.chart("area", [
      { name: "Requests", tone: "accent", values: [18, 29, 24, 42, 38, 55] },
      { name: "Cached", tone: "success", values: [8, 14, 15, 22, 25, 34] },
    ], {
      key: "chart-area",
      caption: t("charts.areaTitle"),
      labels: ["T1", "T2", "T3", "T4", "T5", "T6"],
      maxHeight: 190,
      grow: 1,
      onChange: ["selectChartDatum", "area"],
    }),
    ui.chart("stacked-bar", [
      { name: "Pass", tone: "success", values: [12, 18, 15, 21] },
      { name: "Warn", tone: "warning", values: [3, 2, 4, 1] },
      { name: "Fail", tone: "danger", values: [1, 0, 2, 0] },
    ], {
      key: "chart-stacked",
      caption: t("charts.stackedTitle"),
      labels: ["M1", "M2", "M3", "M4"],
      maxHeight: 190,
      grow: 1,
      onChange: ["selectChartDatum", "stacked-bar"],
    }),
    ui.chart("heatmap", [
      { name: "Parser", tone: "accent", values: [12, 34, 68, 45, 19] },
      { name: "Wasm", tone: "success", values: [28, 52, 74, 61, 33] },
      { name: "GUI", tone: "warning", values: [15, 41, 88, 72, 26] },
    ], {
      key: "chart-heatmap",
      caption: t("charts.heatmapTitle"),
      labels: ["Mon", "Tue", "Wed", "Thu", "Fri"],
      maxHeight: 200,
      grow: 1,
      onChange: ["selectChartDatum", "heatmap"],
    }),
    ui.card([
      ui.row([
        ui.icon("sparkles", { tone: selectedChart ? "accent" : "muted" }),
        ui.text(selectedChart || t("charts.emptyHint"), {
          size: "small",
          tone: selectedChart ? "accent" : "muted",
          grow: 1,
        }),
      ], { gap: 8 }),
    ], {
      key: "chart-inspector",
      text: t("charts.inspectorTitle"),
      icon: "sparkles",
    }),
  ], { key: "charts-sec", gap: 12 });
}

function renderGraphSection(s, ui, ctx) {
  const t = ctx.t;
  const saved = (s.graphPositions && typeof s.graphPositions === "object" && !Array.isArray(s.graphPositions)) ? s.graphPositions : {};
  const withSavedPos = (node) => {
    const pos = saved[node.id];
    if (isValidGraphPos(pos)) {
      return { ...node, x: pos.x, y: pos.y, hasPos: true, pinned: true };
    }
    return node;
  };

  const nodes = [
    withSavedPos({
      id: "wb-lead",
      label: "Lead Agent",
      group: "Control Plane",
      tone: "accent",
      subtitle: "orchestrator · main",
      shape: "card",
      icon: "bot",
      status: "working",
      badges: [
        { text: "main", icon: "git-branch", tone: "accent", variant: "soft" },
        { text: "lead", tone: "success", variant: "soft" },
      ],
    }),
    withSavedPos({
      id: "wb-router",
      label: "RPC Router",
      group: "Control Plane",
      tone: "success",
      subtitle: "daemon · ipc",
      shape: "circle",
      icon: "network",
      status: "done",
    }),
    withSavedPos({
      id: "wb-explorer",
      label: "Explorer",
      group: "Workers",
      tone: "muted",
      subtitle: "scout · read-only",
      shape: "card",
      icon: "radar",
      status: "idle",
      badges: [{ text: "scout", tone: "muted", variant: "outline" }],
    }),
    withSavedPos({
      id: "wb-impl",
      label: "Implementer",
      group: "Workers",
      tone: "warning",
      subtitle: "worker · feat",
      shape: "card",
      icon: "terminal",
      status: "waiting",
      badges: [{ text: "Bash", icon: "shield-alert", tone: "warning", variant: "soft" }],
    }),
    withSavedPos({
      id: "wb-verify",
      label: "QA Verifier",
      group: "Workers",
      tone: "success",
      subtitle: "qa · tests",
      shape: "circle",
      icon: "circle-check",
      status: "idle",
    }),
  ];

  const edges = [
    { from: "wb-lead", to: "wb-router", label: "route", tone: "accent", active: true, kind: "agentMessage" },
    { from: "wb-lead", to: "wb-explorer", label: "spawn", tone: "muted", kind: "parent" },
    { from: "wb-lead", to: "wb-impl", label: "assign", tone: "warning", active: true, kind: "agentMessage" },
    { from: "wb-impl", to: "wb-verify", label: "verify", tone: "success", dashed: true, kind: "dependency" },
  ];

  const validSelected = nodes.some((n) => n.id === s.graphSelected) ? s.graphSelected : "";
  const validDoubleTap = (typeof s.graphLastDoubleTap === "string" && nodes.some((n) => n.id === s.graphLastDoubleTap)) ? s.graphLastDoubleTap : "";
  const layout = (s.graphLayout === "hierarchy" || s.graphLayout === "radial") ? s.graphLayout : "force";
  const pinnedCount = nodes.filter((n) => n.hasPos).length;

  return ui.column([
    ui.row([
      ui.text(t("graph.title"), { variant: "heading" }),
      ui.spacer(),
      ui.radio(layout, ["force", "hierarchy", "radial"], {
        key: "graph-layout",
        direction: "horizontal",
        onChange: ["setGraphLayout"],
      }),
      ui.button(t("graph.resetPos"), {
        key: "graph-reset-pos",
        icon: "refresh-cw",
        variant: "outline",
        size: "small",
        onTap: ["resetGraphPositions"],
      }),
    ], { key: "graph-head", gap: 8 }),
    ui.graph(nodes, edges, {
      key: "demo-graph",
      caption: t("graph.caption"),
      variant: "cards",
      layout,
      minimap: true,
      fill: true,
      maxHeight: 400,
      value: validSelected,
      contextMenu: "wb-graph-menu",
      onChange: ["selectGraphNode"],
      onDoubleTap: ["doubleTapGraphNode"],
      onDrop: ["dropGraphNode"],
    }),
    ui.contextMenu("wb-graph-menu", [
      ui.menuItem(t("graph.ctxInspect"), {
        key: "wb-ctx-inspect",
        icon: "sparkles",
        onTap: ["inspectGraphNodeFromMenu"],
      }),
    ]),
    ui.card([
      ui.column([
        ui.row([
          ui.icon("network", { tone: validSelected ? "accent" : "muted" }),
          ui.text(
            validSelected ? t("graph.selectedNode", { node: validSelected }) : t("graph.emptyHint"),
            { size: "small", tone: validSelected ? "accent" : "muted", grow: 1 },
          ),
          ui.pill(t("graph.pinnedCount", { count: pinnedCount }), {
            tone: pinnedCount > 0 ? "success" : "muted",
            variant: "soft",
          }),
        ], { gap: 8 }),
        ...(validDoubleTap ? [
          ui.text(t("graph.lastDoubleTap", { node: validDoubleTap }), {
            variant: "caption",
            tone: "accent",
          }),
        ] : []),
      ], { gap: 4 }),
    ], {
      key: "graph-inspector",
      text: t("graph.inspectorTitle"),
      icon: "network",
    }),
  ], { key: "graph-sec", gap: 12 });
}
