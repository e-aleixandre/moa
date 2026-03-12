import { useState } from 'preact/hooks';
import { Check, X, Loader2, ChevronRight } from 'lucide-preact';
import { formatArgs, toolSummary, truncateText } from '../util/format.js';

export function ToolCall({ tool }) {
  const [open, setOpen] = useState(false);

  const statusClass = tool.status === 'running' ? 'running'
    : tool.status === 'error' ? 'error' : 'done';

  const StatusIcon = tool.status === 'running' ? Loader2
    : tool.status === 'error' ? X : Check;

  const summary = toolSummary(tool.tool_name, tool.args);

  return (
    <div class={`tool-call ${open ? 'open' : ''} status-${statusClass}`}>
      <div class="tool-call-header" onClick={() => setOpen(!open)}>
        <span class={`tool-call-icon ${statusClass}`}><StatusIcon /></span>
        <span class="tool-call-name">{tool.tool_name}</span>
        {summary && <span class="tool-call-summary">{summary}</span>}
        <span class="tool-call-chevron"><ChevronRight /></span>
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
