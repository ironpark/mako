// Status lines between the toolbar and main body:
// - Forge PR & CI status line (matching internal/gui/forge.go:1087-1118)
// - Busy spinner / dismissible git output note (matching gitops.go:250-264)
// - Conflict / in-progress operation banner (matching gitactions.go:345-392)

import { MAX_AUTO_RUNS, autoFixAvailable, isPrOpen } from "../autofix.js";
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

// autoFixLook is how an enabled Auto-fix shows in each phase: the forge line
// button's badge, and the strip's icon, tone, and headline.
function autoFixLook(af, t) {
  const count = { used: af.attempts, max: af.maxAttempts };
  switch (af.phase) {
    case "starting":
      return { icon: "loader-circle", tone: "warning", badge: t("Working"), headline: t("Auto-fix: starting an agent") };
    case "asking":
      return { icon: "clock", tone: "warning", badge: t("Waiting"), headline: t("Auto-fix: waiting for you to allow the agent") };
    case "fixing":
      return {
        icon: "loader-circle",
        tone: "warning",
        badge: t("Working"),
        headline: af.lastTrigger === "reviews"
          ? t("Auto-fix: addressing review comments")
          : t("Auto-fix: fixing CI ({used}/{max})", count),
      };
    case "waiting_ci":
      return { icon: "clock", tone: "warning", badge: t("On"), headline: t("Auto-fix: waiting for CI") };
    case "waiting_session":
      return { icon: "clock", tone: "warning", badge: t("On"), headline: t("Auto-fix: waiting for the agent to finish its turn") };
    case "paused":
      return {
        icon: "shield-alert",
        tone: "warning",
        badge: t("Paused"),
        headline: af.pauseReason === "runs"
          ? t("Auto-fix: paused after {count} fixes", { count: MAX_AUTO_RUNS })
          : t("Auto-fix: CI fixes paused ({used}/{max})", count),
      };
    case "error":
      return { icon: "shield-alert", tone: "danger", badge: t("Error"), headline: t("Auto-fix: needs attention") };
    default: {
      let headline = t("Auto-fix: on");
      if (af.fixCi && af.fixReviews) headline = t("Auto-fix: watching CI and reviews");
      else if (af.fixCi) headline = t("Auto-fix: watching CI");
      else if (af.fixReviews) headline = t("Auto-fix: watching reviews");
      return { icon: "sparkles", tone: "accent", badge: t("On"), headline };
    }
  }
}

function renderAutoFixButton(af, ui, t) {
  const on = Boolean(af && af.enabled);
  const look = on ? autoFixLook(af, t) : null;
  const btn = ui.button(t("Auto-fix"), {
    key: "forge-autofix",
    icon: look ? look.icon : "sparkles",
    tone: look ? look.tone : "muted",
    variant: "ghost",
    size: "small",
    placeholder: look ? look.headline : t("Fix failing checks and review comments automatically"),
    onTap: ["toggleAutoFix"],
  });
  if (look) btn.badges([{ text: look.badge, tone: look.tone, variant: "soft" }]);
  return btn;
}

function renderAutoFixStrip(s, ui, ctx) {
  const f = s.forge;
  const af = s.autofix;
  if (!f || !f.hasPr || !f.prNumber || !af) return null;
  if (!af.enabled && af.phase !== "error") return null;

  const t = ctx.t;
  const look = autoFixLook(af, t);
  const headRow = [
    ui.icon(look.icon, { key: "af-icon", size: "small", tone: look.tone }),
    ui.text(look.headline, { key: "af-status", size: "small", weight: "medium", tone: look.tone, grow: 1 }),
  ];

  if (af.activeSessionId) {
    headRow.push(ui.button(truncateRunes(sanitizeText(af.activeSessionName || t("Session")), 18), {
      key: "af-session",
      icon: "bot",
      variant: "ghost",
      size: "small",
      tone: "accent",
      placeholder: t("Open the Auto-fix session"),
      onTap: ["openAutoFixSession", af.activeSessionId],
    }));
  }

  if (af.phase === "paused" || (af.phase === "error" && af.hold)) {
    headRow.push(ui.button(t("Reset"), {
      key: "af-reset",
      variant: "ghost",
      size: "small",
      placeholder: t("Start the attempts again"),
      onTap: ["resetAutoFix"],
    }));
  }

  headRow.push(
    ui.button("", {
      key: "af-fix-now",
      icon: "send-horizontal",
      variant: "ghost",
      size: "small",
      placeholder: t("Fix now"),
      disabled: af.phase === "starting" || af.phase === "asking",
      onTap: ["fixNow"],
    }),
    ui.button("", {
      key: "af-settings",
      icon: "sliders-horizontal",
      variant: "ghost",
      size: "small",
      placeholder: t("Auto-fix settings"),
      onTap: ["openAutoFixSettings"],
    }),
    ui.button("", {
      key: "af-off",
      icon: "x",
      variant: "ghost",
      size: "small",
      placeholder: t("Turn off Auto-fix"),
      onTap: ["disableAutoFix"],
    }),
  );

  const cardKids = [ui.row(headRow, { key: "af-row", gap: 4 })];
  if (af.lastError) {
    cardKids.push(ui.text(sanitizeText(af.lastError), { key: "af-detail", size: "small", tone: "danger" }));
  } else if (af.lastSummary) {
    cardKids.push(ui.text(t("Last sent: {summary}", { summary: sanitizeText(af.lastSummary) }), {
      key: "af-detail",
      size: "small",
      tone: "muted",
    }));
  }

  return ui.card([
    ui.card(cardKids, { key: "af-card", variant: "soft", tone: look.tone, gap: 4 }),
  ], { key: "af-strip", variant: "ghost", gap: 0 });
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
      onTap: ["openForgeRuns", f.runId || f.runNumber],
    }));
  }

  // Auto-fix sends to this node's sessions: a peer's folder has none here.
  const autoFix = f.hasPr && f.prNumber > 0 && isPrOpen(f.prState) && autoFixAvailable(ctx);
  if (autoFix) {
    parts.push(renderAutoFixButton(s.autofix, ui, t));
  }

  const colKids = [
    ui.card([ui.row(parts, { key: "forge-row", gap: 4 })], { key: "forge-pad", variant: "ghost", gap: 0 }),
  ];
  const strip = autoFix ? renderAutoFixStrip(s, ui, ctx) : null;
  if (strip) colKids.push(strip);
  colKids.push(ui.divider({ key: "forge-div" }));

  return ui.column(colKids, { key: "forge-line", gap: 0 });
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
