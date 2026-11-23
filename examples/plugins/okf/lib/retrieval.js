// Pure JS 2-stage hybrid retrieval for OKF Knowledge:
// Stage 0: Local BM25F + Korean/CJK character bigrams + graph centrality pre-filter
// Stage 1: Decision model batch relevance scoring, entry-point choice, and coverage check
// Stage 2: Exploration agent prompt builder + structured JSON answer schema & parser

const EN_STOPWORDS = new Set([
  "the", "a", "an", "is", "are", "was", "were", "be", "been", "being",
  "to", "of", "in", "on", "at", "for", "with", "by", "from", "as",
  "it", "its", "this", "that", "these", "those", "and", "or", "not", "but",
  "if", "what", "which", "who", "how", "why", "when", "where",
  "do", "does", "did", "can", "could", "should", "would", "will",
  "about", "into", "than", "then", "there", "their", "they", "we", "you",
  "our", "your", "have", "has", "had",
]);

const KO_STOPWORDS = new Set([
  "은", "는", "이", "가", "을", "를", "의", "에", "에서", "로", "으로",
  "와", "과", "도", "만", "그", "저", "것", "수", "등", "및", "또는",
  "그리고", "대해", "대한", "무엇", "어떤", "어떻게", "왜", "언제", "어디",
]);

const CJK_HANGUL_RE = /[\u3131-\u318E\uAC00-\uD7A3\u4E00-\u9FFF\u3040-\u30FF]/;
const TOKEN_RE = /[\p{L}\p{N}]+/gu;

const FIELD_WEIGHTS = {
  title: 3.0,
  tags: 2.5,
  headings: 2.0,
  description: 1.5,
  path: 1.2,
  body: 1.0,
};

const FIELDS = Object.keys(FIELD_WEIGHTS);
const BM25_K1 = 1.2;
const BM25_B = 0.75;

export const DECISION_LEVELS = ["Unrelated", "Tangential", "Relevant", "Essential"];

export const ANSWER_SCHEMA = {
  type: "object",
  additionalProperties: false,
  required: ["answer", "citations", "confidence", "follow_ups", "missing"],
  properties: {
    answer: { type: "string", description: "Markdown answer with [[path]] citations" },
    citations: { type: "array", items: { type: "string" } },
    confidence: { type: "string", enum: ["high", "medium", "low"] },
    follow_ups: { type: "array", items: { type: "string" }, maxItems: 3 },
    missing: { type: "array", items: { type: "string" } },
  },
};

function utf8Bytes(str) {
  let bytes = 0;
  for (const ch of String(str || "")) {
    const cp = ch.codePointAt(0);
    if (cp <= 0x7f) bytes += 1;
    else if (cp <= 0x7ff) bytes += 2;
    else if (cp <= 0xffff) bytes += 3;
    else bytes += 4;
  }
  return bytes;
}

function cutUtf8(str, maxBytes, suffix = "\n…(truncated)") {
  const s = String(str || "");
  if (utf8Bytes(s) <= maxBytes) return s;
  const budget = Math.max(0, maxBytes - utf8Bytes(suffix));
  let out = "";
  let used = 0;
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    const b = cp <= 0x7f ? 1 : cp <= 0x7ff ? 2 : cp <= 0xffff ? 3 : 4;
    if (used + b > budget) break;
    out += ch;
    used += b;
  }
  return out + suffix;
}

