// Bundled first-party Git plugin (mako-git).
// Replaces the native Git panel with a folder-scoped Mako plugin supporting
// both tabbed compact and standalone wide layouts, full English + Korean i18n,
// async git operations, and window intents.

import { createActions } from "./lib/actions.js";
import { loadPanel } from "./lib/data.js";
import { catalog } from "./lib/i18n.js";
import { initialState } from "./lib/state.js";
import { renderLayout } from "./lib/view/layout.js";

let gitDoneHandler = null;

export default {
  activate(mako) {
    mako.i18n.register(catalog);
    const { on, handleGitDone } = createActions(mako);
    gitDoneHandler = handleGitDone;

    mako.blocks.register("panel", {
      state: initialState(),
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
  },
};
