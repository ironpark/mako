// Pull request Auto-fix for mako-git, after Claude Code's: while it is on for
// an open pull request, a check that fails or a review comment someone leaves
// goes to the agent session working in the pull request's folder (or to a new
// agent when none runs there). The agent investigates, pushes a fix when one
// is clear, asks the user when a comment is unclear, and replies on GitHub.
//
// One record per folder scope and pull request is shared by every panel
// instance that shows it, and kept in scoped storage, so two panels never
// send the same fix and a restart forgets nothing.

import { sanitizeText, truncateRunes } from "./state.js";

const STORAGE_PREFIX = "autofix:pr:";
const MAX_HANDLED_COMMENTS = 300;
const MAX_EVENTS = 10;
const MAX_PROMPT_COMMENTS = 30;
const MIN_AUTO_COOLDOWN_MS = 20_000;
const DETAIL_POLL_MS = 25_000;
// A session still idle this soon after a dispatch has not picked it up yet.
const PICKUP_GRACE_MS = 90_000;
// A start that tells nothing this long after is taken for lost.
const START_TIMEOUT_MS = 120_000;
const MAX_LOG_CHARS = 14_000;
const MAX_PROMPT_CHARS = 28_000;

// The panel loads every POLL_SECONDS while an Auto-fix is on, and every
// IDLE_POLL_SECONDS otherwise, which bounds how long an Auto-fix kept in
// storage waits for its first check after the plugin starts again.
export const POLL_SECONDS = 20;
export const IDLE_POLL_SECONDS = 60;

// REPLY_MARKER ends every reply the agent posts, so that its own replies are
// never taken for new review comments.
export const REPLY_MARKER = "<!-- mako-autofix -->";

export const MAX_ATTEMPT_CHOICES = [1, 2, 3, 5];

const FAILED_CONCLUSIONS = new Set([
  "failure",
  "timed_out",
  "startup_failure",
  "action_required",
  "error",
]);

// Bots whose comments are not review feedback. GitHub's GraphQL API names a
// bot without the "[bot]" suffix its REST API adds, so both forms are known.
// Claude's own bot is listened to, as Claude Code does.
const BOT_LOGINS = new Set([
  "github-actions",
  "dependabot",
  "renovate",
  "codecov",
  "vercel",
  "netlify",
  "sonarcloud",
  "changeset-bot",
  "coderabbitai",
  "copilot-pull-request-reviewer",
  "gemini-code-assist",
]);
const TRUSTED_BOTS = new Set(["claude"]);

// records maps recordKey(scopeKey, pr) to the record every panel instance of
// that folder shares; views maps the same key to the runtime state keys of
// the instances showing it, to reload them when a session finishes.
const records = new Map();
const views = new Map();
const pendingStarts = new Map();
const evaluating = new Set();
let sessionEventsSubscribed = false;

function recordKey(scopeKey, prNumber) {
  return `${scopeKey || ""}|${Number(prNumber) || 0}`;
}

function storageKey(prNumber) {
  return `${STORAGE_PREFIX}${Number(prNumber) || 0}`;
}

function runtimeKey(ctx) {
  const scopeKey = (ctx && ctx.scopeKey) || "";
  const tab = ctx && ctx.tab && ctx.tab.key ? String(ctx.tab.key) : "";
  return tab ? `${scopeKey}#${tab}` : scopeKey;
}

export function anyAutoFixEnabled() {
  for (const r of records.values()) {
    if (r.enabled) return true;
  }
  return false;
}

export function isPrOpen(state) {
  const s = String(state || "").toUpperCase();
  return s === "OPEN" || s === "DRAFT" || s === "";
}

export function defaultAutoFix(prNumber = 0) {
  return {
    prNumber: Number(prNumber) || 0,
    scopeKey: "",
    enabled: false,
    fixCi: true,
    fixReviews: true,
    replyOnGitHub: true,
    maxAttempts: 3,
    preferredAgent: "",
    instructions: "",
    phase: "off",
    // attempts counts automatic CI fixes in a row: a passing CI run, Reset,
    // and Fix now start it again.
    attempts: 0,
    lastTrigger: "",
    lastHeadOid: "",
    lastHint: "",
    lastHandledCiKey: "",
    handledCommentKeys: [],
    // baselinePending takes the comments a pull request already has, when
    // Auto-fix is turned on, for handled: only new ones start a fix.
    baselinePending: false,
    activeSessionId: "",
    activeSessionName: "",
    pendingRequestId: "",
    inFlight: false,
    sawBusy: false,
    lastDispatchAt: 0,
    lastCheckAt: 0,
    lastSummary: "",
    lastError: "",
    events: [],
  };
}

