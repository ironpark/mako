// OKF (Open Knowledge Format) project knowledge vault plugin.
// Stores pure OKF v0.2 Markdown + YAML frontmatter files inside each project's
// isolated private data directory (ctx.datafs) and exposes MCP/CLI tools for agents.

import {
  DOC_STATUSES,
  DOC_TYPES,
  FOLDERS,
  buildVaultIndex,
  normalizeDocPath,
  parseDocument,
  parseTags,
  rebuildIndexMarkdown,
  searchDocuments,
  serializeDoc,
  slugify,
  starterOverviewMarkdown,
  todayISO,
} from "./lib/okf.js";

let mako;

const TYPE_FILTERS = ["All", ...DOC_TYPES, "Index"];

function safeKey(prefix, raw) {
  const clean = String(raw || "")
    .replace(/[\/#]+/g, "_")
    .slice(0, 96);
  return `${prefix}-${clean}`;
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

function formatBytes(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  return `${(v / (1024 * 1024)).toFixed(2)} MB`;
}

// formatBodyForReader renders OKF [[target|Label]] wiki-links cleanly as bold
// text for the reader view while leaving fenced code blocks and inline backtick
// spans untouched. Underlying files always keep pure OKF [[...]] syntax.
function formatBodyForReader(body) {
  const src = String(body || "");
  if (!src.includes("[[")) return src;
  const parts = src.split(/(```[\s\S]*?```|`[^`\n]+`)/g);
  return parts
    .map((part, i) => {
      if (i % 2 === 1) return part; // fenced or inline code
      return part.replace(/\[\[([^\]|#]+)(?:#[^\]|]+)?(?:\|([^\]]+))?\]\]/g, (_m, target, label) => {
        const display = (label || target || "").trim();
        return display ? `**${display}**` : _m;
      });
    })
    .join("");
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
  let mdEntries = (listed.entries || []).filter((e) => !e.dir && e.path.endsWith(".md"));

  if (bootstrap && mdEntries.length === 0) {
    const overview = starterOverviewMarkdown(ctx.projectName);
    await ctx.datafs.write("concepts/overview.md", overview);
    const seedDocs = [parseDocument("concepts/overview.md", overview)];
    const indexMd = rebuildIndexMarkdown(ctx.projectName, seedDocs);
    await ctx.datafs.write("index.md", indexMd);
    listed = await ctx.datafs.list("", { recursive: true });
    mdEntries = (listed.entries || []).filter((e) => !e.dir && e.path.endsWith(".md"));
  }

  const docs = [];
  for (const entry of mdEntries) {
    const file = await ctx.datafs.read(entry.path);
    if (!file.found || file.binary) continue;
    docs.push(parseDocument(entry.path, file.text || "", file.size || entry.size || 0, file.modifiedMs || entry.modifiedMs || 0));
  }
  const index = buildVaultIndex(docs);
  const stats = await ctx.datafs.stats();
  return { index, stats };
}

async function syncIndexFile(ctx) {
  if (!(await shouldAutoIndex(ctx))) return;
  const { index } = await loadVaultDocs(ctx, false);
  const md = rebuildIndexMarkdown(ctx.projectName, index.docs);
  await ctx.datafs.write("index.md", md);
}

async function refreshState(s, ctx, bootstrap = false) {
  const { index, stats } = await loadVaultDocs(ctx, bootstrap);
  s.docs = index.docs;
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

function renderTopBar(s, ui, activeTab, tabOptions) {
  return ui.row([
    ui.tabs(activeTab, tabOptions, { key: "main-tabs", onChange: ["setTab"] }),
    ui.text(`${s.docs.length} docs · ${formatBytes(s.usedBytes)}`, { size: "small", tone: "faint", grow: 1 }),
    ui.button("New", { key: "btn-new", icon: "file-text", tone: "accent", onTap: ["newDoc"] }),
    ui.button("Index", { key: "btn-index", icon: "folder-open", onTap: ["rebuildIndex"] }),
  ], { gap: 6 });
}

function renderBrowse(s, ui) {
  const results = searchDocuments(s.docs, s.query, s.typeFilter === "All" ? "" : s.typeFilter, s.tagFilter);
  const selected = s.selectedPath ? s.docs.find((d) => d.path === s.selectedPath) : null;

  if (selected) {
    return renderReader(s, ui, selected);
  }

  const controls = [
    ui.row([
      ui.input(s.query, {
        key: "search",
        placeholder: "Search title, tags, or body…",
        grow: 1,
        onChange: ["setQuery"],
        onSubmit: ["setQuery"],
      }),
      ui.select(s.typeFilter, TYPE_FILTERS, { key: "type-filter", onChange: ["setTypeFilter"] }),
    ], { gap: 6 }),
  ];

  if (s.tagFilter) {
    controls.push(ui.row([
      ui.text("Tag:", { size: "small", tone: "muted" }),
      ui.chip(`#${s.tagFilter}`, { tone: "accent" }),
      ui.button("Clear", { key: "clear-tag", size: "small", onTap: ["setTagFilter", ""] }),
    ], { gap: 6 }));
  }

  if (results.length === 0) {
    controls.push(ui.card([
      ui.text("No matching OKF documents.", { tone: "muted" }),
      ui.button("Create Document", { key: "empty-new", tone: "accent", onTap: ["newDoc"] }),
    ], { gap: 8 }));
    return ui.column(controls, { gap: 8 });
  }

  // Let the outer block frame scroll full-height instead of clipping inside a fixed maxHeight.
  controls.push(
    ui.list(results, ({ doc, snippet }) => {
      const metaRow = [
        ui.pill(doc.type, { tone: typeTone(doc.type) }),
        ui.pill(doc.status, { tone: statusTone(doc.status) }),
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
          ui.scroll(
            doc.tags.slice(0, 6).map((t) => ui.chip(`#${t}`, { key: safeKey("t", t), tone: "muted", onTap: ["setTagFilter", t] })),
            { direction: "horizontal", gap: 4 },
          ),
        );
      }
      return ui.card(cardChildren, { key: safeKey("doc", doc.path), gap: 4, onTap: ["openDoc", doc.path] });
    }, { gap: 6 }),
  );

  return ui.column(controls, { gap: 8 });
}

