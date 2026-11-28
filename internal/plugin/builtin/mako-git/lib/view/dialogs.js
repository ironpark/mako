// Declarative dialogs for name prompts (branch, stash, tag) and destructive
// confirmations (discard, drop stash, delete branch, detach, abort, continue).

import { MAX_ATTEMPT_CHOICES } from "../autofix.js";
import { ago, shortHash } from "../i18n.js";
import { safeKey, sanitizeText } from "../state.js";

function renderAutoFixDialog(s, d, ui, t) {
  const af = s.autofix || {};
  const agentOptions = Array.isArray(d.agentOptions) && d.agentOptions.length > 0
    ? d.agentOptions
    : [{ value: "", label: t("Auto (default agent)") }];
  const optionLabels = agentOptions.map((o) => o.label);
  const currentOpt = agentOptions.find((o) => o.value === (d.preferredAgent || "")) || agentOptions[0];

  const children = [
    ui.toggle(Boolean(d.enabled), t("Auto-fix this pull request"), {
      key: "af-enabled",
      onChange: ["setAutoFixDialogField", "enabled"],
    }),
    ui.text(t("While the pull request is open, a check that fails or a new review comment goes to the agent session in this folder, or to a new agent. The agent pushes clear fixes and asks you about unclear comments. Mako checks while the Git panel is open."), {
      key: "af-desc",
      size: "small",
      tone: "muted",
    }),
    ui.text(
      d.defaultEnabled
        ? t("Auto-fix is on by default for new pull requests (Settings ▸ Plugins ▸ mako-git).")
        : t("To turn on Auto-fix by default for new pull requests, enable it in Settings ▸ Plugins ▸ mako-git."),
      {
        key: "af-default-note",
        size: "small",
        tone: "faint",
      },
    ),
    ui.column([
      ui.checkbox(d.fixCi ? "on" : "off", t("Fix failing CI checks"), {
        key: "af-fix-ci",
        onChange: ["setAutoFixDialogField", "fixCi"],
      }),
      ui.checkbox(d.fixReviews ? "on" : "off", t("Address review comments"), {
        key: "af-fix-reviews",
        onChange: ["setAutoFixDialogField", "fixReviews"],
      }),
      ui.checkbox(d.replyOnGitHub ? "on" : "off", t("Reply to the comments it addressed"), {
        key: "af-reply",
        onChange: ["setAutoFixDialogField", "replyOnGitHub"],
      }),
    ], { key: "af-triggers", gap: 6 }),
    ui.column([
      ui.checkbox(d.anyAuthor ? "on" : "off", t("Answer comments from people outside the repository"), {
        key: "af-any-author",
        onChange: ["setAutoFixDialogField", "anyAuthor"],
      }),
      ui.text(t("When this is off, only comments of the repository's owners, members, and collaborators, and of Claude's bot, start a fix. Forgejo and Gitea do not say who those are."), {
        key: "af-any-author-note",
        size: "small",
        tone: "muted",
      }),
      ui.checkbox(d.ownComments ? "on" : "off", t("Answer my own comments"), {
        key: "af-own",
        onChange: ["setAutoFixDialogField", "ownComments"],
      }),
      ui.checkbox(d.allowYolo ? "on" : "off", t("Let a new agent act without asking (YOLO)"), {
        key: "af-yolo",
        onChange: ["setAutoFixDialogField", "allowYolo"],
      }),
    ], { key: "af-trust", gap: 6 }),
    ui.row([
      ui.column([
        ui.text(t("CI fix attempts in a row"), { key: "af-max-lbl", size: "small", weight: "medium" }),
        ui.select(String(d.maxAttempts || 3), MAX_ATTEMPT_CHOICES.map(String), {
          key: "af-max-attempts",
          onChange: ["setAutoFixDialogField", "maxAttempts"],
        }),
      ], { key: "af-max-col", grow: 1, gap: 4 }),
      ui.column([
        ui.text(t("Agent for a new session"), { key: "af-agent-lbl", size: "small", weight: "medium" }),
        ui.select(currentOpt.label, optionLabels, {
          key: "af-agent",
          onChange: ["setAutoFixDialogField", "preferredAgentLabel"],
        }),
      ], { key: "af-agent-col", grow: 1, gap: 4 }),
    ], { key: "af-options-row", gap: 8 }),
    ui.text(t("Custom instructions"), { key: "af-inst-lbl", size: "small", weight: "medium" }),
    ui.textarea(d.instructions || "", {
      key: "af-instructions",
      size: "small",
      placeholder: t("For example: only fix lint and test failures, and run the tests before you push."),
      onChange: ["setAutoFixDialogField", "instructions"],
    }),
  ];

  const events = Array.isArray(af.events) ? af.events.slice(-4).reverse() : [];
  if (events.length > 0) {
    children.push(
      ui.divider({ key: "af-hist-div" }),
      ui.text(t("Recent activity"), { key: "af-hist-lbl", size: "small", weight: "medium", tone: "muted" }),
      ui.column(events.map((ev, idx) => {
        const when = ago(ev.at, t);
        const text = when ? `${sanitizeText(ev.summary || "")} (${when})` : sanitizeText(ev.summary || "");
        return ui.row([
          ui.icon(ev.ok ? "circle-check" : "circle-x", {
            key: safeKey("af-ev-ic", `${idx}-${ev.at}`),
            size: "small",
            tone: ev.ok ? "success" : "danger",
          }),
          ui.text(text, {
            key: safeKey("af-ev-tx", `${idx}-${ev.at}`),
            size: "small",
            tone: ev.ok ? "muted" : "danger",
            grow: 1,
          }),
        ], { key: safeKey("af-ev", `${idx}-${ev.at}`), gap: 6 });
      }), { key: "af-hist-list", gap: 4 }),
    );
  }

  const footer = [];
  if (af.attempts > 0) {
    footer.push(ui.button(t("Reset attempts"), {
      key: "af-dlg-reset",
      variant: "ghost",
      size: "small",
      onTap: ["resetAutoFix"],
    }));
  }
  footer.push(
    ui.button(t("Fix now"), {
      key: "af-dlg-now",
      icon: "send-horizontal",
      variant: "outline",
      size: "small",
      onTap: ["fixNow"],
    }),
    ui.text("", { key: "dlg-spacer", grow: 1 }),
    ui.button(t("Cancel"), { key: "dlg-cancel", variant: "outline", onTap: ["closeDialog"] }),
    ui.button(t("Save"), { key: "dlg-save", variant: "primary", onTap: ["saveAutoFixSettings"] }),
  );
  children.push(ui.row(footer, { key: "dlg-actions", gap: 6 }));

  return ui.dialog(t("Auto-fix PR #{number}", { number: d.prNumber }), children, {
    key: "git-dialog",
    onChange: ["closeDialog"],
  });
}

