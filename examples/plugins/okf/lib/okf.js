// Pure JS Open Knowledge Format (OKF v0.2) parser, serializer, graph builder,
// and linter operating on standard Markdown files with YAML frontmatter.

export const DOC_TYPES = ["Concept", "Decision", "Architecture", "Procedure", "Reference"];
export const DOC_STATUSES = ["active", "needs_review", "deprecated"];
export const FOLDERS = ["concepts", "decisions", "architecture", "procedures", "reference"];

let _bm25Ranker = null;

// setSearchRanker wires the BM25F ranker from lib/retrieval.js into searchDocuments
// without introducing a reverse alphabetical module-load dependency between lib/* files.
export function setSearchRanker(fn) {
  _bm25Ranker = typeof fn === "function" ? fn : null;
}

// isHiddenPath returns true if any path segment starts with "." (e.g. ".okf/...").
export function isHiddenPath(p) {
  const clean = String(p || "").trim().replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
  if (!clean) return false;
  return clean.split("/").some((seg) => seg.startsWith("."));
}

// typeIcon maps an OKF document type to a bundled Lucide icon name.
export function typeIcon(type) {
  if (type === "Concept") return "book-open";
  if (type === "Decision") return "circle-check";
  if (type === "Architecture") return "layers";
  if (type === "Procedure") return "list";
  if (type === "Reference") return "file-text";
  if (type === "Index") return "layout-grid";
  return "file-text";
}

export function todayISO() {
  return new Date().toISOString().slice(0, 10);
}

export function slugify(s) {
  return String(s || "")
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9가-힣._-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
}

export function normalizeDocPath(raw, fallbackFolder = "concepts") {
  let p = String(raw || "").trim().replace(/\\/g, "/").replace(/^\/+/, "");
  if (!p) return "";
  p = p
    .split("/")
    .map((seg) => seg.trim())
    .filter((seg) => seg && seg !== "." && seg !== "..")
    .join("/");
  if (!p) return "";
  if (!p.includes("/") && p !== "index.md" && fallbackFolder) {
    p = `${fallbackFolder}/${p}`;
  }
  if (!p.endsWith(".md")) {
    p += ".md";
  }
  return p;
}

export function parseTags(raw) {
  if (Array.isArray(raw)) {
    return [...new Set(raw.map((t) => String(t || "").trim()).filter(Boolean))];
  }
  return [...new Set(String(raw || "").split(",").map((t) => t.trim()).filter(Boolean))];
}

function unquote(v) {
  const s = String(v || "").trim();
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1).replace(/\\"/g, '"').replace(/\\\\/g, "\\");
  }
  return s;
}

