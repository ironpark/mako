// Pure JS Open Knowledge Format (OKF v0.2) parser, serializer, graph builder,
// and linter operating on standard Markdown files with YAML frontmatter.

export const DOC_TYPES = ["Concept", "Decision", "Architecture", "Procedure", "Reference"];
export const DOC_STATUSES = ["active", "needs_review", "deprecated"];
export const FOLDERS = ["concepts", "decisions", "architecture", "procedures", "reference"];

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
export function buildVaultIndex(docs) {
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
      missingMeta.push({ path: d.path, reason: !d.hasFrontmatter ? "Missing YAML frontmatter" : "Missing title or type" });
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
  const nonIndex = docs
    .filter((d) => d.path !== "index.md")
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
      "- Ask an AI agent to document key architecture decisions via `plugin_okf_upsert_doc`.",
      "- Link related documents with `[[concepts/example]]` to build a navigable graph.",
    ].join("\n"),
  });
}

export function searchDocuments(docs, query, typeFilter = "", tagFilter = "") {
  const q = String(query || "").trim().toLowerCase();
  const tf = String(typeFilter || "").trim().toLowerCase();
  const tgf = String(tagFilter || "").trim().toLowerCase();
  const results = [];

  for (const d of docs) {
    if (tf && tf !== "all" && d.type.toLowerCase() !== tf) continue;
    if (tgf && !d.tags.some((t) => t.toLowerCase() === tgf)) continue;
    if (!q) {
      results.push({ doc: d, score: 1, snippet: d.description || firstSnippet(d.body) });
      continue;
    }
    let score = 0;
    if (d.title.toLowerCase().includes(q)) score += 10;
    if (d.path.toLowerCase().includes(q)) score += 6;
    if (d.description.toLowerCase().includes(q)) score += 5;
    if (d.tags.some((t) => t.toLowerCase().includes(q))) score += 4;
    const bodyLower = d.body.toLowerCase();
    const pos = bodyLower.indexOf(q);
    if (pos >= 0) score += 2;
    if (score > 0) {
      const snippet = pos >= 0 ? excerptAround(d.body, pos, q.length) : (d.description || firstSnippet(d.body));
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
    .find((l) => l && !l.startsWith("#"));
  return line ? line.slice(0, 140) : "";
}

function excerptAround(body, pos, len) {
  const start = Math.max(0, pos - 40);
  const end = Math.min(body.length, pos + len + 80);
  const slice = body.slice(start, end).replace(/\s+/g, " ").trim();
  return (start > 0 ? "…" : "") + slice + (end < body.length ? "…" : "");
}