export function renderDialog(s, ui, ctx) {
  const d = s.dialog;
  if (!d) return null;
  const t = ctx.t;

  if (d.kind === "autofix") {
    return renderAutoFixDialog(s, d, ui, t);
  }

  if (d.kind === "confirm") {
    const title = t(d.titleKey || "", d.titleParams);
    const body = t(d.bodyKey || "", d.bodyParams);
    const actionLabel = t(d.actionKey || "Continue");
    const isDanger = d.variant === "danger";

    return ui.dialog(title, [
      ui.text(sanitizeText(body, true), { key: "dlg-body", size: "small", tone: "muted" }),
      ui.row([
        ui.text("", { key: "dlg-spacer", grow: 1 }),
        ui.button(t("Cancel"), { key: "dlg-cancel", variant: "outline", onTap: ["closeDialog"] }),
        ui.button(actionLabel, {
          key: "dlg-confirm",
          variant: isDanger ? "danger" : "primary",
          onTap: ["confirmDialog"],
        }),
      ], { key: "dlg-actions", gap: 8 }),
    ], {
      key: "git-dialog",
      variant: isDanger ? "danger" : "",
      onChange: ["closeDialog"],
    });
  }

  if (d.kind === "auth") {
    const authKids = [
      ui.text(t("Username (optional)"), { key: "dlg-auth-user-lbl", size: "small", weight: "medium" }),
      ui.input(d.username || "", {
        key: "dialog-auth-user",
        placeholder: "x-access-token",
        onChange: ["setAuthUsername"],
        onSubmit: ["submitDialog"],
      }),
      ui.text(t("Password or token"), { key: "dlg-auth-pass-lbl", size: "small", weight: "medium" }),
      ui.input(d.password || "", {
        key: "dialog-auth-pass",
        variant: "password",
        placeholder: t("Personal access token or password"),
        onChange: ["setAuthPassword"],
        onSubmit: ["submitDialog"],
      }),
      ui.checkbox(d.save !== false ? "on" : "off", t("Save in Git credential store"), {
        key: "dialog-auth-save",
        onChange: ["toggleAuthSave"],
      }),
    ];
    if (d.err) {
      authKids.push(ui.text(sanitizeText(d.err, true), { key: "dlg-err", size: "small", tone: "danger" }));
    }
    authKids.push(ui.text(
      t("Leave username blank to use a personal access token. Credentials are not written to .git/config."),
      { key: "dlg-note", size: "small", tone: "muted" },
    ));
    authKids.push(ui.row([
      ui.text("", { key: "dlg-spacer", grow: 1 }),
      ui.button(t("Cancel"), { key: "dlg-cancel", variant: "outline", onTap: ["closeDialog"] }),
      ui.button(t("Sign in & retry"), { key: "dlg-submit", variant: "primary", onTap: ["submitDialog"] }),
    ], { key: "dlg-actions", gap: 8 }));

    return ui.dialog(t("HTTPS authentication"), authKids, {
      key: "git-dialog",
      onChange: ["closeDialog"],
    });
  }

  let title = "";
  let fieldLabel = "";
  let placeholder = "";
  let note = "";
  let actionLabel = "";

  if (d.kind === "branch") {
    title = t("New branch");
    fieldLabel = t("Branch name");
    placeholder = "feature/name";
    note = d.commit
      ? t("The branch starts at {hash}.", { hash: shortHash(d.commit) })
      : t("The branch starts at the current commit, and your changes come along.");
    actionLabel = t("Create branch");
  } else if (d.kind === "stash") {
    title = t("Stash changes");
    fieldLabel = t("Message");
    placeholder = t("Optional");
    note = t("Every change, new files too, is put aside and the work tree left clean.");
    actionLabel = t("Stash");
  } else if (d.kind === "tag") {
    title = t("New tag");
    fieldLabel = t("Tag name");
    placeholder = "v1.0.0";
    note = t("The tag marks {hash}.", { hash: shortHash(d.commit || "") });
    actionLabel = t("Create tag");
  } else {
    return null;
  }

  const children = [
    ui.text(fieldLabel, { key: "dlg-label", size: "small", weight: "medium" }),
    ui.input(d.value || "", {
      key: "dialog-input",
      placeholder,
      onChange: ["setDialogValue"],
      onSubmit: ["submitDialog"],
    }),
  ];

  if (d.err) {
    children.push(ui.text(sanitizeText(d.err), { key: "dlg-err", size: "small", tone: "danger" }));
  }
  if (note) {
    children.push(ui.text(note, { key: "dlg-note", size: "small", tone: "muted" }));
  }

  children.push(ui.row([
    ui.text("", { key: "dlg-spacer", grow: 1 }),
    ui.button(t("Cancel"), { key: "dlg-cancel", variant: "outline", onTap: ["closeDialog"] }),
    ui.button(actionLabel, { key: "dlg-submit", variant: "primary", onTap: ["submitDialog"] }),
  ], { key: "dlg-actions", gap: 8 }));

  return ui.dialog(title, children, {
    key: "git-dialog",
    onChange: ["closeDialog"],
  });
}
