import { useState, useCallback, useRef, useEffect } from 'preact/hooks';
import { MessageSquarePlus, GripHorizontal, GitFork, Columns2, Rows2, X } from 'lucide-preact';
import { focusTile, assignToTile, swapTiles, splitTile, closeTile } from '../tile-actions.js';
import { getTileCount } from '../store.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { useTouchDrag, registerDropTarget } from '../hooks/useTouchDrag.js';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';
import { SettingsDropdown } from './SettingsDropdown.jsx';
import { ModelPill } from './ModelPill.jsx';
import { TaskBar } from './TaskBar.jsx';

export function Tile({ tileId, tileIndex, sessionId, session, isFocused }) {
  const [dragOver, setDragOver] = useState(false);
  const tileRef = useRef(null);
  const needsAttention = session && (session.state === 'permission' || session.state === 'error');
  const canClose = getTileCount() > 1;

  const classes = ['tile'];
  if (isFocused) classes.push('focused');
  if (needsAttention) classes.push('attention');
  if (session?.flash) classes.push('flash');
  if (dragOver) classes.push('drag-over');

  const handleDragStart = useCallback((e) => {
    e.dataTransfer.setData('text/x-tile-id', String(tileId));
    if (sessionId) e.dataTransfer.setData('text/x-session-id', sessionId);
    e.dataTransfer.effectAllowed = 'move';
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
  }, [tileId, sessionId]);

  const handleDragOver = useCallback((e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    setDragOver(true);
  }, []);

  const handleDragLeave = useCallback(() => setDragOver(false), []);

  const handleDrop = useCallback((e) => {
    e.preventDefault();
    setDragOver(false);
    const fromTileId = e.dataTransfer.getData('text/x-tile-id');
    if (fromTileId) {
      swapTiles(parseInt(fromTileId, 10), tileId);
      return;
    }
    const sid = e.dataTransfer.getData('text/x-session-id');
    if (sid) assignToTile(tileId, sid);
  }, [tileId]);

  // Touch drag: long-press tile header to initiate
  const touchDragProps = useTouchDrag({
    data: { 'text/x-tile-id': String(tileId), 'text/x-session-id': sessionId || '' },
  });

  // Register this tile as a touch drop target
  useEffect(() => {
    const el = tileRef.current;
    if (!el) return;
    return registerDropTarget(el, {
      onDragOver: () => setDragOver(true),
      onDragLeave: () => setDragOver(false),
      onDrop: (data) => {
        setDragOver(false);
        const fromTileId = data['text/x-tile-id'];
        if (fromTileId) {
          swapTiles(parseInt(fromTileId, 10), tileId);
          return;
        }
        const sid = data['text/x-session-id'];
        if (sid) assignToTile(tileId, sid);
      },
    });
  }, [tileId]);

  const stop = (e, fn) => { e.stopPropagation(); fn(); };

  if (!session) {
    return (
      <div
        ref={tileRef}
        class={classes.join(' ')}
        data-tile-id={tileId}
        onClick={() => focusTile(tileId)}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        <div class="tile-header" draggable onDragStart={handleDragStart} {...touchDragProps}>
          <GripHorizontal class="drag-handle" />
          <span class="tile-number" title={formatShortcut(String(tileIndex + 1), { mod: true })}>{tileIndex + 1}</span>
          <span class="tile-title empty-title">Empty</span>
          <button class="tile-action-btn" onClick={(e) => stop(e, () => splitTile(tileId, 'horizontal'))} title="Split right"><Columns2 /></button>
          <button class="tile-action-btn" onClick={(e) => stop(e, () => splitTile(tileId, 'vertical'))} title="Split down"><Rows2 /></button>
          {canClose && (
            <button class="tile-action-btn tile-close-btn" onClick={(e) => stop(e, () => closeTile(tileId))} title="Close pane"><X /></button>
          )}
        </div>
        <div class="tile-empty">
          <MessageSquarePlus />
          <span>Drag a session here</span>
          <span class="tile-empty-hint">{formatShortcut('K', { mod: true })} to pick a session</span>
        </div>
      </div>
    );
  }

  return (
    <div
      ref={tileRef}
      class={classes.join(' ')}
      data-tile-id={tileId}
      onClick={() => focusTile(tileId)}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      <div class="tile-header" draggable onDragStart={handleDragStart} {...touchDragProps}>
        <GripHorizontal class="drag-handle" />
        <span class="tile-number" title={formatShortcut(String(tileIndex + 1), { mod: true })}>{tileIndex + 1}</span>
        <span class={`state-dot ${session.state}`} />
        <span class="tile-title">{session.title || 'Untitled'}</span>
        {session.subagentCount > 0 && (
          <span class="subagent-badge"><GitFork />{session.subagentCount}</span>
        )}
        <ModelPill model={session.model} thinking={session.thinking} />
        <SettingsDropdown sessionId={sessionId} session={session} />
        <button class="tile-action-btn" onClick={(e) => stop(e, () => splitTile(tileId, 'horizontal'))} title="Split right"><Columns2 /></button>
        <button class="tile-action-btn" onClick={(e) => stop(e, () => splitTile(tileId, 'vertical'))} title="Split down"><Rows2 /></button>
        {canClose && (
          <button class="tile-action-btn tile-close-btn" onClick={(e) => stop(e, () => closeTile(tileId))} title="Close pane"><X /></button>
        )}
      </div>

      {session.untrustedMcp && <McpBanner sessionId={sessionId} />}
      <MessageList session={session} />
      <TaskBar session={session} />
      <InputBar sessionId={sessionId} sessionState={session.state} tileId={tileId} pendingSteers={session.pendingSteers} />
    </div>
  );
}