function yamlScalar(v) {
  const s = String(v ?? "").replace(/\r?\n/g, " ").trim();
  if (s === "") return '""';
  if (/[:#[\]{}&*!|>'"%@`,?]/.test(s) || /^\s|\s$/.test(s)) {
    return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
  }
  return s;
}

// parseFrontmatter parses the subset of YAML used by OKF v0.2 documents:
// top-level scalars, inline or block lists (tags), and one-level nested maps
// (provenance: { source, verified_by }).
export function parseFrontmatter(content) {
  const text = String(content || "").replace(/\r\n/g, "\n");
  if (!text.startsWith("---\n")) {
    return { hasFrontmatter: false, meta: {}, body: text.trim() };
  }
  const endIdx = text.indexOf("\n---", 4);
  if (endIdx < 0) {
    return { hasFrontmatter: false, meta: {}, body: text.trim() };
  }
  const fmRaw = text.slice(4, endIdx);
  let body = text.slice(endIdx + 4);
  if (body.startsWith("\n")) body = body.slice(1);
  body = body.replace(/^\n+/, "");

  const meta = {};
  let section = null; // current list or nested object key
  let sectionKind = null; // "list" | "map"

  for (const line of fmRaw.split("\n")) {
    if (!line.trim() || line.trim().startsWith("#")) continue;

    // Indented block item under current section.
    if (/^\s+-\s+/.test(line) && section) {
      if (!Array.isArray(meta[section])) meta[section] = [];
      meta[section].push(unquote(line.replace(/^\s+-\s+/, "")));
      sectionKind = "list";
      continue;
    }
    if (/^\s+[A-Za-z0-9_-]+\s*:/.test(line) && section) {
      const m = line.match(/^\s+([A-Za-z0-9_-]+)\s*:\s*(.*)$/);
      if (m) {
        if (!meta[section] || typeof meta[section] !== "object" || Array.isArray(meta[section])) {
          meta[section] = {};
        }
        meta[section][m[1]] = unquote(m[2]);
        sectionKind = "map";
        continue;
      }
    }

    const top = line.match(/^([A-Za-z0-9_-]+)\s*:\s*(.*)$/);
    if (!top) continue;
    const key = top[1];
    const val = top[2].trim();
    section = null;
    sectionKind = null;

    if (val === "") {
      section = key;
      continue;
    }
    if (val.startsWith("[") && val.endsWith("]")) {
      const inner = val.slice(1, -1).trim();
      meta[key] = inner ? inner.split(",").map((x) => unquote(x)).filter(Boolean) : [];
      continue;
    }
    meta[key] = unquote(val);
  }
  void sectionKind;
  return { hasFrontmatter: true, meta, body: body.trimEnd() };
}

export function serializeDoc(doc) {
  const type = doc.type || "Concept";
  const title = String(doc.title || "").trim() || "Untitled";
  const description = String(doc.description || "").trim();
  const status = DOC_STATUSES.includes(doc.status) ? doc.status : "active";
  const updated = doc.updated || todayISO();
  const tags = parseTags(doc.tags);
  const source = String(doc.source || "").trim();
  const verifiedBy = String(doc.verifiedBy || "").trim();

  const lines = ["---"];
  if (doc.okfVersion) {
    lines.push(`okf_version: ${yamlScalar(doc.okfVersion)}`);
  }
  lines.push(`type: ${yamlScalar(type)}`);
  lines.push(`title: ${yamlScalar(title)}`);
  if (description) {
    lines.push(`description: ${yamlScalar(description)}`);
  }
  lines.push(`status: ${yamlScalar(status)}`);
  lines.push(`updated: ${yamlScalar(updated)}`);
  if (tags.length > 0) {
    lines.push("tags:");
    for (const t of tags) {
      lines.push(`  - ${yamlScalar(t)}`);
    }
  }
  if (source || verifiedBy) {
    lines.push("provenance:");
    if (source) lines.push(`  source: ${yamlScalar(source)}`);
    if (verifiedBy) lines.push(`  verified_by: ${yamlScalar(verifiedBy)}`);
  }
  lines.push("---", "");
  const body = String(doc.body || "").trim();
  if (body) {
    lines.push(body, "");
  }
  return lines.join("\n");
}

// extractLinks finds both [[wiki-links]] and relative markdown links to .md files,
// ignoring fenced code blocks and inline code spans.
export function extractLinks(body) {
  const out = [];
  const seen = new Set();
  const text = String(body || "")
    .replace(/```[\s\S]*?```/g, "")
    .replace(/~~~[\s\S]*?~~~/g, "")
    .replace(/`[^`\n]+`/g, "");

  // 1. [[target]] or [[target|label]]
  const wikiRe = /\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]/g;
  let m;
  while ((m = wikiRe.exec(text)) !== null) {
    const raw = m[1].trim();
    if (raw && !seen.has(raw)) {
      seen.add(raw);
      out.push({ raw, kind: "wiki" });
    }
  }

  // 2. [label](relative/path.md)
  const mdRe = /(?<!!)\[[^\]]*\]\(([^)\s#]+)(?:#[^)\s]*)?\)/g;
  while ((m = mdRe.exec(text)) !== null) {
    const raw = m[1].trim();
    if (!raw || /^[a-z]+:/i.test(raw) || raw.startsWith("#")) continue;
    if (!raw.endsWith(".md")) continue;
    if (!seen.has(raw)) {
      seen.add(raw);
      out.push({ raw, kind: "md" });
    }
  }
  return out;
}

function resolveRelative(fromPath, target) {
  const clean = target.replace(/^\.\//, "");
  if (!fromPath.includes("/")) return clean;
  const baseParts = fromPath.split("/").slice(0, -1);
  const parts = [...baseParts];
  for (const seg of clean.split("/")) {
    if (!seg || seg === ".") continue;
    if (seg === "..") {
      if (parts.length > 0) parts.pop();
    } else {
      parts.push(seg);
    }
  }
  return parts.join("/");
}

// resolveDocLink resolves a wiki-link or relative markdown target against the
// vault's documents, matching direct path, relative path, or filename stem.
export function resolveDocLink(docs, fromPath, rawTarget) {
  const raw = String(rawTarget || "").trim().replace(/^okf:/i, "").split("#")[0].trim();
  if (!raw) return null;
  const byPath = new Map();
  const byStem = new Map();
  for (const d of docs || []) {
    byPath.set(d.path, d);
    const noExt = d.path.replace(/\.md$/, "");
    byPath.set(noExt, d);
    const base = noExt.split("/").pop();
    if (base && !byStem.has(base)) {
      byStem.set(base, d);
    }
  }
  const direct = raw.endsWith(".md") ? raw : `${raw}.md`;
  if (byPath.has(direct)) return byPath.get(direct).path;
  if (byPath.has(raw)) return byPath.get(raw).path;
  const rel = resolveRelative(String(fromPath || ""), direct);
  if (byPath.has(rel)) return byPath.get(rel).path;
  const stem = raw.replace(/\.md$/, "").split("/").pop();
  if (stem && byStem.has(stem)) return byStem.get(stem).path;
  return null;
}

// rewriteWikiLinksForMarkdown transforms OKF [[target|Label]] syntax into
// clickable [Label](okf:target.md) Markdown links for the Reader view while
// keeping fenced code blocks and inline backtick spans untouched.
export function rewriteWikiLinksForMarkdown(body, fromPath = "", docs = []) {
  const src = String(body || "");
  if (!src.includes("[[")) return src;
  const byPath = new Map((docs || []).map((d) => [d.path, d]));
  const parts = src.split(/(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]+`)/g);
  return parts
    .map((part, i) => {
      if (i % 2 === 1) return part; // fenced or inline code
      return part.replace(/\[\[([^\]|#]+)(?:#[^\]|]+)?(?:\|([^\]]+))?\]\]/g, (_m, target, label) => {
        const rawTarget = String(target || "").trim();
        if (!rawTarget) return _m;
        const resolved = resolveDocLink(docs, fromPath, rawTarget);
        const resolvedDoc = resolved ? byPath.get(resolved) : null;
        const hrefPath = resolved || (rawTarget.endsWith(".md") ? rawTarget : `${rawTarget}.md`);
        const rawLabel = (label || (resolvedDoc && resolvedDoc.title) || rawTarget).trim();
        const safeLabel = rawLabel.replace(/[[\]]/g, "");
        return `[${safeLabel || rawTarget}](okf:${encodeURI(hrefPath)})`;
      });
    })
    .join("");
}

// Matches control characters, bidi reordering controls, and zero-width marks
// rejected by Mako uiblock's protocol.Spoofing check.
export const SPOOFING_RE = /[\x00-\x1F\x7F-\x9F\u061C\u200B-\u200F\u202A-\u202E\u2066-\u2069\uFEFF]/g;

export function stripSpoofing(str) {
  return String(str ?? "").replace(SPOOFING_RE, "");
}

export function utf8ByteLength(str) {
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

// sliceUtf8Bytes returns the longest prefix of str whose UTF-8 encoding fits
// within maxBytes without splitting code points or collapsing newlines.
export function sliceUtf8Bytes(str, maxBytes) {
  const s = String(str ?? "");
  if (maxBytes <= 0 || !s) return "";
  if (utf8ByteLength(s) <= maxBytes) return s;
  let out = "";
  let used = 0;
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    const b = cp <= 0x7f ? 1 : cp <= 0x7ff ? 2 : cp <= 0xffff ? 3 : 4;
    if (used + b > maxBytes) break;
    out += ch;
    used += b;
  }
  return out;
}

// cutUtf8Bytes truncates str to <= maxBytes UTF-8 bytes while preserving
// newlines (safe for Markdown / code blocks / textarea values).
export function cutUtf8Bytes(str, maxBytes, suffix = "\n…") {
  const s = String(str ?? "");
  if (maxBytes <= 0) return "";
  if (utf8ByteLength(s) <= maxBytes) return s;
  const suf = String(suffix ?? "");
  const sufBytes = utf8ByteLength(suf);
  if (sufBytes >= maxBytes) {
    return sliceUtf8Bytes(s, maxBytes);
  }
  const head = sliceUtf8Bytes(s, maxBytes - sufBytes);
  return `${head}${suf}`;
}

// truncateUtf8Bytes collapses whitespace, strips spoofing chars, and truncates
// str so its UTF-8 encoding never exceeds maxBytes (for single-line labels).
export function truncateUtf8Bytes(str, maxBytes) {
  const s = stripSpoofing(String(str || "").replace(/[\r\n\t]+/g, " ")).trim();
  if (maxBytes <= 0) return "";
  if (utf8ByteLength(s) <= maxBytes) return s;
  const ellipsisBytes = 3; // "…" U+2026
  const budget = Math.max(0, maxBytes - ellipsisBytes);
  const out = sliceUtf8Bytes(s, budget);
  return out ? `${out}…` : "";
}

// stripMarkdownForSnippet strips raw Markdown syntax (#, **, `, [[path|label]])
// to produce readable plain-text snippets for Browse cards.
export function stripMarkdownForSnippet(raw) {
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
  return stripSpoofing(s.replace(/\s+/g, " ")).trim();
}

export function parseDocument(path, content, size = 0, modifiedMs = 0) {
  const { hasFrontmatter, meta, body } = parseFrontmatter(content);
  const fallbackTitle = path.replace(/\.md$/, "").split("/").pop() || path;
  const prov = meta.provenance && typeof meta.provenance === "object" ? meta.provenance : {};
  return {
    path,
    hasFrontmatter,
    okfVersion: meta.okf_version || "",
    type: meta.type || (path === "index.md" ? "Index" : "Concept"),
    title: meta.title || fallbackTitle,
    description: meta.description || "",
    status: meta.status || "active",
    updated: meta.updated || meta.timestamp || "",
    tags: parseTags(meta.tags),
    source: prov.source || "",
    verifiedBy: prov.verified_by || "",
    body,
    size,
    modifiedMs,
    links: [],
    backlinks: [],
    brokenLinks: [],
  };
}

// buildVaultIndex resolves links, backlinks, and OKF lint findings across all docs.
export function buildVaultIndex(rawDocs) {
  const docs = (rawDocs || []).filter((d) => !isHiddenPath(d.path));
  const byPath = new Map();
  const byStem = new Map();
  for (const d of docs) {
    byPath.set(d.path, d);
    const noExt = d.path.replace(/\.md$/, "");
    byPath.set(noExt, d);
    const base = noExt.split("/").pop();
    if (base && !byStem.has(base)) {
      byStem.set(base, d);
    }
    d.links = [];
    d.backlinks = [];
    d.brokenLinks = [];
  }

  const resolveLink = (fromPath, link) => {
    const raw = link.raw;
    const direct = raw.endsWith(".md") ? raw : `${raw}.md`;
    if (byPath.has(direct)) return byPath.get(direct).path;
    if (byPath.has(raw)) return byPath.get(raw).path;
    const rel = resolveRelative(fromPath, direct);
    if (byPath.has(rel)) return byPath.get(rel).path;
    const stem = raw.replace(/\.md$/, "").split("/").pop();
    if (stem && byStem.has(stem)) return byStem.get(stem).path;
    return null;
  };

  let totalLinks = 0;
  const broken = [];
  const missingMeta = [];
  const needsReview = [];

  for (const d of docs) {
    if (!d.hasFrontmatter || !d.title || !d.type) {
      missingMeta.push({
        path: d.path,
        code: !d.hasFrontmatter ? "missing_frontmatter" : "missing_fields",
        reason: !d.hasFrontmatter ? "Missing YAML frontmatter" : "Missing title or type",
      });
    }
    if (d.status === "needs_review") {
      needsReview.push({ path: d.path, title: d.title });
    }
    const rawLinks = extractLinks(d.body);
    const outSet = new Set();
    for (const l of rawLinks) {
      const target = resolveLink(d.path, l);
      if (target) {
        if (target !== d.path && !outSet.has(target)) {
          outSet.add(target);
          d.links.push(target);
          totalLinks++;
          const targetDoc = byPath.get(target);
          if (targetDoc && !targetDoc.backlinks.includes(d.path)) {
            targetDoc.backlinks.push(d.path);
          }
        }
      } else {
        d.brokenLinks.push(l.raw);
        broken.push({ from: d.path, fromTitle: d.title, target: l.raw });
      }
    }
  }

  const orphans = [];
  for (const d of docs) {
    if (d.path === "index.md") continue;
    const nonIndexBacklinks = d.backlinks.filter((p) => p !== "index.md");
    if (nonIndexBacklinks.length === 0 && d.links.length === 0) {
      orphans.push({ path: d.path, title: d.title });
    }
  }

  const hasIndex = byPath.has("index.md");
  return {
    docs,
    hasIndex,
    totalLinks,
    broken,
    missingMeta,
    needsReview,
    orphans,
    issueCount: (hasIndex ? 0 : 1) + broken.length + missingMeta.length + needsReview.length,
  };
}

export function rebuildIndexMarkdown(projectName, docs) {
  const nonIndex = (docs || [])
    .filter((d) => !isHiddenPath(d.path) && d.path !== "index.md")
    .sort((a, b) => a.path.localeCompare(b.path));
  const byType = new Map();
  for (const d of nonIndex) {
    const t = d.type || "Concept";
    if (!byType.has(t)) byType.set(t, []);
    byType.get(t).push(d);
  }
  const heading = projectName ? `${projectName} Knowledge Index` : "OKF Knowledge Index";
  const bodyLines = [`# ${heading}`, ""];
  if (nonIndex.length === 0) {
    bodyLines.push("No knowledge documents yet. Create concepts, decisions, or procedures to build this project's OKF graph.");
  } else {
    for (const [type, list] of [...byType.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      bodyLines.push(`## ${type}`, "");
      for (const d of list) {
        const stem = d.path.replace(/\.md$/, "");
        const desc = d.description ? ` — ${d.description}` : "";
        const badge = d.status && d.status !== "active" ? ` *(${d.status})*` : "";
        bodyLines.push(`- [[${stem}|${d.title}]]${badge}${desc}`);
      }
      bodyLines.push("");
    }
  }
  return serializeDoc({
    okfVersion: "0.2",
    type: "Index",
    title: heading,
    description: `Open Knowledge Format (OKF v0.2) root index for ${projectName || "this project"}.`,
    status: "active",
    updated: todayISO(),
    tags: ["okf", "index"],
    body: bodyLines.join("\n").trim(),
  });
}

export function starterOverviewMarkdown(projectName) {
  const name = projectName || "this project";
  return serializeDoc({
    type: "Concept",
    title: `${projectName || "Project"} Overview`,
    description: `High-level map of ${name}'s architecture, conventions, and key decisions.`,
    status: "active",
    updated: todayISO(),
    tags: ["overview", "onboarding"],
    source: "OKF vault initialization",
    body: [
      `# ${projectName || "Project"} Overview`,
      "",
      `This project knowledge vault uses the **Open Knowledge Format (OKF v0.2)**: plain Markdown files with YAML frontmatter connected via \`[[wiki-links]]\`.`,
      "",
      "## Recommended Structure",
      "- `concepts/` — Domain terms, core abstractions, and invariants",
      "- `decisions/` — Architecture Decision Records (ADRs)",
      "- `architecture/` — System boundaries, data flows, and component maps",
      "- `procedures/` — Runbooks, release steps, and troubleshooting guides",
      "",
      "## Next Steps",
      "- Ask an AI agent to document key architecture decisions via `mako-plugin call okf upsert_doc`.",
      "- Link related documents with `[[concepts/example]]` to build a navigable graph.",
    ].join("\n"),
  });
}

export function searchDocuments(docs, query, typeFilter = "", tagFilter = "") {
  const q = String(query || "").trim();
  const tf = String(typeFilter || "").trim().toLowerCase();
  const tgf = String(tagFilter || "").trim().toLowerCase();

  const filtered = (docs || []).filter((d) => {
    if (isHiddenPath(d.path)) return false;
    if (tf && tf !== "all" && d.type.toLowerCase() !== tf) return false;
    if (tgf && !d.tags.some((t) => t.toLowerCase() === tgf)) return false;
    return true;
  });

  if (!q) {
    return filtered
      .map((d) => ({ doc: d, score: 1, snippet: d.description || firstSnippet(d.body) }))
      .sort((a, b) => a.doc.path.localeCompare(b.doc.path));
  }

  if (_bm25Ranker) {
    return _bm25Ranker(filtered, q).map((r) => ({
      doc: r.doc,
      score: r.raw > 0 ? r.raw : r.local,
      snippet: r.snippet || r.doc.description || firstSnippet(r.doc.body),
    }));
  }

  // Fallback multi-token scorer if searchDocuments is invoked standalone.
  const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
  const results = [];
  for (const d of filtered) {
    let score = 0;
    let firstPos = -1;
    let firstLen = 0;
    const titleL = d.title.toLowerCase();
    const pathL = d.path.toLowerCase();
    const descL = d.description.toLowerCase();
    const bodyL = d.body.toLowerCase();
    for (const term of terms) {
      if (titleL.includes(term)) score += 10;
      if (pathL.includes(term)) score += 6;
      if (descL.includes(term)) score += 5;
      if (d.tags.some((t) => t.toLowerCase().includes(term))) score += 4;
      const pos = bodyL.indexOf(term);
      if (pos >= 0) {
        score += 2;
        if (firstPos < 0) {
          firstPos = pos;
          firstLen = term.length;
        }
      }
    }
    if (score > 0) {
      const snippet = firstPos >= 0 ? excerptAround(d.body, firstPos, firstLen) : (d.description || firstSnippet(d.body));
      results.push({ doc: d, score, snippet });
    }
  }
  results.sort((a, b) => b.score - a.score || a.doc.path.localeCompare(b.doc.path));
  return results;
}

function firstSnippet(body) {
  const line = String(body || "")
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l && !l.startsWith("#") && !l.startsWith("```") && !l.startsWith("---"));
  const clean = stripMarkdownForSnippet(line || "");
  return clean ? clean.slice(0, 140) : "";
}

function excerptAround(body, pos, len) {
  const start = Math.max(0, pos - 40);
  const end = Math.min(body.length, pos + len + 80);
  const slice = stripMarkdownForSnippet(body.slice(start, end));
  if (!slice) return "";
  return (start > 0 ? "…" : "") + slice + (end < body.length ? "…" : "");
}

// buildFolderTree constructs a sorted hierarchical tree from OKF docs and
// directory paths (including empty directories created with datafs.mkdir).
export function buildFolderTree(docs, dirs = []) {
  const root = { kind: "dir", path: "", name: "/", children: new Map(), docs: [] };

  function ensureDir(dirPath) {
    const clean = String(dirPath || "").replace(/^\/+|\/+$/g, "");
    if (!clean) return root;
    let cur = root;
    let acc = "";
    for (const seg of clean.split("/").filter(Boolean)) {
      acc = acc ? `${acc}/${seg}` : seg;
      if (!cur.children.has(seg)) {
        cur.children.set(seg, { kind: "dir", path: acc, name: seg, children: new Map(), docs: [] });
      }
      cur = cur.children.get(seg);
    }
    return cur;
  }

  for (const d of dirs || []) {
    if (isHiddenPath(d)) continue;
    ensureDir(d);
  }
  for (const doc of docs || []) {
    if (isHiddenPath(doc.path)) continue;
    const parts = doc.path.split("/");
    const fileName = parts.pop();
    const dirNode = ensureDir(parts.join("/"));
    dirNode.docs.push({ kind: "doc", path: doc.path, name: fileName, doc });
  }

  function finalize(node, depth) {
    const subDirs = [...node.children.values()]
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((child) => finalize(child, depth + 1));
    const files = node.docs
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((f) => ({ ...f, depth: depth + 1 }));
    const docCount = files.length + subDirs.reduce((sum, d) => sum + d.docCount, 0);
    return {
      kind: "dir",
      path: node.path,
      name: node.name,
      depth,
      docCount,
      dirs: subDirs,
      files,
    };
  }

  return finalize(root, -1);
}

// flattenFolderTree returns a flat ordered list of visible tree rows respecting
// collapsedDirs (object map or array of collapsed directory paths).
export function flattenFolderTree(tree, collapsedDirs = {}) {
  const collapsed = Array.isArray(collapsedDirs)
    ? new Set(collapsedDirs)
    : new Set(Object.keys(collapsedDirs || {}).filter((k) => collapsedDirs[k]));
  const rows = [];

  function walk(node) {
    for (const dir of node.dirs) {
      const isCollapsed = collapsed.has(dir.path);
      rows.push({
        kind: "dir",
        path: dir.path,
        name: dir.name,
        depth: dir.depth,
        docCount: dir.docCount,
        collapsed: isCollapsed,
      });
      if (!isCollapsed) {
        walk(dir);
      }
    }
    for (const f of node.files) {
      rows.push(f);
    }
  }

  walk(tree);
  return rows;
}

export function allDirPaths(tree) {
  const out = [];
  function walk(node) {
    for (const dir of node.dirs) {
      out.push(dir.path);
      walk(dir);
    }
  }
  walk(tree);
  return out;
}

function shortChartLabel(s, maxLen = 14) {
  const str = String(s || "").trim();
  if (str.length <= maxLen) return str;
  return str.slice(0, maxLen - 1) + "…";
}

// buildVaultAnalytics computes chart series, centrality rankings, tag
// frequencies, and health ratios for the OKF Graph & Analytics view.
export function buildVaultAnalytics(docs, brokenCount = 0) {
  const typeOrder = [...DOC_TYPES, "Index"];
  const typeMap = new Map(typeOrder.map((t) => [t, 0]));
  const statusCounts = { active: 0, needs_review: 0, deprecated: 0 };
  const tagMap = new Map();
  let connectedCount = 0;
  let totalLinks = 0;

  for (const d of docs || []) {
    const t = typeMap.has(d.type) ? d.type : "Concept";
    typeMap.set(t, (typeMap.get(t) || 0) + 1);
    if (statusCounts[d.status] !== undefined) statusCounts[d.status]++;
    for (const tag of d.tags || []) {
      tagMap.set(tag, (tagMap.get(tag) || 0) + 1);
    }
    totalLinks += (d.links || []).length;
    if ((d.links || []).length > 0 || (d.backlinks || []).length > 0) {
      connectedCount++;
    }
  }

  const typeStats = [...typeMap.entries()]
    .filter(([, count]) => count > 0)
    .map(([type, count]) => ({ type, label: shortChartLabel(type, 10), count }));

  const rankedHubs = [...(docs || [])]
    .map((d) => ({
      doc: d,
      out: (d.links || []).length,
      in: (d.backlinks || []).length,
      degree: (d.links || []).length + (d.backlinks || []).length,
    }))
    .sort((a, b) => b.degree - a.degree || b.in - a.in || a.doc.path.localeCompare(b.doc.path));

  const topHubs = rankedHubs.slice(0, 8).map((h) => ({
    ...h,
    label: shortChartLabel(h.doc.path.replace(/\.md$/, "").split("/").pop() || h.doc.title, 12),
  }));

  const tagStats = [...tagMap.entries()]
    .map(([tag, count]) => ({ tag, count }))
    .sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag));

  const totalDocs = (docs || []).length;
  const activeRatio = totalDocs > 0 ? statusCounts.active / totalDocs : 1;
  const connectedRatio = totalDocs > 0 ? connectedCount / totalDocs : 1;
  const linkHealthRatio = totalLinks + brokenCount > 0 ? totalLinks / (totalLinks + brokenCount) : 1;

  return {
    totalDocs,
    totalLinks,
    connectedCount,
    statusCounts,
    typeStats,
    rankedHubs,
    topHubs,
    tagStats,
    activeRatio,
    connectedRatio,
    linkHealthRatio,
  };
}

