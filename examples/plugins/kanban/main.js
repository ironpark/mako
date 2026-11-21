// Kanban board plugin for mako (v0.3.0, project-scoped):
// - Project-scoped: each project has its own isolated Kanban board and storage.
// - Drag cards onto any column to move them, or onto another card to reorder.
// - Explicit ui.card, per-column ui.scroll, inline ui.textarea note editing,
//   ui.tabs compact switcher, dragGroup/dropAccept filtering, and live form
//   value reading via mako.blocks.values("board").
// - Exposes project-scoped MCP tools (plugin_kanban_list_cards, add_card,
//   move_card) so running agents read and update only their own project's board.

const STORAGE_KEY = "board:v1";

const COLUMNS = [
  { id: "todo", title: "Todo", tone: "muted", icon: "circle" },
  { id: "doing", title: "In Progress", tone: "accent", icon: "play" },
  { id: "review", title: "Review", tone: "warning", icon: "eye" },
  { id: "done", title: "Done", tone: "success", icon: "circle-check" },
];

const PRIORITIES = ["low", "medium", "high"];
const PRIORITY_TONES = { low: "faint", medium: "accent", high: "danger" };
const STATUS_TONES = { working: "accent", waiting: "warning", idle: "muted", done: "success" };

let mako;

function defaultCards() {
  return [
    { id: "c1", col: "todo", title: "Draft API spec", priority: "high", tag: "design", note: "Outline request/response fields before coding.", sessionId: "" },
    { id: "c2", col: "todo", title: "Add integration tests", priority: "medium", tag: "qa", note: "Cover drag-and-drop and persistence flows.", sessionId: "" },
    { id: "c3", col: "doing", title: "Build Kanban prototype", priority: "high", tag: "plugin", note: "Drag cards between columns or onto another card to reorder.", sessionId: "" },
    { id: "c4", col: "review", title: "Polish mobile transitions", priority: "medium", tag: "android", note: "Verify back navigation animation feels crisp.", sessionId: "" },
    { id: "c5", col: "done", title: "Enable plugin drag & drop", priority: "low", tag: "core", note: "Added drag and onDrop to the UI block vocabulary.", sessionId: "" },
  ];
}

function nextCardId(cards) {
  let max = 0;
  for (const c of cards) {
    const m = /^c(\d+)$/.exec(c.id);
    if (m) max = Math.max(max, Number(m[1]));
  }
  return `c${max + 1}`;
}

async function saveBoard(s, ctx) {
  const store = ctx && ctx.storage ? ctx.storage : mako.blocks.context("board").storage;
  await store.setItem(
    STORAGE_KEY,
    JSON.stringify({ cards: s.cards, viewMode: s.viewMode }),
  );
}

async function refreshSessions(s) {
  try {
    const res = await mako.sessions.listSessions();
    s.sessions = (res.sessions || [])
      .filter((x) => x.state === "running")
      .map((x) => ({
        id: x.id,
        name: x.name || x.id.slice(0, 8),
        agent: x.agent,
        status: x.status,
      }));
  } catch {
    s.sessions = [];
  }
}

async function load(s, ctx) {
  const modeSetting = await ctx.settings.getSetting("view_mode");
  const fallbackMode = modeSetting.setting && modeSetting.setting.text ? modeSetting.setting.text : "board";
  const stored = await ctx.storage.getItem(STORAGE_KEY);
  if (stored.found) {
    try {
      const parsed = JSON.parse(stored.value);
      if (Array.isArray(parsed.cards)) {
        s.cards = parsed.cards;
      }
      s.viewMode = parsed.viewMode || fallbackMode;
    } catch {
      s.cards = defaultCards();
      s.viewMode = fallbackMode;
    }
  } else {
    s.cards = defaultCards();
    s.viewMode = fallbackMode;
    await saveBoard(s, ctx);
  }
  await refreshSessions(s);
}

function moveCard(s, cardId, targetCol, beforeCardId) {
  const idx = s.cards.findIndex((c) => c.id === cardId);
  if (idx < 0) return false;
  if (beforeCardId && beforeCardId === cardId) return false;
  const [card] = s.cards.splice(idx, 1);
  card.col = targetCol;
  if (beforeCardId) {
    const targetIdx = s.cards.findIndex((c) => c.id === beforeCardId);
    if (targetIdx >= 0) {
      s.cards.splice(targetIdx, 0, card);
      return true;
    }
  }
  s.cards.push(card);
  return true;
}

function sessionFor(s, sessionId) {
  if (!sessionId) return null;
  return (s.sessions || []).find((x) => x.id === sessionId) || null;
}