function renderReader(s, ui, doc) {
  const byPath = new Map(s.docs.map((d) => [d.path, d]));
  const actionBar = ui.row([
    ui.button("Back", { key: "reader-back", icon: "arrow-left", onTap: ["closeDoc"] }),
    ui.text("", { grow: 1 }),
    ui.button("Edit", { key: "reader-edit", tone: "accent", onTap: ["editDoc", doc.path] }),
    doc.status === "needs_review"
      ? ui.button("Active", { key: "reader-active", onTap: ["setDocStatus", doc.path, "active"] })
      : ui.button("Review", { key: "reader-review", onTap: ["setDocStatus", doc.path, "needs_review"] }),
    s.confirmDeletePath === doc.path
      ? ui.button("Confirm", { key: "reader-del-confirm", tone: "danger", onTap: ["deleteDoc", doc.path] })
      : ui.button("Delete", { key: "reader-del", tone: "danger", onTap: ["askDelete", doc.path] }),
  ], { gap: 6 });

  const metaLines = [
    ui.row([
      ui.pill(doc.type, { tone: typeTone(doc.type) }),
      ui.pill(doc.status, { tone: statusTone(doc.status) }),
      ui.text(doc.path, { size: "small", tone: "muted", grow: 1 }),
      doc.updated ? ui.text(doc.updated, { size: "small", tone: "faint" }) : ui.text("", { size: "small" }),
    ], { gap: 6 }),
    ui.text(doc.title, { size: "large", weight: "semibold" }),
  ];
  if (doc.description) {
    metaLines.push(ui.text(doc.description, { size: "small", tone: "muted" }));
  }
  if (doc.tags.length > 0) {
    metaLines.push(
      ui.scroll(
        doc.tags.map((t) => ui.chip(`#${t}`, { key: safeKey("rt", t), tone: "accent", onTap: ["filterTagAndClose", t] })),
        { direction: "horizontal", gap: 4 },
      ),
    );
  }
  if (doc.source || doc.verifiedBy) {
    const prov = [doc.source ? `Source: ${doc.source}` : "", doc.verifiedBy ? `Verified by: ${doc.verifiedBy}` : ""]
      .filter(Boolean)
      .join(" · ");
    metaLines.push(ui.text(prov, { size: "small", tone: "faint" }));
  }

  // Full-height body: no nested maxHeight scroll so the document fills the sidebar naturally.
  const sections = [
    actionBar,
    ui.card(metaLines, { gap: 4 }),
    ui.card([
      ui.markdown(formatBodyForReader(doc.body) || "*Empty document.*"),
    ], { gap: 6 }),
  ];

  if (doc.links.length > 0 || doc.backlinks.length > 0 || doc.brokenLinks.length > 0) {
    const graphRows = [];
    if (doc.links.length > 0) {
      graphRows.push(ui.text(`Links (${doc.links.length})`, { size: "small", weight: "semibold", tone: "muted" }));
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
      graphRows.push(ui.text(`Backlinks (${doc.backlinks.length})`, { size: "small", weight: "semibold", tone: "muted" }));
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
      graphRows.push(ui.text(`Broken links (${doc.brokenLinks.length})`, { size: "small", weight: "semibold", tone: "danger" }));
      graphRows.push(
        ui.list(doc.brokenLinks, (target) => ui.row([
          ui.text(`[[${target}]]`, { size: "small", tone: "warning", grow: 1 }),
          ui.button("Create", { key: safeKey("fix", target), size: "small", tone: "warning", onTap: ["createFromLink", target] }),
        ], { key: safeKey("brkr", target), gap: 6 }), { gap: 4 }),
      );
    }
    sections.push(ui.card(graphRows, { gap: 6 }));
  }

  return ui.column(sections, { gap: 8 });
}