// buildKnowledgeGraph builds declarative ui.graph nodes and edges from OKF
// documents, respecting uiblock bounds (<=120 nodes, <=400 edges) and adding
// synthetic danger nodes for broken wiki-links so dashed edges resolve cleanly.
export function buildKnowledgeGraph(docs, opts = {}) {
  const maxNodes = 120;
  const maxEdges = 400;
  const typeTone = opts.typeTone || (() => "muted");
  const labelType = opts.labelType || ((t) => t);
  const missingGroup = truncateUtf8Bytes(opts.missingGroup || "Missing", 48);
  const missingSubtitle = truncateUtf8Bytes(opts.missingSubtitle || "Broken wiki-link target", 120);

  const ranked = [...(docs || [])].sort(
    (a, b) =>
      ((b.links?.length || 0) + (b.backlinks?.length || 0)) -
        ((a.links?.length || 0) + (a.backlinks?.length || 0)) ||
      a.path.localeCompare(b.path),
  );

  const nodes = [];
  const nodeIds = new Set();
  const includedDocs = [];

  for (const doc of ranked) {
    if (nodes.length >= maxNodes) break;
    const id = truncateUtf8Bytes(doc.path, 128);
    if (!id || nodeIds.has(id)) continue;
    const outCount = (doc.links || []).length;
    const inCount = (doc.backlinks || []).length;
    nodeIds.add(id);
    includedDocs.push({ doc, id });
    nodes.push({
      id,
      label: truncateUtf8Bytes(doc.title || doc.path, 64) || id,
      group: truncateUtf8Bytes(labelType(doc.type || "Concept"), 48),
      tone: typeTone(doc.type),
      subtitle: truncateUtf8Bytes(`${doc.path} · ↗${outCount} ↙${inCount}`, 120),
      weight: 1 + outCount + inCount,
    });
  }

  const missingByRaw = new Map();
  const missingByNodeId = new Map();
  for (const { doc } of includedDocs) {
    for (const rawTarget of doc.brokenLinks || []) {
      const clean = String(rawTarget || "").trim();
      if (!clean) continue;
      let entry = missingByRaw.get(clean);
      if (!entry) {
        const missingId = truncateUtf8Bytes(`missing:${clean}`, 128);
        if (!missingId || nodeIds.has(missingId) || nodes.length >= maxNodes) continue;
        nodeIds.add(missingId);
        nodes.push({
          id: missingId,
          label: truncateUtf8Bytes(clean, 64) || clean,
          group: missingGroup,
          tone: "danger",
          subtitle: missingSubtitle,
          weight: 0.8,
        });
        entry = { id: missingId, target: clean, refs: [] };
        missingByRaw.set(clean, entry);
        missingByNodeId.set(missingId, entry);
      }
      if (!entry.refs.includes(doc.path)) {
        entry.refs.push(doc.path);
      }
    }
  }

  const edges = [];
  const edgeKeys = new Set();

  for (const { doc, id: fromId } of includedDocs) {
    const tone = typeTone(doc.type);
    for (const targetPath of doc.links || []) {
      if (edges.length >= maxEdges) break;
      const targetId = truncateUtf8Bytes(targetPath, 128);
      if (!targetId || !nodeIds.has(targetId) || fromId === targetId) continue;
      const ek = `${fromId}\0${targetId}\0ok`;
      if (edgeKeys.has(ek)) continue;
      edgeKeys.add(ek);
      edges.push({ from: fromId, to: targetId, label: "", tone, dashed: false });
    }
    for (const rawTarget of doc.brokenLinks || []) {
      if (edges.length >= maxEdges) break;
      const missing = missingByRaw.get(String(rawTarget || "").trim());
      if (!missing || !nodeIds.has(missing.id)) continue;
      const ek = `${fromId}\0${missing.id}\0broken`;
      if (edgeKeys.has(ek)) continue;
      edgeKeys.add(ek);
      edges.push({ from: fromId, to: missing.id, label: "", tone: "danger", dashed: true });
    }
  }

  return { nodes, edges, nodeIds, missingByNodeId };
}

