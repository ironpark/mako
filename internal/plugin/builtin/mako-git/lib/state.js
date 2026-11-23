// Per-folder-instance state and pure helpers for mako-git.

export function initialState() {
  return {
    status: null,
    statusErr: "",
    root: "",
    base: "",
    history: null,
    fingerprint: "",
    historyErr: "",
    forge: null,
    forgeKey: "",
    forgeBranch: "",
    forgeAt: 0,
    forgeForce: false,
    commit: { hash: "", info: null, loading: false, err: "" },
    skip: {},
    message: "",
    amend: false,
    busy: "",
    note: { text: "", err: false },
    open: { changes: true, stashes: false, branches: false, history: true },
    closed: {},
    dialog: null,
    jobs: {},
  };
}

export function isRepo(s) {
  return Boolean(s && s.status && s.status.root && !s.status.notRepo);
}

export function folderName(path) {
  const p = String(path || "").replace(/[/\\]+$/, "");
  if (!p) return "";
  const idx = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  return idx >= 0 ? p.slice(idx + 1) : p;
}

export function joinRepoPath(root, rel) {
  const base = String(root || "").replace(/[/\\]+$/, "");
  const child = String(rel || "").replace(/^[/\\]+/, "");
  if (!base) return child;
  if (!child) return base;
  return `${base}/${child}`;
}

export function openDialog(s, dlg) {
  s.dialog = dlg;
}

export function closeDialog(s) {
  s.dialog = null;
}

export function setBusy(s, label) {
  s.busy = String(label || "");
}

export function setNote(s, text, err = false) {
  s.note = { text: String(text || ""), err: Boolean(err) };
}

export function clearNote(s) {
  s.note = { text: "", err: false };
}

export function resetForNewRepo(s, nextRoot) {
  s.root = nextRoot || "";
  s.base = "";
  s.skip = {};
  s.message = "";
  s.amend = false;
  s.closed = {};
  s.fingerprint = "";
  s.history = null;
  s.historyErr = "";
  s.forge = null;
  s.forgeKey = "";
  s.forgeBranch = "";
  s.forgeAt = 0;
  s.forgeForce = false;
  s.commit = { hash: "", info: null, loading: false, err: "" };
  s.dialog = null;
}

// pickedCount returns how many changed files are included in the next commit.
export function pickedCount(files, skip) {
  const list = files || [];
  const sk = skip || {};
  let n = 0;
  for (const f of list) {
    if (!sk[f.path]) n++;
  }
  return n;
}

// canCommit matches native gitops.go:178.
export function canCommit(count, msg, amend) {
  return Boolean(amend) || (String(msg || "").trim() !== "" && count > 0);
}

// changedPaths collects each file's path plus its rename origin ("from").
export function changedPaths(files) {
  const out = [];
  for (const f of files || []) {
    if (f.path) out.push(f.path);
    if (f.from) out.push(f.from);
  }
  return out;
}

// commitPaths returns the paths for a commit of picked files, or undefined when
// all files are picked (matching native gitops.go:131).
export function commitPaths(files, skip) {
  const list = files || [];
  const sk = skip || {};
  if (pickedCount(list, sk) === list.length) {
    return undefined;
  }
  const paths = [];
  for (const f of list) {
    if (sk[f.path]) continue;
    if (f.path) paths.push(f.path);
    if (f.from) paths.push(f.from);
  }
  return paths;
}

// safeKey builds a deterministic, collision-free UI node key containing no
// "/" or "#" and bounded well below the 128-byte uiblock limit.
export function safeKey(prefix, raw) {
  const str = String(raw || "");
  let h = 0x811c9dc5;
  for (let i = 0; i < str.length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  const slug = str.replace(/[^A-Za-z0-9._-]/g, "_").slice(0, 64);
  return `${prefix}-${slug}-${h.toString(16)}`;
}

// truncateRunes bounds s to at most max Unicode code points.
export function truncateRunes(str, max) {
  const s = String(str || "");
  if (s.length <= max) return s;
  const chars = [...s];
  if (chars.length <= max) return s;
  return max > 1 ? chars.slice(0, max - 1).join("") + "…" : chars.slice(0, max).join("");
}

// sanitizeText removes C0 control characters (except \n and \t) and BiDi
// reordering marks so untrusted git subjects/paths never fail uiblock checks.
export function sanitizeText(str, allowNewline = false) {
  const s = String(str || "");
  let out = "";
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20) {
      if (allowNewline && (c === 0x0a || c === 0x09)) out += s[i];
      continue;
    }
    if (c === 0x7f || (c >= 0x80 && c <= 0x9f)) continue;
    if ((c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069) || c === 0x200e || c === 0x200f || c === 0x061c) {
      continue;
    }
    out += s[i];
  }
  return out;
}
