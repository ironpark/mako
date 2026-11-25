// Commit history (data-driven `dag` node) and resizable commit detail pane.

import { ago, formatDateTime, shortHash } from "../i18n.js";
import { safeKey, sanitizeText, truncateRunes } from "../state.js";
import { fileBadges, statusIcon } from "../tree.js";
import { renderSectionHeader } from "./changes.js";

function makeContextMenu(ui, key, items) {
  if (typeof ui.contextMenu === "function") {
    return ui.contextMenu(key, items);
  }
  return ui.menu("", items, { key, variant: "context" });
}

export function renderCommitContextMenu(s, ui, t) {
  const busy = Boolean(s.busy);
  return makeContextMenu(ui, "commit", [
    ui.menuItem(t("Check out"), { key: "detach", disabled: busy, onTap: ["askDetach"] }),
    ui.menuItem(t("Create branch here…"), { key: "branch", disabled: busy, onTap: ["askBranch"] }),
    ui.menuItem(t("Create tag…"), { key: "tag", disabled: busy, onTap: ["askTag"] }),
    ui.divider({ key: "cm-div-1" }),
    ui.menuItem(t("Cherry-pick"), { key: "cherry-pick", disabled: busy, onTap: ["cherryPick"] }),
    ui.menuItem(t("Revert"), { key: "revert", disabled: busy, onTap: ["revertCommit"] }),
    ui.divider({ key: "cm-div-2" }),
    ui.menuItem(t("Copy hash"), { key: "copy-hash", onTap: ["copyCommitHash"] }),
    ui.menuItem(t("Copy subject"), { key: "copy-subject", onTap: ["copyCommitSubject"] }),
  ]);
}

function refBadges(refs) {
  const list = Array.isArray(refs) ? refs : [];
  const out = [];
  for (let i = 0; i < list.length; i++) {
    if (i === 2) {
      out.push({ text: `+${list.length - i}`, tone: "faint", variant: "soft" });
      break;
    }
    const raw = sanitizeText(list[i]);
    if (!raw) continue;
    let text = raw;
    let tone = "muted";
    let icon = "git-branch";
    if (raw.startsWith("HEAD -> ")) {
      text = raw.slice(8);
      tone = "accent";
    } else if (raw === "HEAD") {
      tone = "accent";
    } else if (raw.startsWith("tag: ")) {
      text = raw.slice(5);
      icon = "tag";
    }
    if (!text) continue;
    out.push({ text: truncateRunes(text, 32), tone, variant: "soft", icon });
  }
  return out;
}

function isHeadCommit(refs) {
  return Array.isArray(refs) && refs.some((r) => r === "HEAD" || String(r).startsWith("HEAD -> "));
}

function toDagRows(commits, t) {
  const out = [];
  const seen = new Set();
  for (const c of commits || []) {
    const id = String(c.hash || "").slice(0, 64);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const when = ago(c.time, t);
    const author = sanitizeText(c.author || "");
    const detail = when ? (author ? `${author} · ${when}` : when) : author;
    out.push({
      id,
      parents: Array.isArray(c.parents)
        ? c.parents.map((p) => String(p || "").slice(0, 64)).filter(Boolean).slice(0, 8)
        : [],
      text: truncateRunes(sanitizeText(c.subject || ""), 320),
      detail: truncateRunes(detail, 320),
      meta: shortHash(id),
      badges: refBadges(c.refs),
      strong: isHeadCommit(c.refs),
    });
  }
  return out;
}

export function renderHistoryHeader(s, ui, ctx, standalone) {
  const t = ctx.t;
  const refreshBtn = ui.button("", {
    key: "hist-refresh",
    icon: "refresh-cw",
    variant: "ghost",
    size: "small",
    placeholder: t("Refresh"),
    onTap: ["refreshHistory"],
  });

  if (standalone) {
    return ui.card([
      ui.row([
        ui.text(t("History"), { key: "hist-title", size: "small", weight: "semibold", tone: "muted", grow: 1 }),
        refreshBtn,
      ], { key: "hist-head", gap: 4 }),
    ], { key: "hist-head-pad", variant: "ghost", gap: 0 });
  }

  return ui.card([
    renderSectionHeader(ui, {
      key: "sec-history",
      title: t("History"),
      count: 0,
      open: Boolean(s.open && s.open.history),
      toggleSection: "history",
      action: refreshBtn,
      t,
    }),
  ], { key: "hist-head-pad", variant: "ghost", gap: 0 });
}

