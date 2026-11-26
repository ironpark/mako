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
    "sidebar.stat.demosDelta": "4 presets",
    "sidebar.stat.comments": "Notes",
    "sidebar.stat.commentsDelta": "Saved locally",
    "sidebar.coverage": "Rev 49 UI coverage",
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
    "tab.diff": "Diff Review",
    "tab.components": "Component Studio",
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
  },
  ko: {
    "sidebar.title": "워크벤치",
    "sidebar.search": "데모 검색…",
    "sidebar.stat.demos": "데모",
    "sidebar.stat.demosDelta": "4개 프리셋",
    "sidebar.stat.comments": "메모",
    "sidebar.stat.commentsDelta": "로컬 저장됨",
    "sidebar.coverage": "Rev 49 UI 커버리지",
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
    "tab.diff": "Diff 리뷰",
    "tab.components": "컴포넌트 스튜디오",
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
  },
};

function findPreset(id) {
  return PRESETS.find((p) => p.id === id) || PRESETS[0];
}

async function loadSavedComments(ctx, key) {
  try {
    const res = await ctx.storage.getItem(`comments:${key}`);
    if (res.found && res.value) {
      const parsed = JSON.parse(res.value);
      if (Array.isArray(parsed)) return parsed;
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
          if (Array.isArray(arr)) total += arr.length;
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
            ui.progress(0.92, { key: "ring", variant: "ring", tone: "accent" }),
            ui.column([
              ui.text(t("sidebar.coverage"), { size: "small", weight: "medium" }),
              ui.text("9 new + 8 enhanced kinds", { variant: "caption", tone: "muted" }),
            ], { grow: 1, gap: 2 }),
            ui.pill("92%", { tone: "success", variant: "soft" }),
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
      },
      async load(s, ctx) {
        const params = (ctx.tab && ctx.tab.params) || {};
        if (!s.initialized) {
          s.initialized = true;
          if (params.preset) s.presetId = String(params.preset);
          if (params.section) s.section = String(params.section);
          const dm = await ctx.settings.getSetting("default_diff_mode");
          if (dm.setting && dm.setting.text) s.diffMode = dm.setting.text;
          const ts = await ctx.settings.getSetting("default_table_style");
          if (ts.setting && ts.setting.text) s.tableStyle = ts.setting.text;
        }
        const tabKey = (ctx.tab && ctx.tab.key) || "default";
        s.comments = await loadSavedComments(ctx, `${tabKey}:${s.presetId}`);
        const note = await ctx.storage.getItem(`scratch:${tabKey}`);
        if (note.found) s.scratchpad = note.value;
      },
      render(s, ui, ctx) {
        const t = ctx.t;
        const tabKey = (ctx.tab && ctx.tab.key) || "default";
        const tabParams = (ctx.tab && ctx.tab.params) || {};
        const preset = findPreset(s.presetId);

        const sectionLabels = [t("tab.diff"), t("tab.components"), t("tab.instance")];
        const sectionIds = ["diff", "components", "instance"];
        const curIdx = Math.max(0, sectionIds.indexOf(s.section));
        const curSectionLabel = sectionLabels[curIdx];

        const header = ui.toolbar([
          ui.chip(`tab:${tabKey}`, { icon: "layers", variant: "soft", tone: "accent" }),
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
          s.commentDraft = String(val || "");
        },
        async saveComment(s) {
          const ctx = mako.blocks.context("viewer");
          const vals = mako.blocks.values("viewer");
          const text = String(vals["comment-draft"] ?? s.commentDraft ?? "").trim();
          if (!text) return;
          const lineNo = s.selectedLine >= 0 ? s.selectedLine + 1 : 1;
          s.commentSeq = (s.commentSeq || 0) + 1;
          s.comments = [...(s.comments || []), { id: `c${s.commentSeq}`, line: lineNo, text }];
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
          s.scratchpad = String(val || "");
        },
        async saveScratchpad(s) {
          const ctx = mako.blocks.context("viewer");
          const vals = mako.blocks.values("viewer");
          const text = String(vals["scratch-input"] ?? s.scratchpad ?? "");
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

  children.push(ui.card([
    ui.column([
      ui.row([
        ui.text(lineInfo, { size: "small", tone: s.selectedLine >= 0 ? "accent" : "muted" }),
        ui.spacer(),
        ui.kbd(["⌘", "Enter"]),
      ], { gap: 6 }),
      ui.textarea(s.commentDraft, {
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
        ...(s.comments.length > 0 ? [ui.button(t("diff.clearNotes"), {
          key: "btn-clear-comments",
          icon: "trash-2",
          variant: "ghost",
          size: "small",
          onTap: ["clearComments"],
        })] : []),
      ], { gap: 8 }),
      ...(s.comments.length > 0 ? [
        ui.divider(),
        ui.text(t("diff.savedNotes", { count: s.comments.length }), { variant: "caption", tone: "muted" }),
        ...s.comments.map((c) => ui.row([
          ui.chip(`L${c.line}`, { tone: "accent", variant: "soft" }),
          ui.text(c.text, { size: "small", grow: 1 }),
        ], { key: c.id, gap: 8 })),
      ] : []),
    ], { gap: 8 }),
  ], {
    key: "diff-comments-card",
    text: t("diff.savedNotes", { count: s.comments.length }),
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
            ui.radio(s.tableStyle, ["striped", "compact"], {
              key: "tbl-style",
              direction: "horizontal",
              onChange: ["setTableStyle"],
            }),
          ], { gap: 8 }),
          ui.table(["Component", "Category", "Latency (ms)", "Status"], rows, {
            key: "comp-table",
            variant: s.tableStyle,
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
      badges: [{ text: tabKey, variant: "mono", tone: "accent" }],
    }),
    ui.card([
      ui.column([
        ui.textarea(s.scratchpad, {
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
