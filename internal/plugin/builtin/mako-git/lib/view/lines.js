// Status lines between the toolbar and main body:
// - Forge PR & CI status line (matching internal/gui/forge.go:1087-1118)
// - Busy spinner / dismissible git output note (matching gitops.go:250-264)
// - Conflict / in-progress operation banner (matching gitactions.go:345-392)

import { shortHash } from "../i18n.js";
import { isRepo, sanitizeText, truncateRunes } from "../state.js";

function prIconInfo(state) {
  switch (String(state || "").toUpperCase()) {
    case "MERGED":
      return { icon: "git-merge", tone: "accent" };
    case "CLOSED":
      return { icon: "git-pull-request", tone: "danger" };
    case "DRAFT":
      return { icon: "git-pull-request", tone: "faint" };
    default:
      return { icon: "git-pull-request", tone: "success" };
  }
}

function checkRollup(checks) {
  switch (String(checks || "").toLowerCase()) {
    case "success":
      return { icon: "circle-check", tone: "success", labelKey: "Passed" };
    case "failure":
      return { icon: "circle-x", tone: "danger", labelKey: "Failed" };
    case "pending":
      return { icon: "loader-circle", tone: "warning", labelKey: "Running" };
    default:
      return null;
  }
}

export function renderForgeLine(s, ui, ctx) {
  if (!isRepo(s) || !s.forge || !s.forge.forge) return null;
  const t = ctx.t;
  const f = s.forge;
  const parts = [];

  if (f.hasPr && f.prNumber > 0) {
    const pi = prIconInfo(f.prState);
    const title = truncateRunes(sanitizeText(f.prTitle || ""), 60);
    const prBtn = ui.button(`#${f.prNumber} ${title}`.trim(), {
      key: "forge-pr",
      icon: pi.icon,
      tone: pi.tone,
      variant: "ghost",
      size: "small",
      grow: 1,
      onTap: ["openForgePR", f.prNumber],
    });
    const roll = checkRollup(f.prChecks);
    if (roll) {
      prBtn.badges([{ text: t(roll.labelKey), tone: roll.tone, variant: "soft", icon: roll.icon }]);
    }
    parts.push(prBtn);
  } else {
    parts.push(ui.button(t("Create pull request"), {
      key: "forge-create-pr",
      icon: "git-pull-request",
      variant: "ghost",
      size: "small",
      grow: 1,
      onTap: ["createPR"],
    }));
  }

  if (f.runs > 0) {
    const roll = checkRollup(f.runsState) || { icon: "circle-slash", tone: "muted", labelKey: "Actions" };
    const tip = f.runHead
      ? t("{count} runs of {head}", { count: f.runs, head: shortHash(f.runHead) })
      : t("Actions");
    parts.push(ui.button(t(roll.labelKey), {
      key: "forge-runs",
      icon: roll.icon,
      tone: roll.tone,
      variant: "ghost",
      size: "small",
      placeholder: tip,
      onTap: ["openForgeRuns", f.runNumber],
    }));
  }

  return ui.column([
    ui.card([ui.row(parts, { key: "forge-row", gap: 4 })], { key: "forge-pad", variant: "ghost", gap: 0 }),
    ui.divider({ key: "forge-div" }),
  ], { key: "forge-line", gap: 0 });
}

export function renderStatusLine(s, ui, ctx) {
  const t = ctx.t;
  if (s.busy) {
    return ui.column([
      ui.card([
        ui.row([
          ui.progress(0, { key: "busy-spinner", variant: "spinner", text: s.busy }),
        ], { key: "busy-row", gap: 6 }),
      ], { key: "busy-pad", variant: "ghost", gap: 0 }),
      ui.divider({ key: "busy-div" }),
    ], { key: "status-line", gap: 0 });
  }
  if (!s.note || !s.note.text) return null;
  const noteRow = [
    ui.text(sanitizeText(s.note.text, true), {
      key: "note-text",
      size: "small",
      tone: s.note.err ? "danger" : "muted",
      grow: 1,
    }),
  ];
  if (s.note.authRetry) {
    noteRow.push(ui.button(t("Authenticate…"), {
      key: "note-auth",
      icon: "lock",
      variant: "outline",
      size: "small",
      onTap: ["askAuth"],
    }));
  }
  noteRow.push(ui.button("", {
    key: "note-dismiss",
    icon: "x",
    variant: "ghost",
    size: "small",
    placeholder: t("Dismiss"),
    onTap: ["dismissNote"],
  }));
  return ui.column([
    ui.card([
      ui.row(noteRow, { key: "note-row", gap: 6 }),
    ], { key: "note-pad", variant: "ghost", gap: 0 }),
    ui.divider({ key: "note-div" }),
  ], { key: "status-line", gap: 0 });
}

export function renderOperationBanner(s, ui, ctx) {
  if (!isRepo(s) || !s.status.operation) return null;
  const t = ctx.t;
  const op = s.status.operation;
  const names = {
    "merge": t("A merge"),
    "rebase": t("A rebase"),
    "cherry-pick": t("A cherry-pick"),
    "revert": t("A revert"),
  };
  const what = names[op] || op;
  const files = s.status.files || [];
  const conflicts = files.filter((f) => f.status === "conflicted").length;
  const headline = conflicts > 0
    ? (conflicts === 1
      ? t("{what} stopped on 1 conflict.", { what })
      : t("{what} stopped on {count} conflicts.", { what, count: conflicts }))
    : t("{what} is under way.", { what });
  const msgKids = [
    ui.text(headline, { key: "op-msg", size: "small", weight: "medium" }),
  ];
  if (conflicts > 0) {
    msgKids.push(ui.text(t("Resolve them, then continue, or abort."), { key: "op-hint", size: "small", tone: "muted" }));
  }
  const busy = Boolean(s.busy);

  return ui.column([
    ui.card([
      ui.card([
        ui.row([
          ui.icon("shield-alert", { key: "op-icon", tone: "warning" }),
          ui.column(msgKids, { key: "op-msg-col", grow: 1, gap: 2 }),
        ], { key: "op-head", gap: 8 }),
        ui.row([
          ui.text("", { key: "op-spacer", grow: 1 }),
          ui.button(t("Abort"), {
            key: "op-abort",
            variant: "ghost",
            size: "small",
            disabled: busy,
            onTap: ["askAbort"],
          }),
          ui.button(t("Continue"), {
            key: "op-continue",
            variant: "primary",
            size: "small",
            disabled: busy,
            onTap: ["continueOp"],
          }),
        ], { key: "op-actions", gap: 6 }),
      ], { key: "op-card", variant: "soft", tone: "warning", gap: 8 }),
    ], { key: "op-pad", variant: "ghost", gap: 0 }),
    ui.divider({ key: "op-div" }),
  ], { key: "op-banner", gap: 0 });
}