export function extractHeadings(body, maxHeadings = 16) {
  const out = [];
  const lines = String(body || "").split("\n");
  for (const line of lines) {
    const m = line.match(/^#{1,6}\s+(.+?)\s*#*$/);
    if (!m) continue;
    const h = m[1].replace(/[*_`[\]]/g, "").replace(/\s+/g, " ").trim();
    if (h) out.push(h);
    if (out.length >= maxHeadings) break;
  }
  return out;
}

export function tokenize(text) {
  let s = String(text || "");
  if (typeof s.normalize === "function") {
    try {
      s = s.normalize("NFKC");
    } catch {}
  }
  s = s.toLowerCase();
  const matches = s.match(TOKEN_RE);
  if (!matches) return [];

  const tokens = [];
  for (const raw of matches) {
    if (!raw) continue;
    if (CJK_HANGUL_RE.test(raw)) {
      if (!KO_STOPWORDS.has(raw)) {
        tokens.push(raw);
      }
      if (raw.length >= 2) {
        for (let i = 0; i < raw.length - 1; i++) {
          const bg = raw.slice(i, i + 2);
          if (bg !== raw && !KO_STOPWORDS.has(bg)) {
            tokens.push(bg);
          }
        }
      }
      continue;
    }
    if (raw.length <= 1) continue;
    if (EN_STOPWORDS.has(raw)) continue;
    tokens.push(raw);
  }
  return tokens;
}

function countTerms(tokens) {
  const map = new Map();
  for (const t of tokens) {
    map.set(t, (map.get(t) || 0) + 1);
  }
  return map;
}

export function computeDocsSignature(docs) {
  return (docs || [])
    .map((d) => `${d.path}:${d.modifiedMs || 0}:${d.size || 0}:${(d.backlinks || []).length}:${(d.links || []).length}`)
    .join("|");
}

export function buildRetrievalIndex(docs) {
  const list = docs || [];
  const N = list.length;
  const sig = computeDocsSignature(list);
  const df = new Map();
  const docTerms = new Map();
  const docLens = new Map();
  const headings = new Map();
  const centrality = new Map();
  const totalLen = {};
  for (const f of FIELDS) totalLen[f] = 0;

  let maxDegree = 1;
  for (const d of list) {
    const deg = (d.links?.length || 0) + (d.backlinks?.length || 0);
    if (deg > maxDegree) maxDegree = deg;
  }

  for (const d of list) {
    const hList = extractHeadings(d.body);
    headings.set(d.path, hList);

    const fieldTexts = {
      title: d.title || "",
      tags: (d.tags || []).join(" "),
      headings: hList.join(" "),
      description: d.description || "",
      path: (d.path || "").replace(/[/_.-]+/g, " "),
      body: d.body || "",
    };

    const perField = {};
    const perLen = {};
    const seenInDoc = new Set();

    for (const f of FIELDS) {
      const toks = tokenize(fieldTexts[f]);
      perLen[f] = toks.length;
      totalLen[f] += toks.length;
      const tf = countTerms(toks);
      perField[f] = tf;
      for (const term of tf.keys()) {
        seenInDoc.add(term);
      }
    }

    for (const term of seenInDoc) {
      df.set(term, (df.get(term) || 0) + 1);
    }

    docTerms.set(d.path, perField);
    docLens.set(d.path, perLen);

    const deg = (d.links?.length || 0) + (d.backlinks?.length || 0);
    centrality.set(d.path, deg / maxDegree);
  }

  const avgLen = {};
  for (const f of FIELDS) {
    avgLen[f] = N > 0 ? Math.max(1, totalLen[f] / N) : 1;
  }

  return { sig, N, avgLen, df, docTerms, docLens, headings, centrality };
}

function cleanSnippetMarkdown(raw) {
  let s = String(raw || "");
  if (!s) return "";
  s = s
    .replace(/```[a-z0-9_+-]*\s*/gi, " ")
    .replace(/~~~+/g, " ")
    .replace(/\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|([^\]]+))?\]\]/g, (_m, target, label) => (label || target || "").trim())
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/`([^`\n]*)`/g, "$1")
    .replace(/`/g, "")
    .replace(/(^|\n)\s*#{1,6}\s+/g, "$1")
    .replace(/(^|\n)\s*>\s*/g, "$1")
    .replace(/(^|\n)\s*[-*+]\s+/g, "$1")
    .replace(/\*\*([^*\n]+)\*\*/g, "$1")
    .replace(/__([^_\n]+)__/g, "$1")
    .replace(/~~([^~\n]+)~~/g, "$1")
    .replace(/(^|[\s(])\*([^*\n]+)\*(?=[\s).,!?:;]|$)/g, "$1$2")
    .replace(/(^|[\s(])_([^_\n]+)_(?=[\s).,!?:;]|$)/g, "$1$2")
    .replace(/(^|\s)#{1,6}\s+/g, "$1")
    .replace(/\*\*|~~/g, "");
  return s.replace(/\s+/g, " ").trim();
}

function firstBodyLine(body, maxLen = 220) {
  const lines = String(body || "").split("\n");
  for (const raw of lines) {
    const line = raw.trim();
    if (!line || line.startsWith("#") || line.startsWith("```") || line.startsWith("---")) continue;
    const clean = cleanSnippetMarkdown(line);
    if (!clean) continue;
    return clean.length > maxLen ? `${clean.slice(0, maxLen - 1)}…` : clean;
  }
  return "";
}

export function bestSnippet(doc, terms = [], len = 320) {
  const body = String(doc?.body || "");
  if (!body.trim()) {
    const desc = cleanSnippetMarkdown(doc?.description || "");
    return desc.length > len ? `${desc.slice(0, len - 1)}…` : desc;
  }
  const lower = body.toLowerCase();
  const sortedTerms = [...new Set((terms || []).filter((t) => t && t.length >= 2))]
    .sort((a, b) => b.length - a.length);

  let bestPos = -1;
  let bestHitLen = 0;
  for (const term of sortedTerms) {
    const pos = lower.indexOf(term.toLowerCase());
    if (pos >= 0) {
      bestPos = pos;
      bestHitLen = term.length;
      break;
    }
  }

  if (bestPos < 0) {
    const desc = cleanSnippetMarkdown(doc?.description || "");
    if (desc) {
      const first = firstBodyLine(body, Math.max(80, len - desc.length - 4));
      const combined = first && !desc.includes(first.slice(0, 30)) ? `${desc} — ${first}` : desc;
      return combined.length > len ? `${combined.slice(0, len - 1)}…` : combined;
    }
    return firstBodyLine(body, len);
  }

  const before = Math.floor(len * 0.3);
  const start = Math.max(0, bestPos - before);
  const end = Math.min(body.length, start + Math.max(len, bestHitLen + 40));
  const slice = cleanSnippetMarkdown(body.slice(start, end));
  if (!slice) return firstBodyLine(body, len);
  const prefix = start > 0 ? "…" : "";
  const suffix = end < body.length ? "…" : "";
  const out = `${prefix}${slice}${suffix}`;
  return out.length > len + 2 ? `${out.slice(0, len)}…` : out;
}

export function rankDocuments(index, docs, query, opts = {}) {
  const list = docs || [];
  const limit = Math.max(1, Number(opts.limit || 12));
  const pinnedSet = new Set(Array.isArray(opts.pinned) ? opts.pinned.filter(Boolean) : []);
  const qRaw = String(query || "").trim();
  const qLower = qRaw.toLowerCase();
  const qTokens = tokenize(qRaw);
  const uniqueTerms = [...new Set(qTokens)];

  if (!qRaw) {
    return list
      .map((doc) => ({
        path: doc.path,
        doc,
        local: 1,
        raw: 1,
        matched: [],
        snippet: bestSnippet(doc, [], 220),
      }))
      .sort((a, b) => (index.centrality.get(b.path) || 0) - (index.centrality.get(a.path) || 0) || a.path.localeCompare(b.path))
      .slice(0, limit);
  }

  const N = Math.max(1, index.N || list.length);
  const scored = [];
  let maxRaw = 0;

  for (const doc of list) {
    const perField = index.docTerms.get(doc.path);
    const perLen = index.docLens.get(doc.path);
    let raw = 0;
    const matched = [];

    if (perField && perLen) {
      for (const term of uniqueTerms) {
        const df = index.df.get(term) || 0;
        let wTf = 0;
        for (const f of FIELDS) {
          const tf = perField[f].get(term) || 0;
          if (tf <= 0) continue;
          const dl = perLen[f] || 1;
          const avg = index.avgLen[f] || 1;
          wTf += FIELD_WEIGHTS[f] * (tf / ((1 - BM25_B) + BM25_B * (dl / avg)));
        }
        if (wTf > 0) {
          matched.push(term);
          const idf = Math.log(1 + (N - df + 0.5) / (df + 0.5));
          raw += idf * ((wTf * (BM25_K1 + 1)) / (wTf + BM25_K1));
        }
      }
    }

    // Substring match boost/fallback (handles prefixes like "sandbox" -> "sandboxed", paths, or exact phrases).
    if (qLower.length >= 2) {
      const titleL = (doc.title || "").toLowerCase();
      const pathL = (doc.path || "").toLowerCase();
      const descL = (doc.description || "").toLowerCase();
      const tagsL = (doc.tags || []).map((t) => t.toLowerCase());
      const bodyL = (doc.body || "").toLowerCase();

      if (titleL.includes(qLower)) {
        raw += 2.4;
        if (!matched.includes(qLower)) matched.push(qLower);
      } else if (pathL.includes(qLower)) {
        raw += 1.6;
        if (!matched.includes(qLower)) matched.push(qLower);
      } else if (descL.includes(qLower)) {
        raw += 1.2;
        if (!matched.includes(qLower)) matched.push(qLower);
      } else if (tagsL.some((t) => t.includes(qLower))) {
        raw += 1.5;
        if (!matched.includes(qLower)) matched.push(qLower);
      } else if (bodyL.includes(qLower)) {
        raw += 0.6;
        if (!matched.includes(qLower)) matched.push(qLower);
      } else if (raw === 0 && uniqueTerms.length > 0) {
        // Check individual query tokens as substrings for prefix matching.
        for (const t of uniqueTerms) {
          if (t.length < 3) continue;
          if (titleL.includes(t)) { raw += 1.0; matched.push(t); }
          else if (pathL.includes(t) || tagsL.some((tg) => tg.includes(t))) { raw += 0.6; matched.push(t); }
          else if (descL.includes(t) || bodyL.includes(t)) { raw += 0.35; matched.push(t); }
        }
      }
    }

    const isPinned = pinnedSet.has(doc.path);
    if (raw <= 0 && !isPinned) continue;

    // Graph centrality & document lifecycle adjustments.
    const backlinks = (doc.backlinks || []).length;
    raw *= 1 + 0.15 * Math.log(1 + backlinks);
    if (doc.status === "deprecated") raw *= 0.6;
    else if (doc.status === "needs_review") raw *= 0.9;
    if (doc.type === "Index") raw *= 0.5;

    if (raw > maxRaw) maxRaw = raw;
    scored.push({
      path: doc.path,
      doc,
      raw,
      isPinned,
      matched: [...new Set(matched)],
      snippet: bestSnippet(doc, matched.length > 0 ? matched : uniqueTerms, 320),
    });
  }

  scored.sort((a, b) => {
    if (b.raw !== a.raw) return b.raw - a.raw;
    const cb = index.centrality.get(b.path) || 0;
    const ca = index.centrality.get(a.path) || 0;
    if (cb !== ca) return cb - ca;
    return a.path.localeCompare(b.path);
  });

  const picked = [];
  const pickedSet = new Set();
  for (const item of scored) {
    if (picked.length >= limit) break;
    picked.push(item);
    pickedSet.add(item.path);
  }
  // Ensure pinned docs are always included.
  for (const item of scored) {
    if (item.isPinned && !pickedSet.has(item.path)) {
      if (picked.length >= limit) picked.pop();
      picked.push(item);
      pickedSet.add(item.path);
    }
  }

  return picked.map((item) => ({
    path: item.path,
    doc: item.doc,
    local: maxRaw > 0 ? Math.round((item.raw / maxRaw) * 1000) / 1000 : (item.isPinned ? 0.5 : 0),
    raw: Math.round(item.raw * 1000) / 1000,
    matched: item.matched,
    snippet: item.snippet,
  }));
}

export function expandNeighbors(selectedPaths, docs, max = 6, focusPaths = null) {
  const byPath = new Map((docs || []).map((d) => [d.path, d]));
  const selectedSet = new Set(selectedPaths || []);
  const seeds = Array.isArray(focusPaths) && focusPaths.length > 0
    ? focusPaths.slice(0, 3)
    : (selectedPaths || []).slice(0, 3);

  const relMap = new Map();
  for (const sp of seeds) {
    const doc = byPath.get(sp);
    if (!doc) continue;
    for (const target of doc.links || []) {
      if (selectedSet.has(target) || !byPath.has(target)) continue;
      const cur = relMap.get(target);
      relMap.set(target, cur && cur !== "links" ? "both" : "links");
    }
    for (const src of doc.backlinks || []) {
      if (selectedSet.has(src) || !byPath.has(src)) continue;
      const cur = relMap.get(src);
      relMap.set(src, cur && cur !== "backlinks" ? "both" : "backlinks");
    }
  }

  return [...relMap.entries()]
    .map(([path, relation]) => {
      const doc = byPath.get(path);
      const degree = (doc?.links?.length || 0) + (doc?.backlinks?.length || 0);
      return { path, doc, relation, degree };
    })
    .sort((a, b) => b.degree - a.degree || a.path.localeCompare(b.path))
    .slice(0, Math.max(0, max))
    .map(({ path, doc, relation }) => ({ path, doc, relation }));
}

export function subgraphLines(paths, docs, maxLines = 40) {
  const set = new Set(paths || []);
  const byPath = new Map((docs || []).map((d) => [d.path, d]));
  const lines = [];
  for (const p of paths || []) {
    const doc = byPath.get(p);
    if (!doc) continue;
    for (const target of doc.links || []) {
      if (set.has(target) && target !== p) {
        lines.push(`${p} → ${target}`);
        if (lines.length >= maxLines) return lines;
      }
    }
  }
  return lines;
}

export function candidateId(index) {
  return `d${String(index + 1).padStart(2, "0")}`;
}

export function buildDecisionRequest(query, conversation, candidates) {
  const list = (candidates || []).slice(0, 12);
  const candState = {};
  const questions = {};
  const optionIds = [];

  for (let i = 0; i < list.length; i++) {
    const c = list[i];
    const doc = c.doc || c;
    const cid = candidateId(i);
    optionIds.push(cid);
    const headings = Array.isArray(c.headings) ? c.headings : extractHeadings(doc.body, 8);
    const excerpt = bestSnippet(doc, c.matched || tokenize(query), 600);
    candState[cid] = {
      path: doc.path,
      title: doc.title || doc.path,
      type: doc.type || "Concept",
      status: doc.status || "active",
      tags: (doc.tags || []).slice(0, 8),
      description: String(doc.description || "").slice(0, 240),
      headings: headings.slice(0, 8),
      excerpt: excerpt.slice(0, 600),
    };
    questions[cid] = {
      type: "score",
      instructions: `How useful is candidates.${cid} for answering the query (consider the conversation for context)? Judge ${cid} alone.`,
      levels: DECISION_LEVELS,
    };
  }

  questions.entry = {
    type: "choice",
    instructions: "Which candidate is the best single starting point for answering the query? Choose none if no candidate addresses it.",
    options: [...optionIds, "none"],
  };
  questions.needs_more = {
    type: "noul",
    instructions: "Is the query likely NOT answerable from these candidates alone, because it asks about something they do not cover?",
  };

  return {
    state: {
      query: String(query || "").slice(0, 1000),
      conversation: String(conversation || "").slice(0, 1500),
      candidates: candState,
    },
    questions,
  };
}

export function applyDecision(candidates, response) {
  const list = (candidates || []).slice(0, 12);
  const answers = (response && typeof response === "object" && response.answers) || {};
  const idToPath = new Map();

  const ranked = list.map((c, i) => {
    const cid = candidateId(i);
    idToPath.set(cid, c.path);
    const ans = answers[cid];
    let decision = null;
    let level = null;
    let levelLabel = "";

    if (ans && typeof ans === "object" && !ans.refusal) {
      if (typeof ans.level === "number" && Number.isFinite(ans.level)) {
        level = Math.max(0, Math.min(3, Math.round(ans.level)));
        decision = level / 3;
      } else if (typeof ans.label === "string" && DECISION_LEVELS.includes(ans.label)) {
        level = DECISION_LEVELS.indexOf(ans.label);
        decision = level / 3;
      } else if (typeof ans.score === "number" && Number.isFinite(ans.score)) {
        decision = Math.max(0, Math.min(1, ans.score));
        level = Math.max(0, Math.min(3, Math.round(decision * 3)));
      }
      if (level !== null) {
        levelLabel = DECISION_LEVELS[level] || "";
      }
    }

    const local = Number(c.local || 0);
    const final = decision !== null
      ? Math.round((0.7 * decision + 0.3 * local) * 1000) / 1000
      : Math.round(local * 1000) / 1000;

    return {
      ...c,
      cid,
      decision: decision !== null ? Math.round(decision * 1000) / 1000 : null,
      level,
      levelLabel,
      final,
    };
  });

  ranked.sort((a, b) => {
    if (b.final !== a.final) return b.final - a.final;
    const db = b.decision ?? -1;
    const da = a.decision ?? -1;
    if (db !== da) return db - da;
    if (b.local !== a.local) return b.local - a.local;
    return a.path.localeCompare(b.path);
  });

  const entryAns = answers.entry;
  let entryChoice = "";
  if (entryAns && typeof entryAns === "object" && !entryAns.refusal && typeof entryAns.choice === "string") {
    entryChoice = entryAns.choice.trim();
  }
  const entryNone = entryChoice === "none";
  const entry = entryNone
    ? ""
    : (idToPath.get(entryChoice) || ranked[0]?.path || "");

  const nmAns = answers.needs_more;
  let needsMore = false;
  if (nmAns && typeof nmAns === "object" && !nmAns.refusal) {
    if (typeof nmAns.yes === "boolean") needsMore = nmAns.yes;
    else if (typeof nmAns.probability === "number") needsMore = nmAns.probability >= 0.5;
  }

  const notCovered = entryNone && ranked.every((r) => r.level === null || r.level <= 1);

  return {
    ranked,
    entry,
    entryNone,
    needsMore,
    notCovered,
  };
}

export function selectContext(ranked, opts = {}) {
  const maxDocs = Math.max(1, Math.min(12, Math.round(Number(opts.maxDocs || 5))));
  const minLevel = opts.minLevel !== undefined ? Number(opts.minLevel) : 2;
  const list = ranked || [];
  if (opts.notCovered) return [];
  if (list.length === 0) return [];

  const filtered = list.filter((r) => {
    if (r.level !== null && r.level !== undefined) {
      return r.level >= minLevel;
    }
    return Number(r.final ?? r.local ?? 0) >= 0.25;
  });

  const chosen = filtered.length > 0 ? filtered.slice(0, maxDocs) : [list[0]];
  return chosen;
}

export function buildAgentPrompt({
  projectName = "",
  locale = "en",
  outline = [],
  subgraph = [],
  ascii = "",
  sources = [],
  neighbors = [],
  conversation = [],
  query = "",
  notCovered = false,
}) {
  const lang = String(locale || "").toLowerCase().startsWith("ko") ? "Korean" : "English";
  const proj = projectName || "this project";

  const headerLines = [
    `You are the exploration agent for the OKF knowledge vault of the project "${proj}".`,
    "Answer the QUESTION using only the SOURCES and OUTLINE below; they were pre-selected for relevance.",
    "Cite every claim with a wiki link using the exact path, e.g. [[decisions/001-storage.md]].",
    'If the sources do not contain the answer, say so plainly and list what is missing in "missing".',
    `Do not invent documents. Do not modify files. Reply in ${lang}.`,
  ];
  if (notCovered) {
    headerLines.push("Note: Pre-selection indicates this question is likely NOT covered by the vault. State plainly what is missing.");
  }

  const sections = [headerLines.join("\n")];

  // OUTLINE (<= 150 lines)
  const outlineLines = (outline || []).slice(0, 150);
  if (outlineLines.length > 0) {
    sections.push(`## OUTLINE\n${outlineLines.join("\n")}`);
  }

  // KNOWLEDGE SUBGRAPH
  const subParts = [];
  if (ascii) subParts.push(ascii);
  if (Array.isArray(subgraph) && subgraph.length > 0) {
    subParts.push(subgraph.slice(0, 40).join("\n"));
  }
  if (subParts.length > 0) {
    sections.push(`## KNOWLEDGE SUBGRAPH\n${subParts.join("\n\n")}`);
  }

  // SOURCES (<= 16 KiB per doc, <= 60 KiB total)
  const perDocMax = 16 * 1024;
  const totalSourcesMax = 60 * 1024;
  let usedSourceBytes = 0;
  const sourceBlocks = [];

  for (const s of sources || []) {
    const doc = s.doc || s;
    const rel = Number(s.final ?? s.local ?? 0).toFixed(2);
    const entryMark = s.isEntry ? ", entry point" : "";
    const heading = `### [[${doc.path}]] — ${doc.title || doc.path} (${doc.type || "Concept"}, ${doc.status || "active"}, relevance ${rel}${entryMark})`;
    const remaining = totalSourcesMax - usedSourceBytes;
    if (remaining <= 256) break;
    const bodyBudget = Math.min(perDocMax, remaining - utf8Bytes(heading) - 16);
    const body = cutUtf8(doc.body || doc.description || "", Math.max(128, bodyBudget));
    const block = `${heading}\n${body}`;
    usedSourceBytes += utf8Bytes(block);
    sourceBlocks.push(block);
  }
  if (sourceBlocks.length > 0) {
    sections.push(`## SOURCES\n${sourceBlocks.join("\n\n")}`);
  }

  // NEIGHBORS (outline only)
  if (Array.isArray(neighbors) && neighbors.length > 0) {
    const nbLines = neighbors.map((nb) => {
      const d = nb.doc || nb;
      const desc = d.description ? ` — ${String(d.description).slice(0, 140)}` : "";
      return `- [[${d.path}]] (${d.type || "Concept"}, ${nb.relation || "neighbor"})${desc}`;
    });
    sections.push(`## NEIGHBORS (outline only)\n${nbLines.join("\n")}`);
  }

  // CONVERSATION (<= 3 turns, each answer <= 1.5 KiB)
  if (Array.isArray(conversation) && conversation.length > 0) {
    const convLines = conversation.slice(-3).map((turn, idx) => {
      const a = cutUtf8(turn.a || "", 1500, "…");
      return `Turn ${idx + 1} Q: ${turn.q}\nTurn ${idx + 1} A: ${a}`;
    });
    sections.push(`## CONVERSATION\n${convLines.join("\n\n")}`);
  }

  // QUESTION
  sections.push(`## QUESTION\n${String(query || "").trim()}`);

  const full = sections.join("\n\n");
  return cutUtf8(full, 90 * 1024, "\n…(prompt truncated to budget)");
}

function normalizeParsedAnswer(obj, rawFallback) {
  const answer = typeof obj.answer === "string" && obj.answer.trim()
    ? obj.answer.trim()
    : String(rawFallback || "").trim();
  const citations = Array.isArray(obj.citations)
    ? obj.citations.map((x) => String(x || "").trim()).filter(Boolean)
    : [];
  const confRaw = String(obj.confidence || "").toLowerCase().trim();
  const confidence = confRaw === "high" || confRaw === "medium" || confRaw === "low" ? confRaw : "medium";
  const rawFollowUps = Array.isArray(obj.follow_ups)
    ? obj.follow_ups
    : (Array.isArray(obj.followUps) ? obj.followUps : []);
  const followUps = rawFollowUps.map((x) => String(x || "").trim()).filter(Boolean).slice(0, 3);
  const missing = Array.isArray(obj.missing)
    ? obj.missing.map((x) => String(x || "").trim()).filter(Boolean)
    : [];
  return { answer, citations, confidence, followUps, missing };
}

export function parseAgentAnswer(output) {
  const raw = String(output || "").trim();
  if (!raw) {
    return { answer: "", citations: [], confidence: "low", followUps: [], missing: [] };
  }

  // 1. Direct JSON parse (stripping optional markdown code fences).
  let candidate = raw;
  const fenced = raw.match(/^```(?:json)?\s*([\s\S]*?)\s*```$/i);
  if (fenced) {
    candidate = fenced[1].trim();
  }
  try {
    const parsed = JSON.parse(candidate);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed) && typeof parsed.answer === "string") {
      return normalizeParsedAnswer(parsed, raw);
    }
  } catch {}

  // 2. Extract first balanced {...} JSON object.
  const firstBrace = raw.indexOf("{");
  const lastBrace = raw.lastIndexOf("}");
  if (firstBrace >= 0 && lastBrace > firstBrace) {
    const slice = raw.slice(firstBrace, lastBrace + 1);
    try {
      const parsed = JSON.parse(slice);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed) && typeof parsed.answer === "string") {
        return normalizeParsedAnswer(parsed, raw);
      }
    } catch {}
  }

  // 3. Plain markdown fallback.
  return {
    answer: raw,
    citations: [],
    confidence: "medium",
    followUps: [],
    missing: [],
  };
}
