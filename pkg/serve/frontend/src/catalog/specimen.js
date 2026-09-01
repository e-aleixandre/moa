import { setState } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";

// A single frozen conversation so the real screens have something to wear.
// The chrome is the production components; only this data is fake. It is
// allowed to look like a conversation, not like a second ChatHead.

const SPECIMEN_ID = "catalog-session";

const specimen = {
  id: SPECIMEN_ID,
  title: "Fast mode on the status line",
  state: "idle",
  model: "Claude Opus 5",
  provider: "anthropic",
  thinking: "medium",
  fast: true,
  fastSupported: true,
  fastNote: "2.5× faster · billed as separate usage credits",
  cwd: "~/dev/moa",
  updated: Date.now(),
  permissionMode: "yolo",
  contextPercent: 41,
  contextWindow: 200000,
  compactAt: 0,
  costUSD: 1.24,
  runTokensUp: 12800,
  runTokensDown: 3400,
  messages: [
    {
      msg_id: "u1",
      role: "user",
      timestamp: Date.now() - 120000,
      content: [{ type: "text", text: "Put fast on the desktop status strip, after yolo." }],
    },
    {
      msg_id: "a1",
      role: "assistant",
      timestamp: Date.now() - 60000,
      content: [{ type: "text", text: "Done. The word sits after the permission chip, same colour as on mobile." }],
    },
  ],
  subagents: {},
};

export function seedCatalogStore() {
  setState((s) => ({
    sessions: { [SPECIMEN_ID]: specimen },
    sessionsLoaded: true,
    activeSession: SPECIMEN_ID,
    tileTree: setTileSession(s.tileTree, s.focusedTile, SPECIMEN_ID),
  }));
}