function renderEditor(s, ui) {
  const d = s.draft || emptyDraft();
  const heading = d.originalPath ? `Edit ${d.originalPath}` : "New OKF Document";
  return ui.column([
    ui.row([
      ui.text(heading, { weight: "semibold", grow: 1 }),
      ui.button("Cancel", { key: "ed-cancel", onTap: ["cancelEdit"] }),
      ui.button("Save", { key: "ed-save", tone: "accent", onTap: ["saveDraft"] }),
    ], { gap: 6 }),
    ui.card([
      ui.row([
        ui.input(d.title, { key: "ed-title", placeholder: "Document title", grow: 1, onChange: ["draftField", "title"] }),
        ui.select(d.type, DOC_TYPES, { key: "ed-type", onChange: ["draftField", "type"] }),
        ui.select(d.status, DOC_STATUSES, { key: "ed-status", onChange: ["draftField", "status"] }),
      ], { gap: 6 }),
      ui.row([
        ui.input(d.path, { key: "ed-path", placeholder: `${d.folder || "concepts"}/topic-name.md`, grow: 1, onChange: ["draftField", "path"] }),
        ui.input(d.tags, { key: "ed-tags", placeholder: "tags: auth, api", grow: 1, onChange: ["draftField", "tags"] }),
      ], { gap: 6 }),
      ui.input(d.description, { key: "ed-desc", placeholder: "One-line summary (OKF description)", onChange: ["draftField", "description"] }),
      ui.row([
        ui.input(d.source, { key: "ed-source", placeholder: "Provenance source (optional)", grow: 1, onChange: ["draftField", "source"] }),
        ui.input(d.verifiedBy, { key: "ed-verified", placeholder: "Verified by (optional)", grow: 1, onChange: ["draftField", "verifiedBy"] }),
      ], { gap: 6 }),
      ui.textarea(d.body, {
        key: "ed-body",
        size: "large",
        placeholder: "# Heading\n\nWrite Markdown and link concepts with [[concepts/other-doc]]…",
        onChange: ["draftField", "body"],
      }),
    ], { gap: 8 }),
  ], { gap: 8 });
}

