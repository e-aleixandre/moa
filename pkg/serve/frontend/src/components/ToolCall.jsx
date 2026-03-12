import { useState } from 'preact/hooks';
import { formatArgs, toolSummary, truncateText } from '../util/format.js';

export function ToolCall({ tool }) {
  const [open, setOpen] = useState(false);

  const iconClass = tool.status === 'running' ? 'running'
    : tool.status === 'error' ? 'error' : 'done';

  const icon = tool.status === 'running' ? '⋯'
    : tool.status === 'error' ? '✗' : '✓';

  const summary = toolSummary(tool.tool_name, tool.args);

  return (
    <div class={`tool-call ${open ? 'open' : ''}`}>
      <div class="tool-call-header" onClick={() => setOpen(!open)}>
        <span class={`tool-call-icon ${iconClass}`}>{icon}</span>
        <span class="tool-call-name">{tool.tool_name}</span>
        {summary && <span class="tool-call-summary">{summary}</span>}
        <span class="tool-call-chevron">▸</span>
      </div>
      <div class="tool-call-body">
        {formatArgs(tool.args)}
        {tool.result && (
          <>
            {'\n\n--- Result ---\n\n'}
            {truncateText(tool.result)}
          </>
        )}
      </div>
    </div>
  );
}