export function normalizeAutoFix(raw, prNumber = 0) {
  const base = defaultAutoFix(prNumber || (raw && raw.prNumber) || 0);
  if (!raw || typeof raw !== "object") return base;
  const maxAttempts = Number(raw.maxAttempts);
  return {
    ...base,
    prNumber: Number(prNumber || raw.prNumber) || 0,
    scopeKey: String(raw.scopeKey || ""),
    enabled: Boolean(raw.enabled),
    fixCi: raw.fixCi !== false,
    fixReviews: raw.fixReviews !== false,
    replyOnGitHub: raw.replyOnGitHub !== false,
    maxAttempts: MAX_ATTEMPT_CHOICES.includes(maxAttempts) ? maxAttempts : 3,
    preferredAgent: String(raw.preferredAgent || ""),
    instructions: String(raw.instructions || ""),
    phase: String(raw.phase || (raw.enabled ? "watching" : "off")),
    attempts: Math.max(0, Number(raw.attempts) || 0),
    lastTrigger: String(raw.lastTrigger || ""),
    lastHeadOid: String(raw.lastHeadOid || ""),
    lastHint: String(raw.lastHint || ""),
    lastHandledCiKey: String(raw.lastHandledCiKey || ""),
    handledCommentKeys: Array.isArray(raw.handledCommentKeys)
      ? raw.handledCommentKeys.map(String).slice(-MAX_HANDLED_COMMENTS)
      : [],
    baselinePending: Boolean(raw.baselinePending),
    activeSessionId: String(raw.activeSessionId || ""),
    activeSessionName: String(raw.activeSessionName || ""),
    pendingRequestId: String(raw.pendingRequestId || ""),
    inFlight: Boolean(raw.inFlight),
    sawBusy: Boolean(raw.sawBusy),
    lastDispatchAt: Number(raw.lastDispatchAt) || 0,
    lastCheckAt: Number(raw.lastCheckAt) || 0,
    lastSummary: String(raw.lastSummary || ""),
    lastError: String(raw.lastError || ""),
    events: Array.isArray(raw.events) ? raw.events.slice(-MAX_EVENTS) : [],
  };
}

// loadAutoFixState links s.autofix to the shared record of the panel's folder
// and pull request, reading it from storage the first time.
export async function loadAutoFixState(s, ctx, mako, prNumber) {
  const num = Number(prNumber) || 0;
  if (!num) {
    s.autofix = null;
    return null;
  }
  const scopeKey = (ctx && ctx.scopeKey) || "";
  const key = recordKey(scopeKey, num);
  let rec = records.get(key);
  if (!rec) {
    let raw = null;
    try {
      const res = await storageGet(ctx, mako, scopeKey, storageKey(num));
      if (res && res.found && res.value) raw = JSON.parse(res.value);
    } catch {
      // Storage unavailable or corrupted: start from the defaults.
    }
    // Another call may have read it while this one waited.
    rec = records.get(key) || normalizeAutoFix(raw, num);
    rec.scopeKey = scopeKey;
    records.set(key, rec);
  }
  const vk = runtimeKey(ctx);
  if (!views.has(key)) views.set(key, new Set());
  views.get(key).add(vk);
  s.autofix = rec;
  if (rec.enabled) await ensureSessionSubscription(mako);
  return rec;
}

function storageGet(ctx, mako, scopeKey, key) {
  if (ctx && ctx.storage && typeof ctx.storage.getItem === "function") {
    return ctx.storage.getItem(key);
  }
  if (mako && mako.storage) return mako.storage.getItem({ key, scope: scopeKey });
  return Promise.resolve(null);
}

async function persist(rec, mako) {
  if (!rec || !rec.prNumber || !mako || !mako.storage) return;
  try {
    await mako.storage.setItem({
      scope: rec.scopeKey || "",
      key: storageKey(rec.prNumber),
      value: JSON.stringify(rec),
    });
  } catch {
    // Storage full or unavailable: the record still holds in memory.
  }
}

export async function saveAutoFixState(s, _ctx, mako) {
  if (s && s.autofix) await persist(s.autofix, mako);
}

export async function ensureSessionSubscription(mako) {
  if (sessionEventsSubscribed || !mako || !mako.sessions || typeof mako.sessions.subscribe !== "function") {
    return;
  }
  try {
    await mako.sessions.subscribe({ events: ["sessionStatus", "sessionEnded"] });
    sessionEventsSubscribed = true;
  } catch {
    // Without events, evaluateAutoFix still sees a session finish by listing.
  }
}

// enableAutoFix and disableAutoFix are the toggle. Turning it on takes the
// comments the pull request has for handled and starts the attempts again.
export function enableAutoFix(rec) {
  if (!rec.enabled) {
    rec.baselinePending = true;
    rec.attempts = 0;
    rec.lastCheckAt = 0;
  }
  rec.enabled = true;
  rec.lastError = "";
  if (rec.phase === "off" || rec.phase === "closed" || rec.phase === "error") rec.phase = "watching";
}