function renderHealth(s, ui) {
  const statsCard = ui.card([
    ui.scroll([
      ui.pill(`${s.docs.length} docs`, { tone: "accent" }),
      ui.pill(`${s.totalLinks} links`, { tone: "success" }),
      ui.pill(`${s.broken.length} broken`, { tone: s.broken.length > 0 ? "danger" : "muted" }),
      ui.pill(`${s.needsReview.length} review`, { tone: s.needsReview.length > 0 ? "warning" : "muted" }),
      ui.pill(`${s.orphans.length} orphans`, { tone: "muted" }),
    ], { direction: "horizontal", gap: 6 }),
    ui.text(`Vault storage: ${formatBytes(s.usedBytes)} / ${formatBytes(s.quotaBytes)} (${s.fileCount} files)`, { size: "small", tone: "muted" }),
  ], { gap: 6 });

  const items = [statsCard];

  if (!s.hasIndex) {
    items.push(ui.card([
      ui.text("Missing OKF root index.md", { weight: "medium", tone: "warning" }),
      ui.button("Generate index.md", { key: "lint-mk-index", tone: "accent", onTap: ["rebuildIndex"] }),
    ], { gap: 6 }));
  }

  if (s.broken.length > 0) {
    items.push(ui.card([
      ui.text(`Broken Links (${s.broken.length})`, { weight: "semibold", tone: "danger" }),
      ui.list(s.broken, (b, i) => ui.row([
        ui.text(`${b.from} → [[${b.target}]]`, { size: "small", grow: 1 }),
        ui.button("Create", { key: `lint-fix-${i}`, size: "small", tone: "warning", onTap: ["createFromLink", b.target] }),
      ], { key: `brk-${i}`, gap: 6 })),
    ], { gap: 6 }));
  }

  if (s.needsReview.length > 0) {
    items.push(ui.card([
      ui.text(`Needs Review (${s.needsReview.length})`, { weight: "semibold", tone: "warning" }),
      ui.list(s.needsReview, (r) => ui.row([
        ui.text(`${r.title} (${r.path})`, { size: "small", grow: 1 }),
        ui.button("Open", { key: safeKey("rev-open", r.path), size: "small", onTap: ["openDoc", r.path] }),
      ], { key: safeKey("rev", r.path), gap: 6 })),
    ], { gap: 6 }));
  }

  if (s.orphans.length > 0) {
    items.push(ui.card([
      ui.text(`Unlinked Documents (${s.orphans.length})`, { weight: "semibold", tone: "muted" }),
      ui.list(s.orphans, (o) => ui.row([
        ui.text(`${o.title} (${o.path})`, { size: "small", grow: 1 }),
        ui.button("Open", { key: safeKey("orp-open", o.path), size: "small", onTap: ["openDoc", o.path] }),
      ], { key: safeKey("orp", o.path), gap: 6 })),
    ], { gap: 6 }));
  }

  if (s.issueCount === 0 && s.orphans.length === 0) {
    items.push(ui.card([
      ui.row([
        ui.icon("circle-check", { tone: "success" }),
        ui.text("All OKF documents have valid frontmatter and resolved links.", { tone: "success" }),
      ], { gap: 6 }),
    ]));
  }

  return ui.column(items, { gap: 8 });
}

