// Stashes collapsible section with Pop / Apply / Drop actions and context menu.

import { ago } from "../i18n.js";
import { sanitizeText } from "../state.js";
import { renderSectionHeader } from "./changes.js";

function makeContextMenu(ui, key, items) {
  if (typeof ui.contextMenu === "function") {
    return ui.contextMenu(key, items);
  }
  return ui.menu("", items, { key, variant: "context" });
}

export function renderStashContextMenu(s, ui, t) {
  const busy = Boolean(s.busy);
  return makeContextMenu(ui, "stash", [
    ui.menuItem(t("Pop"), { key: "pop", disabled: busy, onTap: ["stashPop"] }),
    ui.menuItem(t("Apply"), { key: "apply", disabled: busy, onTap: ["stashApply"] }),
    ui.divider({ key: "st-div" }),
    ui.menuItem(t("Drop…"), { key: "drop", tone: "danger", disabled: busy, onTap: ["askDropStash"] }),
  ]);
}

export function renderStashesSection(s, ui, ctx) {
  const t = ctx.t;
  const list = (s.status && s.status.stashes) || [];
  const open = Boolean(s.open && s.open.stashes);
  const busy = Boolean(s.busy);

  const head = renderSectionHeader(ui, {
    key: "sec-stashes",
    title: t("Stashes"),
    count: list.length,
    open,
    toggleSection: "stashes",
    action: ui.button("", {
      key: "stashes-add",
      icon: "plus",
      variant: "ghost",
      size: "small",
      placeholder: t("Stash changes…"),
      disabled: busy,
      onTap: ["askStash"],
    }),
    t,
  });

  if (!open) {
    return ui.column([head], { key: "stashes-section", gap: 2 });
  }

  if (list.length === 0) {
    return ui.column([
      head,
      ui.text(t("No stashes."), { key: "stashes-empty", size: "small", tone: "muted" }),
    ], { key: "stashes-section", gap: 2 });
  }

  const rows = list.map((st) => {
    const idx = Number(st.index) || 0;
    const msg = sanitizeText(st.message || `stash@{${idx}}`);
    const when = ago(st.time, t);
    const more = ui.menu("", [
      ui.menuItem(t("Apply"), { key: "apply", disabled: busy, onTap: ["stashApply", idx] }),
      ui.menuItem(t("Drop…"), { key: "drop", tone: "danger", disabled: busy, onTap: ["askDropStash", idx] }),
    ], {
      key: "more",
      icon: "ellipsis",
      variant: "ghost",
      placeholder: t("Stash actions"),
    });

    const rowKids = [
      ui.icon("layers", { key: "ic", size: "small", tone: "muted" }),
      ui.text(msg, { key: "msg", size: "small", grow: 1 }),
    ];
    if (when) {
      rowKids.push(ui.text(when, { key: "when", size: "small", tone: "faint" }));
    }
    rowKids.push(
      ui.button(t("Pop"), {
        key: "pop",
        variant: "ghost",
        size: "small",
        placeholder: t("Apply the stash and drop it"),
        disabled: busy,
        onTap: ["stashPop", idx],
      }),
      more,
    );

    const row = ui.row(rowKids, { key: `st-${idx}`, gap: 6 });
    row.contextMenu("stash", String(idx));
    return row;
  });

  return ui.column([
    head,
    ui.column(rows, { key: "stashes-list", gap: 2 }),
  ], { key: "stashes-section", gap: 2 });
}
