import { memo } from 'preact/compat';
import { renderMarkdown } from '../util/markdown.js';

// ATTACHMENT_RE matches the <attachment name="..."> sentinel wrapping a
// text-file attachment (see pkg/serve/attachments.go), so it can be collapsed
// into an expandable chip instead of dumping the whole file into the bubble.
const ATTACHMENT_RE = /^<attachment name="((?:[^"\\]|\\.)*)">\n([\s\S]*)\n<\/attachment>$/;

// DISK_RE matches the advisory text block the server emits when it saves a
// non-inline attachment to disk (see pkg/serve/attachments.go), so it can be
// shown as a compact chip instead of the full paragraph.
const DISK_RE = /^El usuario ha adjuntado el archivo "((?:[^"\\]|\\.)*)" \(([^)]*)\), guardado en:\n(.+?)\n/;

function renderUserBlock(c, i) {
  if (c.type === 'image') {
    if (!c.data) {
      return (
        <div key={i} class="msg-attachment-chip" title="The original attachment remains in the session history">
          🖼️ {c.filename || 'Image'} <span class="msg-attachment-tag">not loaded on this device</span>
        </div>
      );
    }
    return <img key={i} class="msg-image" src={`data:${c.mime_type};base64,${c.data}`} alt="attachment" />;
  }
  if (c.type === 'document') {
    return (
      <div key={i} class="msg-attachment-chip msg-attachment-native" title="Enviado al modelo (PDF nativo)">
        📄 {c.filename || 'document.pdf'} <span class="msg-attachment-tag">enviado al modelo</span>
      </div>
    );
  }
  if (c.type === 'text') {
    const dm = DISK_RE.exec(c.text || '');
    if (dm) {
      const [, dName, dSize, dPath] = dm;
      const isPdfFallback = c.text.includes('no soporta documentos PDF nativos');
      return (
        <details key={i} class="msg-attachment msg-attachment-disk">
          <summary>💾 {dName} <span class="msg-attachment-tag">{dSize} · {isPdfFallback ? 'guardado en disco (PDF no soportado)' : 'guardado en disco'}</span></summary>
          <pre>{dPath}</pre>
        </details>
      );
    }
    const m = ATTACHMENT_RE.exec(c.text || '');
    if (m) {
      const name = m[1].replaceAll('\\"', '"');
      return (
        <details key={i} class="msg-attachment">
          <summary>📎 {name}</summary>
          <pre>{m[2]}</pre>
        </details>
      );
    }
    if (!c.text) return null;
    return <div key={i} class="msg-user">{c.text}</div>;
  }
  return null;
}

function MessageView({ msg }) {
  if (!msg || !msg.role) return null;

  if (msg.role === 'user') {
    const parts = (msg.content || []).map(renderUserBlock).filter(Boolean);
    if (parts.length === 0) return null;
    return <div class="msg-user-group">{parts}</div>;
  }

  if (msg.role === 'assistant') {
    const text = (msg.content || [])
      .filter(c => c.type === 'text')
      .map(c => c.text)
      .join('');
    if (!text) return null;
    return (
      <div
        class="msg-assistant"
        dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }}
      />
    );
  }

  // tool_result messages are handled by ToolCall state updates, skip here
  return null;
}

// Streaming updates should not reparse every finalized Markdown message in
// every visible pane. WS reducers preserve unchanged message object identity.
export const Message = memo(MessageView);
