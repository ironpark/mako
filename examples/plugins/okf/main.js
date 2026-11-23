// OKF (Open Knowledge Format) project knowledge vault plugin.
// Stores pure OKF v0.2 Markdown + YAML frontmatter files inside each project's
// isolated private data directory (ctx.datafs), exposes CLI tools for agents,
// provides a refined Revision 45 UI with overlay Reader/Editor, and supports
// 2-stage decision-model-accelerated natural-language exploration (Ask).

import {
  DOC_STATUSES,
  DOC_TYPES,
  FOLDERS,
  SPOOFING_RE,
  allDirPaths,
  buildFolderTree,
  buildKnowledgeGraph,
  buildVaultAnalytics,
  buildVaultIndex,
  cutUtf8Bytes,
  isHiddenPath,
  normalizeDocPath,
  parseDocument,
  parseTags,
  rebuildIndexMarkdown,
  renderAsciiGraph,
  resolveDocLink,
  rewriteWikiLinksForMarkdown,
  searchDocuments,
  serializeDoc,
  setSearchRanker,
  sliceUtf8Bytes,
  slugify,
  starterOverviewMarkdown,
  stripMarkdownForSnippet,
  stripSpoofing,
  todayISO,
  truncateUtf8Bytes,
  typeIcon,
  utf8ByteLength,
} from "./lib/okf.js";
import { registerOKFi18n } from "./lib/i18n.js";
import {
  ANSWER_SCHEMA,
  applyDecision,
  buildAgentPrompt,
  buildDecisionRequest,
  buildRetrievalIndex,
  computeDocsSignature,
  expandNeighbors,
  extractHeadings,
  parseAgentAnswer,
  rankDocuments,
  selectContext,
  subgraphLines,
} from "./lib/retrieval.js";

let mako;

setSearchRanker((filteredDocs, query) => {
  const idx = buildRetrievalIndex(filteredDocs);
  return rankDocuments(idx, filteredDocs, query, { limit: Math.max(filteredDocs.length, 1) });
});

const TYPE_FILTERS = ["All", ...DOC_TYPES, "Index"];
const STATUS_FILTERS = ["all", ...DOC_STATUSES];
const SORT_KEYS = ["relevance", "updated", "title", "linked"];

const ASSISTANT_META_PATH = ".okf/assistant.json";
const HISTORY_META_PATH = ".okf/ask-history.json";
const MAX_THREADS = 20;
const MAX_TURNS_PER_THREAD = 30;
const MAX_ANSWER_BYTES = 24 * 1024;
const MAX_ASK_MD_NODE_BYTES = 28 * 1024;
const MAX_ASK_THREAD_MD_BYTES = 160 * 1024;
const MAX_READER_MD_BYTES = 30 * 1024;
const MAX_EDITOR_BODY_BYTES = 7680; // 7.5 KiB (below uiblock's 8 KiB textarea cap)
const MAX_TURN_Q_BYTES = 4 * 1024;
const MAX_SUBGRAPH_BYTES = 4 * 1024;
const MAX_KEY_BYTES = 128;

// Per-scope cached BM25F retrieval index (invalidated automatically via docs signature).
const indexCache = new Map();

function getRetrievalIndex(scopeKey, docs) {
  const sig = computeDocsSignature(docs);
  const cached = indexCache.get(scopeKey || "");
  if (cached && cached.sig === sig) return cached;
  const built = buildRetrievalIndex(docs);
  indexCache.set(scopeKey || "", built);
  return built;
}

function fnv1aHex(str) {
  let h = 0x811c9dc5;
  const s = String(str ?? "");
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0");
}

