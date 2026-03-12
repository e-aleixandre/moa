import { useState, useCallback, useRef } from 'preact/hooks';
import { MessageSquarePlus, GripHorizontal, GitFork } from 'lucide-preact';
import { focusTile, assignTile, swapTiles } from '../state.js';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';
import { SettingsDropdown } from './SettingsDropdown.jsx';
import { ModelPill } from './ModelPill.jsx';

export function Tile({ tileIndex, sessionId, session, isFocused, gridArea }) {
  const [dragOver, setDragOver] = useState(false);
  const tileRef = useRef(null);
  const needsAttention = session && (session.state === 'permission' || session.state === 'error');
  const classes = ['tile'];
  if (isFocused) classes.push('focused');
  if (needsAttention) classes.push('attention');
  if (session?.flash) classes.push('flash');
  if (dragOver) classes.push('drag-over');

  // Drag from the header — use the whole tile as ghost image
  const handleDragStart = useCallback((e) => {
    e.dataTransfer.setData('text/x-tile-index', String(tileIndex));
    if (sessionId) e.dataTransfer.setData('text/x-session-id', sessionId);
    e.dataTransfer.effectAllowed = 'move';

    // Custom drag image: clone the tile so the ghost is clean
    const tile = tileRef.current;
    if (tile) {
      const rect = tile.getBoundingClientRect();
      const ghost = tile.cloneNode(true);
      ghost.style.width = rect.width + 'px';
      ghost.style.height = rect.height + 'px';
      ghost.style.position = 'fixed';
      ghost.style.top = '-9999px';
      ghost.style.opacity = '0.85';
      ghost.style.borderRadius = '8px';
      ghost.style.overflow = 'hidden';
      document.body.appendChild(ghost);
      e.dataTransfer.setDragImage(ghost, e.clientX - rect.left, e.clientY - rect.top);
      requestAnimationFrame(() => ghost.remove());
    }
  }, [tileIndex, sessionId]);

  // Drop target (on the whole tile)
  const handleDragOver = useCallback((e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    setDragOver(true);
  }, []);

  const handleDragLeave = useCallback(() => setDragOver(false), []);

  const handleDrop = useCallback((e) => {
    e.preventDefault();
    setDragOver(false);
    const fromTile = e.dataTransfer.getData('text/x-tile-index');
    if (fromTile !== '') {
      swapTiles(parseInt(fromTile, 10), tileIndex);
      return;
    }
    const sid = e.dataTransfer.getData('text/x-session-id');
    if (sid) {
      assignTile(tileIndex, sid);
    }
  }, [tileIndex]);

  const tileStyle = gridArea ? { gridArea } : undefined;

  if (!session) {
    return (
      <div
        ref={tileRef}
        class={classes.join(' ')}
        style={tileStyle}
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
      ref={tileRef}
      class={classes.join(' ')}
      style={tileStyle}
      onClick={() => focusTile(tileIndex)}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      <div
        class="tile-header"
        draggable
        onDragStart={handleDragStart}
      >
        <GripHorizontal class="drag-handle" />
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
