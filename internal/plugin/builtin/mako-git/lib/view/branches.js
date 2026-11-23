// Branches collapsible section (local + remote) with row context menus.

import { safeKey, sanitizeText } from "../state.js";
import { renderSectionHeader } from "./changes.js";

function makeContextMenu(ui, key, items) {
  if (typeof ui.contextMenu === "function") {
    return ui.contextMenu(key, items);
  }
  return ui.menu("", items, { key, variant: "context" });
}

export function renderBranchContextMenus(s, ui, t) {
  const current = (s.status && s.status.branch) || "HEAD";
  const busy = Boolean(s.busy);

  const currentMenu = makeContextMenu(ui, "branch-current", [
    ui.menuItem(t("Copy name"), { key: "copy", onTap: ["copyBranch"] }),
  ]);

  const localMenu = makeContextMenu(ui, "branch-local", [
    ui.menuItem(t("Switch to branch"), { key: "switch", disabled: busy, onTap: ["checkout"] }),
    ui.menuItem(t("Merge into {branch}", { branch: current }), { key: "merge", disabled: busy, onTap: ["mergeBranch"] }),
    ui.menuItem(t("Rebase {branch} onto this", { branch: current }), { key: "rebase", disabled: busy, onTap: ["rebaseBranch"] }),
    ui.divider({ key: "bl-div-1" }),
    ui.menuItem(t("Delete…"), { key: "del", tone: "danger", disabled: busy, onTap: ["askDeleteBranch"] }),
    ui.menuItem(t("Force delete…"), { key: "fdel", tone: "danger", disabled: busy, onTap: ["askForceDeleteBranch"] }),
    ui.divider({ key: "bl-div-2" }),
    ui.menuItem(t("Copy name"), { key: "copy", onTap: ["copyBranch"] }),
  ]);

  const remoteMenu = makeContextMenu(ui, "branch-remote", [
    ui.menuItem(t("Check out as a local branch"), { key: "track", disabled: busy, onTap: ["trackBranch"] }),
    ui.menuItem(t("Merge into {branch}", { branch: current }), { key: "merge", disabled: busy, onTap: ["mergeBranch"] }),
    ui.menuItem(t("Rebase {branch} onto this", { branch: current }), { key: "rebase", disabled: busy, onTap: ["rebaseBranch"] }),
    ui.divider({ key: "br-div-1" }),
    ui.menuItem(t("Copy name"), { key: "copy", onTap: ["copyBranch"] }),
  ]);

  return [currentMenu, localMenu, remoteMenu];
}

export function renderBranchesSection(s, ui, ctx) {
  const t = ctx.t;
  const st = s.status || {};
  const current = st.branch || "";
  const local = st.branches || [];
  const remote = st.remoteBranches || [];
  const open = Boolean(s.open && s.open.branches);
  const busy = Boolean(s.busy);

  const head = renderSectionHeader(ui, {
    key: "sec-branches",
    title: t("Branches"),
    count: local.length,
    open,
    toggleSection: "branches",
    action: ui.button("", {
      key: "branches-add",
      icon: "plus",
      variant: "ghost",
      size: "small",
      placeholder: t("New branch…"),
      disabled: busy,
      onTap: ["askBranch", ""],
    }),
    t,
  });

  if (!open) {
    return ui.column([head], { key: "branches-section", gap: 2 });
  }

  const rows = [];
  for (const name of local) {
    const isCur = name === current;
    const btn = ui.button(sanitizeText(name), {
      key: safeKey("bl", name),
      icon: isCur ? "check" : "git-branch",
      tone: isCur ? "accent" : "",
      weight: isCur ? "medium" : undefined,
      variant: "ghost",
      size: "small",
      disabled: busy && !isCur,
      onTap: ["checkout", name],
    });
    if (name.length <= 256) {
      btn.contextMenu(isCur ? "branch-current" : "branch-local", name);
    }
    rows.push(btn);
  }

  if (remote.length > 0) {
    rows.push(ui.text(t("Remote"), { key: "branches-remote-hdr", size: "small", tone: "faint" }));
    for (const name of remote) {
      const btn = ui.button(sanitizeText(name), {
        key: safeKey("br", name),
        icon: "cloud-download",
        tone: "muted",
        variant: "ghost",
        size: "small",
        disabled: busy,
        onTap: ["trackBranch", name],
      });
      if (name.length <= 256) {
        btn.contextMenu("branch-remote", name);
      }
      rows.push(btn);
    }
  }

  return ui.column([
    head,
    ui.column(rows, { key: "branches-list", gap: 1 }),
  ], { key: "branches-section", gap: 2 });
}
