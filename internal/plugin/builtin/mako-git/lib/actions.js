// Event handlers (block `on`) and async `gitDone` handler for mako-git.

import {
  MAX_ATTEMPT_CHOICES,
  autoFixAvailable,
  disableAutoFix as turnOffAutoFix,
  enableAutoFix as turnOnAutoFix,
  evaluateAutoFix,
  formatAgentChoice,
  isPrOpen,
  listAvailableAgents,
  loadAutoFixState,
  resetAutoFix as startAttemptsAgain,
  saveAutoFixState,
  wakeViews,
} from "./autofix.js";
import { lastLines, shortHash } from "./i18n.js";
import { loadCommitDetail } from "./data.js";
import {
  canCommit,
  changedPaths,
  clearNote,
  closeDialog,
  commitPaths,
  isRepo,
  joinRepoPath,
  openDialog,
  pickedCount,
  setBusy,
  setNote,
} from "./state.js";
import { buildGitTree, findTreeItem, itemFiles, itemPaths } from "./tree.js";

function callGit(ctx, mako, method, req) {
  if (ctx && ctx.git && typeof ctx.git[method] === "function") {
    return ctx.git[method](req || {});
  }
  return mako.git[method]({ target: { scope: ctx ? ctx.scopeKey : "" }, ...(req || {}) });
}

function sendIntent(ctx, mako, uiMethod, cmdId, arg) {
  if (ctx && ctx.ui && typeof ctx.ui[uiMethod] === "function") {
    return ctx.ui[uiMethod](arg);
  }
  const encoded = typeof arg === "string" ? arg : JSON.stringify(arg || {});
  return mako.commands.runCommand({ id: cmdId, arg: encoded });
}

function errMsg(err) {
  return err && err.message ? String(err.message) : String(err || "");
}

function repoLocation(s, ctx) {
  const node = (s.status && s.status.node) || (ctx && ctx.node) || "";
  const folder = (ctx && ctx.folder) || (s.status && s.status.root) || "";
  const repo = (s.status && s.status.root) || folder;
  return { node, folder, repo };
}

function isTruthyCheck(val) {
  return val === "on" || val === true || val === "true";
}

// The Auto-fix dialog's switches, each a field of the record.
const AUTOFIX_SWITCHES = ["enabled", "fixCi", "fixReviews", "replyOnGitHub", "anyAuthor", "ownComments", "allowYolo"];

// hasAutoFixPr says the panel shows a pull request Auto-fix can work on: of
// a folder of this node's.
function hasAutoFixPr(s, ctx) {
  return Boolean(s.forge && s.forge.hasPr && s.forge.prNumber && autoFixAvailable(ctx));
}

function isHTTPSAuthError(msg) {
  const s = String(msg || "").toLowerCase();
  if (!s) return false;
  return [
    "could not read username",
    "could not read password",
    "terminal prompts disabled",
    "authentication failed",
    "invalid username or password",
    "invalid username or token",
    "credentials rejected",
    "logon failed",
    "returned error: 401",
    "returned error: 403",
    "http 401",
    "http 403",
    "http basic: access denied",
  ].some((needle) => s.includes(needle));
}

function isRemoteSyncOp(op) {
  return op === "fetch" || op === "pull" || op === "push" || op === "pull_request";
}

function busyLabelForOp(t, op) {
  switch (op) {
    case "fetch":
      return t("Fetching…");
    case "pull":
      return t("Pulling…");
    case "push":
      return t("Pushing…");
    case "pull_request":
      return t("Opening a pull request…");
    default:
      return t("Loading…");
  }
}

