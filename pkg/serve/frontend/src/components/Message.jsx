import { renderMarkdown } from '../util/markdown.js';

// ATTACHMENT_RE matches the <attachment name="..."> sentinel wrapping a
// text-file attachment (see pkg/serve/attachments.go), so it can be collapsed
// into an expandable chip instead of dumping the whole file into the bubble.
const ATTACHMENT_RE = /^<attachment name="((?:[^"\\]|\\.)*)">\n([\s\S]*)\n<\/attachment>$/;

function renderUserBlock(c, i) {
  if (c.type === 'image') {
    return <img key={i} class="msg-image" src={`data:${c.mime_type};base64,${c.data}`} alt="attachment" />;
  }
  if (c.type === 'text') {
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

export function Message({ msg }) {
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
