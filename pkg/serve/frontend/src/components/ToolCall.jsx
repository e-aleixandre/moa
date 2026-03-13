import { useState, useRef, useEffect } from 'preact/hooks';
import { Check, X, Loader2, Maximize2, Minimize2 } from 'lucide-preact';
import { toolVerb, toolPath, toolPreview, splitPreview, splitPreviewTail } from '../util/format.js';
import { AskUserPreview } from './AskUserPreview.jsx';

export function ToolCall({ tool }) {
  const [expanded, setExpanded] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);

  const { verb, cls: verbCls } = toolVerb(tool.tool_name);
  const path = toolPath(tool.tool_name, tool.args);

  const isRunning = tool.status === 'running';
  const isRejected = tool.status === 'rejected';
  const isError = tool.status === 'error';
  const statusCls = isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';
  const StatusIcon = isRunning ? Loader2 : (isRejected || isError) ? X : Check;
  const statusLabel = isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';

  const isAskUser = tool.tool_name === 'ask_user';

  // For running tools with streaming, show the live output
  const liveText = isRunning && tool.streamingResult ? tool.streamingResult : null;
  // Final result (from toolPreview) — only used when not streaming
  const preview = !isAskUser && !liveText ? toolPreview(tool.tool_name, tool.args, tool.result) : null;

  // Streaming: show tail. Finished: show head.
  const previewData = liveText
    ? splitPreviewTail(liveText)
    : preview ? splitPreview(preview.text) : null;

  const hasOverflow = previewData && previewData.hidden > 0;
  const fullText = liveText || (preview ? preview.text : '');
  const isErrorBody = !liveText && (tool.status === 'error' || tool.status === 'rejected');

  return (
    <>
      <div class={`tool-call status-${statusCls}`}>
        <div class="tool-call-head" onClick={() => !isAskUser && fullText && setModalOpen(true)} style={{ cursor: !isAskUser && fullText ? 'pointer' : 'default' }}>
          <span class={`tool-call-verb ${verbCls}`}>{verb}</span>
          <span class="tool-call-path">{path}</span>
          {!isAskUser && fullText && (
            <button class="tool-call-expand" title="Expand" onClick={(e) => { e.stopPropagation(); setModalOpen(true); }}>
              <Maximize2 />
            </button>
          )}
          <span class={`tool-call-tag ${statusCls}`}>
            <StatusIcon />
            {statusLabel}
          </span>
        </div>

        {isAskUser && !isRunning && (
          <AskUserPreview args={tool.args} result={tool.result} />
        )}

        {!isAskUser && previewData && previewData.visible && (
          <pre class={`tool-call-preview${isErrorBody ? ' error-body' : ''}${liveText ? ' streaming' : ''}`}>
            {expanded && !liveText ? fullText : previewData.visible}
          </pre>
        )}

        {!liveText && hasOverflow && (
          <div class="tool-call-footer" onClick={() => setExpanded(!expanded)}>
            {expanded
              ? 'collapse'
              : `… ${previewData.hidden} more lines, ${previewData.total} total`
            }
          </div>
        )}

        {liveText && hasOverflow && (
          <div class="tool-call-footer">
            ↑ {previewData.hidden} lines above · {previewData.total} total
          </div>
        )}
      </div>

      {modalOpen && (
        <ToolCallModal
          tool={tool}
          verb={verb}
          verbCls={verbCls}
          path={path}
          fullText={fullText}
          isRunning={isRunning}
          onClose={() => setModalOpen(false)}
        />
      )}
    </>
  );
}

function ToolCallModal({ tool, verb, verbCls, path, fullText, isRunning, onClose }) {
  const contentRef = useRef(null);
  const wasAtBottom = useRef(true);

  // Auto-scroll to bottom when streaming
  useEffect(() => {
    if (contentRef.current && wasAtBottom.current) {
      contentRef.current.scrollTop = contentRef.current.scrollHeight;
    }
  }, [fullText]);

  // Track whether user is at the bottom
  const handleScroll = () => {
    const el = contentRef.current;
    if (!el) return;
    wasAtBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 20;
  };

  // Close on Escape
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') { e.stopPropagation(); onClose(); }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  const isRejected = tool.status === 'rejected';
  const isError = tool.status === 'error';
  const statusCls = isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';
  const StatusIcon = isRunning ? Loader2 : (isRejected || isError) ? X : Check;
  const statusLabel = isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';

  return (
    <div class="tool-modal-overlay" onClick={onClose}>
      <div class="tool-modal" onClick={(e) => e.stopPropagation()}>
        <div class="tool-modal-header">
          <span class={`tool-call-verb ${verbCls}`}>{verb}</span>
          <span class="tool-call-path">{path}</span>
          <span class={`tool-call-tag ${statusCls}`}>
            <StatusIcon />
            {statusLabel}
          </span>
          <button class="tool-modal-close" onClick={onClose}>
            <Minimize2 />
          </button>
        </div>
        <pre
          ref={contentRef}
          class={`tool-modal-content${(tool.status === 'error' || tool.status === 'rejected') ? ' error-body' : ''}`}
          onScroll={handleScroll}
        >
          {fullText || '(no output)'}
        </pre>
      </div>
    </div>
  );
}
