// Declarative dialogs for name prompts (branch, stash, tag) and destructive
// confirmations (discard, drop stash, delete branch, detach, abort, continue).

import { shortHash } from "../i18n.js";
import { sanitizeText } from "../state.js";

export function renderDialog(s, ui, ctx) {
  const d = s.dialog;
  if (!d) return null;
  const t = ctx.t;

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
