// Kanban board plugin for mako:
// - Drag cards onto any column to move them, or onto another card to reorder.
// - Click a card to inspect details, adjust priority, move with buttons, or
//   dispatch it directly to a running agent session.
// - Persists board state in mako.storage and reflects live session status.

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

async function saveBoard(s) {
  await mako.storage.setItem({
    key: STORAGE_KEY,
    value: JSON.stringify({ cards: s.cards, viewMode: s.viewMode }),
  });
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

async function load(s) {
  const modeSetting = await mako.settings.getSetting({ key: "view_mode" });
  const fallbackMode = modeSetting.setting && modeSetting.setting.text ? modeSetting.setting.text : "board";
  const stored = await mako.storage.getItem({ key: STORAGE_KEY });
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
    await saveBoard(s);
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
    if (card.note) {
      body.push(ui.text(card.note, { size: "small", tone: "muted" }));
    }
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

  return ui.column(body, {
    key: card.id,
    gap: 6,
    drag: card.id,
    onTap: ["select", card.id],
    onDrop: ["moveBefore", card.col, card.id],
  });
}

function renderColumn(s, ui, col) {
  const cards = s.cards.filter((c) => c.col === col.id);
  const children = [
    ui.row([
      ui.icon(col.icon, { size: "small", tone: col.tone }),
      ui.text(col.title, { weight: "semibold" }).grow(),
      ui.pill(String(cards.length), { tone: col.tone }),
    ], { key: "head", gap: 6 }),
    ui.divider({ key: "div" }),
  ];
  if (cards.length === 0) {
    children.push(ui.text("Drop a card here", { key: "empty", size: "small", tone: "faint" }));
  } else {
    for (const card of cards) {
      children.push(renderCard(s, ui, card));
    }
  }
  return ui.column(children, {
    key: `col-${col.id}`,
    gap: 8,
    grow: 1,
    onDrop: ["moveToColumn", col.id],
  });
}

function render(s, ui) {
  const total = s.cards.length;
  const doneCount = s.cards.filter((c) => c.col === "done").length;
  const progress = total > 0 ? doneCount / total : 0;

  const header = ui.row([
    ui.icon("columns-3", { tone: "accent" }),
    ui.text(`Kanban · ${doneCount}/${total} done`, { weight: "semibold" }).grow(),
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
    const tabs = COLUMNS.map((c) => {
      const count = s.cards.filter((x) => x.col === c.id).length;
      return ui.button(`${c.title} (${count})`, {
        key: `tab-${c.id}`,
        tone: c.id === activeCol.id ? "accent" : "",
        onTap: ["pickCol", c.id],
        onDrop: ["moveToColumn", c.id],
      });
    });
    rows.push(ui.row(tabs, { key: "tabs", gap: 6 }));
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
          const title = String(text ?? s.draft).trim();
          if (!title) return;
          s.cards.unshift({
            id: nextCardId(s.cards),
            col: s.viewMode === "compact" ? s.activeCol : "todo",
            title,
            priority: s.draftPriority || "medium",
            tag: "",
            note: "",
            sessionId: "",
          });
          s.draft = "";
          s.notice = `Added "${title}".`;
          await saveBoard(s);
        },
        async addFromButton(s) {
          const title = String(s.draft || "").trim();
          if (!title) return;
          s.cards.unshift({
            id: nextCardId(s.cards),
            col: s.viewMode === "compact" ? s.activeCol : "todo",
            title,
            priority: s.draftPriority || "medium",
            tag: "",
            note: "",
            sessionId: "",
          });
          s.draft = "";
          s.notice = `Added "${title}".`;
          await saveBoard(s);
        },
        select(s, cardId) {
          s.selectedId = s.selectedId === cardId ? "" : cardId;
          s.notice = "";
        },
        pickCol(s, colId) {
          if (COLUMNS.some((c) => c.id === colId)) s.activeCol = colId;
        },
        async toggleView(s) {
          s.viewMode = s.viewMode === "compact" ? "board" : "compact";
          await saveBoard(s);
        },
        async moveToColumn(s, colId, draggedId) {
          if (!COLUMNS.some((c) => c.id === colId)) return;
          if (moveCard(s, draggedId, colId, "")) {
            const col = COLUMNS.find((c) => c.id === colId);
            s.notice = `Moved card to ${col ? col.title : colId}.`;
            await saveBoard(s);
          }
        },
        async moveBefore(s, colId, beforeCardId, draggedId) {
          if (!COLUMNS.some((c) => c.id === colId)) return;
          if (moveCard(s, draggedId, colId, beforeCardId)) {
            s.notice = "";
            await saveBoard(s);
          }
        },
        async step(s, cardId, delta) {
          const card = s.cards.find((c) => c.id === cardId);
          if (!card) return;
          const idx = COLUMNS.findIndex((c) => c.id === card.col);
          const next = COLUMNS[idx + delta];
          if (!next) return;
          card.col = next.id;
          s.notice = `Moved "${card.title}" to ${next.title}.`;
          await saveBoard(s);
        },
        async cyclePriority(s, cardId) {
          const card = s.cards.find((c) => c.id === cardId);
          if (!card) return;
          const idx = PRIORITIES.indexOf(card.priority);
          card.priority = PRIORITIES[(idx + 1) % PRIORITIES.length];
          await saveBoard(s);
        },
        async remove(s, cardId) {
          const idx = s.cards.findIndex((c) => c.id === cardId);
          if (idx < 0) return;
          const [removed] = s.cards.splice(idx, 1);
          if (s.selectedId === cardId) s.selectedId = "";
          s.notice = `Removed "${removed.title}".`;
          await saveBoard(s);
        },
        async dispatch(s, cardId) {
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
          await saveBoard(s);
        },
      },
    });

    mako.commands.register("reset", async () => {
      const s = mako.blocks.state("board");
      s.cards = defaultCards();
      s.selectedId = "";
      s.notice = "Reset board to demo cards.";
      await saveBoard(s);
    });

    mako.commands.register("toggle-view", async () => {
      const s = mako.blocks.state("board");
      s.viewMode = s.viewMode === "compact" ? "board" : "compact";
      await saveBoard(s);
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
        if (changed) await saveBoard(s);
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
