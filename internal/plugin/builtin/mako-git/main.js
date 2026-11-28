// Bundled first-party Git plugin (mako-git).
// Replaces the native Git panel with a folder-scoped Mako plugin supporting
// both tabbed compact and standalone wide layouts, full English + Korean i18n,
// async git operations, and window intents.

import { createActions } from "./lib/actions.js";
import {
  POLL_SECONDS,
  anyAutoFixEnabled,
  createAutoFixEventHandlers,
} from "./lib/autofix.js";
import { loadPanel } from "./lib/data.js";
import { catalog } from "./lib/i18n.js";
import { initialState } from "./lib/state.js";
import { renderLayout } from "./lib/view/layout.js";

let gitDoneHandler = null;
let autoFixHandlers = null;

export default {
  activate(mako) {
    mako.i18n.register(catalog);
    const { on, handleGitDone } = createActions(mako);
    gitDoneHandler = handleGitDone;
    autoFixHandlers = createAutoFixEventHandlers(mako);

    mako.blocks.register("panel", {
      state: initialState(),
      // The host reads refresh before each load: the panel polls the pull
      // request's checks and comments only while an Auto-fix is on, and
      // loads on Git's changes alone otherwise.
      get refresh() {
        return anyAutoFixEnabled() ? POLL_SECONDS : 0;
      },
      load(s, ctx) {
        return loadPanel(s, ctx, mako);
      },
      render(s, ui, ctx) {
        return renderLayout(s, ui, ctx);
      },
      on,
    });
  },

  on: {
    gitDone(ev) {
      if (typeof gitDoneHandler === "function") {
        gitDoneHandler(ev);
      }
    },
    agentStarted(ev) {
      if (autoFixHandlers) return autoFixHandlers.handleAgentStarted(ev);
    },
    agentRefused(ev) {
      if (autoFixHandlers) return autoFixHandlers.handleAgentRefused(ev);
    },
    sessionStatus(ev) {
      if (autoFixHandlers) return autoFixHandlers.handleSessionEvent(ev);
    },
    sessionEnded(ev) {
      if (autoFixHandlers) return autoFixHandlers.handleSessionEvent(ev);
    },
  },
};
