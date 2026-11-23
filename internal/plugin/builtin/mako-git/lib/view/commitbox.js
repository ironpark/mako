// Commit message textarea, Amend checkbox, and primary Commit button.
// Hidden when not a repo, comparing against a worktree base, or mid-operation.

import { isRepo, pickedCount } from "../state.js";

export function renderCommitBox(s, ui, ctx) {
  if (!isRepo(s) || s.base || (s.status && s.status.operation)) {
    return null;
  }
  const t = ctx.t;
  const files = (s.status && s.status.files) || [];
  const count = pickedCount(files, s.skip);
  const busy = Boolean(s.busy);
  const amend = Boolean(s.amend);

  let label;
  if (amend) {
    label = t("Amend last commit");
  } else if (count === 1) {
    label = t("Commit 1 file");
  } else {
    label = t("Commit {count} files", { count });
  }

  // Keep the button clickable whenever there is something to commit (or Amend)
  // so users can type a message and immediately click Commit without first
  // blurring the textarea; on.commit reads the live form value and validates.
  const disabled = busy || (!amend && count === 0);

  return ui.column([
    ui.divider({ key: "commit-div" }),
    ui.card([
      ui.textarea(s.message || "", {
        key: "commit-msg",
        size: "small",
        placeholder: t("Commit message"),
        disabled: busy,
        onChange: ["setMessage"],
        onSubmit: ["commit"],
      }),
      ui.row([
        ui.checkbox(amend ? "on" : "off", t("Amend"), {
          key: "commit-amend",
          disabled: busy,
          grow: 1,
          onChange: ["toggleAmend"],
        }),
        ui.button(label, {
          key: "commit-btn",
          variant: "primary",
          disabled,
          onTap: ["commit"],
        }),
      ], { key: "commit-actions", gap: 8 }),
    ], { key: "commit-inner", variant: "ghost", gap: 8 }),
  ], { key: "commit-box", gap: 0 });
}