function safeKey(prefix, raw) {
  const pfx = stripSpoofing(String(prefix || "")).replace(/[\/#]+/g, "_");
  const orig = String(raw || "");
  const clean = stripSpoofing(orig.replace(/[\r\n\t]+/g, "_")).replace(/[\/#]+/g, "_");
  const simple = `${pfx}-${clean}`;
  const needsHash =
    orig.includes("#") ||
    orig.includes("_") ||
    /\/{2,}/.test(orig) ||
    /[^\x20-\x7E]/.test(orig) ||
    utf8ByteLength(simple) > MAX_KEY_BYTES;
  if (!needsHash) {
    return simple;
  }
  const hash8 = fnv1aHex(orig);
  const prefixBytes = utf8ByteLength(pfx);
  const headBudget = Math.max(0, MAX_KEY_BYTES - prefixBytes - 10);
  const head = sliceUtf8Bytes(clean, headBudget);
  return head ? `${pfx}-${head}-${hash8}` : `${pfx}-${hash8}`;
}

function makeKeyDeduper() {
  const used = new Set();
  return (prefix, raw) => {
    const base = safeKey(prefix, raw);
    if (!used.has(base)) {
      used.add(base);
      return base;
    }
    for (let i = 2; ; i++) {
      const suffix = `-${i}`;
      const head = sliceUtf8Bytes(base, Math.max(0, MAX_KEY_BYTES - utf8ByteLength(suffix)));
      const cand = `${head}${suffix}`;
      if (!used.has(cand)) {
        used.add(cand);
        return cand;
      }
    }
  };
}

function cleanInline(raw) {
  return stripSpoofing(String(raw ?? "").replace(/[\r\n\t]+/g, " ")).trim();
}

function statusTone(status) {
  if (status === "active") return "success";
  if (status === "needs_review") return "warning";
  if (status === "deprecated") return "danger";
  return "muted";
}

// typeTone intentionally avoids "accent" so tree active-row highlighting stays unambiguous.
function typeTone(type) {
  if (type === "Decision") return "warning";
  if (type === "Procedure") return "success";
  if (type === "Architecture") return "muted";
  if (type === "Reference") return "faint";
  if (type === "Index") return "muted";
  return "muted";
}

function labelType(t, type) {
  const key = `type.${type}`;
  const res = t(key);
  return res === key ? String(type || "") : res;
}

function labelStatus(t, status) {
  const key = `status.${status}`;
  const res = t(key);
  return res === key ? String(status || "") : res;
}

function labelSort(t, sortKey) {
  if (sortKey === "updated") return t("browse.sortUpdated");
  if (sortKey === "title") return t("browse.sortTitle");
  if (sortKey === "linked") return t("browse.sortLinked");
  return t("browse.sortRelevance");
}

function canonicalSort(value, t) {
  const v = String(value || "").trim();
  for (const sk of SORT_KEYS) {
    if (v === sk || v === labelSort(t, sk)) return sk;
  }
  return "relevance";
}

function canonicalTypeFilter(value, t) {
  const v = String(value || "").trim();
  if (!v) return "All";
  for (const tp of TYPE_FILTERS) {
    if (v === tp || v === labelType(t, tp)) return tp;
  }
  return "All";
}

function canonicalDocType(value, t) {
  const v = String(value || "").trim();
  for (const tp of DOC_TYPES) {
    if (v === tp || v === labelType(t, tp)) return tp;
  }
  return "Concept";
}

function canonicalDocStatus(value, t) {
  const v = String(value || "").trim();
  for (const st of DOC_STATUSES) {
    if (v === st || v === labelStatus(t, st)) return st;
  }
  return "active";
}

function formatBytes(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  return `${(v / (1024 * 1024)).toFixed(2)} MB`;
}

function formatPct(ratio) {
  const pct = Math.round(Math.max(0, Math.min(1, Number(ratio) || 0)) * 100);
  return `${pct}%`;
}

function formatScore(v) {
  if (v === null || v === undefined || Number.isNaN(Number(v))) return "—";
  return Number(v).toFixed(2);
}

function formatRelTime(t, ms) {
  const diffSec = Math.max(0, Math.floor((Date.now() - (Number(ms) || 0)) / 1000));
  if (diffSec < 60) return t("ask.justNow");
  const mins = Math.floor(diffSec / 60);
  if (mins < 60) return t("ask.minsAgo", { n: mins });
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return t("ask.hoursAgo", { n: hrs });
  return t("ask.daysAgo", { n: Math.floor(hrs / 24) });
}

function normalizeDirPath(raw) {
  return String(raw || "")
    .trim()
    .replace(/\\/g, "/")
    .replace(/^\/+|\/+$/g, "")
    .split("/")
    .map((seg) => slugify(seg))
    .filter(Boolean)
    .join("/");
}

function getCtxT(ctx) {
  if (ctx && typeof ctx.t === "function") return ctx.t;
  return (key, params) => mako.i18n.t(key, params);
}

function isAskableQuery(q) {
  const s = String(q || "").trim();
  if (!s) return false;
  if (s.endsWith("?")) return true;
  return s.split(/\s+/).filter(Boolean).length >= 3;
}

function clampNumber(v, min, max, fallback) {
  const n = Number(v);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, Math.round(n)));
}

async function shouldAutoIndex(ctx) {
  const res = await ctx.settings.getSetting("auto_index");
  return res?.setting ? Boolean(res.setting.flag) : true;
}

async function defaultFolder(ctx) {
  const res = await ctx.settings.getSetting("default_folder");
  const v = res?.setting?.text || "concepts";
  return FOLDERS.includes(v) ? v : "concepts";
}

async function getAskSettings(ctx) {
  let accelerate = true;
  let maxDocs = 5;
  let timeoutSec = 120;
  try {
    const acc = await ctx.settings.getSetting("ask_accelerate");
    if (acc?.setting && acc.setting.flag !== undefined) accelerate = Boolean(acc.setting.flag);
  } catch {}
  try {
    const md = await ctx.settings.getSetting("ask_max_docs");
    if (md?.setting && md.setting.number !== undefined) maxDocs = clampNumber(md.setting.number, 1, 12, 5);
  } catch {}
  try {
    const to = await ctx.settings.getSetting("ask_timeout");
    if (to?.setting && to.setting.number !== undefined) timeoutSec = clampNumber(to.setting.number, 15, 300, 120);
  } catch {}
  return { accelerate, maxDocs, timeoutSec };
}

function defaultAskState() {
  return {
    agentsLoaded: false,
    agents: [],
    agentsError: "",
    decision: { available: false, provider: "", model: "" },
    assigned: null,
    showPicker: false,
    showHistory: false,
    threads: [],
    activeThreadId: "",
    inputSeq: 0,
    busy: false,
    whyOpen: {},
    pinnedPath: "",
  };
}

function ensureAskState(s) {
  if (!s.ask || typeof s.ask !== "object") {
    s.ask = defaultAskState();
  }
  return s.ask;
}

function isActiveTurnPhase(phase) {
  return phase === "selecting" || phase === "consent" || phase === "answering";
}

function recomputeAskBusy(ask) {
  if (!ask || typeof ask !== "object") return false;
  const busy = Array.isArray(ask.threads) && ask.threads.some(
    (th) => Array.isArray(th.turns) && th.turns.some((u) => isActiveTurnPhase(u.phase)),
  );
  ask.busy = busy;
  return busy;
}

async function loadVaultDocs(ctx, bootstrap = false) {
  let listed = await ctx.datafs.list("", { recursive: true });
  let entries = listed.entries || [];
  let mdEntries = entries.filter((e) => !e.dir && !isHiddenPath(e.path) && e.path.endsWith(".md"));

  if (bootstrap && mdEntries.length === 0) {
    const overview = starterOverviewMarkdown(ctx.projectName);
    await ctx.datafs.write("concepts/overview.md", overview);
    const seedDocs = [parseDocument("concepts/overview.md", overview)];
    const indexMd = rebuildIndexMarkdown(ctx.projectName, seedDocs);
    await ctx.datafs.write("index.md", indexMd);
    listed = await ctx.datafs.list("", { recursive: true });
    entries = listed.entries || [];
    mdEntries = entries.filter((e) => !e.dir && !isHiddenPath(e.path) && e.path.endsWith(".md"));
  }

  const dirs = entries.filter((e) => e.dir && !isHiddenPath(e.path)).map((e) => e.path);
  const docs = [];
  for (const entry of mdEntries) {
    const file = await ctx.datafs.read(entry.path);
    if (!file.found || file.binary) continue;
    docs.push(parseDocument(entry.path, file.text || "", file.size || entry.size || 0, file.modifiedMs || entry.modifiedMs || 0));
  }
  const index = buildVaultIndex(docs);
  const stats = await ctx.datafs.stats();
  return { index, stats, dirs };
}

async function loadAskMeta(s, ctx) {
  const ask = ensureAskState(s);
  // Read persisted assistant assignment from .okf/assistant.json (datafs only — no decision/agents calls in load).
  try {
    const f = await ctx.datafs.read(ASSISTANT_META_PATH);
    if (f?.found && f.text) {
      const parsed = JSON.parse(f.text);
      if (parsed && typeof parsed.agent === "string" && parsed.agent.trim()) {
        ask.assigned = {
          agent: parsed.agent.trim(),
          profile: String(parsed.profile || "").trim(),
          label: String(parsed.label || (parsed.profile ? `${parsed.agent} · ${parsed.profile}` : parsed.agent)),
          assignedAt: String(parsed.assignedAt || ""),
        };
      }
    }
  } catch {}

  // Read persisted conversation history from .okf/ask-history.json.
  try {
    const h = await ctx.datafs.read(HISTORY_META_PATH);
    if (h?.found && h.text) {
      const parsed = JSON.parse(h.text);
      if (parsed && Array.isArray(parsed.threads)) {
        const sanitized = parsed.threads.slice(0, MAX_THREADS).map((th) => ({
          id: String(th.id || ""),
          title: String(th.title || ""),
          updatedMs: Number(th.updatedMs || 0),
          turns: (Array.isArray(th.turns) ? th.turns : []).slice(-MAX_TURNS_PER_THREAD).map((u) => {
            const unfinished = u.phase === "selecting" || u.phase === "consent" || u.phase === "answering";
            return {
              ...u,
              phase: unfinished ? "error" : (u.phase || "done"),
              error: unfinished ? { code: "interrupted", message: "" } : (u.error || null),
            };
          }),
        })).filter((th) => th.id);
        // Preserve in-memory busy thread if one is actively running.
        if (!ask.busy) {
          ask.threads = sanitized;
          if (ask.activeThreadId && !ask.threads.some((th) => th.id === ask.activeThreadId)) {
            ask.activeThreadId = "";
          }
        }
      }
    }
  } catch {}
}

function serializeThreadsForStorage(threads) {
  const out = (threads || []).slice(0, MAX_THREADS).map((th) => ({
    id: th.id,
    title: th.title,
    updatedMs: th.updatedMs || Date.now(),
    turns: (th.turns || []).slice(-MAX_TURNS_PER_THREAD).map((u) => {
      const unfinished = u.phase === "selecting" || u.phase === "consent" || u.phase === "answering";
      const ret = u.retrieval
        ? {
            mode: u.retrieval.mode || "local",
            provider: u.retrieval.provider || "",
            model: u.retrieval.model || "",
            ms: Number(u.retrieval.ms || 0),
            total: Number(u.retrieval.total || 0),
            candidateCount: Number(u.retrieval.candidateCount || 0),
            selected: u.retrieval.selected || [],
            entry: u.retrieval.entry || "",
            needsMore: Boolean(u.retrieval.needsMore),
            notCovered: Boolean(u.retrieval.notCovered),
            fallbackReason: u.retrieval.fallbackReason || "",
            subgraph: cutUtf8Bytes(String(u.retrieval.subgraph || ""), MAX_SUBGRAPH_BYTES),
            rows: u.retrieval.rows || [],
          }
        : null;
      let ans = null;
      if (u.answer) {
        const rawMd = String(u.answer.markdown || "");
        const mdCut = utf8ByteLength(rawMd) > MAX_ANSWER_BYTES;
        ans = {
          markdown: mdCut ? cutUtf8Bytes(rawMd, MAX_ANSWER_BYTES) : rawMd,
          citations: Array.isArray(u.answer.citations) ? u.answer.citations : [],
          confidence: u.answer.confidence || "medium",
          followUps: Array.isArray(u.answer.followUps) ? u.answer.followUps.slice(0, 3) : [],
          missing: Array.isArray(u.answer.missing) ? u.answer.missing : [],
          agentLabel: u.answer.agentLabel || "",
          ms: Number(u.answer.ms || 0),
          truncated: Boolean(u.answer.truncated) || mdCut,
        };
      }
      return {
        id: u.id,
        q: u.q,
        createdMs: u.createdMs || Date.now(),
        phase: unfinished ? "error" : u.phase,
        retrieval: ret,
        answer: ans,
        error: unfinished ? { code: "interrupted", message: "" } : (u.error || null),
      };
    }),
  }));
  return JSON.stringify({ version: 1, threads: out });
}

async function persistAskHistory(scopeKey, threads) {
  try {
    await mako.datafs.writeData({
      path: HISTORY_META_PATH,
      text: serializeThreadsForStorage(threads),
      makeDirs: true,
      scope: scopeKey || "",
    });
  } catch {}
}

async function mergeTurnIntoPersistedHistory(scopeKey, threadId, turnId, mutateFn) {
  try {
    const res = await mako.datafs.readData({ path: HISTORY_META_PATH, scope: scopeKey || "" });
    if (!res?.found || !res.text) return;
    const data = JSON.parse(res.text);
    if (!data || !Array.isArray(data.threads)) return;
    const th = data.threads.find((x) => x.id === threadId);
    if (!th || !Array.isArray(th.turns)) return;
    const turn = th.turns.find((u) => u.id === turnId);
    if (!turn) return;
    mutateFn(turn);
    th.updatedMs = Date.now();
    await mako.datafs.writeData({
      path: HISTORY_META_PATH,
      text: serializeThreadsForStorage(data.threads),
      makeDirs: true,
      scope: scopeKey || "",
    });
  } catch {}
}

async function ensureAskLoaded(s, ctx, force = false) {
  const ask = ensureAskState(s);
  if (ask.agentsLoaded && !force) return;
  const t = getCtxT(ctx);

  try {
    const st = await mako.decision.status();
    ask.decision = {
      available: Boolean(st && st.available),
      provider: String(st?.provider || ""),
      model: String(st?.model || ""),
    };
  } catch {
    ask.decision = { available: false, provider: "", model: "" };
  }

  try {
    const list = await mako.agents.list();
    ask.agents = Array.isArray(list) ? list : [];
    ask.agentsError = "";
    // Sync label/availability if an agent was already assigned.
    if (ask.assigned) {
      const match = ask.agents.find(
        (a) => a.agent === ask.assigned.agent && String(a.profile || "") === String(ask.assigned.profile || ""),
      );
      if (match) {
        ask.assigned.label = match.label || ask.assigned.label;
      }
    }
  } catch (err) {
    ask.agents = [];
    ask.agentsError = (err && err.message) ? String(err.message) : t("ask.err.unavailable");
  }
  ask.agentsLoaded = true;
}

async function syncIndexFile(ctx) {
  if (!(await shouldAutoIndex(ctx))) return;
  const { index } = await loadVaultDocs(ctx, false);
  const md = rebuildIndexMarkdown(ctx.projectName, index.docs);
  await ctx.datafs.write("index.md", md);
}

async function refreshState(s, ctx, bootstrap = false) {
  const { index, stats, dirs } = await loadVaultDocs(ctx, bootstrap);
  s.docs = index.docs;
  s.dirs = dirs;
  s.hasIndex = index.hasIndex;
  s.totalLinks = index.totalLinks;
  s.broken = index.broken;
  s.missingMeta = index.missingMeta;
  s.needsReview = index.needsReview;
  s.orphans = index.orphans;
  s.issueCount = index.issueCount;
  s.usedBytes = stats.usedBytes || 0;
  s.quotaBytes = stats.quotaBytes || 0;
  s.fileCount = stats.fileCount || 0;
  if (s.selectedPath && !s.docs.some((d) => d.path === s.selectedPath)) {
    s.selectedPath = "";
  }
  if (s.graphFocusPath && !s.graphFocusPath.startsWith("missing:") && !s.docs.some((d) => d.path === s.graphFocusPath)) {
    s.graphFocusPath = "";
  }
  s.error = "";
}

function emptyDraft(folder = "concepts", presetPath = "", presetTitle = "") {
  return {
    originalPath: "",
    path: presetPath,
    title: presetTitle,
    type: "Concept",
    status: "active",
    description: "",
    tags: "",
    source: "",
    verifiedBy: "",
    body: presetTitle ? `# ${presetTitle}\n\n` : "",
    folder,
  };
}

function draftFromDoc(doc) {
  return {
    originalPath: doc.path,
    path: doc.path,
    title: doc.title,
    type: DOC_TYPES.includes(doc.type) ? doc.type : "Concept",
    status: DOC_STATUSES.includes(doc.status) ? doc.status : "active",
    description: doc.description || "",
    tags: (doc.tags || []).join(", "),
    source: doc.source || "",
    verifiedBy: doc.verifiedBy || "",
    body: doc.body || "",
    folder: doc.path.includes("/") ? doc.path.split("/")[0] : "concepts",
  };
}

function makeTag(scopeKey, threadId, turnId) {
  return JSON.stringify({ s: String(scopeKey || ""), t: String(threadId || ""), u: String(turnId || "") });
}

function parseTag(raw) {
  try {
    const obj = JSON.parse(String(raw || ""));
    if (obj && typeof obj === "object" && typeof obj.t === "string" && typeof obj.u === "string") {
      return { s: String(obj.s || ""), t: obj.t, u: obj.u };
    }
  } catch {}
  return null;
}

function locateTurn(tag) {
  if (!tag) return null;
  let s;
  try {
    s = mako.blocks.state("vault", tag.s);
  } catch {
    return null;
  }
  if (!s || !s.ask || !Array.isArray(s.ask.threads)) return null;
  const thread = s.ask.threads.find((th) => th.id === tag.t);
  if (!thread || !Array.isArray(thread.turns)) return null;
  const turn = thread.turns.find((u) => u.id === tag.u);
  if (!turn) return null;
  return { s, thread, turn };
}

function formatConversationSummary(prevTurns) {
  const completed = (prevTurns || []).filter((u) => u.phase === "done" && u.answer?.markdown).slice(-2);
  if (completed.length === 0) return "";
  return completed
    .map((u, i) => `Q${i + 1}: ${u.q}\nA${i + 1}: ${String(u.answer.markdown || "").slice(0, 600)}`)
    .join("\n\n")
    .slice(0, 1500);
}

function buildConversationForAgent(prevTurns) {
  return (prevTurns || [])
    .filter((u) => u.phase === "done" && u.answer?.markdown)
    .slice(-3)
    .map((u) => ({ q: u.q, a: u.answer.markdown }));
}

function hydrateCandidates(docs, slimCandidates) {
  const byPath = new Map((docs || []).map((d) => [d.path, d]));
  const out = [];
  for (const c of slimCandidates || []) {
    const doc = byPath.get(c.path);
    if (!doc) continue;
    out.push({
      path: doc.path,
      doc,
      local: c.local,
      raw: c.raw,
      matched: c.matched || [],
      snippet: c.snippet || "",
      headings: extractHeadings(doc.body, 8),
    });
  }
  return out;
}

function finalizeRetrievalSelection(s, turn, opts) {
  const { mode, ranked, entry, needsMore, notCovered, ms, provider, model, fallbackReason } = opts;
  const byPath = new Map((s.docs || []).map((d) => [d.path, d]));
  let selected;
  if (mode === "all") {
    selected = ranked.slice(0, turn.maxDocs || 5);
  } else {
    selected = selectContext(ranked, {
      maxDocs: turn.maxDocs || 5,
      minLevel: 2,
      notCovered: Boolean(notCovered),
    });
  }

  const chosenEntry = entry || selected[0]?.path || ranked[0]?.path || "";
  const selectedPaths = selected.map((x) => x.path);
  const ascii = chosenEntry && s.docs.length > 0 ? renderAsciiGraph(s.docs, chosenEntry, "focus") : "";
  const edges = subgraphLines(selectedPaths, s.docs);
  const subgraphText = [ascii, edges.length > 0 ? edges.join("\n") : ""].filter(Boolean).join("\n\n");

  turn.retrieval = {
    mode,
    provider: provider || "",
    model: model || "",
    ms: Math.max(0, Math.round(Number(ms) || 0)),
    total: (s.docs || []).length,
    candidateCount: ranked.length,
    candidates: [],
    selected: selected.map((x) => {
      const d = byPath.get(x.path);
      return {
        path: x.path,
        title: d ? d.title : x.path,
        local: Number(x.local || 0),
        decision: x.decision !== undefined ? x.decision : null,
        level: x.level !== undefined ? x.level : null,
        levelLabel: x.levelLabel || "",
        final: Number(x.final ?? x.local ?? 0),
      };
    }),
    entry: chosenEntry,
    needsMore: Boolean(needsMore),
    notCovered: Boolean(notCovered),
    fallbackReason: fallbackReason || "",
    subgraph: subgraphText,
    rows: ranked.slice(0, 12).map((x) => {
      const d = byPath.get(x.path);
      return {
        path: x.path,
        title: d ? d.title : x.path,
        local: Number(x.local || 0),
        decision: x.decision !== undefined ? x.decision : null,
        level: x.level !== undefined ? x.level : null,
        levelLabel: x.levelLabel || "",
        final: Number(x.final ?? x.local ?? 0),
      };
    }),
  };
}

async function startAgentForTurn(s, scopeKey, thread, turn) {
  const ask = ensureAskState(s);
  if (!ask.assigned) {
    turn.phase = "error";
    turn.error = { code: "failed", message: "No exploration agent assigned" };
    recomputeAskBusy(ask);
    return;
  }

  const byPath = new Map((s.docs || []).map((d) => [d.path, d]));
  const ret = turn.retrieval || {};
  const selectedItems = Array.isArray(ret.selected) ? ret.selected : [];
  const selectedPaths = selectedItems.map((x) => x.path);
  const entryPath = ret.entry || selectedPaths[0] || "";

  const outlineDocs = [...(s.docs || [])].sort((a, b) => a.path.localeCompare(b.path));
  const outline = outlineDocs.slice(0, 150).map((d) => `${d.path} — ${d.title} · ${d.type}`);

  const sources = [];
  if (ret.notCovered) {
    // Outline + top 3 centrality docs only (no full sources).
  } else {
    for (const item of selectedItems) {
      const doc = byPath.get(item.path);
      if (!doc) continue;
      sources.push({
        doc,
        final: item.final,
        local: item.local,
        isEntry: item.path === entryPath,
      });
    }
  }

  const focusPaths = [entryPath, ...selectedPaths.filter((p) => p !== entryPath)].filter(Boolean).slice(0, 3);
  let neighbors;
  if (ret.notCovered) {
    neighbors = [...(s.docs || [])]
      .sort((a, b) => ((b.links?.length || 0) + (b.backlinks?.length || 0)) - ((a.links?.length || 0) + (a.backlinks?.length || 0)) || a.path.localeCompare(b.path))
      .slice(0, 3)
      .map((doc) => ({ path: doc.path, doc, relation: "hub" }));
  } else {
    neighbors = expandNeighbors(selectedPaths, s.docs, ret.needsMore ? 10 : 6, focusPaths);
  }

  const ascii = entryPath && s.docs.length > 0 ? renderAsciiGraph(s.docs, entryPath, "focus") : "";
  const edges = subgraphLines(selectedPaths, s.docs);
  const prevTurns = (thread.turns || []).filter((u) => u.id !== turn.id);

  const prompt = buildAgentPrompt({
    projectName: turn.project?.name || "",
    locale: turn.project?.locale || "en",
    outline,
    subgraph: edges,
    ascii,
    sources,
    neighbors,
    conversation: buildConversationForAgent(prevTurns),
    query: turn.q,
    notCovered: Boolean(ret.notCovered),
  });

  const tag = makeTag(scopeKey, thread.id, turn.id);
  const title = truncateUtf8Bytes(cleanInline(`OKF: ${turn.q}`), 180) || "OKF Question";

  try {
    const res = await mako.agents.ask({
      agent: ask.assigned.agent,
      profile: ask.assigned.profile || "",
      project: turn.project?.id || "",
      root: turn.project?.root || "",
      prompt,
      format: "json",
      jsonSchema: JSON.stringify(ANSWER_SCHEMA),
      timeoutMs: (turn.timeoutSec || 120) * 1000,
      title,
      tag,
    });
    turn.askReq = String(res?.requestId || "");
    turn.phase = res?.asked ? "consent" : "answering";
    recomputeAskBusy(ask);
  } catch (err) {
    turn.phase = "error";
    turn.error = { code: "failed", message: err?.message ? String(err.message) : String(err) };
    recomputeAskBusy(ask);
  }
}

async function runAskQuestion(s, ctx, rawQuestion, opts = {}) {
  const ask = ensureAskState(s);
  recomputeAskBusy(ask);
  const q = String(rawQuestion || "").trim();
  if (!q || !ask.assigned || ask.busy) return;

  await ensureAskLoaded(s, ctx, false);

  let thread = ask.threads.find((th) => th.id === ask.activeThreadId);
  if (!thread) {
    thread = {
      id: `t${Date.now().toString(36)}${Math.floor(Math.random() * 1000).toString(36)}`,
      title: truncateUtf8Bytes(cleanInline(q), 64) || q.slice(0, 48),
      updatedMs: Date.now(),
      turns: [],
    };
    ask.threads = [thread, ...ask.threads].slice(0, MAX_THREADS);
    ask.activeThreadId = thread.id;
  }

  // If retrying the last failed turn, replace it in place for a cleaner thread.
  if (opts.replaceTurnId) {
    const idx = thread.turns.findIndex((u) => u.id === opts.replaceTurnId);
    if (idx >= 0 && idx === thread.turns.length - 1) {
      thread.turns.splice(idx, 1);
    }
  }

  const settings = await getAskSettings(ctx);
  const pinned = Array.isArray(opts.pinned)
    ? opts.pinned.filter(Boolean)
    : (ask.pinnedPath ? [ask.pinnedPath] : []);
  ask.pinnedPath = "";
  ask.showHistory = false;

  const turn = {
    id: `u${Date.now().toString(36)}${Math.floor(Math.random() * 10000).toString(36)}`,
    q,
    createdMs: Date.now(),
    phase: "selecting",
    project: {
      id: ctx.projectId || "",
      root: (ctx.roots && ctx.roots[0]) || "",
      name: ctx.projectName || "",
      locale: ctx.locale || "en",
    },
    pinned,
    maxDocs: settings.maxDocs,
    timeoutSec: settings.timeoutSec,
    decisionReq: "",
    askReq: "",
    retrieval: {
      mode: "local",
      provider: "",
      model: "",
      ms: 0,
      stage0Ms: 0,
      total: (s.docs || []).length,
      candidateCount: 0,
      candidates: [],
      selected: [],
      entry: "",
      needsMore: false,
      notCovered: false,
      fallbackReason: "",
      subgraph: "",
      rows: [],
    },
    answer: null,
    error: null,
  };

  thread.turns.push(turn);
  if (thread.turns.length > MAX_TURNS_PER_THREAD) {
    thread.turns = thread.turns.slice(-MAX_TURNS_PER_THREAD);
  }
  thread.updatedMs = Date.now();
  recomputeAskBusy(ask);

  // Stage 0: Local BM25F + Korean/CJK bigram + graph centrality pre-filter.
  const t0 = Date.now();
  const prevTurn = thread.turns.length >= 2 ? thread.turns[thread.turns.length - 2] : null;
  const effectiveQuery = prevTurn ? `${q} ${prevTurn.q}` : q;
  const idx = getRetrievalIndex(ctx.scopeKey, s.docs || []);
  let cands = rankDocuments(idx, s.docs || [], effectiveQuery, { limit: 12, pinned });

  if (cands.length < 3 && (s.docs || []).length > cands.length) {
    const have = new Set(cands.map((c) => c.path));
    const fillers = [...(s.docs || [])]
      .filter((d) => !have.has(d.path))
      .sort((a, b) => (idx.centrality.get(b.path) || 0) - (idx.centrality.get(a.path) || 0) || a.path.localeCompare(b.path))
      .slice(0, Math.min(3, s.docs.length) - cands.length);
    for (const d of fillers) {
      cands.push({
        path: d.path,
        doc: d,
        local: 0.1,
        raw: 0,
        matched: [],
        snippet: d.description || "",
      });
    }
  }

  const stage0Ms = Date.now() - t0;
  turn.retrieval.stage0Ms = stage0Ms;
  turn.retrieval.candidateCount = cands.length;
  turn.retrieval.candidates = cands.map((c) => ({
    path: c.path,
    local: c.local,
    raw: c.raw,
    matched: c.matched,
    snippet: c.snippet,
  }));

  const smallVault = (s.docs || []).length <= settings.maxDocs;

  // Stage 1: Single-batch asynchronous Decision Model evaluation when enabled and vault > maxDocs.
  if (settings.accelerate && ask.decision.available && !smallVault && cands.length > 0) {
    const req = buildDecisionRequest(q, formatConversationSummary(thread.turns.slice(0, -1)), cands);
    const tag = makeTag(ctx.scopeKey, thread.id, turn.id);
    try {
      const sub = await mako.decision.submit(req, { tag, timeoutMs: 20000 });
      turn.decisionReq = String(sub?.requestId || "");
      await persistAskHistory(ctx.scopeKey, ask.threads);
      return;
    } catch (err) {
      const msg = err?.message ? String(err.message) : String(err);
      if (/decision unavailable/i.test(msg)) {
        ask.decision.available = false;
      }
      turn.retrieval.fallbackReason = msg;
    }
  }

  // Direct Stage 2 with local ranking or whole-vault mode.
  const mode = smallVault ? "all" : "local";
  const ranked = cands.map((c) => ({
    ...c,
    decision: null,
    level: null,
    levelLabel: "",
    final: c.local,
  }));
  finalizeRetrievalSelection(s, turn, {
    mode,
    ranked,
    entry: ranked[0]?.path || "",
    needsMore: false,
    notCovered: false,
    ms: stage0Ms,
    provider: "",
    model: "",
    fallbackReason: turn.retrieval.fallbackReason,
  });
  await startAgentForTurn(s, ctx.scopeKey, thread, turn);
  await persistAskHistory(ctx.scopeKey, ask.threads);
}

function generateSuggestedQuestions(docs, t) {
  const out = [];
  const list = docs || [];
  if (list.length === 0) return out;
  const nonIndex = list.filter((d) => d.path !== "index.md");
  const analytics = buildVaultAnalytics(nonIndex.length > 0 ? nonIndex : list, 0);

  // 1. Top hub document
  const topHub = analytics.rankedHubs[0]?.doc;
  if (topHub) {
    out.push(t("ask.suggest.hub", { title: topHub.title }));
  }
  // 2. Most frequent tag
  const topTag = analytics.tagStats.find((x) => x.tag !== "okf" && x.tag !== "index") || analytics.tagStats[0];
  if (topTag) {
    out.push(t("ask.suggest.tag", { tag: topTag.tag }));
  }
  // 3. Needs review
  if (list.some((d) => d.status === "needs_review")) {
    out.push(t("ask.suggest.review"));
  }
  // 4. Most populated folder
  const folderCounts = new Map();
  for (const d of nonIndex) {
    if (d.path.includes("/")) {
      const f = d.path.split("/")[0];
      folderCounts.set(f, (folderCounts.get(f) || 0) + 1);
    }
  }
  const topFolder = [...folderCounts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))[0];
  if (topFolder) {
    out.push(t("ask.suggest.folder", { folder: topFolder[0] }));
  }
  // 5. Latest Decision document
  const decisions = list
    .filter((d) => d.type === "Decision")
    .sort((a, b) => String(b.updated || "").localeCompare(String(a.updated || "")) || a.path.localeCompare(b.path));
  if (decisions[0]) {
    out.push(t("ask.suggest.decision", { title: decisions[0].title }));
  }

  return [...new Set(out)].slice(0, 5);
}

// --- Rendering ---

function renderTopBar(s, ui, t, activeTab, tabOptions) {
  return ui.row([
    ui.tabs(activeTab, tabOptions, { key: "main-tabs", grow: 1, onChange: ["setTab"] }),
    ui.button(t("action.new"), {
      key: "btn-new",
      icon: "plus",
      size: "small",
      variant: "ghost",
      tone: "accent",
      onTap: ["newDoc"],
    }),
  ], { key: "topbar", gap: 6 });
}

function renderStatusBar(s, ui, t) {
  const items = [
    ui.text(t("topbar.summary", { docs: s.docs.length, size: formatBytes(s.usedBytes) }), {
      size: "small",
      tone: "faint",
      grow: 1,
    }),
  ];
  if (s.draft && !s.editorOpen) {
    items.push(
      ui.pill(t("status.draft"), {
        key: "status-draft-pill",
        variant: "soft",
        tone: "accent",
        icon: "pencil",
        onTap: ["resumeDraft"],
      }),
    );
  }
  if (s.issueCount > 0) {
    items.push(
      ui.pill(t("status.issues", { count: s.issueCount }), {
        key: "status-issues-pill",
        variant: "soft",
        tone: "warning",
        icon: "shield-alert",
        onTap: ["setTab", "health"],
      }),
    );
  }
  return ui.row(items, { key: "statusbar", gap: 6 });
}

function renderKpiCard(ui, key, label, value, tone = "accent") {
  return ui.card([
    ui.text(label, { size: "small", tone: "muted" }),
    ui.text(String(value), { size: "large", weight: "semibold", tone }),
  ], { key, variant: "soft", gap: 2 });
}

function renderBrowseBar(s, ui, t) {
  const typeFilterOptions = TYPE_FILTERS.map((tp) => labelType(t, tp));
  const sortOptions = SORT_KEYS.map((sk) => labelSort(t, sk));

  const rows = [
    ui.row([
      ui.input(s.query, {
        key: "search",
        placeholder: t("browse.searchPlaceholder"),
        grow: 1,
        onChange: ["setQuery"],
        onSubmit: ["setQuery"],
      }),
      ui.button("", {
        key: "browse-filters",
        icon: "sliders-horizontal",
        size: "small",
        variant: s.showFilters ? "secondary" : "ghost",
        onTap: ["toggleFilters"],
      }),
    ], { gap: 6 }),
  ];

  if (s.showFilters) {
    const statusChips = STATUS_FILTERS.map((st) => {
      const selected = s.statusFilter === st;
      return ui.chip(labelStatus(t, st), {
        key: `status-${st}`,
        variant: selected ? "solid" : "soft",
        tone: st === "all" ? (selected ? "accent" : "") : statusTone(st),
        onTap: ["setStatusFilter", st],
      });
    });
    rows.push(
      ui.wrap([
        ui.select(labelType(t, s.typeFilter), typeFilterOptions, {
          key: "type-filter",
          onChange: ["setTypeFilter"],
        }),
        ui.select(labelSort(t, s.sortBy), sortOptions, {
          key: "sort",
          onChange: ["setSort"],
        }),
        ...statusChips,
      ], { key: "browse-filter-panel", gap: 6 }),
    );
  } else {
    const activeChips = [];
    if (s.typeFilter && s.typeFilter !== "All") {
      activeChips.push(
        ui.chip(labelType(t, s.typeFilter), {
          key: "af-type",
          variant: "outline",
          icon: "x",
          onTap: ["setTypeFilter", "All"],
        }),
      );
    }
    if (s.statusFilter && s.statusFilter !== "all") {
      activeChips.push(
        ui.chip(labelStatus(t, s.statusFilter), {
          key: "af-status",
          variant: "outline",
          tone: statusTone(s.statusFilter),
          icon: "x",
          onTap: ["setStatusFilter", "all"],
        }),
      );
    }
    if (s.tagFilter) {
      activeChips.push(
        ui.chip(`#${s.tagFilter}`, {
          key: "af-tag",
          variant: "outline",
          tone: "accent",
          icon: "x",
          onTap: ["setTagFilter", ""],
        }),
      );
    }
    if (s.sortBy && s.sortBy !== "relevance") {
      activeChips.push(
        ui.chip(labelSort(t, s.sortBy), {
          key: "af-sort",
          variant: "outline",
          icon: "x",
          onTap: ["setSort", "relevance"],
        }),
      );
    }
    if (activeChips.length > 0) {
      rows.push(ui.wrap(activeChips, { key: "browse-active-filters", gap: 4 }));
    }
  }

  return ui.column(rows, { key: "browse-bar", gap: 6 });
}

function renderBrowseBody(s, ui, t) {
  let results = searchDocuments(s.docs, s.query, s.typeFilter === "All" ? "" : s.typeFilter, s.tagFilter);
  if (s.statusFilter && s.statusFilter !== "all") {
    results = results.filter(({ doc }) => doc.status === s.statusFilter);
  }

  if (s.sortBy === "updated") {
    results = [...results].sort(
      (a, b) =>
        (b.doc.modifiedMs || 0) - (a.doc.modifiedMs || 0) ||
        String(b.doc.updated || "").localeCompare(String(a.doc.updated || "")) ||
        a.doc.path.localeCompare(b.doc.path),
    );
  } else if (s.sortBy === "title") {
    results = [...results].sort((a, b) => a.doc.title.localeCompare(b.doc.title) || a.doc.path.localeCompare(b.doc.path));
  } else if (s.sortBy === "linked") {
    results = [...results].sort(
      (a, b) =>
        ((b.doc.links.length + b.doc.backlinks.length) - (a.doc.links.length + a.doc.backlinks.length)) ||
        a.doc.path.localeCompare(b.doc.path),
    );
  }

  const children = [];

  if (isAskableQuery(s.query)) {
    children.push(
      ui.card([
        ui.row([
          ui.icon("sparkles", { size: "small", tone: "accent" }),
          ui.text(t("browse.askCta", { q: truncateUtf8Bytes(cleanInline(s.query), 72) }), { weight: "medium", grow: 1 }),
          ui.icon("chevron-right", { size: "small", tone: "accent" }),
        ], { gap: 6 }),
      ], { key: "ask-cta", variant: "soft", tone: "accent", onTap: ["askFromSearch"] }),
    );
  }

  if (results.length === 0) {
    const emptyActions = [
      ui.button(t("action.createDoc"), {
        key: "empty-new",
        icon: "plus",
        size: "small",
        variant: "primary",
        tone: "accent",
        onTap: ["newDoc"],
      }),
    ];
    if (String(s.query || "").trim()) {
      emptyActions.push(
        ui.button(t("browse.askInstead"), {
          key: "empty-ask",
          icon: "sparkles",
          size: "small",
          variant: "ghost",
          tone: "accent",
          onTap: ["askFromSearch"],
        }),
      );
    }
    children.push(
      ui.card([
        ui.text(t("browse.empty"), { tone: "muted" }),
        ui.row(emptyActions, { gap: 6 }),
      ], { key: "browse-empty", variant: "soft", gap: 8 }),
    );
    return children;
  }

  const docKey = makeKeyDeduper();
  children.push(
    ui.list(results, ({ doc, snippet }) => {
      const titleRow = [
        ui.icon(typeIcon(doc.type), { tone: typeTone(doc.type), size: "small" }),
        ui.text(truncateUtf8Bytes(cleanInline(doc.title), 512), { weight: "semibold", grow: 1 }),
      ];
      if (doc.status !== "active") {
        titleRow.push(ui.pill(labelStatus(t, doc.status), { variant: "soft", tone: statusTone(doc.status) }));
      }

      const cardChildren = [ui.row(titleRow, { gap: 6 })];
      const cleanSnippet = truncateUtf8Bytes(stripMarkdownForSnippet(snippet), 400);
      if (cleanSnippet) {
        cardChildren.push(ui.text(cleanSnippet, { size: "small", tone: "muted" }));
      }

      const metaRow = [ui.text(truncateUtf8Bytes(cleanInline(doc.path), 512), { size: "small", tone: "faint", grow: 1 })];
      if (doc.links.length > 0 || doc.backlinks.length > 0) {
        metaRow.push(ui.text(`↙${doc.backlinks.length} ↗${doc.links.length}`, { size: "small", tone: "faint" }));
      }
      if (doc.updated) {
        metaRow.push(ui.text(cleanInline(doc.updated), { size: "small", tone: "faint" }));
      }
      cardChildren.push(ui.row(metaRow, { gap: 8 }));

      if (doc.tags.length > 0) {
        const tagKey = makeKeyDeduper();
        cardChildren.push(
          ui.wrap(
            doc.tags.slice(0, 8).map((tag) =>
              ui.chip(truncateUtf8Bytes(cleanInline(tag), 128), {
                key: tagKey("t", tag),
                variant: "soft",
                icon: "tag",
                onTap: ["setTagFilter", tag],
              }),
            ),
            { gap: 4 },
          ),
        );
      }
      return ui.card(cardChildren, { key: docKey("doc", doc.path), gap: 4, onTap: ["openDoc", doc.path] });
    }, { gap: 6 }),
  );

  return children;
}

function buildTreeItems(node, s, ui, t) {
  const items = [];
  const itemKey = makeKeyDeduper();
  for (const dir of node.dirs) {
    const isCollapsed = Boolean(s.collapsedDirs && s.collapsedDirs[dir.path]);
    const isSelected = s.treeSelectedDir === dir.path;
    const children = buildTreeItems(dir, s, ui, t);
    items.push(
      ui.treeItem(truncateUtf8Bytes(cleanInline(dir.name), 256), children, {
        key: itemKey("dir", dir.path),
        value: String(dir.docCount),
        icon: isCollapsed ? "folder" : "folder-open",
        tone: isSelected ? "accent" : "",
        variant: "badge",
        checked: !isCollapsed,
        onChange: ["toggleDir", dir.path],
        onTap: ["selectDir", dir.path],
        onDrop: ["moveDocToFolder", dir.path],
        dropAccept: ["okf-doc"],
      }),
    );
  }
  for (const file of node.files) {
    const doc = file.doc;
    const badge = doc.status !== "active"
      ? labelStatus(t, doc.status)
      : (doc.backlinks.length > 0 ? `↙${doc.backlinks.length}` : "");
    const itemOpts = {
      key: itemKey("tdoc", doc.path),
      value: truncateUtf8Bytes(cleanInline(badge), 64),
      icon: typeIcon(doc.type),
      tone: doc.path === s.selectedPath ? "accent" : typeTone(doc.type),
      variant: "badge",
      onTap: ["openDoc", doc.path],
    };
    if (utf8ByteLength(doc.path) <= 256 && !SPOOFING_RE.test(doc.path)) {
      itemOpts.drag = doc.path;
      itemOpts.dragGroup = "okf-doc";
    }
    items.push(
      ui.treeItem(truncateUtf8Bytes(cleanInline(doc.title || file.name), 256), itemOpts),
    );
  }
  return items;
}

function renderTreeBar(s, ui, t, totalDirs, validDirs) {
  const rows = [
    ui.row([
      ui.icon("folder-open", { size: "small", tone: "accent" }),
      ui.text(t("tree.summary", { dirs: totalDirs, docs: s.docs.length }), { size: "small", tone: "muted", grow: 1 }),
      ui.button("", {
        key: "tree-new-dir-btn",
        icon: "folder-plus",
        size: "small",
        variant: "ghost",
        onTap: ["toggleNewFolder"],
      }),
      ui.button("", {
        key: "tree-expand-all",
        icon: "chevrons-up-down",
        size: "small",
        variant: "ghost",
        onTap: ["expandAllDirs"],
      }),
      ui.button("", {
        key: "tree-collapse-all",
        icon: "square-minus",
        size: "small",
        variant: "ghost",
        onTap: ["collapseAllDirs"],
      }),
    ], {
      key: "tree-root",
      gap: 4,
      onDrop: ["moveDocToFolder", ""],
      dropAccept: ["okf-doc"],
    }),
  ];

  if (s.treeSelectedDir && validDirs.includes(s.treeSelectedDir)) {
    rows.push(
      ui.card([
        ui.row([
          ui.icon("folder", { size: "small", tone: "accent" }),
          ui.text(s.treeSelectedDir, { weight: "medium", grow: 1 }),
          ui.button(t("tree.newDocHere"), {
            key: "tree-sel-newdoc",
            icon: "plus",
            size: "small",
            variant: "primary",
            tone: "accent",
            onTap: ["newDocInFolder", s.treeSelectedDir],
          }),
          ui.button(t("tree.newFolderHere"), {
            key: "tree-sel-newdir",
            icon: "folder-plus",
            size: "small",
            variant: "ghost",
            onTap: ["newFolderIn", s.treeSelectedDir],
          }),
          ui.button("", {
            key: "tree-sel-close",
            icon: "x",
            size: "small",
            variant: "ghost",
            onTap: ["selectDir", ""],
          }),
        ], { gap: 6 }),
      ], { key: "tree-selection", variant: "soft", tone: "accent", gap: 6 }),
    );
  }

  if (s.showNewFolder) {
    const ph = s.newFolderParent ? `${s.newFolderParent}/…` : t("tree.newFolderPlaceholder");
    rows.push(
      ui.row([
        ui.input(s.newFolderDraft, {
          key: "tree-mkdir-input",
          placeholder: ph,
          grow: 1,
          onChange: ["setNewFolderDraft"],
          onSubmit: ["createFolder"],
        }),
        ui.button(t("action.create"), {
          key: "tree-mkdir-submit",
          size: "small",
          variant: "primary",
          tone: "accent",
          onTap: ["createFolder"],
        }),
        ui.button(t("action.cancel"), {
          key: "tree-mkdir-cancel",
          size: "small",
          variant: "ghost",
          onTap: ["toggleNewFolder"],
        }),
      ], { key: "tree-mkdir-row", gap: 6 }),
    );
  }

  return ui.column(rows, { key: "tree-bar", gap: 6 });
}

function renderTreeBody(s, ui, t, tree) {
  const rootItems = buildTreeItems(tree, s, ui, t);
  const sections = [];

  if (rootItems.length === 0) {
    sections.push(ui.text(t("tree.empty"), { size: "small", tone: "muted" }));
  } else {
    sections.push(ui.tree(rootItems, { key: "vault-tree" }));
  }

  const recentDocs = [...(s.docs || [])]
    .sort((a, b) => (b.modifiedMs || 0) - (a.modifiedMs || 0) || String(b.updated || "").localeCompare(String(a.updated || "")) || a.path.localeCompare(b.path))
    .slice(0, 5);

  if (recentDocs.length > 0) {
    const recKey = makeKeyDeduper();
    sections.push(
      ui.collapsible(
        t("tree.recent"),
        [
          ui.list(recentDocs, (d) => ui.row([
            ui.icon(typeIcon(d.type), { size: "small", tone: typeTone(d.type) }),
            ui.text(truncateUtf8Bytes(cleanInline(d.title), 256), { size: "small", weight: "medium", grow: 1 }),
            ui.text(truncateUtf8Bytes(cleanInline(d.updated || d.path), 128), { size: "small", tone: "faint" }),
          ], { key: recKey("rec", d.path), gap: 6, onTap: ["openDoc", d.path] }), { gap: 4 }),
        ],
        {
          key: "tree-recent",
          icon: "history",
          tone: "muted",
          checked: Boolean(s.treeRecentOpen),
          onChange: ["toggleTreeRecent"],
        },
      ),
    );
  }

  return sections;
}

function computeGraphContext(s, t) {
  const analytics = buildVaultAnalytics(s.docs, s.broken.length);
  const byPath = new Map(s.docs.map((d) => [d.path, d]));
  const graph = buildKnowledgeGraph(s.docs, {
    typeTone,
    labelType: (tp) => labelType(t, tp),
    missingGroup: t("graph.missingGroup"),
    missingSubtitle: t("graph.missingSubtitle"),
  });

  let focusId = s.graphFocusPath;
  if (!focusId || !graph.nodeIds.has(focusId)) {
    focusId = analytics.rankedHubs[0]?.doc?.path || graph.nodes[0]?.id || "";
  }
  const focusDoc = focusId ? byPath.get(focusId) : null;
  const focusMissing = focusId ? graph.missingByNodeId.get(focusId) : null;
  return { analytics, byPath, graph, focusId, focusDoc, focusMissing };
}

function renderGraphBar(s, ui, t, gctx) {
  const label = s.graphFocusPath && gctx.focusDoc
    ? t("graph.focused", { title: gctx.focusDoc.title })
    : (s.graphFocusPath && gctx.focusMissing
      ? t("graph.focused", { title: gctx.focusMissing.target })
      : t("graph.hint"));
  const rowItems = [ui.text(cleanInline(label), { size: "small", tone: "muted", grow: 1 })];
  if (s.graphFocusPath) {
    rowItems.push(
      ui.button(t("graph.reset"), {
        key: "graph-reset",
        icon: "x",
        size: "small",
        variant: "ghost",
        onTap: ["setGraphFocus", ""],
      }),
    );
  }
  return ui.row(rowItems, { key: "graph-bar", gap: 6 });
}

function renderGraphBody(s, ui, t, gctx) {
  const { analytics, byPath, graph, focusId, focusDoc, focusMissing } = gctx;
  const sections = [];

  // 1. Compact 3-column KPI grid
  sections.push(
    ui.grid(3, [
      renderKpiCard(ui, "kpi-docs", t("graph.kpiDocs"), analytics.totalDocs, "accent"),
      renderKpiCard(ui, "kpi-links", t("graph.kpiLinks"), analytics.totalLinks, "success"),
      renderKpiCard(
        ui,
        "kpi-integrity",
        t("graph.kpiIntegrity"),
        formatPct(analytics.linkHealthRatio),
        s.broken.length > 0 ? "warning" : "success",
      ),
    ], { key: "graph-kpis", gap: 6 }),
  );

  // 2. Interactive Knowledge Graph Canvas
  if (graph.nodes.length > 0) {
    sections.push(
      ui.card([
        ui.graph(graph.nodes, graph.edges, {
          key: "knowledge-graph",
          caption: t("graph.canvasCaption"),
          value: focusId && graph.nodeIds.has(focusId) ? focusId : "",
          maxHeight: 340,
          onChange: ["setGraphFocus"],
        }),
      ], { gap: 6 }),
    );
  } else {
    sections.push(ui.card([ui.text(t("graph.empty"), { size: "small", tone: "muted" })], { variant: "soft" }));
  }

  // 3. Selected-node inspector card
  if (focusDoc) {
    const neighbors = [...new Set([...focusDoc.links, ...focusDoc.backlinks])];
    const inspectorChildren = [
      ui.row([
        ui.icon(typeIcon(focusDoc.type), { size: "small", tone: typeTone(focusDoc.type) }),
        ui.text(cleanInline(focusDoc.title), { weight: "semibold", grow: 1 }),
        ui.pill(labelType(t, focusDoc.type), { variant: "soft", tone: typeTone(focusDoc.type) }),
        ui.pill(labelStatus(t, focusDoc.status), { variant: "soft", tone: statusTone(focusDoc.status) }),
      ], { gap: 6 }),
      ui.text(`${focusDoc.path} · ↗${focusDoc.links.length} ↙${focusDoc.backlinks.length}`, { size: "small", tone: "faint" }),
    ];

    if (neighbors.length > 0) {
      const gnKey = makeKeyDeduper();
      inspectorChildren.push(
        ui.wrap(
          neighbors.map((p) => {
            const nb = byPath.get(p);
            return ui.chip(truncateUtf8Bytes(cleanInline(nb ? nb.title : p), 128), {
              key: gnKey("gn", p),
              variant: "soft",
              icon: "file-text",
              onTap: ["setGraphFocus", p],
            });
          }),
          { gap: 4 },
        ),
      );
    } else if (focusDoc.brokenLinks.length === 0) {
      inspectorChildren.push(ui.text(t("graph.inspectorNoNeighbors"), { size: "small", tone: "faint" }));
    }

    if (focusDoc.brokenLinks.length > 0) {
      const gbrkKey = makeKeyDeduper();
      inspectorChildren.push(ui.text(t("graph.inspectorBroken"), { size: "small", weight: "medium", tone: "danger" }));
      inspectorChildren.push(
        ui.wrap(
          focusDoc.brokenLinks.map((raw) =>
            ui.chip(truncateUtf8Bytes(`+ [[${cleanInline(raw)}]]`, 128), {
              key: gbrkKey("gbrk", raw),
              variant: "outline",
              tone: "warning",
              onTap: ["createFromLink", raw],
            }),
          ),
          { gap: 4 },
        ),
      );
    }

    inspectorChildren.push(
      ui.row([
        ui.button(t("action.open"), {
          key: "graph-open-focus",
          size: "small",
          variant: "primary",
          tone: "accent",
          onTap: ["openDoc", focusDoc.path],
        }),
        ui.button(t("action.askAbout"), {
          key: "graph-ask-focus",
          icon: "sparkles",
          size: "small",
          variant: "ghost",
          tone: "accent",
          onTap: ["askAboutDoc", focusDoc.path],
        }),
        ui.button(t("action.edit"), {
          key: "graph-edit-focus",
          icon: "pencil",
          size: "small",
          variant: "ghost",
          onTap: ["editDoc", focusDoc.path],
        }),
      ], { gap: 6 }),
    );

    sections.push(ui.card(inspectorChildren, { key: "graph-inspector", gap: 6 }));
  } else if (focusMissing) {
    const gmrefKey = makeKeyDeduper();
    sections.push(
      ui.card([
        ui.row([
          ui.text(truncateUtf8Bytes(cleanInline(t("graph.missingInspectorTitle", { target: focusMissing.target })), 256), { weight: "semibold", tone: "danger", grow: 1 }),
          ui.button(t("action.create"), {
            key: "graph-create-missing",
            size: "small",
            variant: "primary",
            tone: "warning",
            onTap: ["createFromLink", focusMissing.target],
          }),
        ], { gap: 6 }),
        ui.text(t("graph.missingInspectorDesc", { count: focusMissing.refs.length }), { size: "small", tone: "muted" }),
        ui.wrap(
          focusMissing.refs.map((p) => {
            const src = byPath.get(p);
            return ui.chip(truncateUtf8Bytes(cleanInline(src ? src.title : p), 128), {
              key: gmrefKey("gmref", p),
              variant: "soft",
              onTap: ["setGraphFocus", p],
            });
          }),
          { gap: 4 },
        ),
      ], { key: "graph-inspector-missing", variant: "danger", gap: 6 }),
    );
  }

  // 4. Collapsible Insights (keeps charts, centrality table, and tag cloud tidy while preserving all nodes in tree)
  const insightChildren = [];
  if (analytics.typeStats.length > 0) {
    insightChildren.push(
      ui.card([
        ui.chart("donut", [
          { name: t("graph.chartDocs"), tone: "accent", values: analytics.typeStats.map((x) => x.count) },
        ], {
          key: "chart-types",
          caption: t("graph.chartByType"),
          labels: analytics.typeStats.map((x) => truncateUtf8Bytes(labelType(t, x.type), 24)),
        }),
      ], { gap: 6 }),
    );
  }
  if (analytics.topHubs.length > 0) {
    insightChildren.push(
      ui.card([
        ui.chart("bar", [
          { name: t("graph.chartLinksOut"), tone: "accent", values: analytics.topHubs.map((x) => x.out) },
          { name: t("graph.chartLinksIn"), tone: "success", values: analytics.topHubs.map((x) => x.in) },
        ], {
          key: "chart-hubs",
          caption: t("graph.chartTopConnected"),
          labels: analytics.topHubs.map((x) => truncateUtf8Bytes(x.label, 24)),
        }),
      ], { gap: 6 }),
    );
  }
  if (analytics.rankedHubs.length > 0) {
    const hubKey = makeKeyDeduper();
    const tableRows = analytics.rankedHubs.slice(0, 10).map((h) => ui.row([
      ui.text(truncateUtf8Bytes(cleanInline(h.doc.title), 256), { size: "small", weight: h.doc.path === focusId ? "semibold" : "medium", grow: 1 }),
      ui.pill(labelType(t, h.doc.type), { variant: "soft", tone: typeTone(h.doc.type) }),
      ui.text(String(h.out), { size: "small", tone: "accent" }),
      ui.text(String(h.in), { size: "small", tone: "success" }),
    ], { key: hubKey("hub", h.doc.path), gap: 6, onTap: ["setGraphFocus", h.doc.path] }));
    insightChildren.push(
      ui.card([
        ui.text(t("graph.centralityTitle"), { size: "small", weight: "semibold", tone: "muted" }),
        ui.table([t("graph.colDocument"), t("graph.colType"), "↗", "↙"], tableRows),
      ], { gap: 6 }),
    );
  }
  if (analytics.tagStats.length > 0) {
    const gtagKey = makeKeyDeduper();
    insightChildren.push(
      ui.card([
        ui.text(t("graph.tagDistribution", { count: analytics.tagStats.length }), { size: "small", weight: "semibold", tone: "muted" }),
        ui.wrap(
          analytics.tagStats.slice(0, 24).map((item) => ui.chip(truncateUtf8Bytes(`${cleanInline(item.tag)} (${item.count})`, 128), {
            key: gtagKey("gtag", item.tag),
            variant: "soft",
            icon: "tag",
            onTap: ["filterTagAndClose", item.tag],
          })),
          { gap: 4 },
        ),
      ], { gap: 6 }),
    );
  }

  if (insightChildren.length > 0) {
    sections.push(
      ui.collapsible(t("graph.insights"), insightChildren, {
        key: "graph-insights",
        icon: "layers",
        tone: "muted",
        checked: Boolean(s.insightsOpen),
        onChange: ["toggleInsights"],
      }),
    );
  }

  return sections;
}

function renderHealthBar(s, ui, t) {
  return ui.row([
    ui.text(
      t("health.storage", {
        used: formatBytes(s.usedBytes),
        quota: formatBytes(s.quotaBytes),
        files: s.fileCount,
      }),
      { size: "small", tone: "muted", grow: 1 },
    ),
    ui.button(t("action.index"), {
      key: "btn-index",
      icon: "refresh-cw",
      size: "small",
      variant: "ghost",
      onTap: ["rebuildIndex"],
    }),
  ], { key: "health-bar", gap: 6 });
}

function renderHealthBody(s, ui, t) {
  const items = [
    ui.grid(3, [
      renderKpiCard(ui, "health-kpi-broken", t("health.kpiBroken"), s.broken.length, s.broken.length > 0 ? "danger" : "success"),
      renderKpiCard(ui, "health-kpi-review", t("health.kpiReview"), s.needsReview.length, s.needsReview.length > 0 ? "warning" : "success"),
      renderKpiCard(ui, "health-kpi-orphans", t("health.kpiOrphans"), s.orphans.length, s.orphans.length > 0 ? "muted" : "success"),
    ], { key: "health-kpis", gap: 6 }),
  ];

  if (!s.hasIndex) {
    items.push(
      ui.card([
        ui.row([
          ui.text(t("health.missingIndex"), { weight: "medium", tone: "warning", grow: 1 }),
          ui.button(t("action.generateIndex"), {
            key: "lint-mk-index",
            size: "small",
            variant: "primary",
            tone: "accent",
            onTap: ["rebuildIndex"],
          }),
        ], { gap: 6 }),
      ], { variant: "soft", tone: "warning", gap: 6 }),
    );
  }

  if (s.broken.length > 0) {
    items.push(
      ui.collapsible(
        t("health.brokenTitle", { count: s.broken.length }),
        [
          ui.list(s.broken, (b, i) => ui.row([
            ui.text(`${b.from} → [[${b.target}]]`, { size: "small", grow: 1 }),
            ui.button(t("action.create"), {
              key: `lint-fix-${i}`,
              size: "small",
              variant: "outline",
              tone: "warning",
              onTap: ["createFromLink", b.target],
            }),
          ], { key: `brk-${i}`, gap: 6 }), { gap: 4 }),
        ],
        {
          key: "health-broken",
          icon: "shield-alert",
          tone: "danger",
          checked: true,
        },
      ),
    );
  }

  if (s.needsReview.length > 0) {
    const revKey = makeKeyDeduper();
    items.push(
      ui.collapsible(
        t("health.reviewTitle", { count: s.needsReview.length }),
        [
          ui.list(s.needsReview, (r) => ui.row([
            ui.text(truncateUtf8Bytes(cleanInline(`${r.title} (${r.path})`), 512), { size: "small", grow: 1 }),
            ui.button(t("action.open"), {
              key: safeKey("rev-open", r.path),
              size: "small",
              variant: "ghost",
              onTap: ["openDoc", r.path],
            }),
          ], { key: revKey("rev", r.path), gap: 6 }), { gap: 4 }),
        ],
        {
          key: "health-review",
          icon: "info",
          tone: "warning",
          checked: true,
        },
      ),
    );
  }

  if (s.missingMeta.length > 0) {
    const metaKey = makeKeyDeduper();
    items.push(
      ui.collapsible(
        t("health.missingMetaTitle", { count: s.missingMeta.length }),
        [
          ui.list(s.missingMeta, (m) => {
            const reason = m.code === "missing_frontmatter"
              ? t("health.missingFrontmatter")
              : t("health.missingTitleOrType");
            return ui.row([
              ui.text(truncateUtf8Bytes(cleanInline(`${m.path} · ${reason}`), 512), { size: "small", grow: 1 }),
              ui.button(t("action.open"), {
                key: safeKey("meta-open", m.path),
                size: "small",
                variant: "ghost",
                onTap: ["openDoc", m.path],
              }),
            ], { key: metaKey("meta", m.path), gap: 6 });
          }, { gap: 4 }),
        ],
        {
          key: "health-meta",
          icon: "info",
          tone: "warning",
          checked: true,
        },
      ),
    );
  }

  if (s.orphans.length > 0) {
    const orpKey = makeKeyDeduper();
    items.push(
      ui.collapsible(
        t("health.orphansTitle", { count: s.orphans.length }),
        [
          ui.list(s.orphans, (o) => ui.row([
            ui.text(truncateUtf8Bytes(cleanInline(`${o.title} (${o.path})`), 512), { size: "small", grow: 1 }),
            ui.button(t("action.open"), {
              key: safeKey("orp-open", o.path),
              size: "small",
              variant: "ghost",
              onTap: ["openDoc", o.path],
            }),
          ], { key: orpKey("orp", o.path), gap: 6 }), { gap: 4 }),
        ],
        {
          key: "health-orphans",
          icon: "file-text",
          tone: "muted",
          checked: s.broken.length === 0 && s.needsReview.length === 0,
        },
      ),
    );
  }

  if (s.issueCount === 0 && s.orphans.length === 0) {
    items.push(
      ui.card([
        ui.row([
          ui.icon("circle-check", { tone: "success" }),
          ui.text(t("health.allHealthy"), { tone: "success" }),
        ], { gap: 6 }),
      ], { variant: "soft", tone: "success" }),
    );
  }

  return items;
}

function renderReaderBar(s, ui, t, doc) {
  const pathSegments = doc.path.split("/").filter(Boolean);
  return ui.column([
    ui.row([
      ui.button(t("action.back"), {
        key: "reader-back",
        icon: "arrow-left",
        size: "small",
        variant: "ghost",
        onTap: ["closeDoc"],
      }),
      ui.text("", { grow: 1 }),
      ui.button("", {
        key: "reader-ask",
        icon: "sparkles",
        size: "small",
        variant: "ghost",
        tone: "accent",
        onTap: ["askAboutDoc", doc.path],
      }),
      ui.button("", {
        key: "reader-graph",
        icon: "network",
        size: "small",
        variant: "ghost",
        onTap: ["openInGraph", doc.path],
      }),
      ui.button(t("action.edit"), {
        key: "reader-edit",
        icon: "pencil",
        size: "small",
        variant: "ghost",
        tone: "accent",
        onTap: ["editDoc", doc.path],
      }),
      doc.status === "needs_review"
        ? ui.button(t("action.markActive"), {
          key: "reader-active",
          size: "small",
          variant: "ghost",
          tone: "success",
          onTap: ["setDocStatus", doc.path, "active"],
        })
        : ui.button(t("action.markReview"), {
          key: "reader-review",
          size: "small",
          variant: "ghost",
          tone: "warning",
          onTap: ["setDocStatus", doc.path, "needs_review"],
        }),
      s.confirmDeletePath === doc.path
        ? ui.button(t("action.confirmDelete"), {
          key: "reader-del-confirm",
          size: "small",
          variant: "danger",
          tone: "danger",
          onTap: ["deleteDoc", doc.path],
        })
        : ui.button("", {
          key: "reader-del",
          icon: "trash-2",
          size: "small",
          variant: "ghost",
          tone: "danger",
          onTap: ["askDelete", doc.path],
        }),
    ], { gap: 4 }),
    ui.breadcrumb([t("breadcrumb.vault"), ...pathSegments.map((seg) => truncateUtf8Bytes(cleanInline(seg), 300))], {
      key: "reader-breadcrumb",
      onChange: ["onBreadcrumb", doc.path],
    }),
  ], { key: "reader-bar", gap: 4 });
}

function renderReaderBody(s, ui, t, doc) {
  const byPath = new Map(s.docs.map((d) => [d.path, d]));

  const headerLines = [
    ui.row([
      ui.pill(labelType(t, doc.type), { variant: "soft", icon: typeIcon(doc.type), tone: typeTone(doc.type) }),
      ui.pill(labelStatus(t, doc.status), { variant: "soft", tone: statusTone(doc.status) }),
      ui.text(cleanInline(doc.updated || ""), { size: "small", tone: "faint", grow: 1 }),
    ], { gap: 6 }),
    ui.text(truncateUtf8Bytes(cleanInline(doc.title), 1024), { size: "large", weight: "semibold" }),
  ];
  if (doc.description) {
    headerLines.push(ui.text(truncateUtf8Bytes(cleanInline(doc.description), 2048), { size: "small", tone: "muted" }));
  }
  if (doc.tags.length > 0) {
    const rtKey = makeKeyDeduper();
    headerLines.push(
      ui.wrap(
        doc.tags.map((tag) =>
          ui.chip(truncateUtf8Bytes(cleanInline(tag), 128), {
            key: rtKey("rt", tag),
            variant: "soft",
            icon: "tag",
            onTap: ["filterTagAndClose", tag],
          }),
        ),
        { gap: 4 },
      ),
    );
  }

  const metaDetails = [
    ui.row([
      ui.text(t("reader.pathLabel"), { size: "small", tone: "muted" }),
      ui.text(truncateUtf8Bytes(cleanInline(doc.path), 1024), { size: "small", grow: 1 }),
    ], { gap: 6 }),
    ui.text(t("reader.linksSummary", { out: doc.links.length, in: doc.backlinks.length }), { size: "small", tone: "muted" }),
  ];
  if (doc.updated) {
    metaDetails.push(ui.row([
      ui.text(t("reader.updatedLabel"), { size: "small", tone: "muted" }),
      ui.text(cleanInline(doc.updated), { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }
  if (doc.okfVersion) {
    metaDetails.push(ui.row([
      ui.text(t("reader.versionLabel"), { size: "small", tone: "muted" }),
      ui.text(`OKF v${cleanInline(doc.okfVersion)}`, { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }
  if (doc.source) {
    metaDetails.push(ui.row([
      ui.text(t("reader.sourceLabel"), { size: "small", tone: "muted" }),
      ui.text(truncateUtf8Bytes(cleanInline(doc.source), 1024), { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }
  if (doc.verifiedBy) {
    metaDetails.push(ui.row([
      ui.text(t("reader.verifiedByLabel"), { size: "small", tone: "muted" }),
      ui.text(truncateUtf8Bytes(cleanInline(doc.verifiedBy), 1024), { size: "small", grow: 1 }),
    ], { gap: 6 }));
  }

  const rawRenderedBody = rewriteWikiLinksForMarkdown(doc.body, doc.path, s.docs) || t("reader.emptyBody");
  const renderedBody = cutUtf8Bytes(rawRenderedBody, MAX_READER_MD_BYTES);

  const sections = [
    ui.card(headerLines, { variant: "soft", gap: 4 }),
    ui.collapsible(t("reader.metadataTitle"), metaDetails, {
      key: "reader-meta",
      icon: "info",
      tone: "muted",
      checked: Boolean(s.metaExpanded),
      onChange: ["setMetaExpanded"],
    }),
    ui.card([
      ui.markdown(renderedBody, {
        key: "reader-body",
        onLink: ["openLink"],
      }),
    ], { gap: 6 }),
  ];

  if (doc.links.length > 0 || doc.backlinks.length > 0 || doc.brokenLinks.length > 0) {
    const graphRows = [];
    if (doc.links.length > 0) {
      const outKey = makeKeyDeduper();
      graphRows.push(ui.text(t("reader.outgoingLinks", { count: doc.links.length }), { size: "small", weight: "semibold", tone: "muted" }));
      graphRows.push(
        ui.list(doc.links, (p) => {
          const target = byPath.get(p);
          const titleText = cleanInline(target ? target.title : p);
          const showPathSub = Boolean(target && target.title && target.title !== p);
          const labelNodes = [ui.text(truncateUtf8Bytes(titleText, 256), { size: "small", weight: "medium" })];
          if (showPathSub) {
            labelNodes.push(ui.text(truncateUtf8Bytes(cleanInline(p), 256), { size: "small", tone: "faint" }));
          }
          return ui.row([
            ui.text("↗", { size: "small", tone: "accent" }),
            ui.column(labelNodes, { grow: 1, gap: 0 }),
          ], { key: outKey("out", p), gap: 6, onTap: ["openDoc", p] });
        }, { gap: 4 }),
      );
    }
    if (doc.backlinks.length > 0) {
      const inKey = makeKeyDeduper();
      graphRows.push(ui.text(t("reader.backlinks", { count: doc.backlinks.length }), { size: "small", weight: "semibold", tone: "muted" }));
      graphRows.push(
        ui.list(doc.backlinks, (p) => {
          const src = byPath.get(p);
          const titleText = cleanInline(src ? src.title : p);
          const showPathSub = Boolean(src && src.title && src.title !== p);
          const labelNodes = [ui.text(truncateUtf8Bytes(titleText, 256), { size: "small", weight: "medium" })];
          if (showPathSub) {
            labelNodes.push(ui.text(truncateUtf8Bytes(cleanInline(p), 256), { size: "small", tone: "faint" }));
          }
          return ui.row([
            ui.text("↙", { size: "small", tone: "muted" }),
            ui.column(labelNodes, { grow: 1, gap: 0 }),
          ], { key: inKey("in", p), gap: 6, onTap: ["openDoc", p] });
        }, { gap: 4 }),
      );
    }
    if (doc.brokenLinks.length > 0) {
      const brkrKey = makeKeyDeduper();
      graphRows.push(ui.text(t("reader.brokenLinks", { count: doc.brokenLinks.length }), { size: "small", weight: "semibold", tone: "danger" }));
      graphRows.push(
        ui.list(doc.brokenLinks, (target) => ui.row([
          ui.text(truncateUtf8Bytes(`[[${cleanInline(target)}]]`, 256), { size: "small", tone: "warning", grow: 1 }),
          ui.button(t("action.create"), {
            key: safeKey("fix", target),
            size: "small",
            variant: "outline",
            tone: "warning",
            onTap: ["createFromLink", target],
          }),
        ], { key: brkrKey("brkr", target), gap: 6 }), { gap: 4 }),
      );
    }
    sections.push(ui.card(graphRows, { gap: 6 }));
  }

  return sections;
}

function renderEditorBar(s, ui, t) {
  const d = s.draft || emptyDraft();
  const heading = d.originalPath ? t("editor.editTitle", { path: d.originalPath }) : t("editor.newTitle");
  return ui.row([
    ui.text(cleanInline(heading), { weight: "semibold", grow: 1 }),
    ui.button(t("action.cancel"), { key: "ed-cancel", size: "small", variant: "ghost", onTap: ["cancelEdit"] }),
    ui.button(t("action.save"), { key: "ed-save", size: "small", variant: "primary", tone: "accent", onTap: ["saveDraft"] }),
  ], { key: "editor-bar", gap: 6 });
}

function renderEditorBody(s, ui, t) {
  const d = s.draft || emptyDraft();
  const typeOptions = DOC_TYPES.map((tp) => labelType(t, tp));
  const statusOptions = DOC_STATUSES.map((st) => labelStatus(t, st));
  const rawBody = String(d.body || "");
  const bodyOverLimit = utf8ByteLength(rawBody) > MAX_EDITOR_BODY_BYTES;
  const safeBody = bodyOverLimit ? cutUtf8Bytes(rawBody, MAX_EDITOR_BODY_BYTES) : rawBody;

  const editorChildren = [
    ui.row([
      ui.input(sliceUtf8Bytes(d.title || "", 2048), {
        key: "ed-title",
        placeholder: t("editor.titlePlaceholder"),
        grow: 1,
        onChange: ["draftField", "title"],
      }),
      ui.select(labelType(t, d.type), typeOptions, { key: "ed-type", onChange: ["draftField", "type"] }),
      ui.select(labelStatus(t, d.status), statusOptions, { key: "ed-status", onChange: ["draftField", "status"] }),
    ], { gap: 6 }),
    ui.row([
      ui.input(sliceUtf8Bytes(d.path || "", 2048), {
        key: "ed-path",
        placeholder: t("editor.pathPlaceholder", { folder: d.folder || "concepts" }),
        grow: 1,
        onChange: ["draftField", "path"],
      }),
      ui.input(sliceUtf8Bytes(d.tags || "", 2048), {
        key: "ed-tags",
        placeholder: t("editor.tagsPlaceholder"),
        grow: 1,
        onChange: ["draftField", "tags"],
      }),
    ], { gap: 6 }),
    ui.input(sliceUtf8Bytes(d.description || "", 2048), {
      key: "ed-desc",
      placeholder: t("editor.descPlaceholder"),
      onChange: ["draftField", "description"],
    }),
    ui.row([
      ui.input(sliceUtf8Bytes(d.source || "", 2048), {
        key: "ed-source",
        placeholder: t("editor.sourcePlaceholder"),
        grow: 1,
        onChange: ["draftField", "source"],
      }),
      ui.input(sliceUtf8Bytes(d.verifiedBy || "", 2048), {
        key: "ed-verified",
        placeholder: t("editor.verifiedPlaceholder"),
        grow: 1,
        onChange: ["draftField", "verifiedBy"],
      }),
    ], { gap: 6 }),
  ];

  if (bodyOverLimit) {
    editorChildren.push(
      ui.card([ui.text(t("editor.bodyTooLarge"), { size: "small", tone: "warning" })], {
        key: "ed-body-limit-warn",
        variant: "soft",
        tone: "warning",
      }),
    );
  }

  editorChildren.push(
    ui.textarea(safeBody, {
      key: "ed-body",
      size: "large",
      placeholder: t("editor.bodyPlaceholder"),
      onChange: ["draftField", "body"],
    }),
  );

  return [ui.card(editorChildren, { gap: 8 })];
}

function renderDecisionPill(ask, ui, t) {
  const on = Boolean(ask.decision && ask.decision.available);
  const label = on
    ? t("ask.decisionOn", { model: ask.decision.model || ask.decision.provider || "on" })
    : t("ask.decisionOff");
  return ui.pill(cleanInline(label), {
    key: "ask-decision-pill",
    variant: "soft",
    tone: on ? "success" : "muted",
    icon: "sparkles",
  });
}

function renderAskBar(s, ui, t) {
  const ask = ensureAskState(s);
  const askable = (ask.agents || []).filter((a) => a.askable);
  const unaskable = (ask.agents || []).filter((a) => !a.askable);
  const decisionPill = renderDecisionPill(ask, ui, t);

  if (!ask.assigned || ask.showPicker) {
    const cardChildren = [
      ui.row([
        ui.icon("bot", { tone: "accent" }),
        ui.column([
          ui.text(t("ask.assignTitle"), { weight: "semibold" }),
          ui.text(t("ask.assignDesc"), { size: "small", tone: "muted" }),
        ], { grow: 1, gap: 2 }),
      ], { gap: 8 }),
    ];

    if (askable.length > 0) {
      const options = askable.map((a) => a.label);
      const currentVal = ask.assigned && options.includes(ask.assigned.label) ? ask.assigned.label : "";
      cardChildren.push(
        ui.select(currentVal, options, {
          key: "ask-agent",
          onChange: ["assignAgent"],
        }),
      );
      if (currentVal) {
        const selectedProfile = askable.find((a) => a.label === currentVal);
        if (selectedProfile?.description) {
          cardChildren.push(ui.text(truncateUtf8Bytes(cleanInline(selectedProfile.description), 240), { size: "small", tone: "faint" }));
        }
      } else {
        cardChildren.push(ui.text(t("ask.chooseAgentHint"), { size: "small", tone: "faint" }));
      }
    } else if (ask.agentsLoaded && !ask.agentsError) {
      cardChildren.push(ui.text(t("ask.err.noAgents"), { size: "small", tone: "warning" }));
    }

    if (unaskable.length > 0) {
      const reasons = unaskable
        .slice(0, 4)
        .map((a) => `${a.label}${a.reason ? ` (${a.reason})` : ""}`)
        .join(", ");
      cardChildren.push(
        ui.text(truncateUtf8Bytes(cleanInline(t("ask.unavailableAgents", { list: reasons })), 220), {
          size: "small",
          tone: "faint",
        }),
      );
    }

    const footerBtns = [
      decisionPill,
      ui.text("", { grow: 1 }),
      ui.button(t("ask.refresh"), {
        key: "ask-refresh",
        icon: "refresh-cw",
        size: "small",
        variant: "ghost",
        onTap: ["refreshAgents"],
      }),
    ];
    if (ask.assigned) {
      footerBtns.push(
        ui.button(t("action.done"), {
          key: "ask-picker-done",
          size: "small",
          variant: "ghost",
          onTap: ["toggleAgentPicker"],
        }),
      );
    }
    cardChildren.push(ui.row(footerBtns, { gap: 6 }));

    if (ask.agentsError) {
      cardChildren.push(ui.text(cleanInline(ask.agentsError), { size: "small", tone: "danger" }));
    }

    return ui.card(cardChildren, { key: "ask-assign", variant: "soft", tone: "accent", gap: 8 });
  }

  return ui.row([
    ui.chip(cleanInline(ask.assigned.label), {
      key: "ask-agent-chip",
      variant: "soft",
      tone: "accent",
      icon: "bot",
      onTap: ["toggleAgentPicker"],
    }),
    decisionPill,
    ui.text("", { grow: 1 }),
    ui.button("", {
      key: "ask-history-btn",
      icon: "history",
      size: "small",
      variant: ask.showHistory ? "secondary" : "ghost",
      onTap: ["toggleHistory"],
    }),
    ui.button(t("ask.new"), {
      key: "ask-new",
      icon: "plus",
      size: "small",
      variant: "ghost",
      onTap: ["newThread"],
    }),
  ], { key: "ask-agentbar", gap: 6 });
}

function renderAskErrorText(err, t) {
  const code = String(err?.code || "failed");
  if (code === "refused") return t("ask.err.refused");
  if (code === "timeout") return t("ask.err.timeout");
  if (code === "canceled") return t("ask.err.canceled");
  if (code === "interrupted") return t("ask.err.interrupted");
  if (code === "unavailable") return t("ask.err.unavailable");
  return t("ask.err.failed", { reason: cleanInline(err?.message || code) });
}

function renderAskBody(s, ui, t) {
  const ask = ensureAskState(s);
  const byPath = new Map((s.docs || []).map((d) => [d.path, d]));
  const sections = [];

  if (ask.showHistory) {
    if (ask.threads.length === 0) {
      sections.push(
        ui.card([ui.text(t("ask.historyEmpty"), { size: "small", tone: "muted" })], { key: "ask-history", variant: "soft" }),
      );
    } else {
      const thKey = makeKeyDeduper();
      sections.push(
        ui.card([
          ui.list(ask.threads, (th) => ui.row([
            ui.text(truncateUtf8Bytes(cleanInline(th.title || th.id), 160), {
              size: "small",
              weight: th.id === ask.activeThreadId ? "semibold" : "medium",
              grow: 1,
              onTap: ["openThread", th.id],
            }),
            ui.text(formatRelTime(t, th.updatedMs), { size: "small", tone: "faint" }),
            ui.button("", {
              key: safeKey("delth", th.id),
              icon: "trash-2",
              size: "small",
              variant: "ghost",
              onTap: ["deleteThread", th.id],
            }),
          ], { key: thKey("th", th.id), gap: 6 }), { gap: 4 }),
        ], { key: "ask-history", gap: 6 }),
      );
    }
  }

  const activeThread = ask.threads.find((th) => th.id === ask.activeThreadId);
  if (!activeThread || activeThread.turns.length === 0) {
    const suggestions = generateSuggestedQuestions(s.docs, t);
    const emptyChildren = [
      ui.text(t("ask.emptyTitle"), { weight: "semibold" }),
      ui.text(t("ask.emptyDesc"), { size: "small", tone: "muted" }),
    ];
    if (suggestions.length > 0) {
      emptyChildren.push(
        ui.wrap(
          suggestions.map((q, i) =>
            ui.chip(truncateUtf8Bytes(cleanInline(q), 120), {
              key: `sug-${i}`,
              variant: "soft",
              icon: "sparkles",
              onTap: ["askSuggested", q],
            }),
          ),
          { key: "ask-suggest", gap: 6 },
        ),
      );
    }
    sections.push(ui.column(emptyChildren, { key: "ask-empty", gap: 8 }));
    return sections;
  }

  // Pre-allocate thread-wide Markdown byte budget (newest turns first) so the
  // rendered UI tree stays strictly below uiblock's 32 KiB/node and 256 KiB/tree caps.
  const renderedMdByTurnId = new Map();
  const truncatedByTurnId = new Map();
  let remainingMdBudget = MAX_ASK_THREAD_MD_BYTES;
  for (let i = activeThread.turns.length - 1; i >= 0; i--) {
    const u = activeThread.turns[i];
    if (u.phase !== "done" || !u.answer) continue;
    const rewritten = rewriteWikiLinksForMarkdown(u.answer.markdown || "", "", s.docs);
    const nodeLimit = Math.min(MAX_ASK_MD_NODE_BYTES, remainingMdBudget);
    let mdText;
    let wasCut = false;
    if (nodeLimit < 32) {
      mdText = "…";
      wasCut = utf8ByteLength(rewritten) > 3;
    } else if (utf8ByteLength(rewritten) > nodeLimit) {
      mdText = cutUtf8Bytes(rewritten, nodeLimit);
      wasCut = true;
    } else {
      mdText = rewritten;
    }
    remainingMdBudget = Math.max(0, remainingMdBudget - utf8ByteLength(mdText));
    renderedMdByTurnId.set(u.id, mdText);
    truncatedByTurnId.set(u.id, Boolean(u.answer.truncated) || wasCut);
  }

  const turnKey = makeKeyDeduper();
  for (const turn of activeThread.turns) {
    const turnNodes = [];

    // 1. User question bubble (right-aligned)
    turnNodes.push(
      ui.row([
        ui.text("", { grow: 1 }),
        ui.card([ui.text(truncateUtf8Bytes(cleanInline(turn.q), MAX_TURN_Q_BYTES))], { variant: "soft", tone: "accent" }),
      ], { key: `turn-${turn.id}-q`, gap: 6 }),
    );

    // 2. Retrieval card (Stage 1 spinner + cancel or selected sources + why collapsible)
    const ret = turn.retrieval || {};
    if (turn.phase === "selecting") {
      turnNodes.push(
        ui.card([
          ui.row([
            ui.progress(0, {
              key: `turn-${turn.id}-s1`,
              variant: "spinner",
              text: cleanInline(t("ask.stage1", { n: ret.candidateCount || 0 })),
            }),
            ui.text("", { grow: 1 }),
            ui.button(t("action.cancel"), {
              key: `cancel-${turn.id}`,
              size: "small",
              variant: "ghost",
              onTap: ["cancelAsk", turn.id],
            }),
          ], { gap: 6 }),
        ], { key: `turn-${turn.id}-ret`, variant: "outline", gap: 6 }),
      );
    } else if (ret.total > 0 || (ret.selected && ret.selected.length > 0)) {
      const selected = ret.selected || [];
      const modeKey = ret.mode === "decision" || ret.mode === "all" ? ret.mode : "local";
      const retChildren = [
        ui.row([
          ui.icon("sparkles", { size: "small", tone: "accent" }),
          ui.text(
            cleanInline(
              t("ask.selected", {
                k: selected.length,
                total: ret.total || s.docs.length,
                ms: ret.ms || 0,
                mode: t(`ask.mode.${modeKey}`),
              }),
            ),
            { size: "small", tone: "muted", grow: 1 },
          ),
        ], { gap: 6 }),
      ];

      if (selected.length > 0) {
        retChildren.push(
          ui.wrap(
            selected.map((item, i) => {
              const isEssential = item.level === 3;
              return ui.chip(truncateUtf8Bytes(cleanInline(item.title || item.path), 48), {
                key: `ref-${i}`,
                variant: isEssential ? "solid" : "soft",
                tone: isEssential ? "accent" : "",
                icon: "file-text",
                onTap: ["openDoc", item.path],
              });
            }),
            { gap: 4 },
          ),
        );
      }

      const whyChildren = [];
      if (ret.fallbackReason) {
        whyChildren.push(
          ui.text(cleanInline(t("ask.fallbackNote", { reason: truncateUtf8Bytes(ret.fallbackReason, 120) })), {
            size: "small",
            tone: "warning",
          }),
        );
      }
      const rows = ret.rows || [];
      if (rows.length > 0) {
        const tableRows = rows.map((r, i) => ui.row([
          ui.text(truncateUtf8Bytes(cleanInline(r.title || r.path), 40), { size: "small", grow: 1 }),
          ui.text(formatScore(r.local), { size: "small", tone: "muted" }),
          ui.text(
            r.decision !== null && r.decision !== undefined
              ? cleanInline(r.levelLabel || formatScore(r.decision))
              : "—",
            { size: "small", tone: r.level === 3 ? "accent" : "muted" },
          ),
          ui.text(formatScore(r.final), { size: "small", weight: "medium" }),
        ], { key: `wr-${i}`, gap: 6 }));
        whyChildren.push(
          ui.table([t("ask.colDoc"), t("ask.colLocal"), t("ask.colDecision"), t("ask.colFinal")], tableRows),
        );
      }
      if (ret.entry) {
        const entryDoc = byPath.get(ret.entry);
        whyChildren.push(
          ui.text(truncateUtf8Bytes(cleanInline(t("ask.entry", { title: entryDoc ? entryDoc.title : ret.entry })), 256), {
            size: "small",
            tone: "muted",
          }),
        );
      }
      if (ret.subgraph) {
        whyChildren.push(ui.code(cutUtf8Bytes(ret.subgraph, MAX_SUBGRAPH_BYTES), "text"));
      }

      if (whyChildren.length > 0) {
        retChildren.push(
          ui.collapsible(t("ask.why"), whyChildren, {
            key: `turn-${turn.id}-why`,
            icon: "info",
            tone: "muted",
            checked: Boolean(ask.whyOpen && ask.whyOpen[turn.id]),
            onChange: ["toggleWhy", turn.id],
          }),
        );
      }

      turnNodes.push(ui.card(retChildren, { key: `turn-${turn.id}-ret`, variant: "outline", gap: 6 }));
    }

    // 3. Answer card (Consent / Stage 2 spinner / Done / Error)
    if (turn.phase === "consent") {
      turnNodes.push(
        ui.card([
          ui.row([
            ui.progress(0, { key: `turn-${turn.id}-cons`, variant: "spinner", text: cleanInline(t("ask.consent")) }),
            ui.text("", { grow: 1 }),
            ui.button(t("action.cancel"), {
              key: `cancel-${turn.id}`,
              size: "small",
              variant: "ghost",
              onTap: ["cancelAsk", turn.id],
            }),
          ], { gap: 6 }),
        ], { key: `turn-${turn.id}-ans`, gap: 6 }),
      );
    } else if (turn.phase === "answering") {
      turnNodes.push(
        ui.card([
          ui.row([
            ui.progress(0, {
              key: `turn-${turn.id}-s2`,
              variant: "spinner",
              text: cleanInline(t("ask.stage2", { agent: ask.assigned?.label || "" })),
            }),
            ui.text("", { grow: 1 }),
            ui.button(t("action.cancel"), {
              key: `cancel-${turn.id}`,
              size: "small",
              variant: "ghost",
              onTap: ["cancelAsk", turn.id],
            }),
          ], { gap: 6 }),
        ], { key: `turn-${turn.id}-ans`, gap: 6 }),
      );
    } else if (turn.phase === "done" && turn.answer) {
      const ans = turn.answer;
      const renderedMd = renderedMdByTurnId.get(turn.id) ?? "";
      const isTruncated = Boolean(truncatedByTurnId.get(turn.id));
      const ansChildren = [
        ui.markdown(renderedMd, { key: `turn-${turn.id}-md`, onLink: ["openLink"] }),
      ];

      if (Array.isArray(ans.citations) && ans.citations.length > 0) {
        ansChildren.push(
          ui.wrap(
            ans.citations.map((p, i) => {
              const d = byPath.get(p);
              return ui.chip(truncateUtf8Bytes(cleanInline(d ? d.title : p), 48), {
                key: `cit-${i}`,
                variant: "outline",
                icon: "file-text",
                onTap: ["openDoc", p],
              });
            }),
            { gap: 4 },
          ),
        );
      }

      const conf = ans.confidence === "high" || ans.confidence === "low" ? ans.confidence : "medium";
      const secs = (Number(ans.ms || 0) / 1000).toFixed(1);
      const metaRow = [
        ui.pill(t(`ask.confidence.${conf}`), {
          variant: "soft",
          tone: conf === "high" ? "success" : (conf === "low" ? "warning" : ""),
        }),
        ui.text(truncateUtf8Bytes(cleanInline(`${ans.agentLabel || ask.assigned?.label || ""} · ${secs}s`), 160), {
          size: "small",
          tone: "faint",
          grow: 1,
        }),
      ];
      if (isTruncated) {
        metaRow.push(ui.pill(t("ask.truncated"), { variant: "soft", tone: "warning" }));
      }
      ansChildren.push(ui.row(metaRow, { gap: 6 }));

      if (Array.isArray(ans.followUps) && ans.followUps.length > 0) {
        ansChildren.push(
          ui.wrap(
            ans.followUps.slice(0, 3).map((f, i) =>
              ui.chip(truncateUtf8Bytes(cleanInline(f), 120), {
                key: `fu-${i}`,
                variant: "soft",
                icon: "sparkles",
                onTap: ["askFollowUp", f],
              }),
            ),
            { gap: 4 },
          ),
        );
      }

      if (Array.isArray(ans.missing) && ans.missing.length > 0) {
        ansChildren.push(
          ui.wrap(
            ans.missing.slice(0, 4).map((m, i) =>
              ui.chip(truncateUtf8Bytes(cleanInline(t("ask.createMissing", { title: m })), 96), {
                key: `miss-${i}`,
                variant: "outline",
                icon: "plus",
                onTap: ["createFromLink", m],
              }),
            ),
            { gap: 4 },
          ),
        );
      }

      turnNodes.push(ui.card(ansChildren, { key: `turn-${turn.id}-ans`, gap: 6 }));
    } else if (turn.phase === "error" || turn.phase === "canceled") {
      turnNodes.push(
        ui.card([
          ui.text(truncateUtf8Bytes(cleanInline(renderAskErrorText(turn.error, t)), 4096), { size: "small", tone: "danger" }),
          ui.row([
            ui.button(t("ask.retry"), {
              key: `retry-${turn.id}`,
              icon: "refresh-cw",
              size: "small",
              variant: "outline",
              onTap: ["retryTurn", turn.id],
            }),
          ], { gap: 6 }),
        ], { key: `turn-${turn.id}-ans`, variant: "danger", gap: 6 }),
      );
    }

    sections.push(ui.column(turnNodes, { key: turnKey("turn", turn.id), gap: 6 }));
  }

  return sections;
}

function renderAskComposer(s, ui, t) {
  const ask = ensureAskState(s);
  const assigned = Boolean(ask.assigned);
  return ui.row([
    ui.input("", {
      key: `ask-input-${ask.inputSeq || 0}`,
      placeholder: assigned ? t("ask.placeholder") : t("ask.placeholderAssign"),
      grow: 1,
      disabled: !assigned || ask.busy,
      onSubmit: ["submitAsk"],
    }),
    ui.button("", {
      key: "ask-send",
      icon: "send-horizontal",
      size: "small",
      variant: "primary",
      tone: "accent",
      disabled: !assigned || ask.busy,
      onTap: ["submitAsk"],
    }),
  ], { key: "ask-composer", gap: 6 });
}

function readComposerValue(vals, seq) {
  const key = `ask-input-${seq}`;
  if (Object.prototype.hasOwnProperty.call(vals, key)) return String(vals[key] ?? "");
  const suffix = `/${key}`;
  for (const [k, v] of Object.entries(vals || {})) {
    if (k.endsWith(suffix)) return String(v ?? "");
  }
  return "";
}

export default {
  activate(m) {
    mako = m;
    registerOKFi18n(mako);

    mako.blocks.register("vault", {
      state: {
        tab: "browse",
        readerFrom: "browse",
        editorOpen: false,
        editorFrom: "browse",
        query: "",
        typeFilter: "All",
        statusFilter: "all",
        sortBy: "relevance",
        tagFilter: "",
        showFilters: false,
        selectedPath: "",
        confirmDeletePath: "",
        metaExpanded: false,
        draft: null,
        docs: [],
        dirs: [],
        collapsedDirs: {},
        treeSelectedDir: "",
        treeRecentOpen: false,
        showNewFolder: false,
        newFolderParent: "",
        newFolderDraft: "",
        graphFocusPath: "",
        insightsOpen: false,
        hasIndex: true,
        totalLinks: 0,
        broken: [],
        missingMeta: [],
        needsReview: [],
        orphans: [],
        issueCount: 0,
        usedBytes: 0,
        quotaBytes: 0,
        fileCount: 0,
        error: "",
        ask: defaultAskState(),
      },
      async load(s, ctx) {
        await refreshState(s, ctx, true);
        await loadAskMeta(s, ctx);
      },
      render(s, ui, ctx) {
        const t = getCtxT(ctx);
        const browseLabel = t("tab.browse");
        const treeLabel = t("tab.tree");
        const graphLabel = t("tab.graph");
        const healthLabel = s.issueCount > 0 ? t("tab.healthCount", { count: s.issueCount }) : t("tab.health");
        const askLabel = t("tab.ask");

        const tabOptions = [browseLabel, treeLabel, graphLabel, healthLabel, askLabel];

        let activeTab = browseLabel;
        if (s.tab === "tree") activeTab = treeLabel;
        else if (s.tab === "graph") activeTab = graphLabel;
        else if (s.tab === "health") activeTab = healthLabel;
        else if (s.tab === "ask") activeTab = askLabel;

        const selectedDoc = s.selectedPath ? s.docs.find((d) => d.path === s.selectedPath) : null;
        const isEditor = Boolean(s.editorOpen && s.draft);
        const isReader = !isEditor && Boolean(selectedDoc);

        let ctxBar = null;
        let bodyChildren = [];
        let scrollVariant = undefined;

        if (isEditor) {
          ctxBar = renderEditorBar(s, ui, t);
          bodyChildren = renderEditorBody(s, ui, t);
        } else if (isReader) {
          ctxBar = renderReaderBar(s, ui, t, selectedDoc);
          bodyChildren = renderReaderBody(s, ui, t, selectedDoc);
        } else if (s.tab === "tree") {
          const tree = buildFolderTree(s.docs, s.dirs);
          const validDirs = allDirPaths(tree);
          ctxBar = renderTreeBar(s, ui, t, validDirs.length, validDirs);
          bodyChildren = renderTreeBody(s, ui, t, tree);
        } else if (s.tab === "graph") {
          const gctx = computeGraphContext(s, t);
          ctxBar = renderGraphBar(s, ui, t, gctx);
          bodyChildren = renderGraphBody(s, ui, t, gctx);
        } else if (s.tab === "health") {
          ctxBar = renderHealthBar(s, ui, t);
          bodyChildren = renderHealthBody(s, ui, t);
        } else if (s.tab === "ask") {
          ctxBar = renderAskBar(s, ui, t);
          bodyChildren = renderAskBody(s, ui, t);
          scrollVariant = "follow";
        } else {
          ctxBar = renderBrowseBar(s, ui, t);
          bodyChildren = renderBrowseBody(s, ui, t);
        }

        const scrollChildren = [];
        if (s.error) {
          scrollChildren.push(
            ui.card([ui.text(cleanInline(s.error), { tone: "danger", size: "small" })], { key: "error-card", variant: "danger" }),
          );
        }
        scrollChildren.push(...bodyChildren);

        const footer = !isEditor && !isReader && s.tab === "ask"
          ? renderAskComposer(s, ui, t)
          : renderStatusBar(s, ui, t);

        const rootChildren = [renderTopBar(s, ui, t, activeTab, tabOptions)];
        if (ctxBar) rootChildren.push(ctxBar);
        const scrollOpts = { key: "main-scroll", grow: 1, gap: 8 };
        if (scrollVariant) scrollOpts.variant = scrollVariant;
        rootChildren.push(ui.scroll(scrollChildren, scrollOpts));
        rootChildren.push(footer);

        return ui.column(rootChildren, { gap: 6, fill: true });
      },
      on: {
        async setTab(s, value) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const v = String(value || "").trim();
          const healthPrefix = t("tab.health");
          if (v === t("tab.tree") || v === "Tree" || v === "tree") s.tab = "tree";
          else if (v === t("tab.graph") || v === "Graph" || v === "graph") s.tab = "graph";
          else if (v === healthPrefix || v.startsWith(`${healthPrefix} (`) || v.startsWith("Health") || v === "health") s.tab = "health";
          else if (v === t("tab.ask") || v === "Ask" || v === "ask") s.tab = "ask";
          else s.tab = "browse";
          // Close overlays when switching tabs; keep any unsaved draft for resume.
          s.selectedPath = "";
          s.editorOpen = false;
          s.confirmDeletePath = "";
          if (s.tab === "ask") {
            await ensureAskLoaded(s, ctx, false);
          }
        },
        setQuery(s, value) {
          s.query = String(value ?? "");
        },
        toggleFilters(s) {
          s.showFilters = !s.showFilters;
        },
        setTypeFilter(s, value) {
          const ctx = mako.blocks.context("vault");
          s.typeFilter = canonicalTypeFilter(value, getCtxT(ctx));
        },
        setStatusFilter(s, value) {
          const v = String(value || "all").trim();
          s.statusFilter = STATUS_FILTERS.includes(v) ? v : "all";
        },
        setSort(s, value) {
          const ctx = mako.blocks.context("vault");
          s.sortBy = canonicalSort(value, getCtxT(ctx));
        },
        setTagFilter(s, tag) {
          s.tagFilter = String(tag || "");
        },
        filterTagAndClose(s, tag) {
          s.tagFilter = String(tag || "");
          s.selectedPath = "";
          s.editorOpen = false;
          s.tab = "browse";
        },
        openDoc(s, p) {
          if (!s.selectedPath) {
            s.readerFrom = s.tab || "browse";
          }
          s.selectedPath = String(p || "");
          s.editorOpen = false;
          s.confirmDeletePath = "";
        },
        closeDoc(s) {
          s.selectedPath = "";
          s.confirmDeletePath = "";
          if (s.readerFrom) {
            s.tab = s.readerFrom;
          }
        },
        setMetaExpanded(s, expanded) {
          s.metaExpanded = Boolean(expanded);
        },
        onBreadcrumb(s, docPath, indexStr) {
          const idx = Number(indexStr);
          if (!Number.isFinite(idx) || idx < 0) return;
          const segments = String(docPath || "").split("/").filter(Boolean);
          if (idx === 0) {
            s.selectedPath = "";
            s.editorOpen = false;
            s.confirmDeletePath = "";
            s.tab = "browse";
            return;
          }
          if (idx < segments.length) {
            const nextCollapsed = { ...(s.collapsedDirs || {}) };
            let acc = "";
            for (let i = 0; i < idx; i++) {
              acc = acc ? `${acc}/${segments[i]}` : segments[i];
              nextCollapsed[acc] = false;
            }
            s.collapsedDirs = nextCollapsed;
            s.treeSelectedDir = acc;
            s.selectedPath = "";
            s.editorOpen = false;
            s.confirmDeletePath = "";
            s.tab = "tree";
          }
        },
        async openLink(s, rawHref) {
          const href = String(rawHref || "").trim();
          if (!href || /^https?:\/\//i.test(href)) return;
          let cleaned = href.replace(/^okf:/i, "").split("#")[0].trim();
          try {
            cleaned = decodeURIComponent(cleaned);
          } catch {}
          if (!cleaned) return;
          const resolved = resolveDocLink(s.docs, s.selectedPath, cleaned);
          if (resolved) {
            if (!s.selectedPath) {
              s.readerFrom = s.tab || "browse";
            }
            s.selectedPath = resolved;
            s.editorOpen = false;
            s.confirmDeletePath = "";
            return;
          }
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const folder = await defaultFolder(ctx);
          const normPath = normalizeDocPath(cleaned, folder);
          if (!normPath || isHiddenPath(cleaned) || isHiddenPath(normPath)) {
            s.error = t("error.hiddenPath");
            return;
          }
          const stem = normPath.replace(/\.md$/, "").split("/").pop() || cleaned;
          const prettyTitle = stem.replace(/[-_]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
          s.editorFrom = s.selectedPath ? "reader" : (s.tab || "browse");
          s.draft = emptyDraft(folder, normPath, prettyTitle);
          s.editorOpen = true;
          s.error = "";
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.creatingMissingDoc", { path: normPath }) });
        },
        openInGraph(s, p) {
          s.selectedPath = "";
          s.editorOpen = false;
          s.graphFocusPath = String(p || "");
          s.tab = "graph";
        },
        setGraphFocus(s, id) {
          s.graphFocusPath = String(id || "");
        },
        toggleInsights(s, open) {
          s.insightsOpen = typeof open === "boolean" ? open : !s.insightsOpen;
        },
        toggleDir(s, dirPath, expanded) {
          const k = String(dirPath || "");
          if (!k) return;
          const nextCollapsed = typeof expanded === "boolean" ? !expanded : !s.collapsedDirs[k];
          s.collapsedDirs = { ...s.collapsedDirs, [k]: nextCollapsed };
        },
        selectDir(s, dirPath) {
          const next = String(dirPath || "");
          s.treeSelectedDir = s.treeSelectedDir === next ? "" : next;
        },
        toggleTreeRecent(s, open) {
          s.treeRecentOpen = typeof open === "boolean" ? open : !s.treeRecentOpen;
        },
        expandAllDirs(s) {
          s.collapsedDirs = {};
        },
        collapseAllDirs(s) {
          const tree = buildFolderTree(s.docs, s.dirs);
          const next = {};
          for (const p of allDirPaths(tree)) next[p] = true;
          s.collapsedDirs = next;
        },
        toggleNewFolder(s) {
          s.showNewFolder = !s.showNewFolder;
          s.newFolderParent = "";
          s.newFolderDraft = "";
          s.error = "";
        },
        newFolderIn(s, parentDir) {
          s.showNewFolder = true;
          s.newFolderParent = String(parentDir || "").replace(/^\/+|\/+$/g, "");
          s.newFolderDraft = "";
          s.error = "";
        },
        setNewFolderDraft(s, value) {
          s.newFolderDraft = String(value ?? "");
        },
        async createFolder(s) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const vals = mako.blocks.values("vault");
          const raw = String(vals["tree-mkdir-input"] ?? s.newFolderDraft ?? "").trim();
          const combined = s.newFolderParent && raw && !raw.startsWith(`${s.newFolderParent}/`)
            ? `${s.newFolderParent}/${raw}`
            : raw;
          const clean = normalizeDirPath(combined);
          if (!clean) {
            s.error = t("error.folderRequired");
            return;
          }
          if (isHiddenPath(combined) || isHiddenPath(clean)) {
            s.error = t("error.hiddenPath");
            return;
          }
          await ctx.datafs.mkdir(clean);
          s.showNewFolder = false;
          s.newFolderParent = "";
          s.newFolderDraft = "";
          s.collapsedDirs = { ...s.collapsedDirs, [clean]: false };
          s.treeSelectedDir = clean;
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.createdFolder", { path: clean }) });
        },
        newDocInFolder(s, dirPath) {
          const folder = String(dirPath || "concepts").replace(/^\/+|\/+$/g, "");
          s.editorFrom = s.tab || "tree";
          s.draft = emptyDraft(folder, `${folder}/`);
          s.editorOpen = true;
          s.error = "";
        },
        async moveDocToFolder(s, targetFolder, droppedPath) {
          const srcPath = String(droppedPath || "").trim();
          if (!srcPath || srcPath === "index.md") return;
          const fileName = srcPath.split("/").pop();
          if (!fileName) return;
          const destFolder = String(targetFolder || "").replace(/^\/+|\/+$/g, "");
          const destPath = destFolder ? `${destFolder}/${fileName}` : fileName;
          if (destPath === srcPath) return;
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          if (isHiddenPath(srcPath) || isHiddenPath(destFolder) || isHiddenPath(destPath)) {
            s.error = t("error.hiddenPath");
            return;
          }
          await ctx.datafs.move(srcPath, destPath);
          if (s.selectedPath === srcPath) s.selectedPath = destPath;
          if (s.graphFocusPath === srcPath) s.graphFocusPath = destPath;
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.movedDoc", { path: destPath }) });
        },
        async newDoc(s) {
          const ctx = mako.blocks.context("vault");
          const folder = await defaultFolder(ctx);
          s.editorFrom = s.selectedPath ? "reader" : (s.tab || "browse");
          s.draft = emptyDraft(folder);
          s.editorOpen = true;
          s.error = "";
        },
        editDoc(s, p) {
          const doc = s.docs.find((d) => d.path === p);
          if (!doc) return;
          s.editorFrom = s.selectedPath === p ? "reader" : (s.tab || "browse");
          s.draft = draftFromDoc(doc);
          s.editorOpen = true;
          s.error = "";
        },
        resumeDraft(s) {
          if (!s.draft) return;
          s.editorOpen = true;
          s.error = "";
        },
        async createFromLink(s, target) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const folder = await defaultFolder(ctx);
          const normPath = normalizeDocPath(target, folder);
          if (!normPath || isHiddenPath(target) || isHiddenPath(normPath)) {
            s.error = t("error.hiddenPath");
            return;
          }
          const stem = normPath.replace(/\.md$/, "").split("/").pop() || target;
          const prettyTitle = stem.replace(/[-_]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
          s.editorFrom = s.selectedPath ? "reader" : (s.tab || "browse");
          s.draft = emptyDraft(folder, normPath, prettyTitle);
          s.editorOpen = true;
          s.error = "";
        },
        cancelEdit(s) {
          const from = s.editorFrom || s.tab || "browse";
          s.draft = null;
          s.editorOpen = false;
          s.error = "";
          if (from === "reader" && s.selectedPath && s.docs.some((d) => d.path === s.selectedPath)) {
            // Return to Reader overlay cleanly.
            return;
          }
          s.selectedPath = "";
          s.tab = from === "reader" ? (s.readerFrom || "browse") : from;
        },
        draftField(s, field, value) {
          if (!s.draft) return;
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          if (field === "type") {
            s.draft.type = canonicalDocType(value, t);
          } else if (field === "status") {
            s.draft.status = canonicalDocStatus(value, t);
          } else {
            s.draft[field] = String(value ?? "");
          }
        },
        async saveDraft(s) {
          if (!s.draft) return;
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const vals = mako.blocks.values("vault");
          const d = {
            ...s.draft,
            title: vals["ed-title"] ?? s.draft.title,
            type: canonicalDocType(vals["ed-type"] ?? s.draft.type, t),
            status: canonicalDocStatus(vals["ed-status"] ?? s.draft.status, t),
            path: vals["ed-path"] ?? s.draft.path,
            tags: vals["ed-tags"] ?? s.draft.tags,
            description: vals["ed-desc"] ?? s.draft.description,
            source: vals["ed-source"] ?? s.draft.source,
            verifiedBy: vals["ed-verified"] ?? s.draft.verifiedBy,
            body: vals["ed-body"] ?? s.draft.body,
          };
          const title = String(d.title || "").trim();
          if (!title) {
            s.error = t("error.titleRequired");
            return;
          }
          const folder = d.folder || (await defaultFolder(ctx));
          const rawPath = String(d.path || "").trim();
          const candidatePath = !rawPath || rawPath.endsWith("/")
            ? `${(rawPath || folder).replace(/\/+$/, "")}/${slugify(title) || "note"}.md`
            : rawPath;
          const targetPath = normalizeDocPath(candidatePath, folder);
          if (!targetPath) {
            s.error = t("error.pathRequired");
            return;
          }
          if (isHiddenPath(rawPath) || isHiddenPath(targetPath) || (d.originalPath && isHiddenPath(d.originalPath))) {
            s.error = t("error.hiddenPath");
            return;
          }

          const content = serializeDoc({
            type: d.type,
            title,
            description: d.description,
            status: d.status,
            updated: todayISO(),
            tags: parseTags(d.tags),
            source: d.source,
            verifiedBy: d.verifiedBy,
            body: d.body,
          });

          if (d.originalPath && d.originalPath !== targetPath) {
            await ctx.datafs.move(d.originalPath, targetPath);
          }
          await ctx.datafs.write(targetPath, content);
          await syncIndexFile(ctx);
          s.draft = null;
          s.editorOpen = false;
          s.selectedPath = targetPath;
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.savedDoc", { path: targetPath }) });
        },
        async setDocStatus(s, p, nextStatus) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const doc = s.docs.find((d) => d.path === p);
          if (!doc) return;
          const content = serializeDoc({
            okfVersion: doc.okfVersion,
            type: doc.type,
            title: doc.title,
            description: doc.description,
            status: nextStatus,
            updated: todayISO(),
            tags: doc.tags,
            source: doc.source,
            verifiedBy: doc.verifiedBy,
            body: doc.body,
          });
          await ctx.datafs.write(doc.path, content);
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({
            id: "mako.toast",
            arg: t("toast.statusUpdated", { status: labelStatus(t, nextStatus) }),
          });
        },
        askDelete(s, p) {
          s.confirmDeletePath = String(p || "");
        },
        async deleteDoc(s, p) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const target = String(p || "");
          if (isHiddenPath(target)) {
            s.error = t("error.hiddenPath");
            return;
          }
          await ctx.datafs.delete(target);
          s.confirmDeletePath = "";
          if (s.selectedPath === p) s.selectedPath = "";
          if (s.graphFocusPath === p) s.graphFocusPath = "";
          await syncIndexFile(ctx);
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.deletedDoc", { path: p }) });
        },
        async rebuildIndex(s) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const { index } = await loadVaultDocs(ctx, false);
          await ctx.datafs.write("index.md", rebuildIndexMarkdown(ctx.projectName, index.docs));
          await refreshState(s, ctx, false);
          await mako.commands.runCommand({ id: "mako.toast", arg: t("toast.rebuiltIndex") });
        },

        // --- Ask tab & AI exploration handlers ---

        async refreshAgents(s) {
          const ctx = mako.blocks.context("vault");
          await ensureAskLoaded(s, ctx, true);
        },
        toggleAgentPicker(s) {
          const ask = ensureAskState(s);
          ask.showPicker = !ask.showPicker;
        },
        async assignAgent(s, selectedLabel) {
          const ctx = mako.blocks.context("vault");
          const ask = ensureAskState(s);
          const target = String(selectedLabel || "").trim();
          const match = (ask.agents || []).find((a) => a.askable && (a.label === target || a.agent === target));
          if (!match) return;
          ask.assigned = {
            agent: match.agent,
            profile: match.profile || "",
            label: match.label,
            assignedAt: todayISO(),
          };
          ask.showPicker = false;
          await ctx.datafs.write(ASSISTANT_META_PATH, JSON.stringify(ask.assigned, null, 2));
        },
        toggleHistory(s) {
          const ask = ensureAskState(s);
          ask.showHistory = !ask.showHistory;
        },
        newThread(s) {
          const ask = ensureAskState(s);
          if (ask.busy) return;
          ask.activeThreadId = "";
          ask.showHistory = false;
        },
        openThread(s, threadId) {
          const ask = ensureAskState(s);
          if (ask.busy) return;
          ask.activeThreadId = String(threadId || "");
          ask.showHistory = false;
        },
        async deleteThread(s, threadId) {
          const ctx = mako.blocks.context("vault");
          const ask = ensureAskState(s);
          const id = String(threadId || "");
          if (ask.busy && ask.activeThreadId === id) return;
          ask.threads = ask.threads.filter((th) => th.id !== id);
          if (ask.activeThreadId === id) {
            ask.activeThreadId = ask.threads[0]?.id || "";
          }
          await persistAskHistory(ctx.scopeKey, ask.threads);
        },
        toggleWhy(s, turnId, open) {
          const ask = ensureAskState(s);
          const id = String(turnId || "");
          if (!id) return;
          const next = typeof open === "boolean" ? open : !ask.whyOpen[id];
          ask.whyOpen = { ...(ask.whyOpen || {}), [id]: next };
        },
        async submitAsk(s, value) {
          const ctx = mako.blocks.context("vault");
          const ask = ensureAskState(s);
          const vals = mako.blocks.values("vault");
          const text = typeof value === "string" && value.trim()
            ? value.trim()
            : readComposerValue(vals, ask.inputSeq || 0).trim();
          if (!text || !ask.assigned || ask.busy) return;
          ask.inputSeq = (ask.inputSeq || 0) + 1;
          await runAskQuestion(s, ctx, text);
        },
        async askSuggested(s, q) {
          const ctx = mako.blocks.context("vault");
          const ask = ensureAskState(s);
          if (!ask.assigned) {
            ask.showPicker = true;
            return;
          }
          await runAskQuestion(s, ctx, q);
        },
        async askFollowUp(s, q) {
          const ctx = mako.blocks.context("vault");
          await runAskQuestion(s, ctx, q);
        },
        async askFromSearch(s) {
          const ctx = mako.blocks.context("vault");
          const q = String(s.query || "").trim();
          s.selectedPath = "";
          s.editorOpen = false;
          s.tab = "ask";
          await ensureAskLoaded(s, ctx, false);
          const ask = ensureAskState(s);
          if (q && ask.assigned && !ask.busy) {
            await runAskQuestion(s, ctx, q);
          }
        },
        async askAboutDoc(s, docPath) {
          const ctx = mako.blocks.context("vault");
          const t = getCtxT(ctx);
          const doc = s.docs.find((d) => d.path === docPath);
          s.selectedPath = "";
          s.editorOpen = false;
          s.tab = "ask";
          await ensureAskLoaded(s, ctx, false);
          const ask = ensureAskState(s);
          ask.pinnedPath = String(docPath || "");
          if (doc && ask.assigned && !ask.busy) {
            const q = t("ask.suggest.hub", { title: doc.title });
            await runAskQuestion(s, ctx, q, { pinned: [doc.path] });
          }
        },
        async cancelAsk(s, turnId) {
          const ctx = mako.blocks.context("vault");
          const ask = ensureAskState(s);
          let thread = ask.threads.find((th) => th.id === ask.activeThreadId);
          let turn = thread?.turns?.find((u) => u.id === turnId);
          if (!turn) {
            for (const th of ask.threads || []) {
              const found = th.turns?.find((u) => u.id === turnId);
              if (found) {
                thread = th;
                turn = found;
                break;
              }
            }
          }
          if (!turn) return;
          if (turn.askReq) {
            try {
              await mako.agents.cancel(turn.askReq);
            } catch {}
          }
          turn.phase = "canceled";
          turn.error = { code: "canceled", message: "" };
          if (turn.retrieval) turn.retrieval.candidates = [];
          recomputeAskBusy(ask);
          await persistAskHistory(ctx.scopeKey, ask.threads);
        },
        async retryTurn(s, turnId) {
          const ctx = mako.blocks.context("vault");
          const ask = ensureAskState(s);
          const thread = ask.threads.find((th) => th.id === ask.activeThreadId);
          const turn = thread?.turns?.find((u) => u.id === turnId);
          if (!turn || ask.busy || !ask.assigned) return;
          await runAskQuestion(s, ctx, turn.q, { replaceTurnId: turn.id, pinned: turn.pinned });
        },
      },
    });

    mako.commands.register("rebuild-index", async (ctx) => {
      if (!ctx || !ctx.scopeKey) return;
      const { index } = await loadVaultDocs(ctx, false);
      await ctx.datafs.write("index.md", rebuildIndexMarkdown(ctx.projectName, index.docs));
      mako.blocks.reload("vault", ctx.scopeKey);
    });

    // --- Project-scoped tools for AI agents (mako-plugin CLI) ---

    mako.tools.register("list_docs", async (args, ctx) => {
      const { index, stats } = await loadVaultDocs(ctx, false);
      const tf = String(args?.type || "").trim().toLowerCase();
      const sf = String(args?.status || "").trim().toLowerCase();
      const tgf = String(args?.tag || "").trim().toLowerCase();
      const docs = index.docs
        .filter((d) => (!tf || d.type.toLowerCase() === tf))
        .filter((d) => (!sf || d.status.toLowerCase() === sf))
        .filter((d) => (!tgf || d.tags.some((t) => t.toLowerCase() === tgf)))
        .map((d) => ({
          path: d.path,
          type: d.type,
          title: d.title,
          description: d.description,
          status: d.status,
          updated: d.updated,
          tags: d.tags,
          links: d.links.length,
          backlinks: d.backlinks.length,
        }));
      return { project: ctx.projectName, count: docs.length, usedBytes: stats.usedBytes, docs };
    });

    mako.tools.register("search", async (args, ctx) => {
      const query = String(args?.query || "").trim();
      if (!query) throw new Error("query is required");
      const { index } = await loadVaultDocs(ctx, false);
      const hits = searchDocuments(index.docs, query, args?.type || "", args?.tag || "");
      return {
        project: ctx.projectName,
        query,
        count: hits.length,
        results: hits.slice(0, 20).map(({ doc, score, snippet }) => ({
          path: doc.path,
          type: doc.type,
          title: doc.title,
          status: doc.status,
          tags: doc.tags,
          score,
          snippet,
        })),
      };
    });

    mako.tools.register("explore", async (args, ctx) => {
      const query = String(args?.query || "").trim();
      if (!query) throw new Error("query is required");
      const maxDocs = clampNumber(args?.max_docs, 1, 12, 5);
      const wantDecision = String(args?.use_decision ?? "true").trim().toLowerCase() !== "false";

      const { index } = await loadVaultDocs(ctx, false);
      const docs = index.docs;
      const byPath = new Map(docs.map((d) => [d.path, d]));
      const idx = getRetrievalIndex(ctx.scopeKey, docs);
      let cands = rankDocuments(idx, docs, query, { limit: 12 });
      if (cands.length < 3 && docs.length > cands.length) {
        const have = new Set(cands.map((c) => c.path));
        const fillers = [...docs]
          .filter((d) => !have.has(d.path))
          .sort((a, b) => (idx.centrality.get(b.path) || 0) - (idx.centrality.get(a.path) || 0) || a.path.localeCompare(b.path))
          .slice(0, Math.min(3, docs.length) - cands.length);
        for (const d of fillers) {
          cands.push({ path: d.path, doc: d, local: 0.1, raw: 0, matched: [], snippet: d.description || "" });
        }
      }

      const smallVault = docs.length <= maxDocs;
      let mode = smallVault ? "all" : "local";
      let decisionInfo = null;
      let ranked = cands.map((c) => ({ ...c, decision: null, level: null, levelLabel: "", final: c.local }));
      let entry = ranked[0]?.path || "";
      let needsMore = false;
      let notCovered = false;

      if (wantDecision && !smallVault && cands.length > 0) {
        let canCall = false;
        try {
          const st = await mako.decision.status();
          canCall = Boolean(st && st.available);
        } catch {}
        if (canCall) {
          const req = buildDecisionRequest(query, "", cands);
          const ev = await mako.decision.tryEvaluate(req, { timeoutMs: 15000 });
          if (ev.ok && ev.result) {
            const applied = applyDecision(cands, ev.result);
            ranked = applied.ranked;
            entry = applied.entry;
            needsMore = applied.needsMore;
            notCovered = applied.notCovered;
            mode = "decision";
            decisionInfo = {
              provider: ev.result.provider || "",
              model: ev.result.model || "",
              elapsedMs: Number(ev.result.elapsedMs || 0),
            };
          }
        }
      }

      const selected = mode === "all"
        ? ranked.slice(0, maxDocs)
        : selectContext(ranked, { maxDocs, minLevel: 2, notCovered });
      const selectedPaths = selected.map((x) => x.path);
      const effectiveEntry = entry || selectedPaths[0] || "";
      const focusPaths = [effectiveEntry, ...selectedPaths.filter((p) => p !== effectiveEntry)].filter(Boolean).slice(0, 3);
      const neighbors = expandNeighbors(selectedPaths, docs, needsMore ? 10 : 6, focusPaths);
      const edges = subgraphLines(selectedPaths, docs);
      const subgraph = effectiveEntry && docs.length > 0 ? renderAsciiGraph(docs, effectiveEntry, "focus") : "";

      return {
        query,
        mode,
        decision: decisionInfo,
        entry: effectiveEntry,
        results: selected.map((r) => {
          const doc = byPath.get(r.path) || r.doc;
          return {
            path: r.path,
            title: doc?.title || r.path,
            type: doc?.type || "Concept",
            status: doc?.status || "active",
            local: r.local,
            decision: r.decision,
            final: r.final,
            level: r.levelLabel || "",
            snippet: r.snippet || doc?.description || "",
            headings: extractHeadings(doc?.body || "", 8),
          };
        }),
        neighbors: neighbors.map((nb) => ({
          path: nb.path,
          title: nb.doc?.title || nb.path,
          relation: nb.relation,
        })),
        subgraph,
        edges,
        needsMore,
      };
    });

    mako.tools.register("read_doc", async (args, ctx) => {
      const target = normalizeDocPath(args?.path, "");
      if (!target) throw new Error("path is required");
      if (isHiddenPath(args?.path) || isHiddenPath(target)) {
        return { found: false, path: target };
      }
      const { index } = await loadVaultDocs(ctx, false);
      const doc = index.docs.find((d) => d.path === target);
      if (!doc) return { found: false, path: target };
      return {
        found: true,
        path: doc.path,
        type: doc.type,
        title: doc.title,
        description: doc.description,
        status: doc.status,
        updated: doc.updated,
        tags: doc.tags,
        provenance: { source: doc.source, verified_by: doc.verifiedBy },
        links: doc.links,
        backlinks: doc.backlinks,
        brokenLinks: doc.brokenLinks,
        body: doc.body,
      };
    });

    mako.tools.register("upsert_doc", async (args, ctx) => {
      const title = String(args?.title || "").trim();
      if (!title) throw new Error("title is required");
      const folder = await defaultFolder(ctx);
      const target = normalizeDocPath(args?.path, folder);
      if (!target) throw new Error("path is required");
      if (isHiddenPath(args?.path) || isHiddenPath(target)) {
        throw new Error("hidden paths (starting with '.') are reserved and cannot be written");
      }
      const content = serializeDoc({
        type: DOC_TYPES.includes(args?.type) ? args.type : "Concept",
        title,
        description: args?.description || "",
        status: DOC_STATUSES.includes(args?.status) ? args.status : "active",
        updated: todayISO(),
        tags: parseTags(args?.tags),
        source: args?.source || "",
        verifiedBy: args?.verified_by || "",
        body: args?.body || "",
      });
      const written = await ctx.datafs.write(target, content);
      await syncIndexFile(ctx);
      mako.blocks.reload("vault", ctx.scopeKey);
      return {
        saved: true,
        path: target,
        size: written.size,
        usedBytes: written.usedBytes,
        quotaBytes: written.quotaBytes,
      };
    });

    mako.tools.register("move_doc", async (args, ctx) => {
      const fromPath = normalizeDocPath(args?.from_path, "");
      const toPath = normalizeDocPath(args?.to_path, "");
      if (!fromPath || !toPath) throw new Error("from_path and to_path are required");
      if (isHiddenPath(args?.from_path) || isHiddenPath(args?.to_path) || isHiddenPath(fromPath) || isHiddenPath(toPath)) {
        throw new Error("hidden paths (starting with '.') are reserved and cannot be moved");
      }
      await ctx.datafs.move(fromPath, toPath);
      await syncIndexFile(ctx);
      mako.blocks.reload("vault", ctx.scopeKey);
      return { moved: true, from: fromPath, to: toPath };
    });

    mako.tools.register("delete_doc", async (args, ctx) => {
      const target = normalizeDocPath(args?.path, "");
      if (!target) throw new Error("path is required");
      if (isHiddenPath(args?.path) || isHiddenPath(target)) {
        throw new Error("hidden paths (starting with '.') are reserved and cannot be deleted");
      }
      const res = await ctx.datafs.delete(target);
      await syncIndexFile(ctx);
      mako.blocks.reload("vault", ctx.scopeKey);
      return { deleted: Boolean(res.deleted), path: target };
    });

    mako.tools.register("lint", async (_args, ctx) => {
      const { index } = await loadVaultDocs(ctx, false);
      return {
        project: ctx.projectName,
        docs: index.docs.length,
        hasIndex: index.hasIndex,
        totalLinks: index.totalLinks,
        issueCount: index.issueCount,
        brokenLinks: index.broken,
        missingFrontmatter: index.missingMeta,
        needsReview: index.needsReview,
        orphans: index.orphans,
      };
    });
  },

  // Asynchronous Host API event handlers (Stage 1 decision + Stage 2 agent ask).
  on: {
    async decisionAnswered(ev) {
      const tag = parseTag(ev?.tag);
      const loc = locateTurn(tag);
      if (!loc) return;
      const { s, thread, turn } = loc;
      if (turn.phase !== "selecting") return;
      if (turn.decisionReq && ev?.requestId && turn.decisionReq !== ev.requestId) return;

      const cands = hydrateCandidates(s.docs, turn.retrieval?.candidates);
      let parsed = {};
      try {
        parsed = ev?.responseJson ? JSON.parse(ev.responseJson) : {};
      } catch {}

      const applied = applyDecision(cands, parsed);
      const totalMs = Number(ev?.elapsedMs || 0) + Number(turn.retrieval?.stage0Ms || 0);
      finalizeRetrievalSelection(s, turn, {
        mode: "decision",
        ranked: applied.ranked,
        entry: applied.entry,
        needsMore: applied.needsMore,
        notCovered: applied.notCovered,
        ms: totalMs,
        provider: parsed.provider || s.ask?.decision?.provider || "",
        model: parsed.model || s.ask?.decision?.model || "",
        fallbackReason: "",
      });

      await startAgentForTurn(s, tag.s, thread, turn);
      await persistAskHistory(tag.s, s.ask.threads);
    },

    async decisionFailed(ev) {
      const tag = parseTag(ev?.tag);
      const loc = locateTurn(tag);
      if (!loc) return;
      const { s, thread, turn } = loc;
      if (turn.phase !== "selecting") return;
      if (turn.decisionReq && ev?.requestId && turn.decisionReq !== ev.requestId) return;

      if (ev?.code === "unavailable" && s.ask?.decision) {
        s.ask.decision.available = false;
      }

      const cands = hydrateCandidates(s.docs, turn.retrieval?.candidates);
      const ranked = cands.map((c) => ({
        ...c,
        decision: null,
        level: null,
        levelLabel: "",
        final: c.local,
      }));
      const totalMs = Number(ev?.elapsedMs || 0) + Number(turn.retrieval?.stage0Ms || 0);
      finalizeRetrievalSelection(s, turn, {
        mode: "local",
        ranked,
        entry: ranked[0]?.path || "",
        needsMore: false,
        notCovered: false,
        ms: totalMs,
        provider: "",
        model: "",
        fallbackReason: String(ev?.error || ev?.code || "failed"),
      });

      await startAgentForTurn(s, tag.s, thread, turn);
      await persistAskHistory(tag.s, s.ask.threads);
    },

    agentAskStarted(ev) {
      const tag = parseTag(ev?.tag);
      const loc = locateTurn(tag);
      if (!loc) return;
      const { s, turn } = loc;
      if (turn.phase !== "consent" && turn.phase !== "answering") return;
      if (turn.askReq && ev?.requestId && turn.askReq !== ev.requestId) return;
      if (turn.phase === "consent") {
        turn.phase = "answering";
        recomputeAskBusy(s.ask);
      }
    },

    async agentAnswered(ev) {
      const tag = parseTag(ev?.tag);
      const parsed = parseAgentAnswer(ev?.output || "");
      const rawAnswer = String(parsed.answer || "");
      const clampedAnswer = cutUtf8Bytes(rawAnswer, MAX_ANSWER_BYTES);
      const answerTruncated = Boolean(ev?.truncated) || utf8ByteLength(rawAnswer) > MAX_ANSWER_BYTES;
      const loc = locateTurn(tag);
      if (!loc) {
        if (tag) {
          await mergeTurnIntoPersistedHistory(tag.s, tag.t, tag.u, (turn) => {
            if (turn.phase === "canceled" || turn.phase === "done") return;
            turn.phase = "done";
            turn.error = null;
            turn.answer = {
              markdown: clampedAnswer,
              citations: parsed.citations,
              confidence: parsed.confidence,
              followUps: parsed.followUps,
              missing: parsed.missing,
              agentLabel: "",
              ms: Number(ev?.elapsedMs || 0),
              truncated: answerTruncated,
            };
          });
        }
        return;
      }

      const { s, thread, turn } = loc;
      if (turn.phase !== "consent" && turn.phase !== "answering") return;
      if (turn.askReq && ev?.requestId && turn.askReq !== ev.requestId) return;

      const resolvedCitations = [];
      const seen = new Set();
      for (const rawCite of parsed.citations || []) {
        const res = resolveDocLink(s.docs, "", rawCite);
        if (res && !seen.has(res)) {
          seen.add(res);
          resolvedCitations.push(res);
        }
      }

      turn.answer = {
        markdown: clampedAnswer,
        citations: resolvedCitations,
        confidence: parsed.confidence,
        followUps: parsed.followUps,
        missing: parsed.missing,
        agentLabel: s.ask?.assigned?.label || "",
        ms: Number(ev?.elapsedMs || 0),
        truncated: answerTruncated,
      };
      turn.phase = "done";
      turn.error = null;
      if (turn.retrieval) turn.retrieval.candidates = [];
      recomputeAskBusy(s.ask);
      thread.updatedMs = Date.now();
      await persistAskHistory(tag.s, s.ask.threads);
    },

    async agentAskFailed(ev) {
      const tag = parseTag(ev?.tag);
      const code = String(ev?.code || "failed");
      const message = String(ev?.error || "");
      const loc = locateTurn(tag);
      if (!loc) {
        if (tag) {
          await mergeTurnIntoPersistedHistory(tag.s, tag.t, tag.u, (turn) => {
            if (turn.phase === "canceled" || turn.phase === "done") return;
            turn.phase = code === "canceled" ? "canceled" : "error";
            turn.error = { code, message };
          });
        }
        return;
      }

      const { s, thread, turn } = loc;
      if (turn.phase !== "consent" && turn.phase !== "answering") return;
      if (turn.askReq && ev?.requestId && turn.askReq !== ev.requestId) return;

      turn.phase = code === "canceled" ? "canceled" : "error";
      turn.error = { code, message };
      if (turn.retrieval) turn.retrieval.candidates = [];
      recomputeAskBusy(s.ask);
      thread.updatedMs = Date.now();
      await persistAskHistory(tag.s, s.ask.threads);
    },
  },
};
