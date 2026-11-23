// Top toolbar (row with variant "toolbar"): lifted into the dock header when
// standalone and drawn cleanly under the tab strip when tabbed.

import { lineCount } from "../i18n.js";
import { folderName, isRepo, safeKey, sanitizeText } from "../state.js";

function makeToolbar(ui, children, opts) {
  if (typeof ui.toolbar === "function") {
    return ui.toolbar(children, opts);
  }
  return ui.row(children, { ...(opts || {}), variant: "toolbar" });
}

export function renderToolbar(s, ui, ctx) {
  const t = ctx.t;
  const fallbackName = folderName(ctx.folder || (ctx.roots && ctx.roots[0]) || ctx.projectName || "") || t("Git");

  if (!isRepo(s)) {
    return makeToolbar(ui, [
      ui.icon("git-branch", { key: "tb-icon", tone: "muted" }),
      ui.text(fallbackName, { key: "tb-title", weight: "medium", grow: 1 }),
      ui.button("", {
        key: "tb-refresh",
        icon: "refresh-cw",
        variant: "ghost",
        size: "small",
        placeholder: t("Refresh"),
        onTap: ["refresh"],
      }),
    ], { key: "toolbar", gap: 6 });
  }

  const st = s.status;
  const branch = st.branch || fallbackName;
  const branches = st.branches || [];
  const files = st.files || [];
  const wt = st.worktree || null;
  const busy = Boolean(s.busy);

  // Branch dropdown menu
  const branchItems = branches.map((b) =>
    ui.menuItem(sanitizeText(b), {
      key: safeKey("br", b),
      checked: b === branch,
      disabled: busy,
      onTap: ["checkout", b],
    }),
  );
  if (branchItems.length > 0) {
    branchItems.push(ui.divider({ key: "br-div" }));
  }
  branchItems.push(ui.menuItem(t("New branch…"), {
    key: "br-new",
    icon: "plus",
    disabled: busy,
    onTap: ["askBranch", ""],
  }));

  const leftItems = [
    ui.menu(sanitizeText(branch), branchItems, {
      key: "branch-menu",
      icon: "git-branch",
      variant: "ghost",
      placeholder: t("Branch"),
    }),
  ];

  // Session worktree base toggle: "vs HEAD" / "since <base>"
  if (wt && wt.base) {
    const baseLabel = s.base ? t("since {base}", { base: wt.base }) : t("vs HEAD");
    leftItems.push(ui.button(baseLabel, {
      key: "base-toggle",
      variant: "ghost",
      size: "small",
      placeholder: t("Compare with HEAD, or with the branch the worktree started from"),
      onTap: ["toggleBase"],
    }));
  }

  // Pull & Push sync buttons with behind/ahead count badges
  const pullLabel = st.upstream ? t("Pull from {upstream}", { upstream: st.upstream }) : t("Pull");
  const pushLabel = st.upstream ? t("Push to {upstream}", { upstream: st.upstream }) : t("Push");

  const pullBtn = ui.button("", {
    key: "tb-pull",
    icon: "arrow-down",
    variant: "ghost",
    size: "small",
    placeholder: pullLabel,
    disabled: busy,
    onTap: ["pull"],
  });
  if (st.behind > 0) {
    pullBtn.badges([{ text: lineCount(st.behind), tone: "muted", variant: "soft" }]);
  }

  const pushBtn = ui.button("", {
    key: "tb-push",
    icon: "arrow-up",
    variant: "ghost",
    size: "small",
    placeholder: pushLabel,
    disabled: busy,
    onTap: ["push"],
  });
  if (st.ahead > 0) {
    pushBtn.badges([{ text: lineCount(st.ahead), tone: "muted", variant: "soft" }]);
  }

  // Overflow ⋯ menu
  const moreItems = [
    ui.menuItem(t("Fetch"), { key: "op-fetch", disabled: busy, onTap: ["fetch"] }),
    ui.menuItem(t("Pull"), { key: "op-pull", disabled: busy, onTap: ["pull"] }),
    ui.menuItem(t("Pull (rebase)"), { key: "op-pull-rebase", disabled: busy, onTap: ["pullRebase"] }),
    ui.menuItem(t("Push"), { key: "op-push", disabled: busy, onTap: ["push"] }),
    ui.menuItem(t("Create pull request…"), { key: "op-pr", onTap: ["createPR"] }),
    ui.divider({ key: "more-div-1" }),
    ui.menuItem(t("Stash changes…"), { key: "op-stash", disabled: busy, onTap: ["askStash"] }),
    ui.menuItem(t("Discard all changes…"), {
      key: "op-discard-all",
      tone: "danger",
      disabled: busy || files.length === 0,
      onTap: ["askDiscardAll"],
    }),
  ];

  if (wt) {
    const mergeLabel = wt.base
      ? t("Merge into {branch}", { branch: wt.base })
      : t("Merge into the base branch");
    moreItems.push(
      ui.divider({ key: "more-div-wt" }),
      ui.menuItem(mergeLabel, { key: "wt-merge", disabled: busy, onTap: ["worktreeOp", "merge"] }),
      ui.menuItem(t("Open a pull request"), { key: "wt-pr", disabled: busy, onTap: ["worktreeOp", "pull_request"] }),
      ui.menuItem(t("Remove worktree"), { key: "wt-remove", tone: "danger", disabled: busy, onTap: ["worktreeOp", "remove_worktree"] }),
    );
  }

  moreItems.push(
    ui.divider({ key: "more-div-end" }),
    ui.menuItem(t("Refresh"), { key: "op-refresh", icon: "refresh-cw", onTap: ["refresh"] }),
  );

  const moreMenu = ui.menu("", moreItems, {
    key: "more-menu",
    icon: "ellipsis",
    variant: "ghost",
    placeholder: t("Git actions"),
  });

  return makeToolbar(ui, [
    ui.row(leftItems, { key: "tb-left", gap: 4, grow: 1 }),
    ui.row([pullBtn, pushBtn, moreMenu], { key: "tb-right", gap: 2 }),
  ], { key: "toolbar", gap: 4 });
}