function renderCard(s, ui, card) {
  const colIdx = COLUMNS.findIndex((c) => c.id === card.col);
  const selected = s.selectedId === card.id;
  const sess = sessionFor(s, card.sessionId);

  const meta = [
    ui.pill(card.priority, { tone: PRIORITY_TONES[card.priority] || "muted" }),
  ];
  if (card.tag) meta.push(ui.chip(card.tag));
  if (sess) {
    meta.push(ui.pill(`${sess.name} · ${sess.status}`, { tone: STATUS_TONES[sess.status] || "muted" }));
  }

  const body = [
    ui.row([
      ui.text(card.title, { weight: selected ? "semibold" : "medium" }).grow(),
      ui.icon("grip-vertical", { size: "small", tone: "faint" }),
    ], { gap: 6 }),
    ui.row(meta, { gap: 6 }),
  ];

  if (selected) {
    body.push(ui.textarea(card.note || "", {
      key: `note-${card.id}`,
      placeholder: "Add notes or acceptance criteria…",
      onChange: ["saveNote", card.id],
    }));
    const actions = [];
    if (colIdx > 0) {
      actions.push(ui.button("←", { key: "prev", onTap: ["step", card.id, -1] }));
    }
    if (colIdx >= 0 && colIdx < COLUMNS.length - 1) {
      actions.push(ui.button("→", { key: "next", onTap: ["step", card.id, 1] }));
    }
    actions.push(ui.button("Priority", { key: "prio", onTap: ["cyclePriority", card.id] }));
    if (s.sessions && s.sessions.length > 0) {
      actions.push(ui.button("Send to agent", { key: "send", icon: "send", tone: "accent", onTap: ["dispatch", card.id] }));
    }
    actions.push(ui.button("Delete", { key: "del", icon: "trash-2", tone: "danger", onTap: ["remove", card.id] }));
    body.push(ui.divider());
    body.push(ui.row(actions, { gap: 6 }));
  }

  return ui.card(body, {
    key: card.id,
    gap: 6,
    drag: card.id,
    dragGroup: "card",
    dropAccept: ["card"],
    onTap: ["select", card.id],
    onDrop: ["moveBefore", card.col, card.id],
  });
}

function renderColumn(s, ui, col) {
  const cards = s.cards.filter((c) => c.col === col.id);
  const listItems = cards.length === 0
    ? [ui.text("Drop a card here", { key: "empty", size: "small", tone: "faint" })]
    : cards.map((card) => renderCard(s, ui, card));

  return ui.column([
    ui.row([
      ui.icon(col.icon, { size: "small", tone: col.tone }),
      ui.text(col.title, { weight: "semibold" }).grow(),
      ui.pill(String(cards.length), { tone: col.tone }),
    ], { key: "head", gap: 6 }),
    ui.divider({ key: "div" }),
    ui.scroll(listItems, { key: "cards", gap: 8, maxHeight: 420 }),
  ], {
    key: `col-${col.id}`,
    gap: 8,
    grow: 1,
    dropAccept: ["card"],
    onDrop: ["moveToColumn", col.id],
  });
}

function render(s, ui, ctx) {
  const total = s.cards.length;
  const doneCount = s.cards.filter((c) => c.col === "done").length;
  const progress = total > 0 ? doneCount / total : 0;
  const heading = ctx && ctx.projectName
    ? `Kanban · ${ctx.projectName} · ${doneCount}/${total} done`
    : `Kanban · ${doneCount}/${total} done`;

  const header = ui.row([
    ui.icon("columns-3", { tone: "accent" }),
    ui.text(heading, { weight: "semibold" }).grow(),
    ui.button(s.viewMode === "compact" ? "Board" : "Compact", {
      key: "mode",
      onTap: ["toggleView"],
    }),
  ], { key: "header", gap: 8 });

  const addBar = ui.row([
    ui.input(s.draft, {
      key: "draft",
      placeholder: "Add a card to Todo…",
      grow: 1,
      onChange: ["setDraft"],
      onSubmit: ["addCard"],
    }),
    ui.select(s.draftPriority, PRIORITIES, {
      key: "draft-priority",
      onChange: ["setDraftPriority"],
    }),
    ui.button("Add", {
      key: "add-btn",
      icon: "plus",
      tone: "accent",
      onTap: ["addFromButton"],
    }),
  ], { key: "addbar", gap: 8 });

  const rows = [header, ui.progress(progress, { key: "progress" }), addBar];
  if (s.notice) {
    rows.push(ui.text(s.notice, { key: "notice", size: "small", tone: "accent" }));
  }

  if (s.viewMode === "compact") {
    const activeCol = COLUMNS.find((c) => c.id === s.activeCol) || COLUMNS[0];
    const tabLabels = COLUMNS.map((c) => {
      const count = s.cards.filter((x) => x.col === c.id).length;
      return `${c.title} (${count})`;
    });
    const activeLabel = tabLabels[COLUMNS.indexOf(activeCol)] || tabLabels[0];
    rows.push(ui.tabs(activeLabel, tabLabels, { key: "col-tabs", onChange: ["pickColLabel"] }));
    rows.push(renderColumn(s, ui, activeCol));
  } else {
    rows.push(ui.row(COLUMNS.map((c) => renderColumn(s, ui, c)), { key: "columns", gap: 10 }));
  }

  return ui.column(rows, { gap: 10 });
}

