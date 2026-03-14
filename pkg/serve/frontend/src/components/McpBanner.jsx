import { useState } from 'preact/hooks';
import { AlertTriangle } from 'lucide-preact';
import { trustMcp } from '../session-actions.js';

export function McpBanner({ sessionId }) {
  const [dismissed, setDismissed] = useState(false);
  const [loading, setLoading] = useState(false);

  if (dismissed) return null;

  const handleTrust = async () => {
    setLoading(true);
    try {
      await trustMcp(sessionId);
    } catch (e) {
      console.error('Trust MCP failed:', e);
      setLoading(false);
    }
  };

  return (
    <div class="mcp-banner">
      <span class="mcp-banner-icon"><AlertTriangle /></span>
      <span class="mcp-banner-text">This project has MCP servers that haven't been trusted yet.</span>
      <button class="btn-trust" onClick={handleTrust} disabled={loading}>
        {loading ? '…' : 'Trust & Load'}
      </button>
      <button class="btn-dismiss" onClick={() => setDismissed(true)}>Dismiss</button>
    </div>
  );
}
