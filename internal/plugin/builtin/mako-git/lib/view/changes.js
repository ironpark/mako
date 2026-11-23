// Changes section: collapsible header + hierarchical file tree with collapsed
// single-child directory chains, aggregated +added/-removed badges, tri-state
// commit pick checkboxes, tap-to-open-diff, and file/dir context menus.

import { lineCount } from "../i18n.js";
import { safeKey, sanitizeText } from "../state.js";
import { buildGitTree, countBadges, fileBadges, pickState, statusIcon } from "../tree.js";

function makeContextMenu(ui, key, items) {
  if (typeof ui.contextMenu === "function") {
    return ui.contextMenu(key, items);
  }
  return ui.menu("", items, { key, variant: "context" });
}

export function renderSectionHeader(ui, { key, title, count, open, toggleSection, action, t }) {
  const toggleName = open
    ? t("Collapse {title}", { title })
    : t("Expand {title}", { title });
  const btn = ui.button(title, {
    key: `${key}-toggle`,
    icon: open ? "chevron-down" : "chevron-right",
    variant: "ghost",
    size: "small",
    weight: "semibold",
    tone: "muted",
    placeholder: toggleName,
    onTap: ["toggleSection", toggleSection],
  });
  if (count > 0) {
    btn.badges([{ text: lineCount(count), tone: "faint", variant: "soft" }]);
  }
  const kids = [btn, ui.text("", { key: `${key}-spacer`, grow: 1 })];
  if (action) kids.push(action);
  return ui.row(kids, { key: `${key}-head`, gap: 2 });
}

export function renderChangeContextMenus(s, ui, t) {
  const readOnly = Boolean(s && s.base);
  const fileItems = [
    ui.menuItem(t("Open diff"), { key: "open-diff", onTap: ["openFileDiff"] }),
    ui.menuItem(t("Open file"), { key: "open-file", onTap: ["openInEditor"] }),
  ];
  if (!readOnly) {
    fileItems.push(
      ui.menuItem(t("Discard changes…"), { key: "discard", tone: "danger", onTap: ["askDiscard"] }),
      ui.menuItem(t("Add to .gitignore"), { key: "ignore", onTap: ["ignorePath"] }),
    );
  }
  fileItems.push(
    ui.divider({ key: "cf-div" }),
    ui.menuItem(t("Copy path"), { key: "copy-path", onTap: ["copyPath"] }),
  );
  const fileMenu = makeContextMenu(ui, "change-file", fileItems);

  const dirItems = [];
  if (!readOnly) {
    dirItems.push(
      ui.menuItem(t("Discard changes…"), { key: "discard", tone: "danger", onTap: ["askDiscard"] }),
      ui.menuItem(t("Add to .gitignore"), { key: "ignore", onTap: ["ignorePath"] }),
      ui.divider({ key: "cd-div" }),
    );
  }
  dirItems.push(ui.menuItem(t("Copy path"), { key: "copy-path", onTap: ["copyPath"] }));
  const dirMenu = makeContextMenu(ui, "change-dir", dirItems);

  return [fileMenu, dirMenu];
}

function renderTreeNodes(items, s, ui, t) {
  const allowPick = !s.base;
  const closed = s.closed || {};
  const skip = s.skip || {};

  return items.map((it) => {
    const label = sanitizeText(it.name);
    if (it.dir) {
      const isClosed = Boolean(closed[it.key]);
      const children = renderTreeNodes(it.children, s, ui, t);
      const opts = {
        key: safeKey("d", it.key),
        icon: isClosed ? "folder" : "folder-open",
        checked: !isClosed,
        onChange: ["toggleDir", it.key],
      };
      if (allowPick) {
        opts.placeholder = t("Commit {name}", { name: label });
      }
      const node = ui.treeItem(label, children, opts);
      node.badges(countBadges(it.added, it.removed));
      if (allowPick) {
        node
          .check(pickState(it, skip))
          .onCheck("pick", it.key);
      }
      if (it.key.length <= 256) {
        node.contextMenu("change-dir", it.key);
      }
      return node;
    }

    const si = statusIcon(it.file && it.file.status);
    const opts = {
      key: safeKey("f", it.key),
      icon: si.icon,
      tone: si.tone,
      variant: "badge",
      onTap: ["openFile", it.key],
    };
    if (allowPick) {
      opts.placeholder = t("Commit {name}", { name: label });
    }
    const node = ui.treeItem(label, opts);
    node.badges(fileBadges(it.file, t));
    if (allowPick) {
      node
        .check(pickState(it, skip))
        .onCheck("pick", it.key);
    }
    if (it.key.length <= 256) {
      node.contextMenu("change-file", it.key);
    }
    return node;
  });
}

export function renderChangesBody(s, ui, ctx) {
  const t = ctx.t;
  const st = s.status;
  const files = (st && st.files) || [];

  if (st && st.notRepo) {
    return ui.text(t("Not a git repository."), { key: "changes-not-repo", size: "small", tone: "muted" });
  }
  if (s.statusErr && files.length === 0) {
    return ui.text(sanitizeText(s.statusErr), { key: "changes-err", size: "small", tone: "danger" });
  }
  if (!st) {
    return ui.progress(0, { key: "changes-loading", variant: "spinner", text: t("Loading…") });
  }
  if (files.length === 0) {
    return ui.text(t("No changes."), { key: "changes-empty", size: "small", tone: "muted" });
  }

  const roots = buildGitTree(files);
  return ui.tree(renderTreeNodes(roots, s, ui, t), { key: "changes-tree" });
}

export function renderChangesSection(s, ui, ctx) {
  const t = ctx.t;
  const files = (s.status && s.status.files) || [];
  const open = Boolean(s.open && s.open.changes);
  const children = [
    renderSectionHeader(ui, {
      key: "sec-changes",
      title: t("Changes"),
      count: files.length,
      open,
      toggleSection: "changes",
      t,
    }),
  ];
  if (open) {
    children.push(renderChangesBody(s, ui, ctx));
  }
  return ui.column(children, { key: "changes-section", gap: 2 });
}