function renderCommitDetail(s, ui, ctx) {
  const t = ctx.t;
  const cv = s.commit || { hash: "", info: null, loading: false, err: "" };

  const head = ui.card([
    ui.row([
      ui.icon("git-commit-horizontal", { key: "cd-icon", size: "small", tone: "muted" }),
      ui.text(shortHash(cv.hash), { key: "cd-hash", size: "small", weight: "medium", grow: 1 }),
      ui.button("", {
        key: "cd-copy",
        icon: "copy",
        variant: "ghost",
        size: "small",
        placeholder: t("Copy hash"),
        onTap: ["copyCommitHash", cv.hash],
      }),
      ui.button("", {
        key: "cd-close",
        icon: "x",
        variant: "ghost",
        size: "small",
        placeholder: t("Close commit"),
        onTap: ["closeCommit"],
      }),
    ], { key: "cd-head", gap: 4 }),
  ], { key: "cd-head-pad", variant: "ghost", gap: 0 });

  let body;
  if (cv.loading) {
    body = ui.card([
      ui.progress(0, { key: "cd-loading", variant: "spinner", text: t("Loading…") }),
    ], { key: "cd-loading-pad", variant: "ghost", gap: 0 });
  } else if (cv.err) {
    body = ui.card([
      ui.text(sanitizeText(cv.err), { key: "cd-err", size: "small", tone: "danger" }),
    ], { key: "cd-err-pad", variant: "ghost", gap: 0 });
  } else if (cv.info) {
    const info = cv.info;
    const cm = info.commit || {};
    const metaKids = [
      ui.text(sanitizeText(cm.subject || ""), { key: "cd-subj", weight: "semibold" }),
    ];
    if (info.body) {
      metaKids.push(ui.text(sanitizeText(info.body, true), { key: "cd-body", size: "small", tone: "muted" }));
    }
    const authorLine = info.email
      ? `${sanitizeText(cm.author || "")} <${sanitizeText(info.email)}>`
      : sanitizeText(cm.author || "");
    const timeLine = formatDateTime(cm.time);
    const whoWhen = timeLine ? `${authorLine}\n${timeLine}` : authorLine;
    if (whoWhen) {
      metaKids.push(ui.text(whoWhen, { key: "cd-meta", size: "small", tone: "muted" }));
    }

    const files = Array.isArray(info.files) ? info.files : [];
    const fileNodes = files.length === 0
      ? [ui.text(t("No files changed."), { key: "cd-no-files", size: "small", tone: "muted" })]
      : files.map((f) => {
          const si = statusIcon(f.status);
          const btn = ui.button(sanitizeText(f.path || ""), {
            key: safeKey("cf", f.path || ""),
            icon: si.icon,
            tone: si.tone,
            variant: "ghost",
            size: "small",
            onTap: ["openCommitFile", f.path || ""],
          });
          btn.badges(fileBadges(f, t));
          return btn;
        });

    body = ui.scroll([
      ui.card([
        ui.column(metaKids, { key: "cd-meta-col", gap: 6 }),
        ui.divider({ key: "cd-meta-div" }),
        ui.column(fileNodes, { key: "cd-files", gap: 2 }),
      ], { key: "cd-body-pad", variant: "ghost", gap: 8 }),
    ], { key: "cd-scroll", grow: 1, gap: 0 });
  } else {
    body = ui.text("", { key: "cd-empty" });
  }

  return ui.column([
    head,
    ui.divider({ key: "cd-div" }),
    body,
  ], { key: "commit-detail", grow: 1, gap: 0 });
}

export function renderHistoryPane(s, ui, ctx, standalone) {
  const t = ctx.t;
  const h = s.history;
  const commits = (h && h.commits) || [];
  const head = renderHistoryHeader(s, ui, ctx, standalone);

  let listContent;
  if (s.historyErr && commits.length === 0) {
    listContent = ui.card([
      ui.text(sanitizeText(s.historyErr), { key: "hist-err", size: "small", tone: "danger" }),
    ], { key: "hist-err-pad", variant: "ghost", gap: 0 });
  } else if (!h) {
    listContent = ui.card([
      ui.progress(0, { key: "hist-loading", variant: "spinner", text: t("Loading…") }),
    ], { key: "hist-loading-pad", variant: "ghost", gap: 0 });
  } else if (commits.length === 0) {
    listContent = ui.card([
      ui.text(t("No commits yet."), { key: "hist-empty", size: "small", tone: "muted" }),
    ], { key: "hist-empty-pad", variant: "ghost", gap: 0 });
  } else {
    const rows = toDagRows(commits, t);
    const selHash = (s.commit && s.commit.hash) || "";
    const selectedValid = selHash && rows.some((r) => r.id === selHash);
    const dag = ui.dag(rows, {
      key: "history-dag",
      value: selectedValid ? selHash : "",
      grow: 1,
      onChange: ["selectCommit"],
    });
    dag.contextMenu("commit");

    if (h.more) {
      listContent = ui.column([
        dag,
        ui.card([
          ui.text(t("Showing the newest {count} commits.", { count: commits.length }), {
            key: "hist-more",
            size: "small",
            tone: "faint",
          }),
        ], { key: "hist-more-pad", variant: "ghost", gap: 0 }),
      ], { key: "hist-dag-wrap", grow: 1, gap: 0 });
    } else {
      listContent = dag;
    }
  }

  const listPane = ui.column([head, listContent], { key: "history-list", grow: 1, gap: 0 });

  if (s.commit && s.commit.hash) {
    return ui.split(listPane, renderCommitDetail(s, ui, ctx), {
      key: "commit-split",
      direction: standalone ? "auto" : "vertical",
      ratio: 0.5,
      text: t("Commit divider"),
      grow: 1,
    });
  }

  return listPane;
}
