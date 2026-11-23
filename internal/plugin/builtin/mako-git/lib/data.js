// Data loading for mako-git: watches the folder's worktree, loads status,
// history (fingerprint-deduplicated), forge branch status, and selected commit
// details in parallel while preserving previous view data on transient errors.

import { resetForNewRepo } from "./state.js";

function callGit(ctx, mako, method, req) {
  if (ctx && ctx.git && typeof ctx.git[method] === "function") {
    return ctx.git[method](req || {});
  }
  return mako.git[method]({ target: { scope: ctx ? ctx.scopeKey : "" }, ...(req || {}) });
}

function callForge(ctx, mako, req) {
  if (ctx && ctx.forge && typeof ctx.forge.branchStatus === "function") {
    return ctx.forge.branchStatus(req || {});
  }
  return mako.forge.branchStatus({ target: { scope: ctx ? ctx.scopeKey : "" }, ...(req || {}) });
}

function errMsg(err) {
  return err && err.message ? String(err.message) : String(err || "");
}

export async function loadPanel(s, ctx, mako) {
  // Register an idempotent instance-bound watch so worktree/HEAD/index changes
  // automatically trigger a reload from the host.
  try {
    await callGit(ctx, mako, "watch", {});
  } catch {
    // Non-repo folders or unavailable watch should still surface status cleanly.
  }

  const requestedBase = s.base || "";
  const knownFingerprint = s.fingerprint || "";

  const [statusRes, historyRes] = await Promise.allSettled([
    callGit(ctx, mako, "status", { base: requestedBase }),
    callGit(ctx, mako, "history", { max: 500, known: knownFingerprint }),
  ]);

  if (statusRes.status === "fulfilled") {
    const st = statusRes.value || {};
    const nextRoot = st.root || "";
    if (nextRoot && s.root && nextRoot !== s.root) {
      resetForNewRepo(s, nextRoot);
    } else if (nextRoot && !s.root) {
      s.root = nextRoot;
    }
    s.status = {
      root: nextRoot,
      node: st.node || (ctx ? ctx.node : "") || "",
      branch: st.branch || "",
      notRepo: Boolean(st.notRepo),
      files: Array.isArray(st.files) ? st.files : [],
      upstream: st.upstream || "",
      ahead: Number(st.ahead) || 0,
      behind: Number(st.behind) || 0,
      branches: Array.isArray(st.branches) ? st.branches : [],
      remoteBranches: Array.isArray(st.remoteBranches) ? st.remoteBranches : [],
      stashes: Array.isArray(st.stashes) ? st.stashes : [],
      operation: st.operation || "",
      worktree: st.worktree || null,
      base: st.base || "",
    };
    s.statusErr = "";
    // Prune skipped paths that are no longer in the changed file list.
    if (s.skip && Object.keys(s.skip).length > 0) {
      const present = new Set(s.status.files.map((f) => f.path));
      const nextSkip = {};
      for (const k of Object.keys(s.skip)) {
        if (s.skip[k] && present.has(k)) nextSkip[k] = true;
      }
      s.skip = nextSkip;
    }
  } else {
    s.statusErr = errMsg(statusRes.reason);
  }

  if (historyRes.status === "fulfilled") {
    const h = historyRes.value || {};
    if (!h.unchanged) {
      s.history = {
        commits: Array.isArray(h.commits) ? h.commits : [],
        current: h.current || "",
        more: Boolean(h.more),
        fingerprint: h.fingerprint || "",
      };
      s.fingerprint = h.fingerprint || "";
    }
    s.historyErr = "";
  } else {
    s.historyErr = errMsg(historyRes.reason);
  }

  // Fetch forge PR/CI status (throttled) and selected commit details in parallel.
  const followups = [];
  if (s.status && !s.status.notRepo && s.status.root && s.status.branch) {
    const branch = s.status.branch;
    const headHash = (s.history && s.history.current) || "";
    const forgeKey = `${s.status.root}\0${branch}\0${headHash}\0${s.status.ahead}\0${s.status.behind}`;
    const runsActive = Boolean(
      s.forge && (s.forge.runsState === "running" || s.forge.runsState === "pending" || s.forge.prChecks === "pending"),
    );
    const ttl = runsActive ? 10_000 : 60_000;
    const now = Date.now();
    const force = Boolean(s.forgeForce);
    const shouldFetch = force || forgeKey !== s.forgeKey || !s.forgeAt || (now - s.forgeAt) >= ttl;
    if (shouldFetch) {
      s.forgeForce = false;
      followups.push(
        callForge(ctx, mako, { branch })
          .then((res) => {
            s.forgeKey = forgeKey;
            s.forgeBranch = branch;
            s.forgeAt = Date.now();
            s.forge = res && res.forge ? {
              forge: res.forge || "",
              hasPr: Boolean(res.hasPr),
              prNumber: Number(res.prNumber) || 0,
              prTitle: res.prTitle || "",
              prState: res.prState || "",
              prChecks: res.prChecks || "",
              runs: Number(res.runs) || 0,
              runsState: res.runsState || "",
              runNumber: Number(res.runNumber) || 0,
              runHead: res.runHead || "",
            } : null;
          })
          .catch(() => {
            s.forgeAt = Date.now();
            if (s.forgeBranch !== branch) {
              s.forge = null;
              s.forgeBranch = branch;
            }
          }),
      );
    }
  } else {
    s.forge = null;
    s.forgeKey = "";
    s.forgeBranch = "";
    s.forgeAt = 0;
    s.forgeForce = false;
  }

  if (s.commit && s.commit.hash && !s.commit.info && !s.commit.err) {
    followups.push(loadCommitDetail(s, ctx, mako, s.commit.hash));
  }

  if (followups.length > 0) {
    await Promise.all(followups);
  }
}

export async function loadCommitDetail(s, ctx, mako, hash) {
  const h = String(hash || "");
  if (!h) {
    s.commit = { hash: "", info: null, loading: false, err: "" };
    return;
  }
  s.commit = { hash: h, info: null, loading: true, err: "" };
  try {
    const info = await callGit(ctx, mako, "showCommit", { hash: h });
    if (s.commit.hash !== h) return;
    s.commit = {
      hash: h,
      info: {
        commit: (info && info.commit) || { hash: h, subject: "", author: "", time: "", parents: [], refs: [] },
        email: (info && info.email) || "",
        body: (info && info.body) || "",
        files: (info && Array.isArray(info.files)) ? info.files : [],
      },
      loading: false,
      err: "",
    };
  } catch (err) {
    if (s.commit.hash !== h) return;
    s.commit = { hash: h, info: null, loading: false, err: errMsg(err) };
  }
}
