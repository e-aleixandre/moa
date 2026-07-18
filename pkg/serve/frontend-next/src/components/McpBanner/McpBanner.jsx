import { useState } from "preact/hooks";
import { TriangleAlert } from "lucide-preact";
import { Button } from "../../primitives/index.js";
import { trustMcp } from "../../data/session-actions.js";
import "./McpBanner.css";

// McpBanner — ports the old SPA's McpBanner.jsx (pkg/serve/frontend/src/
// components/McpBanner.jsx) verbatim: warns that the project's MCP servers
// haven't been trusted yet, offers "Trust & Load" (calls trustMcp, which
// flips session.untrustedMcp off) or a local-only "Dismiss" (just hides the
// banner in this component instance; it comes back on reload/reconnect until
// actually trusted — same as before).
export function McpBanner({ sessionId }) {
  const [dismissed, setDismissed] = useState(false);
  const [loading, setLoading] = useState(false);

  if (dismissed) return null;

  const handleTrust = async () => {
    setLoading(true);
    try {
      await trustMcp(sessionId);
    } catch (e) {
      console.error("Trust MCP failed:", e);
      setLoading(false);
    }
  };

  return (
    <div class="mcp-banner">
      <span class="mcp-banner-icon" aria-hidden="true"><TriangleAlert size={15} /></span>
      <span class="mcp-banner-text">This project has MCP servers that haven't been trusted yet.</span>
      <Button variant="solid" size="sm" className="mcp-banner-trust" disabled={loading} onClick={handleTrust}>
        {loading ? "…" : "Trust & Load"}
      </Button>
      <Button variant="ghost" size="sm" onClick={() => setDismissed(true)}>
        Dismiss
      </Button>
    </div>
  );
}
