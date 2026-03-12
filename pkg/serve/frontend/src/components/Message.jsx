import { renderMarkdown } from '../util/markdown.js';

export function Message({ msg }) {
  if (!msg || !msg.role) return null;

  if (msg.role === 'user') {
    const text = (msg.content || [])
      .filter(c => c.type === 'text')
      .map(c => c.text)
      .join('');
    if (!text) return null;
    return <div class="msg-user">{text}</div>;
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