export default {
  activate(m) {
    mako = m;

    mako.blocks.register("vault", {
      state: {
        tab: "Browse",
        query: "",
        typeFilter: "All",
        tagFilter: "",
        selectedPath: "",
        confirmDeletePath: "",
        draft: null,
        docs: [],
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
      render(s, ui) {
        const healthLabel = s.issueCount > 0 ? `Health (${s.issueCount})` : "Health";
        const tabOptions = s.draft ? ["Browse", healthLabel, "Editor"] : ["Browse", healthLabel];
        const activeTab = s.tab === "Health" ? healthLabel : (s.tab === "Editor" && s.draft ? "Editor" : "Browse");

        const parts = [
          renderTopBar(s, ui, activeTab, tabOptions),
        ];
        if (s.error) {
          parts.push(ui.card([ui.text(s.error, { tone: "danger", size: "small" })]));
        }
        if (activeTab === "Editor" && s.draft) {
          parts.push(renderEditor(s, ui));
        } else if (activeTab === healthLabel) {
          parts.push(renderHealth(s, ui));
        } else {
          parts.push(renderBrowse(s, ui));
        }
        return ui.column(parts, { gap: 8 });
      },
      on: {
        setTab(s, value) {
          const v = String(value || "Browse");
          if (v.startsWith("Health")) s.tab = "Health";
          else if (v === "Editor" && s.draft) s.tab = "Editor";
          else s.tab = "Browse";
          s.confirmDeletePath = "";
        },
        setQuery(s, value) {
          s.query = String(value ?? "");
        },
        setTypeFilter(s, value) {
          s.typeFilter = String(value || "All");
        },
        setTagFilter(s, tag) {
          s.tagFilter = String(tag || "");
        },
        filterTagAndClose(s, tag) {
          s.tagFilter = String(tag || "");
          s.selectedPath = "";
          s.tab = "Browse";
        },
        openDoc(s, p) {
          s.selectedPath = String(p || "");
          s.confirmDeletePath = "";
          s.tab = "Browse";
        },
        closeDoc(s) {
          s.selectedPath = "";
          s.confirmDeletePath = "";
        },
        async newDoc(s) {
          const ctx = mako.blocks.context("vault");
          const folder = await defaultFolder(ctx);
          s.draft = emptyDraft(folder);
          s.tab = "Editor";
          s.error = "";
        },
        editDoc(s, p) {
          const doc = s.docs.find((d) => d.path === p);
          if (!doc) return;
          s.draft = draftFromDoc(doc);
          s.tab = "Editor";
          s.error = "";
        },
        async createFromLink(s, target) {
          const ctx = mako.blocks.context("vault");
          const folder = await defaultFolder(ctx);
          const normPath = normalizeDocPath(target, folder);
          const stem = normPath.replace(/\.md$/, "").split("/").pop() || target;
          const prettyTitle = stem.replace(/[-_]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
          s.draft = emptyDraft(folder, normPath, prettyTitle);
          s.tab = "Editor";
          s.error = "";
        },
        cancelEdit(s) {
          s.draft = null;
          s.tab = "Browse";
          s.error = "";
        },
        draftField(s, field, value) {
          if (!s.draft) return;
          s.draft[field] = String(value ?? "");
        },
        async saveDraft(s) {
          if (!s.draft) return;
          const ctx = mako.blocks.context("vault");
          const vals = mako.blocks.values("vault");
          const d = {
            ...s.draft,
            title: vals["ed-title"] ?? s.draft.title,
            type: vals["ed-type"] ?? s.draft.type,
            status: vals["ed-status"] ?? s.draft.status,
            path: vals["ed-path"] ?? s.draft.path,
            tags: vals["ed-tags"] ?? s.draft.tags,
            description: vals["ed-desc"] ?? s.draft.description,
            source: vals["ed-source"] ?? s.draft.source,
            verifiedBy: vals["ed-verified"] ?? s.draft.verifiedBy,
            body: vals["ed-body"] ?? s.draft.body,
          };
          const title = String(d.title || "").trim();
          if (!title) {
            s.error = "Title is required.";
            return;
          }
          const folder = d.folder || (await defaultFolder(ctx));
          const rawPath = String(d.path || "").trim() || `${folder}/${slugify(title) || "note"}.md`;
          const targetPath = normalizeDocPath(rawPath, folder);
          if (!targetPath) {
            s.error = "Valid relative .md path is required.";
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
          s.tab = "Browse";
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: `Saved ${targetPath}` });
        },
        async setDocStatus(s, p, nextStatus) {
          const ctx = mako.blocks.context("vault");
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
        },
        askDelete(s, p) {
          s.confirmDeletePath = String(p || "");
        },
        async deleteDoc(s, p) {
          const ctx = mako.blocks.context("vault");
          await ctx.datafs.delete(String(p || ""));
          s.confirmDeletePath = "";
          if (s.selectedPath === p) s.selectedPath = "";
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: `Deleted ${p}` });
        },
        async rebuildIndex(s) {
          const ctx = mako.blocks.context("vault");
          const { index } = await loadVaultDocs(ctx, false);
          await ctx.datafs.write("index.md", rebuildIndexMarkdown(ctx.projectName, index.docs));
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: "Rebuilt OKF index.md" });
        },
      },
    });

    mako.commands.register("rebuild-index", async (ctx) => {
      if (!ctx || !ctx.scopeKey) return;
      const { index } = await loadVaultDocs(ctx, false);
      await ctx.datafs.write("index.md", rebuildIndexMarkdown(ctx.projectName, index.docs));
      mako.blocks.reload("vault", ctx.scopeKey);
    });

    // --- Project-scoped tools for AI agents (MCP & mako-plugin CLI) ---

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
