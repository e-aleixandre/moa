import { useState, useCallback } from 'preact/hooks';
import { MessageSquarePlus, GitFork } from 'lucide-preact';
import { focusTile, assignTile, swapTiles } from '../state.js';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';
import { SettingsDropdown } from './SettingsDropdown.jsx';
import { ModelPill } from './ModelPill.jsx';

export function Tile({ tileIndex, sessionId, session, isFocused }) {
  const [dragOver, setDragOver] = useState(false);
  const needsAttention = session && (session.state === 'permission' || session.state === 'error');
  const classes = ['tile'];
  if (isFocused) classes.push('focused');
  if (needsAttention) classes.push('attention');
  if (session?.flash) classes.push('flash');
  if (dragOver) classes.push('drag-over');

  // Drag source: drag this tile's session
  const handleDragStart = useCallback((e) => {
    e.dataTransfer.setData('text/x-tile-index', String(tileIndex));
    if (sessionId) e.dataTransfer.setData('text/x-session-id', sessionId);
    e.dataTransfer.effectAllowed = 'move';
  }, [tileIndex, sessionId]);

  // Drop target
  const handleDragOver = useCallback((e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    setDragOver(true);
  }, []);

  const handleDragLeave = useCallback(() => setDragOver(false), []);

  const handleDrop = useCallback((e) => {
    e.preventDefault();
    setDragOver(false);
    // From another tile → swap
    const fromTile = e.dataTransfer.getData('text/x-tile-index');
    if (fromTile !== '') {
      swapTiles(parseInt(fromTile, 10), tileIndex);
      return;
    }
    // From sidebar → assign
    const sid = e.dataTransfer.getData('text/x-session-id');
    if (sid) {
      assignTile(tileIndex, sid);
    }
  }, [tileIndex]);

  if (!session) {
    return (
      <div
        class={classes.join(' ')}
        onClick={() => focusTile(tileIndex)}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        <div class="tile-empty">
          <MessageSquarePlus />
          <span>Click a session or drag it here</span>
        </div>
      </div>
    );
  }

  return (
    <div
      class={classes.join(' ')}
      onClick={() => focusTile(tileIndex)}
      draggable
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      <div class="tile-header">
        <span class={`state-dot ${session.state}`} />
        <span class="tile-title">{session.title || 'Untitled'}</span>
        {session.subagentCount > 0 && (
          <span class="subagent-badge"><GitFork />{session.subagentCount}</span>
        )}
        <ModelPill model={session.model} thinking={session.thinking} />
        <SettingsDropdown sessionId={sessionId} session={session} />
        <span class="tile-number">#{tileIndex + 1}</span>
      </div>

      {session.untrustedMcp && <McpBanner sessionId={sessionId} />}

      <MessageList session={session} />

      <InputBar sessionId={sessionId} sessionState={session.state} />
    </div>
  );
}
