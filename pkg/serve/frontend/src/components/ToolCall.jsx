import { useState } from 'preact/hooks';
import { Check, X, Loader2 } from 'lucide-preact';
import { toolVerb, toolPath, toolPreview, splitPreview } from '../util/format.js';

export function ToolCall({ tool }) {
  const [expanded, setExpanded] = useState(false);

  const { verb, cls: verbCls } = toolVerb(tool.tool_name);
  const path = toolPath(tool.tool_name, tool.args);
  const preview = toolPreview(tool.tool_name, tool.args, tool.result);

  const statusCls = tool.status === 'running' ? 'running'
    : tool.status === 'error' ? 'error' : 'done';

  const StatusIcon = tool.status === 'running' ? Loader2
    : tool.status === 'error' ? X : Check;

  const statusLabel = tool.status === 'running' ? 'running'
    : tool.status === 'error' ? 'error' : 'done';

  // Split preview into visible + hidden
  const previewData = preview ? splitPreview(preview.text) : null;
  const hasOverflow = previewData && previewData.hidden > 0;

  return (
    <div class={`tool-call status-${statusCls}`}>
      <div class="tool-call-head">
        <span class={`tool-call-verb ${verbCls}`}>{verb}</span>
        <span class="tool-call-path">{path}</span>
        <span class={`tool-call-tag ${statusCls}`}>
          <StatusIcon />
          {statusLabel}
        </span>
      </div>

      {previewData && previewData.visible && (
        <pre class={`tool-call-preview ${preview.kind === 'input' ? 'input' : ''} ${tool.status === 'error' ? 'error-body' : ''}`}>
          {expanded ? preview.text : previewData.visible}
        </pre>
      )}

      {hasOverflow && (
        <div class="tool-call-footer" onClick={() => setExpanded(!expanded)}>
          {expanded
            ? 'collapse'
            : `… ${previewData.hidden} more lines, ${previewData.total} total`
          }
        </div>
      )}
    </div>
  );
}
