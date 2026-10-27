package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/ironpark/mako/internal/agent"
)

// OpenCode keeps its conversations in a database, which mako does not
// read. The plugin mako has it load writes each conversation down as it
// goes, a record a line, in <data>/opencode/mako/<session>.jsonl, <data>
// being OpenCode's own data folder: what is already in the database when
// it first sees a session, then each message part as it is finished. It
// also runs mako's hook on OpenCode's events, the input as Claude Code
// hands its hooks, so CommonHooks reads it. A subagent's session is
// neither written down nor reported.

// plugin is the plugin's source after HookScript, hook the shell command
// it runs.
const plugin = `import { appendFileSync, existsSync, mkdirSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const hook = %s;
const logs = join(process.env.XDG_DATA_HOME || join(homedir(), ".local", "share"), "opencode", "mako");

function report(s, name, extra) {
	runHook(hook, { hook_event_name: name, session_id: s.id, transcript_path: s.log, ...extra });
}

// The events a session is opened for.
const followed = new Set(["session.created", "message.updated", "message.part.updated", "session.status"]);

export const MakoPlugin = async ({ client, directory }) => {
	const children = new Set(); // subagents' sessions
	const sessions = new Map(); // a session, as it opens

	// write writes a record down, once.
	function write(s, record) {
		if (record.id && s.written.has(record.id)) return;
		if (record.id) s.written.add(record.id);
		try {
			appendFileSync(s.log, JSON.stringify(record) + "\n");
		} catch {}
	}

	// part writes a message's part down once it is finished.
	function part(s, p) {
		const role = s.roles.get(p.messageID);
		if (p.type === "text" && !p.synthetic && !p.ignored && p.text) {
			if (role === "user") write(s, { type: "user", id: p.id, text: p.text });
			else if (role && p.time && p.time.end) write(s, { type: role === "summary" ? "summary" : "text", id: p.id, text: p.text });
		} else if (p.type === "tool" && p.state && (p.state.status === "completed" || p.state.status === "error")) {
			const st = p.state;
			write(s, {
				type: "tool", id: p.callID, tool: p.tool, input: st.input,
				output: st.status === "error" ? st.error : st.output, error: st.status === "error",
				exit: st.metadata && typeof st.metadata.exit === "number" ? st.metadata.exit : undefined,
			});
		}
	}

	// message notes a message's role, and its tokens once it is done.
	function message(s, info) {
		s.roles.set(info.id, info.role === "assistant" && (info.summary || info.mode === "compaction") ? "summary" : info.role);
		if (info.role === "assistant" && info.time && info.time.completed && info.tokens) {
			const t = info.tokens;
			const usage = { input: t.input || 0, output: (t.output || 0) + (t.reasoning || 0), cacheRead: (t.cache && t.cache.read) || 0, cacheWrite: (t.cache && t.cache.write) || 0 };
			write(s, { type: "usage", id: "usage-" + info.id, usage });
		}
	}

	// load opens a session's log: what it holds already, or its header,
	// then what OpenCode has of the session that it does not.
	async function load(id, info) {
		const s = { id, log: join(logs, id + ".jsonl"), written: new Set(), roles: new Map() };
		try {
			mkdirSync(logs, { recursive: true });
			if (existsSync(s.log)) {
				for (const line of readFileSync(s.log, "utf8").split("\n")) {
					try {
						const r = JSON.parse(line);
						if (r.id) s.written.add(r.id);
					} catch {}
				}
			} else {
				const created = (info && info.time && info.time.created) || Date.now();
				appendFileSync(s.log, JSON.stringify({ type: "session", id, cwd: (info && info.directory) || directory, time: created }) + "\n");
			}
			const res = await client.session.messages({ path: { id } });
			for (const m of (res && res.data) || []) {
				message(s, m.info);
				for (const p of m.parts || []) part(s, p);
			}
		} catch {}
		report(s, "SessionStart", {});
		return s;
	}

	// open is a session, opened once.
	function open(id, info) {
		if (!sessions.has(id)) sessions.set(id, load(id, info));
		return sessions.get(id);
	}

	const top = (id) => id && !children.has(id);

	return {
		event: async ({ event }) => {
			const props = event.properties || {};
			const id = props.sessionID;
			if ((event.type === "session.created" || event.type === "session.updated") && props.info && props.info.parentID) {
				children.add(id);
			}
			if (!top(id) || !followed.has(event.type)) return;
			const s = await open(id, props.info);
			switch (event.type) {
				case "message.updated":
					message(s, props.info);
					break;
				case "message.part.updated":
					part(s, props.part);
					break;
				case "session.status": {
					// Reported as it changes.
					const type = props.status && props.status.type;
					if (type === s.status) break;
					s.status = type;
					if (type === "busy") report(s, "UserPromptSubmit", {});
					else if (type === "idle") report(s, "Stop", {});
					break;
				}
			}
		},
		"tool.execute.after": async (input) => {
			if (top(input.sessionID)) report(await open(input.sessionID), "PostToolUse", { tool_name: input.tool });
		},
		"experimental.session.compacting": async (input) => {
			if (top(input.sessionID)) report(await open(input.sessionID), "PreCompact", {});
		},
	};
};
`

// WithHooks has OpenCode load the plugin, written in dir. The status line
// is Claude's alone.
func (*Agent) WithHooks(l *agent.Launch, hook, _, dir string) bool {
	hookJSON, _ := json.Marshal(hook)
	source := "// Written by mako: it runs mako's hook on OpenCode's events, and writes\n// the conversations down where mako reads them.\n" + agent.HookScript + fmt.Sprintf(plugin, hookJSON)
	path, err := agent.WriteOnce(dir, "opencode-mako", ".js", source)
	return err == nil && withConfig(l, "plugin", []any{fileURL(path)}) == nil
}
