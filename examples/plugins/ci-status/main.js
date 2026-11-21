// CI status: the latest GitHub Actions runs of the repository the
// settings name, kept in the plugin's store for when GitHub cannot be
// reached, and a command that runs the latest failed one again.

const API = "https://api.github.com";

// Tones of a run's conclusion or status.
const tones = { success: "success", failure: "danger", cancelled: "faint", in_progress: "accent", queued: "muted" };

let mako;

async function setting(key, ctx) {
  const { setting } = ctx && ctx.settings
    ? await ctx.settings.getSetting(key)
    : await mako.settings.getSetting({ key });
  return setting ? setting.text : "";
}

async function github(path, method = "GET", ctx = null) {
  const headers = { Accept: "application/vnd.github+json" };
  const token = await setting("token", ctx);
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await mako.http.fetch({ url: API + path, method, headers });
  if (res.status >= 300) throw new Error(`GitHub answered ${res.status}`);
  return res.body ? JSON.parse(res.body) : {};
}

async function load(s, ctx) {
  s.repo = await setting("repo", ctx);
  const { sessions } = await mako.sessions.listSessions();
  s.working = sessions.filter((x) => x.status === "working").length;
  if (!s.repo) {
    s.runs = [];
    s.error = "Set the repository in Settings ▸ Plugins.";
    return;
  }
  const store = ctx && ctx.storage ? ctx.storage : mako.storage;
  try {
    const data = await github(`/repos/${s.repo}/actions/runs?per_page=8`, "GET", ctx);
    s.runs = (data.workflow_runs || []).map((r) => ({
      id: r.id, name: r.name, branch: r.head_branch, number: r.run_number,
      status: r.conclusion || r.status, url: r.html_url,
    }));
    s.error = "";
    await store.setItem({ key: `runs:${s.repo}`, value: JSON.stringify(s.runs) });
  } catch (e) {
    const kept = await store.getItem({ key: `runs:${s.repo}` });
    s.runs = kept.found ? JSON.parse(kept.value) : [];
    s.error = `${e.message}${kept.found ? " (showing the runs last read)" : ""}`;
  }
}

function render(s, ui) {
  const rows = [
    ui.row([
      ui.icon("github", { tone: "muted" }),
      ui.text(s.repo || "No repository").weight("medium").grow(),
      ui.button("Refresh", { icon: "refresh-cw", onTap: ["refresh"] }),
    ], { gap: 8 }),
  ];
  if (s.error) rows.push(ui.text(s.error, { tone: "danger", size: "small" }));
  rows.push(ui.list(s.runs, (r) =>
    ui.row([
      ui.pill(r.status, { tone: tones[r.status] || "muted" }),
      ui.text(r.name).grow(),
      ui.chip(r.branch),
      ui.text(`#${r.number}`, { tone: "faint", size: "small" }),
    ], { key: r.id, gap: 8, onTap: ["open", r.id] })));
  if (s.note) rows.push(ui.text(s.note, { tone: "accent", size: "small" }));
  rows.push(ui.text(`${s.working} agent${s.working === 1 ? "" : "s"} at work on this node`, { tone: "faint", size: "small" }));
  return ui.column(rows, { gap: 8 });
}

export default {
  activate(m) {
    mako = m;
    mako.blocks.register("ci", {
      state: { repo: "", runs: [], error: "", note: "", working: 0 },
      refresh: 60,
      load,
      render,
      on: {
        refresh: () => mako.blocks.reload("ci"),
        open: (s, id) => {
          const run = s.runs.find((r) => r.id === id);
          if (run) return mako.commands.runCommand({ id: "mako.openUrl", arg: run.url });
        },
      },
    });
    mako.commands.register("rerun", async (ctx) => {
      const scopeKey = ctx ? ctx.scopeKey : "";
      const s = mako.blocks.state("ci", scopeKey);
      const failed = s.runs.find((r) => r.status === "failure");
      if (!failed) {
        s.note = "No failed run to run again.";
        return;
      }
      await github(`/repos/${s.repo}/actions/runs/${failed.id}/rerun`, "POST", ctx);
      s.note = `Run #${failed.number} was started again.`;
      mako.blocks.reload("ci", scopeKey);
    });
  },
};
