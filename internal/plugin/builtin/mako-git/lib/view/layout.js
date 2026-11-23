// Top-level layout composition: adapts between compact (tabbed) vertical split
// and standalone (solo) responsive `split(direction: "auto")` layout.

import { isRepo } from "../state.js";
import { renderBranchContextMenus, renderBranchesSection } from "./branches.js";
import { renderChangeContextMenus, renderChangesBody, renderChangesSection } from "./changes.js";
import { renderCommitBox } from "./commitbox.js";
import { renderDialog } from "./dialogs.js";
import { renderCommitContextMenu, renderHistoryHeader, renderHistoryPane } from "./history.js";
import { renderForgeLine, renderOperationBanner, renderStatusLine } from "./lines.js";
import { renderStashContextMenu, renderStashesSection } from "./stashes.js";
import { renderToolbar } from "./toolbar.js";

export function renderLayout(s, ui, ctx) {
  const t = ctx.t;
  const standalone = Boolean(ctx && ctx.view && ctx.view.standalone);
  const repo = isRepo(s);

  // Child 0 is always the toolbar so the GUI lifts it into the dock header
  // when standalone and renders it cleanly under the tab strip when tabbed.
  const rootChildren = [renderToolbar(s, ui, ctx)];

  // Context menu declarations (not drawn visually; referenced by key).
  if (repo) {
    rootChildren.push(
      ...renderChangeContextMenus(s, ui, t),
      renderStashContextMenu(s, ui, t),
      ...renderBranchContextMenus(s, ui, t),
      renderCommitContextMenu(s, ui, t),
    );
  }

  const forgeLine = renderForgeLine(s, ui, ctx);
  if (forgeLine) rootChildren.push(forgeLine);

  const statusLine = renderStatusLine(s, ui, ctx);
  if (statusLine) rootChildren.push(statusLine);

  const opBanner = renderOperationBanner(s, ui, ctx);
  if (opBanner) rootChildren.push(opBanner);

  if (!repo) {
    rootChildren.push(ui.scroll([renderChangesBody(s, ui, ctx)], {
      key: "non-repo-scroll",
      grow: 1,
    }));
  } else {
    const sections = [
      renderChangesSection(s, ui, ctx),
      renderStashesSection(s, ui, ctx),
      renderBranchesSection(s, ui, ctx),
    ];
    const worktreeKids = [
      ui.scroll([
        ui.card(sections, { key: "worktree-inner", variant: "ghost", gap: 4 }),
      ], { key: "worktree-scroll", grow: 1, gap: 0 }),
    ];
    const commitBox = renderCommitBox(s, ui, ctx);
    if (commitBox) worktreeKids.push(commitBox);

    const worktreePane = ui.column(worktreeKids, { key: "worktree-pane", grow: 1, gap: 0 });

    if (standalone) {
      const historyPane = renderHistoryPane(s, ui, ctx, true);
      rootChildren.push(ui.split(worktreePane, historyPane, {
        key: "main-split",
        direction: "auto",
        ratio: 0.45,
        text: t("History divider"),
        grow: 1,
      }));
    } else if (s.open && s.open.history) {
      const historyPane = renderHistoryPane(s, ui, ctx, false);
      rootChildren.push(ui.split(worktreePane, historyPane, {
        key: "main-split",
        direction: "vertical",
        ratio: 0.55,
        text: t("History divider"),
        grow: 1,
      }));
    } else {
      rootChildren.push(ui.column([
        worktreePane,
        ui.divider({ key: "hist-fold-div" }),
        renderHistoryHeader(s, ui, ctx, false),
      ], { key: "main-col", grow: 1, gap: 0 }));
    }
  }

  const dialog = renderDialog(s, ui, ctx);
  if (dialog) rootChildren.push(dialog);

  return ui.column(rootChildren, { key: "root", fill: true, gap: 0 });
}
