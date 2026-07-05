import { useState, useRef, useEffect } from 'preact/hooks';
import { Check, X, Loader2, Maximize2, Minimize2, GitFork } from 'lucide-preact';
import { toolVerb, toolPath, toolPreview, splitPreview, splitPreviewTail } from '../util/format.js';
import { AskUserPreview } from './AskUserPreview.jsx';
import { openPersistedSubagent } from '../session-actions.js';
import { FileCard } from './FileCard.jsx';

export function ToolCall({ tool, sessionId }) {
  const [expanded, setExpanded] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);

  const { verb, cls: verbCls } = toolVerb(tool.tool_name);
  const path = toolPath(tool.tool_name, tool.args);

  // Subagent cards (tool_call_id "subagent-<jobID>") can be reopened as a
  // navigable sub-conversation, loading the persisted transcript from disk.
  const subagentJobId = (tool.tool_name === 'subagent' && typeof tool.tool_call_id === 'string'
    && tool.tool_call_id.startsWith('subagent-'))
    ? tool.tool_call_id.slice('subagent-'.length)
    : null;
  const openSub = (e) => {
    e.stopPropagation();
    if (sessionId && subagentJobId) openPersistedSubagent(sessionId, subagentJobId).catch(() => {});
  };

  const isGenerating = tool.status === 'generating';
  const isRunning = tool.status === 'running' || isGenerating;
  const isRejected = tool.status === 'rejected';
  const isError = tool.status === 'error';
  const statusCls = isGenerating ? 'generating' : isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';
  const StatusIcon = isRunning ? Loader2 : (isRejected || isError) ? X : Check;
  const statusLabel = isGenerating ? 'generating' : isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';

  const isAskUser = tool.tool_name === 'ask_user';
  const isSendFile = tool.tool_name === 'send_file';

  // For running tools with streaming, show the live output
  const liveText = isRunning && tool.streamingResult ? tool.streamingResult : null;
  // Final result (from toolPreview) — only used when not streaming
  const preview = !isAskUser && !liveText
    ? toolPreview(tool.tool_name, tool.args, tool.result, tool.status, tool.start_line)
    : null;
  const isDiff = !isAskUser && !liveText && preview && preview.kind === 'diff';

  // Streaming: show tail. Finished: show head.
  const previewData = liveText
    ? splitPreviewTail(liveText)
    : preview ? splitPreview(preview.text) : null;

  const hasOverflow = previewData && previewData.hidden > 0;
  const fullText = liveText || (preview ? preview.text : '');
  const isErrorBody = !liveText && (tool.status === 'error' || tool.status === 'rejected');
  const note = !liveText && tool.note ? tool.note : null;

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
          {subagentJobId && (
            <button class="tool-call-expand" title="View sub-conversation" onClick={openSub}>
              <GitFork />
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

        {isSendFile && (
          <FileCard result={tool.result} status={tool.status} />
        )}

        {!isAskUser && previewData && previewData.visible && (
          <pre class={`tool-call-preview${isErrorBody ? ' error-body' : ''}${liveText ? ' streaming' : ''}${isDiff ? ' diff-preview' : ''}`}>
            {isDiff
              ? renderDiffLines(expanded && !liveText ? fullText : previewData.visible)
              : (expanded && !liveText ? fullText : previewData.visible)
            }
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

        {note && (
          <div class="tool-call-footer tool-call-note">{note}</div>
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
          isDiff={isDiff}
          onClose={() => setModalOpen(false)}
        />
      )}
    </>
  );
}

function renderDiffLines(text) {
  if (!text) return text;
  return text.split('\n').map((line, i) => {
    let cls = 'diff-ctx';
    if (line.startsWith('@@')) cls = 'diff-hdr';
    else if (/^\s*\d+\s+\+/.test(line)) cls = 'diff-add';
    else if (/^\s*\d+\s+-/.test(line)) cls = 'diff-del';
    // Skip the "Edited path" header line — render as context.
    return <div key={i} class={cls}>{line}</div>;
  });
}

function ToolCallModal({ tool, verb, verbCls, path, fullText, isRunning, isDiff, onClose }) {
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

  const isGenerating = tool.status === 'generating';
  const isRejected = tool.status === 'rejected';
  const isError = tool.status === 'error';
  const statusCls = isGenerating ? 'generating' : isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';
  const StatusIcon = isRunning || isGenerating ? Loader2 : (isRejected || isError) ? X : Check;
  const statusLabel = isGenerating ? 'generating' : isRunning ? 'running' : isRejected ? 'rejected' : isError ? 'error' : 'done';

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
          {isDiff ? renderDiffLines(fullText) : (fullText || '(no output)')}
        </pre>
      </div>
    </div>
  );
}