// renderAsciiGraph renders either a 2-hop neighborhood map around focusPath or
// a full-vault adjacency map in clean Unicode tree syntax.
export function renderAsciiGraph(docs, focusPath = "", mode = "focus") {
  if (!docs || docs.length === 0) {
    return "(empty vault)";
  }
  const byPath = new Map(docs.map((d) => [d.path, d]));

  if (mode === "full") {
    const lines = [];
    const grouped = new Map();
    for (const d of [...docs].sort((a, b) => a.path.localeCompare(b.path))) {
      const folder = d.path.includes("/") ? d.path.split("/")[0] : "/";
      if (!grouped.has(folder)) grouped.set(folder, []);
      grouped.get(folder).push(d);
    }
    for (const [folder, list] of grouped.entries()) {
      lines.push(`[${folder}]`);
      list.forEach((d, idx) => {
        const lastDoc = idx === list.length - 1;
        const prefix = lastDoc ? "└─" : "├─";
        const cont = lastDoc ? "   " : "│  ";
        lines.push(`${prefix} ${d.title} (${d.path})  ↗${d.links.length} ↙${d.backlinks.length}`);
        const edges = [
          ...d.links.map((p) => ({ ok: true, label: byPath.get(p)?.title || p, path: p })),
          ...d.brokenLinks.map((raw) => ({ ok: false, label: `[[${raw}]]`, path: "broken" })),
        ];
        edges.slice(0, 6).forEach((e, ei) => {
          const isLast = ei === Math.min(edges.length, 6) - 1 && edges.length <= 6;
          const arrow = e.ok ? "─►" : "─✗";
          lines.push(`${cont}${isLast ? "└" : "├"}${arrow} ${e.label}`);
        });
        if (edges.length > 6) {
          lines.push(`${cont}└─ … +${edges.length - 6} more`);
        }
      });
      lines.push("");
    }
    return lines.join("\n").trim();
  }

  let focus = focusPath ? byPath.get(focusPath) : null;
  if (!focus) {
    focus = [...docs].sort(
      (a, b) => (b.links.length + b.backlinks.length) - (a.links.length + a.backlinks.length) || a.path.localeCompare(b.path),
    )[0];
  }
  const lines = [];
  if (focus.backlinks.length > 0) {
    lines.push(`Incoming (${focus.backlinks.length})`);
    focus.backlinks.forEach((p, i) => {
      const src = byPath.get(p);
      const branch = i === focus.backlinks.length - 1 ? "└─" : "├─";
      lines.push(`  ${branch} [${src?.type || "Doc"}] ${src?.title || p}`);
    });
    lines.push("       │");
    lines.push("       ▼");
  } else {
    lines.push("Incoming (0)");
  }

  lines.push(`★ [${focus.type}] ${focus.title}`);
  lines.push(`  ${focus.path}  (${focus.status} · ↗${focus.links.length} ↙${focus.backlinks.length})`);

  const outItems = [
    ...focus.links.map((p) => ({ ok: true, doc: byPath.get(p), path: p })),
    ...focus.brokenLinks.map((raw) => ({ ok: false, raw })),
  ];
  if (outItems.length > 0) {
    lines.push("       │");
    outItems.forEach((item, idx) => {
      const isLast = idx === outItems.length - 1;
      const branch = isLast ? "└" : "├";
      const pad = isLast ? "  " : "│ ";
      if (!item.ok) {
        lines.push(`       ${branch}─✗ [[${item.raw}]] (broken)`);
        return;
      }
      const target = item.doc;
      lines.push(`       ${branch}─► [${target?.type || "Doc"}] ${target?.title || item.path}`);
      if (target && target.links.length > 0) {
        const secondHop = target.links.filter((p) => p !== focus.path && !focus.links.includes(p)).slice(0, 3);
        secondHop.forEach((p2, j) => {
          const hopDoc = byPath.get(p2);
          const hopBranch = j === secondHop.length - 1 ? "└" : "├";
          lines.push(`       ${pad}   ${hopBranch}─► ${hopDoc?.title || p2}`);
        });
      }
    });
  } else {
    lines.push("  (no outgoing links)");
  }
  return lines.join("\n");
}