export function disableAutoFix(rec) {
  rec.enabled = false;
  rec.inFlight = false;
  rec.sawBusy = false;
  rec.pendingRequestId = "";
  rec.lastError = "";
  rec.phase = "off";
}

function hashStr(str) {
  let h = 0x811c9dc5;
  for (let i = 0; i < str.length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(16);
}

export function normalizePath(p) {
  return String(p || "").replace(/\\/g, "/").replace(/\/+$/, "");
}

export function isFailedConclusion(conclusion) {
  return FAILED_CONCLUSIONS.has(String(conclusion || "").toLowerCase());
}

function checkDone(c) {
  const status = String((c && c.status) || "").toLowerCase();
  return status === "" || status === "completed";
}

export function findFailedChecks(checkItems) {
  return (checkItems || []).filter((c) => checkDone(c) && isFailedConclusion(c.conclusion));
}

export function hasPendingChecks(checkItems) {
  return (checkItems || []).some((c) => !checkDone(c));
}

// computeCiKey names one failure of one commit: the same checks failing again
// on a re-run of the same commit are not fixed twice.
export function computeCiKey(headOid, failedChecks) {
  const names = (failedChecks || [])
    .map((c) => `${c.workflow || ""}/${c.name || ""}:${c.conclusion || ""}`)
    .sort();
  return `${headOid || ""}|${names.join(",")}`;
}

export function isBotAuthor(login) {
  const l = String(login || "").trim().toLowerCase();
  if (!l) return false;
  const suffixed = l.endsWith("[bot]");
  const name = suffixed ? l.slice(0, -5) : l;
  if (TRUSTED_BOTS.has(name)) return false;
  return suffixed || BOT_LOGINS.has(name);
}

// isActionableReviewComment tells review feedback from noise: line comments,
// change requests, review summaries, and conversation comments of people,
// but not approvals, bots, or the agent's own replies.
export function isActionableReviewComment(c) {
  if (!c) return false;
  const body = String(c.body || "").trim();
  const review = String(c.review || "").toUpperCase();
  if (body.includes(REPLY_MARKER)) return false;
  if (isBotAuthor(c.author)) return false;
  if (review === "CHANGES_REQUESTED") return true;
  if (body === "") return false;
  if (String(c.path || "").trim() !== "") return true;
  return review === "COMMENTED" || review === "";
}

export function commentFingerprint(c) {
  const raw = [
    c.author || "",
    c.time || "",
    c.review || "",
    c.path || "",
    String(c.line || 0),
    String(c.body || "").trim(),
  ].join("\0");
  return `c-${hashStr(raw)}`;
}

export function findActionableComments(comments, handledKeys = []) {
  const handled = new Set(Array.isArray(handledKeys) ? handledKeys : []);
  const all = [];
  const unhandled = [];
  for (const c of comments || []) {
    if (!isActionableReviewComment(c)) continue;
    const entry = { ...c, _fp: commentFingerprint(c) };
    all.push(entry);
    if (!handled.has(entry._fp)) unhandled.push(entry);
  }
  return { all, unhandled };
}

function callForge(ctx, mako, method, req) {
  if (ctx && ctx.forge && typeof ctx.forge[method] === "function") {
    return ctx.forge[method](req || {});
  }
  if (mako && mako.forge && typeof mako.forge[method] === "function") {
    return mako.forge[method]({ target: { scope: ctx ? ctx.scopeKey : "" }, ...(req || {}) });
  }
  return Promise.reject(new Error(`forge.${method} unavailable`));
}

function sessionAlive(sess) {
  const state = String((sess && sess.state) || "").toLowerCase();
  return state === "running" || state === "starting";
}

export function sessionBusy(sess) {
  if (!sessionAlive(sess)) return false;
  const status = String(sess.status || "").toLowerCase();
  return status === "working" || status === "waiting";
}

// selectTargetSession picks the agent session in the pull request's folder to
// send a fix to: the one Auto-fix used before, then the panel's own, then the
// most recently active. A busy session is not interrupted: with no idle one,
// it answers { session: null, busy: true } and the fix waits, unless force
// (Fix now) sends it to the first anyway, for the agent to read when free.
export function selectTargetSession(sessions, folder, root, ctxSessionId, prevSessionId, force = false) {
  const targets = new Set([normalizePath(folder), normalizePath(root)].filter(Boolean));
  const candidates = (sessions || []).filter((sess) => {
    if (!sess || !sess.id || !sessionAlive(sess)) return false;
    const agent = String(sess.agent || "").toLowerCase();
    if (!agent || agent === "shell") return false;
    return targets.has(normalizePath(sess.project));
  });
  if (candidates.length === 0) return { session: null, busy: false };
  const rank = (x) => (prevSessionId && x.id === prevSessionId ? 2 : 0) + (ctxSessionId && x.id === ctxSessionId ? 1 : 0);
  candidates.sort((a, b) => rank(b) - rank(a) || (Number(b.lastActivityAt) || 0) - (Number(a.lastActivityAt) || 0));
  const idle = candidates.find((x) => !sessionBusy(x));
  if (idle) return { session: idle, busy: false };
  if (force) return { session: candidates[0], busy: true };
  return { session: null, busy: true };
}

export function parsePreferredAgent(pref) {
  const raw = String(pref || "").trim();
  if (!raw || raw === "auto") return { agent: "", profile: "" };
  const idx = raw.indexOf("|");
  if (idx < 0) return { agent: raw, profile: "" };
  return { agent: raw.slice(0, idx), profile: raw.slice(idx + 1) };
}

export function formatAgentChoice(agent, profile) {
  return profile ? `${agent}|${profile}` : String(agent || "");
}

export function selectFallbackAgent(agents, preferredAgent) {
  const list = (Array.isArray(agents) ? agents : []).filter((a) => a && a.agent && a.agent !== "shell");
  if (list.length === 0) return null;
  const installed = list.filter((a) => a.installed !== false);
  const pool = installed.length > 0 ? installed : list;

  const pref = parsePreferredAgent(preferredAgent);
  if (pref.agent) {
    const exact = pool.find((a) => a.agent === pref.agent && String(a.profile || "") === pref.profile);
    if (exact) return exact;
    const byAgent = pool.find((a) => a.agent === pref.agent);
    if (byAgent) return byAgent;
  }
  for (const name of ["claude", "codex", "agy"]) {
    const def = pool.find((a) => a.agent === name && !a.profile) || pool.find((a) => a.agent === name);
    if (def) return def;
  }
  return pool[0];
}

export async function listAvailableAgents(mako) {
  if (!mako || !mako.agents) return [];
  try {
    if (typeof mako.agents.list === "function") {
      const list = await mako.agents.list();
      return Array.isArray(list) ? list : [];
    }
    if (typeof mako.agents.listAgents === "function") {
      const res = await mako.agents.listAgents({});
      return Array.isArray(res && res.agents) ? res.agents : [];
    }
  } catch {
    // No agents to list: no new session can start.
  }
  return [];
}

function reviewLabel(review) {
  switch (String(review || "").toUpperCase()) {
    case "CHANGES_REQUESTED":
      return "requested changes";
    case "COMMENTED":
      return "review";
    default:
      return "";
  }
}

export function buildAutoFixPrompt({
  prNumber, prTitle, prUrl, branch, base, headOid,
  failedChecks, failedLogs, newComments, instructions, replyOnGitHub, manual,
}) {
  const lines = [];
  lines.push(`Auto-fix for pull request #${prNumber}${prTitle ? `: ${sanitizeText(prTitle)}` : ""}`);
  if (prUrl) lines.push(`URL: ${prUrl}`);
  if (branch) {
    lines.push(`Branch: ${branch}${base ? ` (into ${base})` : ""}${headOid ? `, head ${headOid.slice(0, 7)}` : ""}`);
  }
  lines.push("");
  const why = [];
  if (failedChecks && failedChecks.length > 0) why.push("CI checks failed");
  if (newComments && newComments.length > 0) why.push("reviewers left comments");
  lines.push(manual
    ? `The user asked you to fix this pull request now: ${why.join(" and ") || "nothing new failed"}.`
    : `The user turned on Auto-fix for this pull request, and ${why.join(" and ")}.`);
  lines.push("");

  if (failedChecks && failedChecks.length > 0) {
    lines.push("## Failing CI checks");
    for (const c of failedChecks) {
      const label = c.workflow ? `${c.workflow} / ${c.name}` : (c.name || "check");
      const concl = c.conclusion ? ` (${c.conclusion})` : "";
      lines.push(`- ${sanitizeText(label)}${concl}${c.url ? `: ${c.url}` : ""}`);
    }
    let logText = failedLogs && failedLogs.logs ? String(failedLogs.logs).trim() : "";
    if (logText.length > MAX_LOG_CHARS) {
      logText = logText.slice(logText.length - MAX_LOG_CHARS);
      const nl = logText.indexOf("\n");
      if (nl >= 0) logText = logText.slice(nl + 1);
    }
    if (logText) {
      lines.push("");
      lines.push(`End of the failed steps' logs${failedLogs.workflow ? ` (${sanitizeText(failedLogs.workflow)})` : ""}:`);
      lines.push("```");
      lines.push(logText);
      lines.push("```");
    } else {
      lines.push("");
      lines.push("Read the failed logs with `gh run view <run-id> --log-failed`.");
    }
    lines.push("");
  }

  if (newComments && newComments.length > 0) {
    lines.push("## Review comments");
    for (const c of newComments) {
      const loc = c.path ? (c.line > 0 ? `${c.path}:${c.line}` : c.path) : "conversation";
      const kind = reviewLabel(c.review);
      const who = [c.author ? sanitizeText(c.author) : "", kind].filter(Boolean).join(", ");
      const body = truncateRunes(sanitizeText(String(c.body || "").trim(), true), 2000);
      lines.push(`- ${sanitizeText(loc)}${who ? ` (${who})` : ""}: ${body}`);
    }
    lines.push("");
  }

  const custom = String(instructions || "").trim();
  if (custom) {
    lines.push("## Instructions from the user");
    lines.push(sanitizeText(custom, true));
    lines.push("");
  }

  lines.push("## How to respond");
  lines.push(`- Work on the pull request's branch${branch ? ` \`${branch}\`` : ""} in this folder. Pull first if the remote branch moved.`);
  lines.push("- Find the root cause. If the fix is clear, make the smallest change that fixes it, run the relevant tests, commit, and push to the branch.");
  lines.push("- If a check failed for a reason the change did not cause (a flaky test, an outage, a missing secret), do not change code for it. Say so.");
  lines.push("- If a comment can be read more than one way, or asks for an architectural change, ask the user before you act.");
  lines.push("- If a comment needs no change, or repeats one already handled, note that and move on.");
  if (replyOnGitHub) {
    lines.push(`- After you push, reply on GitHub to each review comment you addressed, in one or two sentences. End each reply with the line ${REPLY_MARKER} so that Mako does not take your reply for a new comment.`);
  }
  lines.push("- Do not merge, approve, or close the pull request, and do not force-push.");
  lines.push("- When you finish, summarize what you changed and what you left for the user.");

  let prompt = lines.join("\n").trim();
  if (prompt.length > MAX_PROMPT_CHARS) {
    prompt = `${prompt.slice(0, MAX_PROMPT_CHARS)}\n(truncated)`;
  }
  return prompt;
}

function appendEvent(rec, ev) {
  const list = Array.isArray(rec.events) ? rec.events.slice(-(MAX_EVENTS - 1)) : [];
  list.push({
    at: new Date().toISOString(),
    kind: String(ev.kind || "info"),
    summary: truncateRunes(sanitizeText(ev.summary || ""), 160),
    sessionId: String(ev.sessionId || ""),
    sessionName: String(ev.sessionName || ""),
    ok: ev.ok !== false,
  });
  rec.events = list;
}

function errMsg(err) {
  return err && err.message ? String(err.message) : String(err || "");
}

async function listLocalSessions(mako) {
  if (!mako || !mako.sessions || typeof mako.sessions.listSessions !== "function") return [];
  try {
    const res = await mako.sessions.listSessions({});
    return Array.isArray(res && res.sessions) ? res.sessions : [];
  } catch {
    return [];
  }
}

function sameCommit(a, b) {
  const x = String(a || "");
  const y = String(b || "");
  return x !== "" && y !== "" && (x.startsWith(y) || y.startsWith(x));
}

function closeRecord(rec, state) {
  disableAutoFix(rec);
  rec.phase = "closed";
  rec.lastSummary = String(state || "").toUpperCase() === "MERGED" ? "PR merged" : "PR closed";
  appendEvent(rec, { kind: "closed", summary: rec.lastSummary, ok: true });
}

// evaluateAutoFix looks at the pull request of the panel's branch and sends
// a fix when a check failed or a review comment came since the last one.
// manual (Fix now) sends what fails and every open comment now, past the
// attempt limit, the cooldown, and a busy session; fresh reads the pull
// request again now.
export async function evaluateAutoFix(s, ctx, mako, opts = {}) {
  const manual = Boolean(opts.manual);
  const fresh = Boolean(opts.fresh);
  const f = s && s.forge;
  if (!f || !f.hasPr || !f.prNumber) return { dispatched: false, reason: "no_pr" };
  const rec = await loadAutoFixState(s, ctx, mako, f.prNumber);
  if (!isPrOpen(f.prState)) {
    if (rec.enabled || rec.inFlight) {
      closeRecord(rec, f.prState);
      await persist(rec, mako);
    }
    return { dispatched: false, reason: "pr_closed" };
  }
  if (!manual && !rec.enabled) return { dispatched: false, reason: "disabled" };
  const key = recordKey(rec.scopeKey, rec.prNumber);
  if (evaluating.has(key)) return { dispatched: false, reason: "busy" };
  evaluating.add(key);
  const before = JSON.stringify(rec);
  try {
    return await evaluate(rec, s, ctx, mako, manual, fresh);
  } finally {
    evaluating.delete(key);
    if (JSON.stringify(rec) !== before) await persist(rec, mako);
  }
}

async function evaluate(rec, s, ctx, mako, manual, fresh) {
  const f = s.forge;
  const now = Date.now();
  const sessions = await listLocalSessions(mako);

  if (rec.pendingRequestId) {
    if (now - rec.lastDispatchAt < START_TIMEOUT_MS) {
      rec.phase = "starting";
      if (!manual) return { dispatched: false, reason: "starting" };
    } else {
      rec.pendingRequestId = "";
      rec.inFlight = false;
    }
  }
  if (rec.inFlight) {
    const active = sessions.find((x) => x && x.id === rec.activeSessionId);
    const busy = sessionBusy(active);
    if (busy) rec.sawBusy = true;
    const unread = sessionAlive(active) && !rec.sawBusy && now - rec.lastDispatchAt < PICKUP_GRACE_MS;
    if (busy || unread) {
      rec.phase = "fixing";
      if (!manual) return { dispatched: false, reason: "in_flight" };
    } else {
      rec.inFlight = false;
      rec.sawBusy = false;
      rec.lastCheckAt = 0;
    }
  }

  // The pull request's checks and comments are read every DETAIL_POLL_MS, and
  // at once when the branch's CI status changes or the panel was refreshed.
  const hint = `${f.runHead || ""}|${f.runsState || ""}|${f.prChecks || ""}`;
  const due = manual || fresh || rec.baselinePending || !rec.lastCheckAt
    || now - rec.lastCheckAt >= DETAIL_POLL_MS || hint !== rec.lastHint;
  if (!due) {
    if (!rec.inFlight && (rec.phase === "fixing" || rec.phase === "starting")) rec.phase = idlePhase(rec, null);
    return { dispatched: false, reason: "throttled" };
  }
  let detail;
  try {
    detail = await callForge(ctx, mako, "pullRequestDetail", { number: f.prNumber });
  } catch (err) {
    rec.lastCheckAt = now;
    if (manual) {
      rec.phase = "error";
      rec.lastError = errMsg(err);
      appendEvent(rec, { kind: "error", summary: rec.lastError, ok: false });
      return { dispatched: false, reason: "detail_error", error: rec.lastError };
    }
    return { dispatched: false, reason: "detail_error" };
  }
  rec.lastCheckAt = now;
  rec.lastHint = hint;

  const detailState = String((detail && detail.state) || f.prState || "").toUpperCase();
  if (!isPrOpen(detailState)) {
    closeRecord(rec, detailState);
    return { dispatched: false, reason: "pr_closed" };
  }

  const headOid = (detail && detail.headOid) || f.runHead || "";
  rec.lastHeadOid = headOid;

  // CI: only the head commit's checks count, once they all finished.
  const checks = detail && Array.isArray(detail.checkItems) ? detail.checkItems : [];
  const failedChecks = findFailedChecks(checks);
  const pendingCi = hasPendingChecks(checks);
  if (checks.length > 0 && !pendingCi && failedChecks.length === 0) {
    rec.attempts = 0;
  }
  const ciKey = failedChecks.length > 0 ? computeCiKey(headOid, failedChecks) : "";
  const capped = rec.attempts >= rec.maxAttempts;
  const wantCi = rec.fixCi && failedChecks.length > 0
    && (manual || (!pendingCi && ciKey !== rec.lastHandledCiKey && !capped));

  // Reviews: new comments only, unless Fix now asks for every open one.
  const { all, unhandled } = findActionableComments(detail && detail.comments, rec.handledCommentKeys);
  if (rec.baselinePending) {
    rec.handledCommentKeys = mergeKeys(rec.handledCommentKeys, all);
    rec.baselinePending = false;
  }
  const newComments = unhandled.filter((c) => !rec.handledCommentKeys.includes(c._fp));
  let targetComments = [];
  if (rec.fixReviews) {
    targetComments = newComments.length > 0 || !manual ? newComments : all;
    targetComments = targetComments.slice(-MAX_PROMPT_COMMENTS);
  }
  const wantReviews = targetComments.length > 0;

  if (!wantCi && !wantReviews) {
    rec.phase = idlePhase(rec, { pendingCi, failed: failedChecks.length > 0 && ciKey !== rec.lastHandledCiKey });
    return { dispatched: false, reason: pendingCi ? "ci_pending" : "nothing_to_fix" };
  }
  // A CI fix waits a little after the last one, against fix-and-fail loops;
  // review comments come at a person's pace.
  if (!manual && wantCi && rec.lastDispatchAt && now - rec.lastDispatchAt < MIN_AUTO_COOLDOWN_MS) {
    return { dispatched: false, reason: "cooldown" };
  }

  const folder = (ctx && ctx.folder) || (s.status && s.status.root) || "";
  const root = (s.status && s.status.root) || folder;
  const pick = selectTargetSession(sessions, folder, root, ctx ? ctx.sessionId : "", rec.activeSessionId, manual);
  if (!pick.session && pick.busy) {
    rec.phase = "waiting_session";
    return { dispatched: false, reason: "session_busy" };
  }

  // Logs of the branch's latest run, when that run is of the head commit:
  // the panel's branch status is read again when it is of an older one.
  let failedLogs = null;
  let run = { number: Number(f.runNumber) || 0, head: f.runHead || "" };
  if (wantCi && !sameCommit(run.head, headOid) && detail && detail.head) {
    try {
      const bs = await callForge(ctx, mako, "branchStatus", { branch: detail.head });
      run = { number: Number(bs && bs.runNumber) || 0, head: (bs && bs.runHead) || "" };
    } catch {
      // Keep the panel's status; the head check below still holds.
    }
  }
  if (wantCi && run.number > 0 && (!run.head || sameCommit(run.head, headOid))) {
    try {
      failedLogs = await callForge(ctx, mako, "failedLogs", { runNumber: run.number, maxLines: 200 });
    } catch {
      // The prompt tells the agent how to read them itself.
    }
  }

  const prompt = buildAutoFixPrompt({
    prNumber: f.prNumber,
    prTitle: (detail && detail.title) || f.prTitle || "",
    prUrl: (detail && detail.url) || "",
    branch: (detail && detail.head) || (s.status && s.status.branch) || "",
    base: (detail && detail.base) || "",
    headOid,
    failedChecks: wantCi ? failedChecks : [],
    failedLogs: wantCi ? failedLogs : null,
    newComments: wantReviews ? targetComments : [],
    instructions: rec.instructions,
    replyOnGitHub: rec.replyOnGitHub,
    manual,
  });

  const trigger = wantCi && wantReviews ? "ci+reviews" : (wantCi ? "ci" : "reviews");
  const summaryParts = [];
  if (wantCi) {
    const first = failedChecks[0];
    const name = first.workflow ? `${first.workflow} / ${first.name}` : first.name;
    summaryParts.push(failedChecks.length > 1 ? `${name} +${failedChecks.length - 1}` : name);
  }
  if (wantReviews) {
    summaryParts.push(targetComments.length === 1 ? "1 review comment" : `${targetComments.length} review comments`);
  }
  const summary = summaryParts.join(", ");

  const markSent = () => {
    if (wantCi) {
      rec.lastHandledCiKey = ciKey;
      rec.attempts = manual ? 1 : rec.attempts + 1;
    }
    if (wantReviews) rec.handledCommentKeys = mergeKeys(rec.handledCommentKeys, targetComments);
    rec.lastTrigger = trigger;
    rec.lastDispatchAt = Date.now();
    rec.lastSummary = summary;
    rec.lastError = "";
    rec.inFlight = true;
    rec.sawBusy = false;
  };

  if (pick.session) {
    try {
      await mako.messages.sendMessage({ sessionId: pick.session.id, text: prompt });
      markSent();
      rec.pendingRequestId = "";
      rec.activeSessionId = pick.session.id;
      rec.activeSessionName = pick.session.name || pick.session.agent || pick.session.id;
      rec.phase = "fixing";
      appendEvent(rec, { kind: trigger, summary, sessionId: rec.activeSessionId, sessionName: rec.activeSessionName });
      await ensureSessionSubscription(mako);
      return { dispatched: true, mode: "session", sessionId: pick.session.id, summary };
    } catch (err) {
      // That session takes no messages: start an agent instead.
      rec.lastError = errMsg(err);
    }
  }

  const chosen = selectFallbackAgent(await listAvailableAgents(mako), rec.preferredAgent);
  if (!chosen) {
    rec.phase = "error";
    rec.lastError = "No agent on this node can run Auto-fix.";
    appendEvent(rec, { kind: "error", summary: rec.lastError, ok: false });
    return { dispatched: false, reason: "no_agent", error: rec.lastError };
  }
  try {
    const res = await mako.messages.startAgent({
      agent: chosen.agent,
      profile: chosen.profile || "",
      project: (ctx && ctx.projectId) || "",
      root: folder || root,
      prompt,
      name: `Auto-fix PR #${f.prNumber}`,
    });
    markSent();
    const label = chosen.label || (chosen.profile ? `${chosen.agent} ${chosen.profile}` : chosen.agent);
    rec.pendingRequestId = (res && res.requestId) || "";
    rec.activeSessionId = "";
    rec.activeSessionName = label;
    rec.phase = "starting";
    if (rec.pendingRequestId) pendingStarts.set(rec.pendingRequestId, recordKey(rec.scopeKey, rec.prNumber));
    appendEvent(rec, { kind: trigger, summary: `${summary} (${label})`, sessionName: label });
    await ensureSessionSubscription(mako);
    return { dispatched: true, mode: "start", requestId: rec.pendingRequestId, summary };
  } catch (err) {
    rec.inFlight = false;
    rec.pendingRequestId = "";
    rec.phase = "error";
    rec.lastError = errMsg(err);
    appendEvent(rec, { kind: "error", summary: rec.lastError, ok: false });
    return { dispatched: false, reason: "start_failed", error: rec.lastError };
  }
}

