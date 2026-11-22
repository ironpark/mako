// OKF (Open Knowledge Format) project knowledge vault plugin.
// Stores pure OKF v0.2 Markdown + YAML frontmatter files inside each project's
// isolated private data directory (ctx.datafs), exposes CLI tools for agents,
// and showcases Mako's Revision 44 rich UI components + en/ko i18n.

import {
  DOC_STATUSES,
  DOC_TYPES,
  FOLDERS,
  allDirPaths,
  buildFolderTree,
  buildKnowledgeGraph,
  buildVaultAnalytics,
  buildVaultIndex,
  normalizeDocPath,
  parseDocument,
  parseTags,
  rebuildIndexMarkdown,
  resolveDocLink,
  rewriteWikiLinksForMarkdown,
  searchDocuments,
  serializeDoc,
  slugify,
  starterOverviewMarkdown,
  todayISO,
  truncateUtf8Bytes,
} from "./lib/okf.js";
import { registerOKFi18n } from "./lib/i18n.js";

let mako;

const TYPE_FILTERS = ["All", ...DOC_TYPES, "Index"];

function safeKey(prefix, raw) {
  const clean = String(raw || "")
    .replace(/[\/#]+/g, "_")
    .replace(/[\r\n\t]+/g, "_")
    .slice(0, 96);
  return `${prefix}-${clean}`;
}

function cleanInline(raw) {
  return String(raw ?? "").replace(/[\r\n\t]+/g, " ").trim();
}

function statusTone(status) {
  if (status === "active") return "success";
  if (status === "needs_review") return "warning";
  if (status === "deprecated") return "danger";
  return "muted";
}

function typeTone(type) {
  if (type === "Index") return "accent";
  if (type === "Decision") return "warning";
  if (type === "Architecture") return "accent";
  if (type === "Procedure") return "success";
  return "muted";
}

function labelType(t, type) {
  const key = `type.${type}`;
  const res = t(key);
  return res === key ? String(type || "") : res;
}

function labelStatus(t, status) {
  const key = `status.${status}`;
  const res = t(key);
  return res === key ? String(status || "") : res;
}

function canonicalTypeFilter(value, t) {
  const v = String(value || "").trim();
  if (!v) return "All";
  for (const tp of TYPE_FILTERS) {
    if (v === tp || v === labelType(t, tp)) return tp;
  }
  return "All";
}

function canonicalDocType(value, t) {
  const v = String(value || "").trim();
  for (const tp of DOC_TYPES) {
    if (v === tp || v === labelType(t, tp)) return tp;
  }
  return "Concept";
}

function canonicalDocStatus(value, t) {
  const v = String(value || "").trim();
  for (const st of DOC_STATUSES) {
    if (v === st || v === labelStatus(t, st)) return st;
  }
  return "active";
}

function formatBytes(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  return `${(v / (1024 * 1024)).toFixed(2)} MB`;
}

function formatPct(ratio) {
  const pct = Math.round(Math.max(0, Math.min(1, Number(ratio) || 0)) * 100);
  return `${pct}%`;
}

function normalizeDirPath(raw) {
  return String(raw || "")
    .trim()
    .replace(/\\/g, "/")
    .replace(/^\/+|\/+$/g, "")
    .split("/")
    .map((seg) => slugify(seg))
    .filter(Boolean)
    .join("/");
}

function getCtxT(ctx) {
  if (ctx && typeof ctx.t === "function") return ctx.t;
  return (key, params) => mako.i18n.t(key, params);
}

async function shouldAutoIndex(ctx) {
  const res = await ctx.settings.getSetting("auto_index");
  return res?.setting ? Boolean(res.setting.flag) : true;
}

async function defaultFolder(ctx) {
  const res = await ctx.settings.getSetting("default_folder");
  const v = res?.setting?.text || "concepts";
  return FOLDERS.includes(v) ? v : "concepts";
}

async function loadVaultDocs(ctx, bootstrap = false) {
  let listed = await ctx.datafs.list("", { recursive: true });
  let entries = listed.entries || [];
  let mdEntries = entries.filter((e) => !e.dir && e.path.endsWith(".md"));

  if (bootstrap && mdEntries.length === 0) {
    const overview = starterOverviewMarkdown(ctx.projectName);
    await ctx.datafs.write("concepts/overview.md", overview);
    const seedDocs = [parseDocument("concepts/overview.md", overview)];
    const indexMd = rebuildIndexMarkdown(ctx.projectName, seedDocs);
    await ctx.datafs.write("index.md", indexMd);
    listed = await ctx.datafs.list("", { recursive: true });
    entries = listed.entries || [];
    mdEntries = entries.filter((e) => !e.dir && e.path.endsWith(".md"));
  }

  const dirs = entries.filter((e) => e.dir).map((e) => e.path);
  const docs = [];
  for (const entry of mdEntries) {
    const file = await ctx.datafs.read(entry.path);
    if (!file.found || file.binary) continue;
    docs.push(parseDocument(entry.path, file.text || "", file.size || entry.size || 0, file.modifiedMs || entry.modifiedMs || 0));
  }
  const index = buildVaultIndex(docs);
  const stats = await ctx.datafs.stats();
  return { index, stats, dirs };
}

async function syncIndexFile(ctx) {
  if (!(await shouldAutoIndex(ctx))) return;
  const { index } = await loadVaultDocs(ctx, false);
  const md = rebuildIndexMarkdown(ctx.projectName, index.docs);
  await ctx.datafs.write("index.md", md);
}

async function refreshState(s, ctx, bootstrap = false) {
  const { index, stats, dirs } = await loadVaultDocs(ctx, bootstrap);
  s.docs = index.docs;
  s.dirs = dirs;
  s.hasIndex = index.hasIndex;
  s.totalLinks = index.totalLinks;
  s.broken = index.broken;
  s.missingMeta = index.missingMeta;
  s.needsReview = index.needsReview;
  s.orphans = index.orphans;
  s.issueCount = index.issueCount;
  s.usedBytes = stats.usedBytes || 0;
  s.quotaBytes = stats.quotaBytes || 0;
  s.fileCount = stats.fileCount || 0;
  if (s.selectedPath && !s.docs.some((d) => d.path === s.selectedPath)) {
    s.selectedPath = "";
  }
  if (s.graphFocusPath && !s.graphFocusPath.startsWith("missing:") && !s.docs.some((d) => d.path === s.graphFocusPath)) {
    s.graphFocusPath = "";
  }
  s.error = "";
}

function emptyDraft(folder = "concepts", presetPath = "", presetTitle = "") {
  return {
    originalPath: "",
    path: presetPath,
    title: presetTitle,
    type: "Concept",
    status: "active",
    description: "",
    tags: "",
    source: "",
    verifiedBy: "",
    body: presetTitle ? `# ${presetTitle}\n\n` : "",
    folder,
  };
}

function draftFromDoc(doc) {
  return {
    originalPath: doc.path,
    path: doc.path,
    title: doc.title,
    type: DOC_TYPES.includes(doc.type) ? doc.type : "Concept",
    status: DOC_STATUSES.includes(doc.status) ? doc.status : "active",
    description: doc.description || "",
    tags: (doc.tags || []).join(", "),
    source: doc.source || "",
    verifiedBy: doc.verifiedBy || "",
    body: doc.body || "",
    folder: doc.path.includes("/") ? doc.path.split("/")[0] : "concepts",
  };
}

function renderTopBar(s, ui, t, activeTab, tabOptions) {
  return ui.row([
    ui.tabs(activeTab, tabOptions, { key: "main-tabs", onChange: ["setTab"] }),
    ui.text(t("topbar.summary", { docs: s.docs.length, size: formatBytes(s.usedBytes) }), {
      size: "small",
      tone: "faint",
      grow: 1,
    }),
    ui.button(t("action.new"), {
      key: "btn-new",
      icon: "file-text",
      size: "small",
      variant: "ghost",
      tone: "accent",
      onTap: ["newDoc"],
    }),
    ui.button(t("action.index"), {
      key: "btn-index",
      icon: "folder-open",
      size: "small",
      variant: "ghost",
      onTap: ["rebuildIndex"],
    }),
  ], { key: "topbar", gap: 6 });
}

function renderBrowse(s, ui, t) {
  const results = searchDocuments(s.docs, s.query, s.typeFilter === "All" ? "" : s.typeFilter, s.tagFilter);
  const selected = s.selectedPath ? s.docs.find((d) => d.path === s.selectedPath) : null;

  if (selected) {
    return renderReader(s, ui, t, selected);
  }

  const typeFilterOptions = TYPE_FILTERS.map((tp) => labelType(t, tp));
  const controls = [
    ui.row([
      ui.input(s.query, {
        key: "search",
        placeholder: t("browse.searchPlaceholder"),
        grow: 1,
        onChange: ["setQuery"],
        onSubmit: ["setQuery"],
      }),
      ui.select(labelType(t, s.typeFilter), typeFilterOptions, {
        key: "type-filter",
        onChange: ["setTypeFilter"],
      }),
    ], { gap: 6 }),
  ];

  if (s.tagFilter) {
    controls.push(ui.row([
      ui.text(t("browse.tagLabel"), { size: "small", tone: "muted" }),
      ui.chip(`#${s.tagFilter}`, { tone: "accent" }),
      ui.button(t("action.clear"), { key: "clear-tag", size: "small", variant: "ghost", onTap: ["setTagFilter", ""] }),
    ], { gap: 6 }));
  }

  if (results.length === 0) {
    controls.push(ui.card([
      ui.text(t("browse.empty"), { tone: "muted" }),
      ui.button(t("action.createDoc"), { key: "empty-new", size: "small", variant: "primary", tone: "accent", onTap: ["newDoc"] }),
    ], { gap: 8 }));
    return ui.column(controls, { gap: 8 });
  }

  controls.push(
    ui.list(results, ({ doc, snippet }) => {
      const metaRow = [
        ui.pill(labelType(t, doc.type), { tone: typeTone(doc.type) }),
        ui.pill(labelStatus(t, doc.status), { tone: statusTone(doc.status) }),
        ui.text(doc.path, { size: "small", tone: "faint", grow: 1 }),
      ];
      if (doc.links.length > 0 || doc.backlinks.length > 0) {
        metaRow.push(ui.text(`↗${doc.links.length} ↙${doc.backlinks.length}`, { size: "small", tone: "muted" }));
      }
      const cardChildren = [
        ui.row(metaRow, { gap: 6 }),
        ui.text(doc.title, { weight: "semibold" }),
      ];
      if (snippet) {
        cardChildren.push(ui.text(snippet, { size: "small", tone: "muted" }));
      }
      if (doc.tags.length > 0) {
        cardChildren.push(
          ui.wrap(
            doc.tags.slice(0, 8).map((tag) =>
              ui.chip(`#${tag}`, { key: safeKey("t", tag), tone: "muted", onTap: ["setTagFilter", tag] }),
            ),
            { gap: 4 },
          ),
        );
      }
      return ui.card(cardChildren, { key: safeKey("doc", doc.path), gap: 4, onTap: ["openDoc", doc.path] });
    }, { gap: 6 }),
  );

  return ui.column(controls, { gap: 8 });
}

function buildTreeItems(node, s, ui, t) {
  const items = [];
  for (const dir of node.dirs) {
    const isCollapsed = Boolean(s.collapsedDirs && s.collapsedDirs[dir.path]);
    const children = buildTreeItems(dir, s, ui, t);
    items.push(
      ui.treeItem(cleanInline(dir.name), children, {
        key: safeKey("dir", dir.path),
        value: t("tree.folderCount", { count: dir.docCount }),
        icon: isCollapsed ? "folder" : "folder-open",
        tone: "accent",
        checked: !isCollapsed,
        onChange: ["toggleDir", dir.path],
        onDrop: ["moveDocToFolder", dir.path],
        dropAccept: ["okf-doc"],
      }),
    );
  }
  for (const file of node.files) {
    const doc = file.doc;
    const typeLbl = labelType(t, doc.type);
    const linkInfo = (doc.links.length > 0 || doc.backlinks.length > 0)
      ? ` · ↗${doc.links.length} ↙${doc.backlinks.length}`
      : "";
    const secondary = doc.title && doc.title !== file.name
      ? `${doc.title} · ${typeLbl}${linkInfo}`
      : `${typeLbl}${linkInfo}`;
    items.push(
      ui.treeItem(cleanInline(file.name), {
        key: safeKey("tdoc", doc.path),
        value: cleanInline(secondary),
        icon: "file-text",
        tone: typeTone(doc.type),
        drag: doc.path,
        dragGroup: "okf-doc",
        onTap: ["openDoc", doc.path],
      }),
    );
  }
  return items;
}

function renderTree(s, ui, t) {
  const tree = buildFolderTree(s.docs, s.dirs);
  const totalDirs = allDirPaths(tree).length;
  const rootItems = buildTreeItems(tree, s, ui, t);

  const sections = [
    ui.row([
      ui.text(t("tree.summary", { dirs: totalDirs, docs: s.docs.length }), { size: "small", tone: "muted", grow: 1 }),
      ui.button(t("action.folder"), {
        key: "tree-new-dir-btn",
        icon: "folder-plus",
        size: "small",
        variant: "ghost",
        tone: "accent",
        onTap: ["toggleNewFolder"],
      }),
      ui.button(t("action.expand"), {
        key: "tree-expand-all",
        size: "small",
        variant: "ghost",
        onTap: ["expandAllDirs"],
      }),
      ui.button(t("action.collapse"), {
        key: "tree-collapse-all",
        size: "small",
        variant: "ghost",
        onTap: ["collapseAllDirs"],
      }),
    ], { gap: 4 }),
  ];

  if (s.showNewFolder) {
    sections.push(ui.card([
      ui.row([
        ui.input(s.newFolderDraft, {
          key: "tree-mkdir-input",
          placeholder: t("tree.newFolderPlaceholder"),
          grow: 1,
          onChange: ["setNewFolderDraft"],
          onSubmit: ["createFolder"],
        }),
        ui.button(t("action.create"), { key: "tree-mkdir-submit", size: "small", variant: "primary", tone: "accent", onTap: ["createFolder"] }),
        ui.button(t("action.cancel"), { key: "tree-mkdir-cancel", size: "small", variant: "ghost", onTap: ["toggleNewFolder"] }),
      ], { gap: 6 }),
    ], { gap: 6 }));
  }

  // Root drop zone lets users drag any nested document back to vault root.
  const treeCardChildren = [
    ui.row([
      ui.icon("folder-open", { size: "small", tone: "accent" }),
      ui.text(t("tree.rootHint"), { size: "small", tone: "muted", grow: 1 }),
    ], {
      key: "tree-root",
      gap: 6,
      onDrop: ["moveDocToFolder", ""],
      dropAccept: ["okf-doc"],
    }),
    ui.divider(),
  ];

  if (rootItems.length === 0) {
    treeCardChildren.push(ui.text(t("tree.empty"), { size: "small", tone: "muted" }));
  } else {
    treeCardChildren.push(ui.tree(rootItems, { key: "vault-tree" }));
  }

  sections.push(ui.card(treeCardChildren, { gap: 6 }));
  return ui.column(sections, { gap: 8 });
}

function renderKpiCard(ui, key, label, value, tone = "accent") {
  return ui.card([
    ui.text(label, { size: "small", tone: "muted" }),
    ui.text(String(value), { size: "large", weight: "semibold", tone }),
  ], { key, variant: "secondary", gap: 2 });
}

function renderGraph(s, ui, t) {
  const analytics = buildVaultAnalytics(s.docs, s.broken.length);
  const byPath = new Map(s.docs.map((d) => [d.path, d]));
  const graph = buildKnowledgeGraph(s.docs, {
    typeTone,
    labelType: (tp) => labelType(t, tp),
    missingGroup: t("graph.missingGroup"),
    missingSubtitle: t("graph.missingSubtitle"),
  });

  let focusId = s.graphFocusPath;
  if (!focusId || !graph.nodeIds.has(focusId)) {
    focusId = analytics.rankedHubs[0]?.doc?.path || graph.nodes[0]?.id || "";
  }
  const focusDoc = focusId ? byPath.get(focusId) : null;
  const focusMissing = focusId ? graph.missingByNodeId.get(focusId) : null;

  const sections = [];

  // 1. 3-column KPI stat grid (Docs, Links, Integrity %)
  sections.push(
    ui.grid(3, [
      renderKpiCard(ui, "kpi-docs", t("graph.kpiDocs"), analytics.totalDocs, "accent"),
      renderKpiCard(ui, "kpi-links", t("graph.kpiLinks"), analytics.totalLinks, "success"),
      renderKpiCard(
        ui,
        "kpi-integrity",
        t("graph.kpiIntegrity"),
        formatPct(analytics.linkHealthRatio),
        s.broken.length > 0 ? "warning" : "success",
      ),
    ], { key: "graph-kpis", gap: 6 }),
  );

  // 2. Interactive Knowledge Graph Canvas
  if (graph.nodes.length > 0) {
    const graphCardChildren = [
      ui.graph(graph.nodes, graph.edges, {
        key: "knowledge-graph",
        caption: t("graph.canvasCaption"),
        value: focusId && graph.nodeIds.has(focusId) ? focusId : "",
        maxHeight: 340,
        onChange: ["setGraphFocus"],
      }),
    ];
    sections.push(ui.card(graphCardChildren, { gap: 6 }));
  } else {
    sections.push(ui.card([ui.text(t("graph.empty"), { size: "small", tone: "muted" })]));
  }

  // 3. Selected-node inspector card (real doc or broken missing target)
  if (focusDoc) {
    const neighbors = [...new Set([...focusDoc.links, ...focusDoc.backlinks])];
    const inspectorChildren = [
      ui.row([
        ui.pill(labelType(t, focusDoc.type), { tone: typeTone(focusDoc.type) }),
        ui.pill(labelStatus(t, focusDoc.status), { tone: statusTone(focusDoc.status) }),
        ui.text(focusDoc.title, { weight: "semibold", grow: 1 }),
        ui.button(t("action.open"), {
          key: "graph-open-focus",
          size: "small",
          variant: "primary",
          tone: "accent",
          onTap: ["openDoc", focusDoc.path],
        }),
        ui.button(t("action.edit"), {
          key: "graph-edit-focus",
          size: "small",
          variant: "ghost",
          onTap: ["editDoc", focusDoc.path],
        }),
      ], { gap: 6 }),
      ui.text(`${focusDoc.path} · ↗${focusDoc.links.length} ↙${focusDoc.backlinks.length}`, { size: "small", tone: "faint" }),
    ];

    if (neighbors.length > 0) {
      inspectorChildren.push(ui.text(t("graph.inspectorLinks"), { size: "small", weight: "medium", tone: "muted" }));
      inspectorChildren.push(
        ui.wrap(
          neighbors.map((p) => {
            const nb = byPath.get(p);
            return ui.button(nb ? nb.title : p, {
              key: safeKey("gn", p),
              size: "small",
              variant: "outline",
              onTap: ["setGraphFocus", p],
            });
          }),
          { gap: 4 },
        ),
      );
    } else if (focusDoc.brokenLinks.length === 0) {
      inspectorChildren.push(ui.text(t("graph.inspectorNoNeighbors"), { size: "small", tone: "faint" }));
    }

    if (focusDoc.brokenLinks.length > 0) {
      inspectorChildren.push(ui.text(t("graph.inspectorBroken"), { size: "small", weight: "medium", tone: "danger" }));
      inspectorChildren.push(
        ui.wrap(
          focusDoc.brokenLinks.map((raw) =>
            ui.button(`+ [[${raw}]]`, {
              key: safeKey("gbrk", raw),
              size: "small",
              variant: "outline",
              tone: "warning",
              onTap: ["createFromLink", raw],
            }),
          ),
          { gap: 4 },
        ),
      );
    }

    sections.push(ui.card(inspectorChildren, { key: "graph-inspector", gap: 6 }));
  } else if (focusMissing) {
    sections.push(ui.card([
      ui.row([
        ui.text(t("graph.missingInspectorTitle", { target: focusMissing.target }), { weight: "semibold", tone: "danger", grow: 1 }),
        ui.button(t("action.create"), {
          key: "graph-create-missing",
          size: "small",
          variant: "primary",
          tone: "warning",
          onTap: ["createFromLink", focusMissing.target],
        }),
      ], { gap: 6 }),
      ui.text(t("graph.missingInspectorDesc", { count: focusMissing.refs.length }), { size: "small", tone: "muted" }),
      ui.wrap(
        focusMissing.refs.map((p) => {
          const src = byPath.get(p);
          return ui.button(src ? src.title : p, {
            key: safeKey("gmref", p),
            size: "small",
            variant: "ghost",
            onTap: ["setGraphFocus", p],
          });
        }),
        { gap: 4 },
      ),
    ], { key: "graph-inspector-missing", gap: 6 }));
  }

  // 4. Native Donut Chart: Documents by OKF Type
  if (analytics.typeStats.length > 0) {
    sections.push(ui.card([
      ui.chart("donut", [
        { name: t("graph.chartDocs"), tone: "accent", values: analytics.typeStats.map((x) => x.count) },
      ], {
        key: "chart-types",
        caption: t("graph.chartByType"),
        labels: analytics.typeStats.map((x) => truncateUtf8Bytes(labelType(t, x.type), 24)),
      }),
    ], { gap: 6 }));
  }

  // 5. Native Bar Chart: Top Connected Documents
  if (analytics.topHubs.length > 0) {
    sections.push(ui.card([
      ui.chart("bar", [
        { name: t("graph.chartLinksOut"), tone: "accent", values: analytics.topHubs.map((x) => x.out) },
        { name: t("graph.chartLinksIn"), tone: "success", values: analytics.topHubs.map((x) => x.in) },
      ], {
        key: "chart-hubs",
        caption: t("graph.chartTopConnected"),
        labels: analytics.topHubs.map((x) => truncateUtf8Bytes(x.label, 24)),
      }),
    ], { gap: 6 }));
  }

  // 6. Centrality Ranking Table (clickable rows focus the node)
  if (analytics.rankedHubs.length > 0) {
    const tableRows = analytics.rankedHubs.slice(0, 10).map((h) => ui.row([
      ui.text(h.doc.title, { size: "small", weight: h.doc.path === focusId ? "semibold" : "medium", grow: 1 }),
      ui.pill(labelType(t, h.doc.type), { tone: typeTone(h.doc.type) }),
      ui.text(String(h.out), { size: "small", tone: "accent" }),
      ui.text(String(h.in), { size: "small", tone: "success" }),
    ], { key: safeKey("hub", h.doc.path), gap: 6, onTap: ["setGraphFocus", h.doc.path] }));
    sections.push(ui.card([
      ui.text(t("graph.centralityTitle"), { size: "small", weight: "semibold", tone: "muted" }),
      ui.table([t("graph.colDocument"), t("graph.colType"), "↗", "↙"], tableRows),
    ], { gap: 6 }));
  }

  // 7. Tag Distribution Cloud using ui.wrap
  if (analytics.tagStats.length > 0) {
    sections.push(ui.card([
      ui.text(t("graph.tagDistribution", { count: analytics.tagStats.length }), { size: "small", weight: "semibold", tone: "muted" }),
      ui.wrap(
        analytics.tagStats.slice(0, 24).map((item) => ui.chip(`#${item.tag} (${item.count})`, {
          key: safeKey("gtag", item.tag),
          tone: "accent",
          onTap: ["filterTagAndClose", item.tag],
        })),
        { gap: 4 },
      ),
    ], { gap: 6 }));
  }

  return ui.column(sections, { gap: 8 });
}

function renderReader(s, ui, t, doc) {
  const byPath = new Map(s.docs.map((d) => [d.path, d]));
  const actionBar = ui.row([
    ui.button(t("action.back"), {
      key: "reader-back",
      icon: "arrow-left",
      size: "small",
      variant: "ghost",
      onTap: ["closeDoc"],
    }),
    ui.text("", { grow: 1 }),
    ui.button(t("action.graph"), {
      key: "reader-graph",
      icon: "network",
      size: "small",
      variant: "ghost",
      onTap: ["openInGraph", doc.path],
    }),
    ui.button(t("action.edit"), {
      key: "reader-edit",
      icon: "pencil",
      size: "small",
      variant: "ghost",
      tone: "accent",
      onTap: ["editDoc", doc.path],
    }),
    doc.status === "needs_review"
      ? ui.button(t("action.markActive"), {
        key: "reader-active",
        size: "small",
        variant: "ghost",
        tone: "success",
        onTap: ["setDocStatus", doc.path, "active"],
      })
      : ui.button(t("action.markReview"), {
        key: "reader-review",
        size: "small",
        variant: "ghost",
        tone: "warning",
        onTap: ["setDocStatus", doc.path, "needs_review"],
      }),
    s.confirmDeletePath === doc.path
      ? ui.button(t("action.confirmDelete"), {
        key: "reader-del-confirm",
        size: "small",
        variant: "danger",
        tone: "danger",
        onTap: ["deleteDoc", doc.path],
      })
      : ui.button(t("action.delete"), {
        key: "reader-del",
        size: "small",
        variant: "ghost",
        tone: "danger",
        onTap: ["askDelete", doc.path],
      }),
  ], { gap: 4 });

  const pathSegments = doc.path.split("/").filter(Boolean);
  const breadcrumb = ui.breadcrumb([t("breadcrumb.vault"), ...pathSegments], {
    key: "reader-breadcrumb",
    onChange: ["onBreadcrumb", doc.path],
  });

  const headerLines = [
    ui.row([
      ui.pill(labelType(t, doc.type), { tone: typeTone(doc.type) }),
      ui.pill(labelStatus(t, doc.status), { tone: statusTone(doc.status) }),
      ui.text(doc.updated || "", { size: "small", tone: "faint", grow: 1 }),
    ], { gap: 6 }),
    ui.text(doc.title, { size: "large", weight: "semibold" }),
  ];
  if (doc.description) {
    headerLines.push(ui.text(doc.description, { size: "small", tone: "muted" }));
  }
  if (doc.tags.length > 0) {
    headerLines.push(
      ui.wrap(
        doc.tags.map((tag) =>
          ui.chip(`#${tag}`, { key: safeKey("rt", tag), tone: "accent", onTap: ["filterTagAndClose", tag] }),
        ),
        { gap: 4 },
      ),
    );
  }

  const metaDetails = [
    ui.row([
      ui.text(t("reader.pathLabel"), { size: "small", tone: "muted" }),
      ui.text(doc.path, { size: "small", grow: 1 }),
    ], { gap: 6 }),
    ui.text(t("reader.linksSummary", { out: doc.links.length, in: doc.backlinks.length }), { size: "small", tone: "muted" }),
  ];
  if (doc.updated) {
    metaDetails.push(ui.row([
      ui.text(t("reader.updatedLabel"), { size: "small", tone: "muted" }),
      ui.text(doc.updated, { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }
  if (doc.okfVersion) {
    metaDetails.push(ui.row([
      ui.text(t("reader.versionLabel"), { size: "small", tone: "muted" }),
      ui.text(`OKF v${doc.okfVersion}`, { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }
  if (doc.source) {
    metaDetails.push(ui.row([
      ui.text(t("reader.sourceLabel"), { size: "small", tone: "muted" }),
      ui.text(doc.source, { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }
  if (doc.verifiedBy) {
    metaDetails.push(ui.row([
      ui.text(t("reader.verifiedByLabel"), { size: "small", tone: "muted" }),
      ui.text(doc.verifiedBy, { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }

  const renderedBody = rewriteWikiLinksForMarkdown(doc.body, doc.path, s.docs) || t("reader.emptyBody");

  const sections = [
    actionBar,
    breadcrumb,
    ui.card(headerLines, { gap: 4 }),
    ui.collapsible(t("reader.metadataTitle"), metaDetails, {
      key: "reader-meta",
      icon: "info",
      tone: "muted",
      checked: Boolean(s.metaExpanded),
      onChange: ["setMetaExpanded"],
    }),
    ui.card([
      ui.markdown(renderedBody, {
        key: "reader-body",
        onLink: ["openLink"],
      }),
    ], { gap: 6 }),
  ];

  if (doc.links.length > 0 || doc.backlinks.length > 0 || doc.brokenLinks.length > 0) {
    const graphRows = [];
    if (doc.links.length > 0) {
      graphRows.push(ui.text(t("reader.outgoingLinks", { count: doc.links.length }), { size: "small", weight: "semibold", tone: "muted" }));
      graphRows.push(
        ui.list(doc.links, (p) => {
          const target = byPath.get(p);
          return ui.row([
            ui.text("↗", { size: "small", tone: "accent" }),
            ui.text(target ? target.title : p, { size: "small", weight: "medium", grow: 1 }),
            ui.text(p, { size: "small", tone: "faint" }),
          ], { key: safeKey("out", p), gap: 6, onTap: ["openDoc", p] });
        }, { gap: 2 }),
      );
    }
    if (doc.backlinks.length > 0) {
      graphRows.push(ui.text(t("reader.backlinks", { count: doc.backlinks.length }), { size: "small", weight: "semibold", tone: "muted" }));
      graphRows.push(
        ui.list(doc.backlinks, (p) => {
          const src = byPath.get(p);
          return ui.row([
            ui.text("↙", { size: "small", tone: "muted" }),
            ui.text(src ? src.title : p, { size: "small", weight: "medium", grow: 1 }),
            ui.text(p, { size: "small", tone: "faint" }),
          ], { key: safeKey("in", p), gap: 6, onTap: ["openDoc", p] });
        }, { gap: 2 }),
      );
    }
    if (doc.brokenLinks.length > 0) {
      graphRows.push(ui.text(t("reader.brokenLinks", { count: doc.brokenLinks.length }), { size: "small", weight: "semibold", tone: "danger" }));
      graphRows.push(
        ui.list(doc.brokenLinks, (target) => ui.row([
          ui.text(`[[${target}]]`, { size: "small", tone: "warning", grow: 1 }),
          ui.button(t("action.create"), {
            key: safeKey("fix", target),
            size: "small",
            variant: "outline",
            tone: "warning",
            onTap: ["createFromLink", target],
          }),
        ], { key: safeKey("brkr", target), gap: 6 }), { gap: 4 }),
      );
    }
    sections.push(ui.card(graphRows, { gap: 6 }));
  }

  return ui.column(sections, { gap: 8 });
}

function renderEditor(s, ui, t) {
  const d = s.draft || emptyDraft();
  const heading = d.originalPath ? t("editor.editTitle", { path: d.originalPath }) : t("editor.newTitle");
  const typeOptions = DOC_TYPES.map((tp) => labelType(t, tp));
  const statusOptions = DOC_STATUSES.map((st) => labelStatus(t, st));

  return ui.column([
    ui.row([
      ui.text(heading, { weight: "semibold", grow: 1 }),
      ui.button(t("action.cancel"), { key: "ed-cancel", size: "small", variant: "ghost", onTap: ["cancelEdit"] }),
      ui.button(t("action.save"), { key: "ed-save", size: "small", variant: "primary", tone: "accent", onTap: ["saveDraft"] }),
    ], { gap: 6 }),
    ui.card([
      ui.row([
        ui.input(d.title, {
          key: "ed-title",
          placeholder: t("editor.titlePlaceholder"),
          grow: 1,
          onChange: ["draftField", "title"],
        }),
        ui.select(labelType(t, d.type), typeOptions, { key: "ed-type", onChange: ["draftField", "type"] }),
        ui.select(labelStatus(t, d.status), statusOptions, { key: "ed-status", onChange: ["draftField", "status"] }),
      ], { gap: 6 }),
      ui.row([
        ui.input(d.path, {
          key: "ed-path",
          placeholder: t("editor.pathPlaceholder", { folder: d.folder || "concepts" }),
          grow: 1,
          onChange: ["draftField", "path"],
        }),
        ui.input(d.tags, {
          key: "ed-tags",
          placeholder: t("editor.tagsPlaceholder"),
          grow: 1,
          onChange: ["draftField", "tags"],
        }),
      ], { gap: 6 }),
      ui.input(d.description, {
        key: "ed-desc",
        placeholder: t("editor.descPlaceholder"),
        onChange: ["draftField", "description"],
      }),
      ui.row([
        ui.input(d.source, {
          key: "ed-source",
          placeholder: t("editor.sourcePlaceholder"),
          grow: 1,
          onChange: ["draftField", "source"],
        }),
        ui.input(d.verifiedBy, {
          key: "ed-verified",
          placeholder: t("editor.verifiedPlaceholder"),
          grow: 1,
          onChange: ["draftField", "verifiedBy"],
        }),
      ], { gap: 6 }),
      ui.textarea(d.body, {
        key: "ed-body",
        size: "large",
        placeholder: t("editor.bodyPlaceholder"),
        onChange: ["draftField", "body"],
      }),
    ], { gap: 8 }),
  ], { gap: 8 });
}

function renderHealth(s, ui, t) {
  const items = [
    ui.grid(3, [
      renderKpiCard(ui, "health-kpi-broken", t("health.kpiBroken"), s.broken.length, s.broken.length > 0 ? "danger" : "success"),
      renderKpiCard(ui, "health-kpi-review", t("health.kpiReview"), s.needsReview.length, s.needsReview.length > 0 ? "warning" : "success"),
      renderKpiCard(ui, "health-kpi-orphans", t("health.kpiOrphans"), s.orphans.length, s.orphans.length > 0 ? "muted" : "success"),
    ], { key: "health-kpis", gap: 6 }),
    ui.card([
      ui.text(
        t("health.storage", {
          used: formatBytes(s.usedBytes),
          quota: formatBytes(s.quotaBytes),
          files: s.fileCount,
        }),
        { size: "small", tone: "muted" },
      ),
    ], { variant: "secondary", gap: 4 }),
  ];

  if (!s.hasIndex) {
    items.push(ui.card([
      ui.row([
        ui.text(t("health.missingIndex"), { weight: "medium", tone: "warning", grow: 1 }),
        ui.button(t("action.generateIndex"), {
          key: "lint-mk-index",
          size: "small",
          variant: "primary",
          tone: "accent",
          onTap: ["rebuildIndex"],
        }),
      ], { gap: 6 }),
    ], { gap: 6 }));
  }

  if (s.broken.length > 0) {
    items.push(
      ui.collapsible(
        t("health.brokenTitle", { count: s.broken.length }),
        [
          ui.list(s.broken, (b, i) => ui.row([
            ui.text(`${b.from} → [[${b.target}]]`, { size: "small", grow: 1 }),
            ui.button(t("action.create"), {
              key: `lint-fix-${i}`,
              size: "small",
              variant: "outline",
              tone: "warning",
              onTap: ["createFromLink", b.target],
            }),
          ], { key: `brk-${i}`, gap: 6 }), { gap: 4 }),
        ],
        {
          key: "health-broken",
          icon: "shield-alert",
          tone: "danger",
          checked: true,
        },
      ),
    );
  }

  if (s.needsReview.length > 0) {
    items.push(
      ui.collapsible(
        t("health.reviewTitle", { count: s.needsReview.length }),
        [
          ui.list(s.needsReview, (r) => ui.row([
            ui.text(`${r.title} (${r.path})`, { size: "small", grow: 1 }),
            ui.button(t("action.open"), {
              key: safeKey("rev-open", r.path),
              size: "small",
              variant: "ghost",
              onTap: ["openDoc", r.path],
            }),
          ], { key: safeKey("rev", r.path), gap: 6 }), { gap: 4 }),
        ],
        {
          key: "health-review",
          icon: "info",
          tone: "warning",
          checked: true,
        },
      ),
    );
  }

  if (s.missingMeta.length > 0) {
    items.push(
      ui.collapsible(
        t("health.missingMetaTitle", { count: s.missingMeta.length }),
        [
          ui.list(s.missingMeta, (m) => {
            const reason = m.code === "missing_frontmatter"
              ? t("health.missingFrontmatter")
              : t("health.missingTitleOrType");
            return ui.row([
              ui.text(`${m.path} · ${reason}`, { size: "small", grow: 1 }),
              ui.button(t("action.open"), {
                key: safeKey("meta-open", m.path),
                size: "small",
                variant: "ghost",
                onTap: ["openDoc", m.path],
              }),
            ], { key: safeKey("meta", m.path), gap: 6 });
          }, { gap: 4 }),
        ],
        {
          key: "health-meta",
          icon: "info",
          tone: "warning",
          checked: true,
        },
      ),
    );
  }

  if (s.orphans.length > 0) {
    items.push(
      ui.collapsible(
        t("health.orphansTitle", { count: s.orphans.length }),
        [
          ui.list(s.orphans, (o) => ui.row([
            ui.text(`${o.title} (${o.path})`, { size: "small", grow: 1 }),
            ui.button(t("action.open"), {
              key: safeKey("orp-open", o.path),
              size: "small",
              variant: "ghost",
              onTap: ["openDoc", o.path],
            }),
          ], { key: safeKey("orp", o.path), gap: 6 }), { gap: 4 }),
        ],
        {
          key: "health-orphans",
          icon: "file-text",
          tone: "muted",
          checked: s.broken.length === 0 && s.needsReview.length === 0,
        },
      ),
    );
  }

  if (s.issueCount === 0 && s.orphans.length === 0) {
    items.push(ui.card([
      ui.row([
        ui.icon("circle-check", { tone: "success" }),
        ui.text(t("health.allHealthy"), { tone: "success" }),
      ], { gap: 6 }),
    ]));
  }

  return ui.column(items, { gap: 8 });
}

export default {
  activate(m) {
    mako = m;
    registerOKFi18n(mako);

    mako.blocks.register("vault", {
      state: {
        tab: "browse",
        query: "",
        typeFilter: "All",
        tagFilter: "",
        selectedPath: "",
        confirmDeletePath: "",
        metaExpanded: false,
        draft: null,
        docs: [],
        dirs: [],
        collapsedDirs: {},
        showNewFolder: false,
        newFolderDraft: "",
        graphFocusPath: "",
        hasIndex: true,
        totalLinks: 0,
        broken: [],
        missingMeta: [],
        needsReview: [],
        orphans: [],
        issueCount: 0,
        usedBytes: 0,
        quotaBytes: 0,
        fileCount: 0,
        error: "",
      },
      async load(s, ctx) {
        await refreshState(s, ctx, true);
      },
      render(s, ui, ctx) {
        const t = getCtxT(ctx);
        const browseLabel = t("tab.browse");
        const treeLabel = t("tab.tree");
        const graphLabel = t("tab.graph");
        const healthLabel = s.issueCount > 0 ? t("tab.healthCount", { count: s.issueCount }) : t("tab.health");
        const editorLabel = t("tab.editor");

        const tabOptions = s.draft
          ? [browseLabel, treeLabel, graphLabel, healthLabel, editorLabel]
          : [browseLabel, treeLabel, graphLabel, healthLabel];

        let activeTab = browseLabel;
        if (s.tab === "tree") activeTab = treeLabel;
        else if (s.tab === "graph") activeTab = graphLabel;
        else if (s.tab === "health") activeTab = healthLabel;
        else if (s.tab === "editor" && s.draft) activeTab = editorLabel;

        const scrollChildren = [];
        if (s.error) {
          scrollChildren.push(ui.card([ui.text(s.error, { tone: "danger", size: "small" })], { key: "error-card" }));
        }
        if (s.tab === "editor" && s.draft) {
          scrollChildren.push(renderEditor(s, ui, t));
        } else if (s.tab === "tree") {
          scrollChildren.push(renderTree(s, ui, t));
        } else if (s.tab === "graph") {
          scrollChildren.push(renderGraph(s, ui, t));
        } else if (s.tab === "health") {
          scrollChildren.push(renderHealth(s, ui, t));
        } else {
          scrollChildren.push(renderBrowse(s, ui, t));
        }

        return ui.column([
          renderTopBar(s, ui, t, activeTab, tabOptions),
          ui.scroll(scrollChildren, { key: "main-scroll", grow: 1, gap: 8 }),
        ], { gap: 8, fill: true });
      },
      on: {
        setTab(s, value) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const v = String(value || "").trim();
          const healthPrefix = t("tab.health");
          if (v === t("tab.tree") || v === "Tree" || v === "tree") s.tab = "tree";
          else if (v === t("tab.graph") || v === "Graph" || v === "graph") s.tab = "graph";
          else if (v === healthPrefix || v.startsWith(`${healthPrefix} (`) || v.startsWith("Health") || v === "health") s.tab = "health";
          else if ((v === t("tab.editor") || v === "Editor" || v === "editor") && s.draft) s.tab = "editor";
          else s.tab = "browse";
          s.confirmDeletePath = "";
        },
        setQuery(s, value) {
          s.query = String(value ?? "");
        },
        setTypeFilter(s, value) {
          const ctx = mako.blocks.context("vault");
          s.typeFilter = canonicalTypeFilter(value, getCtxT(ctx));
        },
        setTagFilter(s, tag) {
          s.tagFilter = String(tag || "");
        },
        filterTagAndClose(s, tag) {
          s.tagFilter = String(tag || "");
          s.selectedPath = "";
          s.tab = "browse";
        },
        openDoc(s, p) {
          s.selectedPath = String(p || "");
          s.confirmDeletePath = "";
          s.tab = "browse";
        },
        closeDoc(s) {
          s.selectedPath = "";
          s.confirmDeletePath = "";
        },
        setMetaExpanded(s, expanded) {
          s.metaExpanded = Boolean(expanded);
        },
        onBreadcrumb(s, docPath, indexStr) {
          const idx = Number(indexStr);
          if (!Number.isFinite(idx) || idx < 0) return;
          const segments = String(docPath || "").split("/").filter(Boolean);
          if (idx === 0) {
            s.selectedPath = "";
            s.confirmDeletePath = "";
            s.tab = "browse";
            return;
          }
          if (idx < segments.length) {
            const nextCollapsed = { ...(s.collapsedDirs || {}) };
            let acc = "";
            for (let i = 0; i < idx; i++) {
              acc = acc ? `${acc}/${segments[i]}` : segments[i];
              nextCollapsed[acc] = false;
            }
            s.collapsedDirs = nextCollapsed;
            s.selectedPath = "";
            s.confirmDeletePath = "";
            s.tab = "tree";
          }
        },
        async openLink(s, rawHref) {
          const href = String(rawHref || "").trim();
          if (!href || /^https?:\/\//i.test(href)) return;
          let cleaned = href.replace(/^okf:/i, "").split("#")[0].trim();
          try {
            cleaned = decodeURIComponent(cleaned);
          } catch {}
          if (!cleaned) return;
          const resolved = resolveDocLink(s.docs, s.selectedPath, cleaned);
          if (resolved) {
            s.selectedPath = resolved;
            s.confirmDeletePath = "";
            s.tab = "browse";
            return;
          }
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const folder = await defaultFolder(ctx);
          const normPath = normalizeDocPath(cleaned, folder);
          const stem = normPath.replace(/\.md$/, "").split("/").pop() || cleaned;
          const prettyTitle = stem.replace(/[-_]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
          s.draft = emptyDraft(folder, normPath, prettyTitle);
          s.tab = "editor";
          s.error = "";
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.creatingMissingDoc", { path: normPath }) });
        },
        openInGraph(s, p) {
          s.graphFocusPath = String(p || "");
          s.tab = "graph";
        },
        setGraphFocus(s, id) {
          s.graphFocusPath = String(id || "");
        },
        toggleDir(s, dirPath, expanded) {
          const k = String(dirPath || "");
          if (!k) return;
          const nextCollapsed = typeof expanded === "boolean" ? !expanded : !s.collapsedDirs[k];
          s.collapsedDirs = { ...s.collapsedDirs, [k]: nextCollapsed };
        },
        expandAllDirs(s) {
          s.collapsedDirs = {};
        },
        collapseAllDirs(s) {
          const tree = buildFolderTree(s.docs, s.dirs);
          const next = {};
          for (const p of allDirPaths(tree)) next[p] = true;
          s.collapsedDirs = next;
        },
        toggleNewFolder(s) {
          s.showNewFolder = !s.showNewFolder;
          s.newFolderDraft = "";
          s.error = "";
        },
        setNewFolderDraft(s, value) {
          s.newFolderDraft = String(value ?? "");
        },
        async createFolder(s) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const vals = mako.blocks.values("vault");
          const raw = vals["tree-mkdir-input"] ?? s.newFolderDraft;
          const clean = normalizeDirPath(raw);
          if (!clean) {
            s.error = t("error.folderRequired");
            return;
          }
          await ctx.datafs.mkdir(clean);
          s.showNewFolder = false;
          s.newFolderDraft = "";
          s.collapsedDirs = { ...s.collapsedDirs, [clean]: false };
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.createdFolder", { path: clean }) });
        },
        newDocInFolder(s, dirPath) {
          const folder = String(dirPath || "concepts");
          s.draft = emptyDraft(folder, `${folder}/`);
          s.tab = "editor";
          s.error = "";
        },
        async moveDocToFolder(s, targetFolder, droppedPath) {
          const srcPath = String(droppedPath || "").trim();
          if (!srcPath || srcPath === "index.md") return;
          const fileName = srcPath.split("/").pop();
          if (!fileName) return;
          const destFolder = String(targetFolder || "").replace(/^\/+|\/+$/g, "");
          const destPath = destFolder ? `${destFolder}/${fileName}` : fileName;
          if (destPath === srcPath) return;
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          await ctx.datafs.move(srcPath, destPath);
          if (s.selectedPath === srcPath) s.selectedPath = destPath;
          if (s.graphFocusPath === srcPath) s.graphFocusPath = destPath;
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.movedDoc", { path: destPath }) });
        },
        async newDoc(s) {
          const ctx = mako.blocks.context("vault");
          const folder = await defaultFolder(ctx);
          s.draft = emptyDraft(folder);
          s.tab = "editor";
          s.error = "";
        },
        editDoc(s, p) {
          const doc = s.docs.find((d) => d.path === p);
          if (!doc) return;
          s.draft = draftFromDoc(doc);
          s.tab = "editor";
          s.error = "";
        },
        async createFromLink(s, target) {
          const ctx = mako.blocks.context("vault");
          const folder = await defaultFolder(ctx);
          const normPath = normalizeDocPath(target, folder);
          const stem = normPath.replace(/\.md$/, "").split("/").pop() || target;
          const prettyTitle = stem.replace(/[-_]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
          s.draft = emptyDraft(folder, normPath, prettyTitle);
          s.tab = "editor";
          s.error = "";
        },
        cancelEdit(s) {
          s.draft = null;
          s.tab = "browse";
          s.error = "";
        },
        draftField(s, field, value) {
          if (!s.draft) return;
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          if (field === "type") {
            s.draft.type = canonicalDocType(value, t);
          } else if (field === "status") {
            s.draft.status = canonicalDocStatus(value, t);
          } else {
            s.draft[field] = String(value ?? "");
          }
        },
        async saveDraft(s) {
          if (!s.draft) return;
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const vals = mako.blocks.values("vault");
          const d = {
            ...s.draft,
            title: vals["ed-title"] ?? s.draft.title,
            type: canonicalDocType(vals["ed-type"] ?? s.draft.type, t),
            status: canonicalDocStatus(vals["ed-status"] ?? s.draft.status, t),
            path: vals["ed-path"] ?? s.draft.path,
            tags: vals["ed-tags"] ?? s.draft.tags,
            description: vals["ed-desc"] ?? s.draft.description,
            source: vals["ed-source"] ?? s.draft.source,
            verifiedBy: vals["ed-verified"] ?? s.draft.verifiedBy,
            body: vals["ed-body"] ?? s.draft.body,
          };
          const title = String(d.title || "").trim();
          if (!title) {
            s.error = t("error.titleRequired");
            return;
          }
          const folder = d.folder || (await defaultFolder(ctx));
          const rawPath = String(d.path || "").trim();
          const candidatePath = !rawPath || rawPath.endsWith("/")
            ? `${(rawPath || folder).replace(/\/+$/, "")}/${slugify(title) || "note"}.md`
            : rawPath;
          const targetPath = normalizeDocPath(candidatePath, folder);
          if (!targetPath) {
            s.error = t("error.pathRequired");
            return;
          }

          const content = serializeDoc({
            type: d.type,
            title,
            description: d.description,
            status: d.status,
            updated: todayISO(),
            tags: parseTags(d.tags),
            source: d.source,
            verifiedBy: d.verifiedBy,
            body: d.body,
          });

          if (d.originalPath && d.originalPath !== targetPath) {
            await ctx.datafs.move(d.originalPath, targetPath);
          }
          await ctx.datafs.write(targetPath, content);
          await syncIndexFile(ctx);
          s.draft = null;
          s.selectedPath = targetPath;
          s.tab = "browse";
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.savedDoc", { path: targetPath }) });
        },
        async setDocStatus(s, p, nextStatus) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const doc = s.docs.find((d) => d.path === p);
          if (!doc) return;
          const content = serializeDoc({
            okfVersion: doc.okfVersion,
            type: doc.type,
            title: doc.title,
            description: doc.description,
            status: nextStatus,
            updated: todayISO(),
            tags: doc.tags,
            source: doc.source,
            verifiedBy: doc.verifiedBy,
            body: doc.body,
          });
          await ctx.datafs.write(doc.path, content);
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({
            id: "mako.toast",
            arg: t("toast.statusUpdated", { status: labelStatus(t, nextStatus) }),
          });
        },
        askDelete(s, p) {
          s.confirmDeletePath = String(p || "");
        },
        async deleteDoc(s, p) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          await ctx.datafs.delete(String(p || ""));
          s.confirmDeletePath = "";
          if (s.selectedPath === p) s.selectedPath = "";
          if (s.graphFocusPath === p) s.graphFocusPath = "";
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.deletedDoc", { path: p }) });
        },
        async rebuildIndex(s) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const { index } = await loadVaultDocs(ctx, false);
          await ctx.datafs.write("index.md", rebuildIndexMarkdown(ctx.projectName, index.docs));
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.rebuiltIndex") });
        },
      },
    });

    mako.commands.register("rebuild-index", async (ctx) => {
      if (!ctx || !ctx.scopeKey) return;
      const { index } = await loadVaultDocs(ctx, false);
      await ctx.datafs.write("index.md", rebuildIndexMarkdown(ctx.projectName, index.docs));
      mako.blocks.reload("vault", ctx.scopeKey);
    });

    // --- Project-scoped tools for AI agents (mako-plugin CLI) ---

    mako.tools.register("list_docs", async (args, ctx) => {
      const { index, stats } = await loadVaultDocs(ctx, false);
      const tf = String(args?.type || "").trim().toLowerCase();
      const sf = String(args?.status || "").trim().toLowerCase();
      const tgf = String(args?.tag || "").trim().toLowerCase();
      const docs = index.docs
        .filter((d) => (!tf || d.type.toLowerCase() === tf))
        .filter((d) => (!sf || d.status.toLowerCase() === sf))
        .filter((d) => (!tgf || d.tags.some((t) => t.toLowerCase() === tgf)))
        .map((d) => ({
          path: d.path,
          type: d.type,
          title: d.title,
          description: d.description,
          status: d.status,
          updated: d.updated,
          tags: d.tags,
          links: d.links.length,
          backlinks: d.backlinks.length,
        }));
      return { project: ctx.projectName, count: docs.length, usedBytes: stats.usedBytes, docs };
    });

    mako.tools.register("search", async (args, ctx) => {
      const query = String(args?.query || "").trim();
      if (!query) throw new Error("query is required");
      const { index } = await loadVaultDocs(ctx, false);
      const hits = searchDocuments(index.docs, query, args?.type || "", args?.tag || "");
      return {
        project: ctx.projectName,
        query,
        count: hits.length,
        results: hits.slice(0, 20).map(({ doc, score, snippet }) => ({
          path: doc.path,
          type: doc.type,
          title: doc.title,
          status: doc.status,
          tags: doc.tags,
          score,
          snippet,
        })),
      };
    });

    mako.tools.register("read_doc", async (args, ctx) => {
      const target = normalizeDocPath(args?.path, "");
      if (!target) throw new Error("path is required");
      const { index } = await loadVaultDocs(ctx, false);
      const doc = index.docs.find((d) => d.path === target);
      if (!doc) return { found: false, path: target };
      return {
        found: true,
        path: doc.path,
        type: doc.type,
        title: doc.title,
        description: doc.description,
        status: doc.status,
        updated: doc.updated,
        tags: doc.tags,
        provenance: { source: doc.source, verified_by: doc.verifiedBy },
        links: doc.links,
        backlinks: doc.backlinks,
        brokenLinks: doc.brokenLinks,
        body: doc.body,
      };
    });

    mako.tools.register("upsert_doc", async (args, ctx) => {
      const title = String(args?.title || "").trim();
      if (!title) throw new Error("title is required");
      const folder = await defaultFolder(ctx);
      const target = normalizeDocPath(args?.path, folder);
      if (!target) throw new Error("path is required");
      const content = serializeDoc({
        type: DOC_TYPES.includes(args?.type) ? args.type : "Concept",
        title,
        description: args?.description || "",
        status: DOC_STATUSES.includes(args?.status) ? args.status : "active",
        updated: todayISO(),
        tags: parseTags(args?.tags),
        source: args?.source || "",
        verifiedBy: args?.verified_by || "",
        body: args?.body || "",
      });
      const written = await ctx.datafs.write(target, content);
      await syncIndexFile(ctx);
      mako.blocks.reload("vault", ctx.scopeKey);
      return {
        saved: true,
        path: target,
        size: written.size,
        usedBytes: written.usedBytes,
        quotaBytes: written.quotaBytes,
      };
    });

    mako.tools.register("move_doc", async (args, ctx) => {
      const fromPath = normalizeDocPath(args?.from_path, "");
      const toPath = normalizeDocPath(args?.to_path, "");
      if (!fromPath || !toPath) throw new Error("from_path and to_path are required");
      await ctx.datafs.move(fromPath, toPath);
      await syncIndexFile(ctx);
      mako.blocks.reload("vault", ctx.scopeKey);
      return { moved: true, from: fromPath, to: toPath };
    });

    mako.tools.register("delete_doc", async (args, ctx) => {
      const target = normalizeDocPath(args?.path, "");
      if (!target) throw new Error("path is required");
      const res = await ctx.datafs.delete(target);
      await syncIndexFile(ctx);
      mako.blocks.reload("vault", ctx.scopeKey);
      return { deleted: Boolean(res.deleted), path: target };
    });

    mako.tools.register("lint", async (_args, ctx) => {
      const { index } = await loadVaultDocs(ctx, false);
      return {
        project: ctx.projectName,
        docs: index.docs.length,
        hasIndex: index.hasIndex,
        totalLinks: index.totalLinks,
        issueCount: index.issueCount,
        brokenLinks: index.broken,
        missingFrontmatter: index.missingMeta,
        needsReview: index.needsReview,
        orphans: index.orphans,
      };
    });
  },
};
