// Port of native gitTree (internal/gui/changes.go:122-170):
// folders first, collapsed single-directory chains ("app/src/gen"), aggregated
// +added/-removed line counts, and tri-state commit pick helpers.

import { lineCount } from "./i18n.js";

export function buildGitTree(files) {
  const root = { item: null, kids: new Map() };
  for (const f of files || []) {
    const path = String(f.path || "");
    if (!path) continue;
    const parts = path.split("/");
    let cur = root;
    for (let i = 0; i < parts.length; i++) {
      const part = parts[i];
      let next = cur.kids.get(part);
      if (!next) {
        next = {
          item: {
            key: parts.slice(0, i + 1).join("/"),
            name: part,
            dir: i < parts.length - 1,
            added: 0,
            removed: 0,
            file: null,
            children: [],
          },
          kids: new Map(),
        };
        cur.kids.set(part, next);
      }
      next.item.added += Number(f.added) || 0;
      next.item.removed += Number(f.removed) || 0;
      if (i === parts.length - 1) {
        next.item.file = f;
      }
      cur = next;
    }
  }

  function build(node) {
    const out = [];
    for (const child of node.kids.values()) {
      const it = { ...child.item, children: build(child) };
      while (it.dir && it.children.length === 1 && it.children[0].dir) {
        const only = it.children[0];
        it.key = only.key;
        it.name = `${it.name}/${only.name}`;
        it.children = only.children;
      }
      out.push(it);
    }
    out.sort((a, b) => {
      if (a.dir !== b.dir) return a.dir ? -1 : 1;
      const al = a.name.toLowerCase();
      const bl = b.name.toLowerCase();
      if (al < bl) return -1;
      if (al > bl) return 1;
      return 0;
    });
    return out;
  }

  return build(root);
}

export function findTreeItem(items, key) {
  for (const it of items || []) {
    if (it.key === key) return it;
    if (it.dir && it.children && it.children.length > 0) {
      const found = findTreeItem(it.children, key);
      if (found) return found;
    }
  }
  return null;
}

export function itemPaths(item, out = []) {
  if (!item) return out;
  if (!item.dir) {
    if (item.file && item.file.path) out.push(item.file.path);
    return out;
  }
  for (const c of item.children || []) {
    itemPaths(c, out);
  }
  return out;
}

export function itemFiles(item, out = []) {
  if (!item) return out;
  if (!item.dir) {
    if (item.file) out.push(item.file);
    return out;
  }
  for (const c of item.children || []) {
    itemFiles(c, out);
  }
  return out;
}

// pickState returns "on" (all files included), "off" (all skipped), or "mixed".
export function pickState(item, skip) {
  const paths = itemPaths(item, []);
  if (paths.length === 0) return "off";
  const sk = skip || {};
  let skipped = 0;
  for (const p of paths) {
    if (sk[p]) skipped++;
  }
  if (skipped === 0) return "on";
  if (skipped === paths.length) return "off";
  return "mixed";
}

// statusIcon maps a git file status to a bundled Lucide icon + theme tone,
// matching native statusIcon (internal/gui/changes.go:182-194).
export function statusIcon(status) {
  switch (status) {
    case "added":
      return { icon: "square-plus", tone: "success" };
    case "deleted":
      return { icon: "square-minus", tone: "danger" };
    case "renamed":
      return { icon: "square-arrow-right", tone: "warning" };
    case "conflicted":
      return { icon: "shield-alert", tone: "danger" };
    default:
      return { icon: "square-dot", tone: "warning" };
  }
}

export function countBadges(added, removed) {
  return [
    { text: `+${lineCount(added)}`, tone: "success", variant: "mono" },
    { text: `-${lineCount(removed)}`, tone: "danger", variant: "mono" },
  ];
}

export function fileBadges(file, t) {
  if (file && file.binary) {
    return [{ text: t("binary"), tone: "muted", variant: "soft" }];
  }
  return countBadges(file ? file.added : 0, file ? file.removed : 0);
}