export default {
  activate(m) {
    mako = m;
    mako.sessions.subscribe({ events: ["sessionStatus", "sessionStarted", "sessionEnded"] }).catch(() => {});

    mako.blocks.register("board", {
      state: {
        cards: [],
        draft: "",
        draftPriority: "medium",
        selectedId: "",
        activeCol: "todo",
        viewMode: "board",
        sessions: [],
        notice: "",
      },
      refresh: 30,
      load,
      render,
      on: {
        setDraft(s, value) {
          s.draft = value;
        },
        setDraftPriority(s, value) {
          if (PRIORITIES.includes(value)) s.draftPriority = value;
        },
        async addCard(s, text) {
          const ctx = mako.blocks.context("board");
          const title = String(text ?? s.draft).trim();
          if (!title) return;
          const card = {
            id: nextCardId(s.cards),
            col: s.viewMode === "compact" ? s.activeCol : "todo",
            title,
            priority: s.draftPriority || "medium",
            tag: "",
            note: "",
            sessionId: "",
          };
          s.cards.unshift(card);
          s.draft = "";
          s.notice = `Added "${title}".`;
          await saveBoard(s, ctx);
          await mako.log.write({ level: "info", message: `added card ${card.id}: ${title}` });
        },
        async addFromButton(s) {
          const ctx = mako.blocks.context("board");
          const vals = mako.blocks.values("board", ctx.scopeKey);
          const title = String(vals.draft ?? s.draft ?? "").trim();
          if (!title) return;
          const card = {
            id: nextCardId(s.cards),
            col: s.viewMode === "compact" ? s.activeCol : "todo",
            title,
            priority: vals["draft-priority"] || s.draftPriority || "medium",
            tag: "",
            note: "",
            sessionId: "",
          };
          s.cards.unshift(card);
          s.draft = "";
          s.notice = `Added "${title}".`;
          await saveBoard(s, ctx);
          await mako.log.write({ level: "info", message: `added card ${card.id}: ${title}` });
        },
        select(s, cardId) {
          s.selectedId = s.selectedId === cardId ? "" : cardId;
          s.notice = "";
        },
        async saveNote(s, cardId, note) {
          const ctx = mako.blocks.context("board");
          const card = s.cards.find((c) => c.id === cardId);
          if (!card) return;
          card.note = String(note ?? "");
          await saveBoard(s, ctx);
        },
        pickColLabel(s, label) {
          const col = COLUMNS.find((c) => String(label).startsWith(c.title));
          if (col) s.activeCol = col.id;
        },
        async toggleView(s) {
          const ctx = mako.blocks.context("board");
          s.viewMode = s.viewMode === "compact" ? "board" : "compact";
          await saveBoard(s, ctx);
        },
        async moveToColumn(s, colId, draggedId) {
          const ctx = mako.blocks.context("board");
          if (!COLUMNS.some((c) => c.id === colId)) return;
          if (moveCard(s, draggedId, colId, "")) {
            const col = COLUMNS.find((c) => c.id === colId);
            s.notice = `Moved card to ${col ? col.title : colId}.`;
            await saveBoard(s, ctx);
            await mako.log.write({ level: "info", message: `moved ${draggedId} -> ${colId}` });
          }
        },
        async moveBefore(s, colId, beforeCardId, draggedId) {
          const ctx = mako.blocks.context("board");
          if (!COLUMNS.some((c) => c.id === colId)) return;
          if (moveCard(s, draggedId, colId, beforeCardId)) {
            s.notice = "";
            await saveBoard(s, ctx);
          }
        },
        async step(s, cardId, delta) {
          const ctx = mako.blocks.context("board");
          const card = s.cards.find((c) => c.id === cardId);
          if (!card) return;
          const idx = COLUMNS.findIndex((c) => c.id === card.col);
          const next = COLUMNS[idx + delta];
          if (!next) return;
          card.col = next.id;
          s.notice = `Moved "${card.title}" to ${next.title}.`;
          await saveBoard(s, ctx);
        },
        async cyclePriority(s, cardId) {
          const ctx = mako.blocks.context("board");
          const card = s.cards.find((c) => c.id === cardId);
          if (!card) return;
          const idx = PRIORITIES.indexOf(card.priority);
          card.priority = PRIORITIES[(idx + 1) % PRIORITIES.length];
          await saveBoard(s, ctx);
        },
        async remove(s, cardId) {
          const ctx = mako.blocks.context("board");
          const idx = s.cards.findIndex((c) => c.id === cardId);
          if (idx < 0) return;
          const [removed] = s.cards.splice(idx, 1);
          if (s.selectedId === cardId) s.selectedId = "";
          s.notice = `Removed "${removed.title}".`;
          await saveBoard(s, ctx);
        },
        async dispatch(s, cardId) {
          const ctx = mako.blocks.context("board");
          const card = s.cards.find((c) => c.id === cardId);
          if (!card) return;
          await refreshSessions(s);
          const target = s.sessions.find((x) => x.status === "idle") || s.sessions[0];
          if (!target) {
            s.notice = "No running agent session available on this node.";
            return;
          }
          const prompt = card.note ? `Kanban task: ${card.title}\n\n${card.note}` : `Kanban task: ${card.title}`;
          await mako.messages.sendMessage({ sessionId: target.id, text: prompt });
          card.sessionId = target.id;
          if (card.col === "todo") card.col = "doing";
          s.notice = `Sent "${card.title}" to ${target.name}.`;
          await mako.commands.runCommand({ id: "mako.toast", arg: s.notice });
          await saveBoard(s, ctx);
        },
      },
    });

    mako.commands.register("reset", async (ctx) => {
      const s = mako.blocks.state("board", ctx.scopeKey);
      s.cards = defaultCards();
      s.selectedId = "";
      s.notice = "Reset board to demo cards.";
      await mako.commands.runCommand({ id: "mako.toast", arg: "Kanban reset to demo cards" });
      await saveBoard(s, ctx);
    });

    mako.commands.register("toggle-view", async (ctx) => {
      const s = mako.blocks.state("board", ctx.scopeKey);
      s.viewMode = s.viewMode === "compact" ? "board" : "compact";
      await saveBoard(s, ctx);
    });

    // Project-scoped MCP tools exposed to running agents on this node.
    mako.tools.register("list_cards", (args, ctx) => {
      const s = mako.blocks.state("board", ctx.scopeKey);
      const col = args && args.column ? String(args.column) : "";
      const cards = col ? s.cards.filter((c) => c.col === col) : s.cards;
      return { project: ctx.projectName || ctx.projectId, columns: COLUMNS.map((c) => c.id), cards };
    });

    mako.tools.register("add_card", async (args, ctx) => {
      const s = mako.blocks.state("board", ctx.scopeKey);
      const title = String((args && args.title) || "").trim();
      if (!title) throw new Error("title is required");
      const col = COLUMNS.some((c) => c.id === (args && args.column)) ? args.column : "todo";
      const priority = PRIORITIES.includes(args && args.priority) ? args.priority : "medium";
      const card = {
        id: nextCardId(s.cards),
        col,
        title,
        priority,
        tag: String((args && args.tag) || ""),
        note: String((args && args.note) || ""),
        sessionId: "",
      };
      s.cards.unshift(card);
      await saveBoard(s, ctx);
      await mako.log.write({ level: "info", message: `agent added card ${card.id}: ${title}` });
      return card;
    });

    mako.tools.register("move_card", async (args, ctx) => {
      const s = mako.blocks.state("board", ctx.scopeKey);
      const id = String((args && args.id) || "");
      const col = String((args && args.column) || "");
      if (!COLUMNS.some((c) => c.id === col)) {
        throw new Error(`unknown column ${col}`);
      }
      if (!moveCard(s, id, col, "")) {
        throw new Error(`card ${id} not found`);
      }
      await saveBoard(s, ctx);
      await mako.log.write({ level: "info", message: `agent moved card ${id} -> ${col}` });
      return { moved: true, id, column: col };
    });
  },
  on: {
    async sessionStatus(ev) {
      const s = mako.blocks.state("board");
      await refreshSessions(s);
      if (ev.session && ev.session.status === "done") {
        let changed = false;
        for (const card of s.cards) {
          if (card.sessionId === ev.session.id && card.col === "doing") {
            card.col = "review";
            changed = true;
          }
        }
        if (changed) await saveBoard(s, mako.blocks.context("board"));
      }
    },
    async sessionStarted() {
      await refreshSessions(mako.blocks.state("board"));
    },
    async sessionEnded() {
      await refreshSessions(mako.blocks.state("board"));
    },
  },
};