function mergeKeys(keys, comments) {
  const merged = new Set(keys || []);
  for (const c of comments || []) {
    if (c && c._fp) merged.add(c._fp);
  }
  return [...merged].slice(-MAX_HANDLED_COMMENTS);
}

// idlePhase is what an enabled record shows with nothing to send.
function idlePhase(rec, ci) {
  if (!rec.enabled) return "off";
  if (ci && ci.pendingCi) return "waiting_ci";
  if (rec.attempts >= rec.maxAttempts && (!ci || ci.failed || rec.phase === "paused")) return "paused";
  return "watching";
}

export function createAutoFixEventHandlers(mako) {
  function recordOfRequest(reqId) {
    const key = pendingStarts.get(reqId);
    pendingStarts.delete(reqId);
    if (key && records.has(key)) return { key, rec: records.get(key) };
    for (const [k, r] of records) {
      if (r.pendingRequestId === reqId) return { key: k, rec: r };
    }
    return null;
  }

  function reloadViews(key, forceForge) {
    for (const vk of views.get(key) || []) {
      if (forceForge) {
        const st = mako.blocks.state("panel", vk);
        if (st) st.forgeForce = true;
      }
      mako.blocks.reload("panel", vk);
    }
  }

  async function handleAgentStarted(ev) {
    const found = recordOfRequest((ev && ev.requestId) || "");
    if (!found) return;
    const { key, rec } = found;
    const sess = (ev && ev.session) || {};
    rec.pendingRequestId = "";
    rec.activeSessionId = sess.id || "";
    rec.activeSessionName = sess.name || sess.agent || rec.activeSessionName || "";
    rec.inFlight = Boolean(sess.id);
    rec.sawBusy = false;
    rec.phase = rec.inFlight ? "fixing" : (rec.enabled ? "watching" : "off");
    const last = rec.events && rec.events[rec.events.length - 1];
    if (last && !last.sessionId && sess.id) {
      last.sessionId = sess.id;
      last.sessionName = rec.activeSessionName;
    }
    await persist(rec, mako);
    reloadViews(key, false);
  }

  async function handleAgentRefused(ev) {
    const found = recordOfRequest((ev && ev.requestId) || "");
    if (!found) return;
    const { key, rec } = found;
    rec.pendingRequestId = "";
    rec.inFlight = false;
    rec.phase = "error";
    rec.lastError = (ev && ev.error) || "The Auto-fix agent did not start.";
    appendEvent(rec, { kind: "error", summary: rec.lastError, ok: false });
    await persist(rec, mako);
    reloadViews(key, false);
  }

  // handleSessionEvent ends a fix when its session goes idle after it worked,
  // or ends. An idle status that comes before the agent picked the message up
  // does not end it.
  async function handleSessionEvent(ev) {
    const sess = ev && ev.session;
    if (!sess || !sess.id) return;
    const ended = ev.kind === "sessionEnded" || !sessionAlive(sess);
    const status = String(sess.status || "").toLowerCase();
    const prev = String(ev.previousStatus || "").toLowerCase();
    const wasBusy = prev === "working" || prev === "waiting";
    for (const [key, rec] of records) {
      if (rec.phase === "waiting_session" && (ended || status === "idle" || status === "done")) {
        // A fix waits for a busy session: look again now that one is free.
        rec.lastCheckAt = 0;
        reloadViews(key, false);
        continue;
      }
      if (!rec.inFlight || rec.activeSessionId !== sess.id) continue;
      if (!ended && (status === "working" || status === "waiting")) {
        if (!rec.sawBusy) {
          rec.sawBusy = true;
          await persist(rec, mako);
        }
        continue;
      }
      const idle = status === "idle" || status === "done";
      if (!ended && !(idle && (rec.sawBusy || wasBusy || Date.now() - rec.lastDispatchAt >= PICKUP_GRACE_MS))) continue;
      rec.inFlight = false;
      rec.sawBusy = false;
      rec.lastCheckAt = 0;
      rec.phase = rec.enabled ? (rec.attempts >= rec.maxAttempts ? "paused" : "watching") : "off";
      await persist(rec, mako);
      reloadViews(key, true);
    }
  }

  return { handleAgentStarted, handleAgentRefused, handleSessionEvent };
}