export function createActions(mako) {
  function getCtx() {
    return mako.blocks.context("panel");
  }

  async function runGit(s, req, label, meta = {}) {
    if (!isRepo(s) || s.busy) return;
    const ctx = getCtx();
    setBusy(s, label);
    clearNote(s);
    try {
      const res = await callGit(ctx, mako, "run", req);
      const job = res && res.job ? String(res.job) : "";
      if (!s.jobs || typeof s.jobs !== "object") s.jobs = {};
      if (job) {
        s.jobs[job] = { op: req.op, ...meta };
      }
    } catch (err) {
      setBusy(s, "");
      setNote(s, errMsg(err), true);
    }
  }

  function findChangedFile(s, path) {
    const files = (s.status && s.status.files) || [];
    return files.find((f) => f.path === path) || null;
  }

  function findCommitFile(s, path) {
    const files = (s.commit && s.commit.info && s.commit.info.files) || [];
    return files.find((f) => f.path === path) || null;
  }

  async function openWorkingDiff(s, file, preview) {
    if (!file) return;
    const ctx = getCtx();
    const loc = repoLocation(s, ctx);
    await sendIntent(ctx, mako, "openDiff", "mako.openDiff", {
      node: loc.node,
      folder: loc.folder,
      repo: loc.repo,
      path: file.path || "",
      from: file.from || "",
      status: file.status || "",
      added: Number(file.added) || 0,
      removed: Number(file.removed) || 0,
      binary: Boolean(file.binary),
      commit: "",
      base: s.base || "",
      preview: Boolean(preview),
    });
  }

  const on = {
    // Sections & tree disclosure ---------------------------------------------
    toggleSection(s, section, nextOpen) {
      if (!s.open || typeof s.open !== "object") {
        s.open = { changes: true, stashes: false, branches: false, history: true };
      }
      if (typeof nextOpen === "boolean") {
        s.open[section] = nextOpen;
      } else {
        s.open[section] = !s.open[section];
      }
    },

    toggleDir(s, key, expanded) {
      if (!s.closed || typeof s.closed !== "object") s.closed = {};
      const isOpen = typeof expanded === "boolean" ? expanded : Boolean(s.closed[key]);
      if (isOpen) {
        delete s.closed[key];
      } else {
        s.closed[key] = true;
      }
    },

    // Refresh & base toggle --------------------------------------------------
    refresh(s) {
      s.fingerprint = "";
      s.forgeForce = true;
      mako.blocks.reload("panel");
    },

    refreshHistory(s) {
      s.fingerprint = "";
      mako.blocks.reload("panel");
    },

    toggleBase(s) {
      const base = (s.status && s.status.worktree && s.status.worktree.base) || "";
      if (!base) return;
      s.base = s.base ? "" : base;
      mako.blocks.reload("panel");
    },

    dismissNote(s) {
      clearNote(s);
    },

    // Changes tree: pick, diff, open file, ignore, copy ----------------------
    pick(s, key, nextVal) {
      const files = (s.status && s.status.files) || [];
      const tree = buildGitTree(files);
      const item = findTreeItem(tree, key);
      if (!item) return;
      const paths = itemPaths(item, []);
      if (!s.skip || typeof s.skip !== "object") s.skip = {};
      const take = isTruthyCheck(nextVal);
      for (const p of paths) {
        if (take) {
          delete s.skip[p];
        } else {
          s.skip[p] = true;
        }
      }
    },

    async openFile(s, key) {
      const file = findChangedFile(s, key);
      if (!file) return;
      await openWorkingDiff(s, file, true);
    },

    async openFileDiff(s, key) {
      const file = findChangedFile(s, key);
      if (!file) return;
      await openWorkingDiff(s, file, false);
    },

    async openInEditor(s, key) {
      if (!key) return;
      const ctx = getCtx();
      const loc = repoLocation(s, ctx);
      await sendIntent(ctx, mako, "openFile", "mako.openFile", {
        node: loc.node,
        path: joinRepoPath(loc.repo, key),
      });
    },

    async ignorePath(s, key) {
      if (!key) return;
      const t = getCtx().t;
      await runGit(s, { op: "ignore", paths: [String(key)] }, t("Ignoring…"));
    },

    async copyPath(_s, key) {
      if (!key) return;
      await sendIntent(getCtx(), mako, "copy", "mako.copyText", String(key));
    },

    // Commit box -------------------------------------------------------------
    setMessage(s, val) {
      s.message = String(val ?? "");
    },

    toggleAmend(s, val) {
      s.amend = isTruthyCheck(val);
    },

    async commit(s, submittedText) {
      const ctx = getCtx();
      const t = ctx.t;
      const vals = mako.blocks.values("panel") || {};
      const raw = submittedText !== undefined
        ? submittedText
        : (vals["commit-msg"] !== undefined ? vals["commit-msg"] : s.message);
      s.message = String(raw ?? "");
      const files = (s.status && s.status.files) || [];
      const count = pickedCount(files, s.skip);
      const trimmed = s.message.trim();
      if (!canCommit(count, trimmed, s.amend)) {
        if (!s.amend && count > 0 && trimmed === "") {
          setNote(s, t("Enter a commit message."), true);
        }
        return;
      }
      const paths = commitPaths(files, s.skip);
      const req = { op: "commit", message: trimmed, amend: Boolean(s.amend) };
      if (paths !== undefined) req.paths = paths;
      await runGit(s, req, t("Committing…"), { clearCommit: true });
    },

    // Sync & worktree ops ----------------------------------------------------
    async fetch(s) {
      await runGit(s, { op: "fetch" }, getCtx().t("Fetching…"));
    },

    async pull(s) {
      await runGit(s, { op: "pull" }, getCtx().t("Pulling…"));
    },

    async pullRebase(s) {
      await runGit(s, { op: "pull", rebase: true }, getCtx().t("Pulling…"), { rebase: true });
    },

    async push(s) {
      await runGit(s, { op: "push" }, getCtx().t("Pushing…"));
    },

    async worktreeOp(s, op) {
      const t = getCtx().t;
      const labels = {
        merge: t("Merging…"),
        pull_request: t("Opening a pull request…"),
        remove_worktree: t("Removing the worktree…"),
      };
      await runGit(s, { op: String(op), worktree: true }, labels[op] || t("Loading…"), { worktree: true });
    },

    // Forge intents ----------------------------------------------------------
    async createPR(s) {
      const ctx = getCtx();
      const loc = repoLocation(s, ctx);
      await sendIntent(ctx, mako, "createPullRequest", "mako.createPullRequest", {
        node: loc.node,
        folder: loc.folder,
      });
    },

    async openForgePR(s, number) {
      const ctx = getCtx();
      const loc = repoLocation(s, ctx);
      await sendIntent(ctx, mako, "openForge", "mako.openForge", {
        node: loc.node,
        folder: loc.folder,
        kind: "pr",
        number: Number(number) || 0,
      });
    },

    async openForgeRuns(s, number) {
      const ctx = getCtx();
      const loc = repoLocation(s, ctx);
      await sendIntent(ctx, mako, "openForge", "mako.openForge", {
        node: loc.node,
        folder: loc.folder,
        kind: "runs",
        number: Number(number) || 0,
      });
    },

    // Pull request Auto-fix ---------------------------------------------------
    async toggleAutoFix(s) {
      const ctx = getCtx();
      const t = ctx.t;
      if (!hasAutoFixPr(s, ctx) || !isPrOpen(s.forge.prState)) return;
      const af = await loadAutoFixState(s, ctx, mako, s.forge.prNumber);
      if (af.enabled) {
        turnOffAutoFix(af);
        await saveAutoFixState(s, ctx, mako);
        return;
      }
      turnOnAutoFix(af);
      await saveAutoFixState(s, ctx, mako);
      wakeViews(mako, af);
      const res = await evaluateAutoFix(s, ctx, mako, { manual: false });
      if (res && res.dispatched) {
        await sendIntent(ctx, mako, "toast", "mako.toast", t("Auto-fix sent a fix for PR #{number}.", { number: s.forge.prNumber }));
      }
    },

    async disableAutoFix(s) {
      const ctx = getCtx();
      if (!hasAutoFixPr(s, ctx)) return;
      turnOffAutoFix(await loadAutoFixState(s, ctx, mako, s.forge.prNumber));
      await saveAutoFixState(s, ctx, mako);
    },

    async fixNow(s) {
      const ctx = getCtx();
      const t = ctx.t;
      if (!hasAutoFixPr(s, ctx) || !isPrOpen(s.forge.prState)) return;
      if (s.dialog && s.dialog.kind === "autofix") closeDialog(s);
      const res = await evaluateAutoFix(s, ctx, mako, { manual: true });
      if (res && res.dispatched) {
        await sendIntent(ctx, mako, "toast", "mako.toast", t("Auto-fix sent a fix for PR #{number}.", { number: s.forge.prNumber }));
      } else if (res && res.reason === "starting") {
        setNote(s, t("An Auto-fix agent is starting for this pull request."), false);
      } else if (res && res.reason === "nothing_to_fix") {
        setNote(s, t("Checks pass and no review comments are open."), false);
      } else if (res && res.reason === "busy") {
        setNote(s, t("Auto-fix is already checking this pull request."), false);
      } else if (res && res.error) {
        setNote(s, res.error, true);
      }
    },

    async resetAutoFix(s) {
      const ctx = getCtx();
      if (!hasAutoFixPr(s, ctx)) return;
      const af = await loadAutoFixState(s, ctx, mako, s.forge.prNumber);
      startAttemptsAgain(af);
      await saveAutoFixState(s, ctx, mako);
      if (af.enabled) await evaluateAutoFix(s, ctx, mako, { manual: false });
    },

    async openAutoFixSession(_s, sessionId) {
      const sid = String(sessionId || "");
      if (!sid) return;
      await sendIntent(getCtx(), mako, "openSession", "mako.openSession", { sessionId: sid });
    },

    async openAutoFixSettings(s) {
      const ctx = getCtx();
      if (!hasAutoFixPr(s, ctx)) return;
      const af = await loadAutoFixState(s, ctx, mako, s.forge.prNumber);
      const agentOptions = [{ value: "", label: ctx.t("Auto (default agent)") }];
      for (const a of await listAvailableAgents(mako)) {
        if (!a || !a.agent || a.agent === "shell" || a.installed === false) continue;
        const value = formatAgentChoice(a.agent, a.profile || "");
        const label = a.label || (a.profile ? `${a.agent} (${a.profile})` : a.agent);
        if (!agentOptions.some((o) => o.value === value)) agentOptions.push({ value, label });
      }
      openDialog(s, {
        kind: "autofix",
        prNumber: s.forge.prNumber,
        enabled: Boolean(af.enabled),
        fixCi: af.fixCi !== false,
        fixReviews: af.fixReviews !== false,
        replyOnGitHub: af.replyOnGitHub !== false,
        anyAuthor: Boolean(af.anyAuthor),
        ownComments: Boolean(af.ownComments),
        allowYolo: Boolean(af.allowYolo),
        maxAttempts: Number(af.maxAttempts) || 3,
        preferredAgent: af.preferredAgent || "",
        instructions: af.instructions || "",
        agentOptions,
      });
    },

    setAutoFixDialogField(s, field, val) {
      if (!s.dialog || s.dialog.kind !== "autofix") return;
      const d = s.dialog;
      if (AUTOFIX_SWITCHES.includes(field)) {
        d[field] = isTruthyCheck(val);
      } else if (field === "maxAttempts") {
        const n = Number(val);
        if (MAX_ATTEMPT_CHOICES.includes(n)) d.maxAttempts = n;
      } else if (field === "preferredAgentLabel") {
        const opts = Array.isArray(d.agentOptions) ? d.agentOptions : [];
        const found = opts.find((o) => o.label === String(val || ""));
        d.preferredAgent = found ? found.value : "";
      } else if (field === "instructions") {
        d.instructions = String(val ?? "");
      }
    },

    async saveAutoFixSettings(s) {
      if (!s.dialog || s.dialog.kind !== "autofix") return;
      const ctx = getCtx();
      const d = s.dialog;
      const vals = mako.blocks.values("panel") || {};
      const instructions = vals["af-instructions"] !== undefined
        ? String(vals["af-instructions"])
        : String(d.instructions || "");
      const af = await loadAutoFixState(s, ctx, mako, d.prNumber);
      af.fixCi = Boolean(d.fixCi);
      af.fixReviews = Boolean(d.fixReviews);
      af.replyOnGitHub = Boolean(d.replyOnGitHub);
      af.anyAuthor = Boolean(d.anyAuthor);
      af.ownComments = Boolean(d.ownComments);
      af.allowYolo = Boolean(d.allowYolo);
      af.maxAttempts = MAX_ATTEMPT_CHOICES.includes(Number(d.maxAttempts)) ? Number(d.maxAttempts) : 3;
      af.preferredAgent = String(d.preferredAgent || "");
      af.instructions = instructions.trim();
      const wasOn = af.enabled;
      if (d.enabled) {
        turnOnAutoFix(af);
      } else {
        turnOffAutoFix(af);
      }
      closeDialog(s);
      await saveAutoFixState(s, ctx, mako);
      if (af.enabled && !wasOn) wakeViews(mako, af);
      if (af.enabled) await evaluateAutoFix(s, ctx, mako, { manual: false });
    },

    // Branches ---------------------------------------------------------------
    async checkout(s, branch) {
      const name = String(branch || "");
      if (!name || (s.status && s.status.branch === name)) return;
      await runGit(s, { op: "checkout", branch: name }, getCtx().t("Switching branch…"));
    },

    async trackBranch(s, branch) {
      const name = String(branch || "");
      if (!name) return;
      await runGit(s, { op: "track", branch: name }, getCtx().t("Checking out…"));
    },

    async mergeBranch(s, branch) {
      const name = String(branch || "");
      if (!name) return;
      await runGit(s, { op: "merge_branch", branch: name }, getCtx().t("Merging…"));
    },

    async rebaseBranch(s, branch) {
      const name = String(branch || "");
      if (!name) return;
      await runGit(s, { op: "rebase", branch: name }, getCtx().t("Rebasing…"));
    },

    async copyBranch(_s, branch) {
      if (!branch) return;
      await sendIntent(getCtx(), mako, "copy", "mako.copyText", String(branch));
    },

    // Stashes ----------------------------------------------------------------
    async stashPop(s, index) {
      await runGit(s, { op: "stash_pop", stash: Number(index) || 0 }, getCtx().t("Popping…"));
    },

    async stashApply(s, index) {
      await runGit(s, { op: "stash_apply", stash: Number(index) || 0 }, getCtx().t("Applying…"));
    },

    // Operation banner (merge/rebase/cherry-pick/revert) ---------------------
    askAbort(s) {
      openDialog(s, {
        kind: "confirm",
        op: "abort",
        variant: "danger",
        titleKey: "Abort the operation?",
        bodyKey: "The work tree goes back to where it was before it began.",
        actionKey: "Abort",
        busyKey: "Aborting…",
      });
    },

    async continueOp(s) {
      const files = (s.status && s.status.files) || [];
      const conflicts = files.filter((f) => f.status === "conflicted").length;
      if (conflicts === 0) {
        await runGit(s, { op: "continue" }, getCtx().t("Continuing…"));
        return;
      }
      openDialog(s, {
        kind: "confirm",
        op: "continue",
        variant: "",
        titleKey: "Mark the conflicts resolved?",
        bodyKey: conflicts === 1
          ? "1 file is still marked as conflicted. Continuing takes it as it is now."
          : "{count} files are still marked as conflicted. Continuing takes them as they are now.",
        bodyParams: { count: conflicts },
        actionKey: "Continue",
        busyKey: "Continuing…",
      });
    },

    // History & commit detail ------------------------------------------------
    async selectCommit(s, hash) {
      const h = String(hash || "");
      if (!h) {
        s.commit = { hash: "", info: null, loading: false, err: "" };
        return;
      }
      await loadCommitDetail(s, getCtx(), mako, h);
    },

    closeCommit(s) {
      s.commit = { hash: "", info: null, loading: false, err: "" };
    },

    async openCommitFile(s, path) {
      const file = findCommitFile(s, path);
      if (!file || !s.commit || !s.commit.hash) return;
      const ctx = getCtx();
      const loc = repoLocation(s, ctx);
      await sendIntent(ctx, mako, "openDiff", "mako.openDiff", {
        node: loc.node,
        folder: loc.folder,
        repo: loc.repo,
        path: file.path || "",
        from: file.from || "",
        status: file.status || "",
        added: Number(file.added) || 0,
        removed: Number(file.removed) || 0,
        binary: Boolean(file.binary),
        commit: s.commit.hash,
        base: "",
        preview: true,
      });
    },

    async cherryPick(s, hash) {
      const h = String(hash || "");
      if (!h) return;
      await runGit(s, { op: "cherry_pick", commit: h }, getCtx().t("Cherry-picking…"));
    },

    async revertCommit(s, hash) {
      const h = String(hash || "");
      if (!h) return;
      await runGit(s, { op: "revert", commit: h }, getCtx().t("Reverting…"));
    },

    async copyCommitHash(s, hash) {
      const h = String(hash || (s.commit && s.commit.hash) || "");
      if (!h) return;
      await sendIntent(getCtx(), mako, "copy", "mako.copyText", h);
    },

    async copyCommitSubject(s, hash) {
      const h = String(hash || "");
      const commits = (s.history && s.history.commits) || [];
      const found = commits.find((c) => c.hash === h);
      const subject = (found && found.subject) || (s.commit && s.commit.info && s.commit.info.commit && s.commit.info.commit.subject) || "";
      if (!subject) return;
      await sendIntent(getCtx(), mako, "copy", "mako.copyText", subject);
    },

    // Dialog openers ---------------------------------------------------------
    askBranch(s, commit) {
      openDialog(s, {
        kind: "branch",
        commit: typeof commit === "string" ? commit : "",
        value: "",
        err: "",
      });
    },

    askStash(s) {
      openDialog(s, {
        kind: "stash",
        value: "",
        err: "",
      });
    },

    askTag(s, commit) {
      const h = String(commit || "");
      if (!h) return;
      openDialog(s, {
        kind: "tag",
        commit: h,
        value: "",
        err: "",
      });
    },

    askDiscard(s, key) {
      const files = (s.status && s.status.files) || [];
      const tree = buildGitTree(files);
      const item = findTreeItem(tree, key);
      if (!item) return;
      const paths = changedPaths(itemFiles(item, []));
      if (paths.length === 0) return;
      openDialog(s, {
        kind: "confirm",
        op: "discard_files",
        paths,
        variant: "danger",
        titleKey: "Discard changes to {what}?",
        titleParams: { what: item.name },
        bodyKey: "The changes are lost, and new files deleted. This cannot be undone.",
        actionKey: "Discard",
        busyKey: "Discarding…",
      });
    },

    askDiscardAll(s) {
      const files = (s.status && s.status.files) || [];
      if (files.length === 0) return;
      const t = getCtx().t;
      openDialog(s, {
        kind: "confirm",
        op: "discard_files",
        paths: changedPaths(files),
        variant: "danger",
        titleKey: "Discard changes to {what}?",
        titleParams: { what: t("{count} files", { count: files.length }) },
        bodyKey: "The changes are lost, and new files deleted. This cannot be undone.",
        actionKey: "Discard",
        busyKey: "Discarding…",
      });
    },

    askDropStash(s, index) {
      openDialog(s, {
        kind: "confirm",
        op: "stash_drop",
        stash: Number(index) || 0,
        variant: "danger",
        titleKey: "Drop this stash?",
        bodyKey: "Its changes are lost.",
        actionKey: "Drop",
        busyKey: "Dropping…",
      });
    },

    askDeleteBranch(s, branch) {
      const name = String(branch || "");
      if (!name) return;
      openDialog(s, {
        kind: "confirm",
        op: "delete_branch",
        branch: name,
        force: false,
        variant: "danger",
        titleKey: "Delete branch {name}?",
        titleParams: { name },
        bodyKey: "A branch not merged anywhere is refused; delete it anyway to lose its commits.",
        actionKey: "Delete",
        busyKey: "Deleting branch…",
      });
    },

    askForceDeleteBranch(s, branch) {
      const name = String(branch || "");
      if (!name) return;
      openDialog(s, {
        kind: "confirm",
        op: "delete_branch",
        branch: name,
        force: true,
        variant: "danger",
        titleKey: "Delete branch {name}?",
        titleParams: { name },
        bodyKey: "Its commits are lost unless another branch has them.",
        actionKey: "Delete",
        busyKey: "Deleting branch…",
      });
    },

    askDetach(s, hash) {
      const h = String(hash || "");
      if (!h) return;
      openDialog(s, {
        kind: "confirm",
        op: "detach",
        commit: h,
        variant: "",
        titleKey: "Check out {hash}?",
        titleParams: { hash: shortHash(h) },
        bodyKey: "The work tree leaves its branch for this commit, on no branch. Changes that conflict stop it.",
        actionKey: "Check out",
        busyKey: "Checking out…",
      });
    },

    // Dialog controls --------------------------------------------------------
    askAuth(s) {
      const retry = (s.note && s.note.authRetry) || { op: "fetch", rebase: false, worktree: false };
      openDialog(s, {
        kind: "auth",
        username: "",
        password: "",
        save: true,
        err: "",
        retry,
      });
    },

    setAuthUsername(s, val) {
      if (!s.dialog || s.dialog.kind !== "auth") return;
      s.dialog.username = String(val ?? "");
      s.dialog.err = "";
    },

    setAuthPassword(s, val) {
      if (!s.dialog || s.dialog.kind !== "auth") return;
      s.dialog.password = String(val ?? "");
      s.dialog.err = "";
    },

    toggleAuthSave(s, val) {
      if (!s.dialog || s.dialog.kind !== "auth") return;
      s.dialog.save = isTruthyCheck(val);
    },

    setDialogValue(s, val) {
      if (!s.dialog) return;
      s.dialog.value = String(val ?? "");
      s.dialog.err = "";
    },

    closeDialog(s) {
      closeDialog(s);
    },

    async submitDialog(s, submittedVal) {
      if (!s.dialog) return;
      const d = s.dialog;
      const ctx = getCtx();
      const t = ctx.t;
      const vals = mako.blocks.values("panel") || {};

      if (d.kind === "auth") {
        const username = String(vals["dialog-auth-user"] !== undefined ? vals["dialog-auth-user"] : (d.username || "")).trim();
        const rawPass = vals["dialog-auth-pass"] !== undefined ? vals["dialog-auth-pass"] : (d.password || "");
        const password = String(rawPass || "");
        const save = vals["dialog-auth-save"] !== undefined ? isTruthyCheck(vals["dialog-auth-save"]) : d.save !== false;
        d.username = username;
        d.save = save;
        if (!password) {
          d.err = t("Enter a password or personal access token.");
          return;
        }
        try {
          await callGit(ctx, mako, "run", {
            op: "auth",
            message: JSON.stringify({ username, password, save }),
            force: save,
          });
        } catch (err) {
          d.password = "";
          d.err = errMsg(err);
          return;
        }
        const retry = d.retry || { op: "fetch", rebase: false, worktree: false };
        closeDialog(s);
        const req = { op: retry.op };
        if (retry.rebase) req.rebase = true;
        if (retry.worktree) req.worktree = true;
        await runGit(s, req, busyLabelForOp(t, retry.op), {
          rebase: Boolean(retry.rebase),
          worktree: Boolean(retry.worktree),
        });
        return;
      }

      const raw = submittedVal !== undefined
        ? submittedVal
        : (vals["dialog-input"] !== undefined ? vals["dialog-input"] : d.value);
      const value = String(raw || "").trim();
      d.value = value;

      if (d.kind === "branch") {
        if (!value) {
          d.err = t("Enter a name.");
          return;
        }
        const branches = (s.status && s.status.branches) || [];
        if (branches.includes(value)) {
          d.err = t("That branch already exists.");
          return;
        }
        const commit = d.commit || "";
        closeDialog(s);
        const req = { op: "branch", branch: value };
        if (commit) req.commit = commit;
        await runGit(s, req, t("Making branch…"));
        return;
      }

      if (d.kind === "stash") {
        closeDialog(s);
        await runGit(s, { op: "stash", message: value }, t("Stashing…"));
        return;
      }

      if (d.kind === "tag") {
        if (!value) {
          d.err = t("Enter a name.");
          return;
        }
        const commit = d.commit || "";
        closeDialog(s);
        await runGit(s, { op: "tag", tag: value, commit }, t("Tagging…"));
      }
    },

    async confirmDialog(s) {
      if (!s.dialog || s.dialog.kind !== "confirm") return;
      const d = s.dialog;
      const t = getCtx().t;
      closeDialog(s);
      const req = { op: d.op };
      if (d.paths) req.paths = d.paths;
      if (d.branch) req.branch = d.branch;
      if (d.commit) req.commit = d.commit;
      if (d.stash !== undefined) req.stash = Number(d.stash) || 0;
      if (d.force) req.force = true;
      await runGit(s, req, t(d.busyKey || "Loading…"));
    },
  };

  function handleGitDone(ev) {
    const scopeKey = (ev && ev.scopeKey) || "";
    const s = mako.blocks.state("panel", scopeKey);
    if (!s) return;
    const job = ev && ev.job ? String(ev.job) : "";
    const meta = (job && s.jobs && s.jobs[job]) || {};
    if (job && s.jobs) delete s.jobs[job];
    if (!s.jobs || Object.keys(s.jobs).length === 0) {
      setBusy(s, "");
    }
    if (!ev || !ev.ok) {
      const errText = (ev && ev.error) || "Git operation failed";
      const op = (ev && ev.op) || meta.op || "";
      if (isRemoteSyncOp(op) && isHTTPSAuthError(errText)) {
        const retry = {
          op,
          rebase: Boolean(meta.rebase),
          worktree: Boolean(meta.worktree),
        };
        setNote(s, errText, true, retry);
        openDialog(s, {
          kind: "auth",
          username: "",
          password: "",
          save: true,
          err: "",
          retry,
        });
      } else {
        setNote(s, errText, true);
      }
    } else {
      setNote(s, lastLines(ev.output || "", 4), false);
      if (meta.clearCommit || ev.op === "commit") {
        s.message = "";
        s.amend = false;
        s.skip = {};
      }
    }
    s.fingerprint = "";
    mako.blocks.reload("panel", scopeKey);
  }

  return { on, handleGitDone };
}
