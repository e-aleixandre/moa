import { Search, PanelRight, PanelBottom, Bell, QrCode } from 'lucide-preact';
import { applyPreset, addPane, assignToTile } from '../tile-actions.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { PRESETS } from '../layoutPresets.js';
import { NotificationSettings } from './NotificationSettings.jsx';

function LayoutPreview({ preset }) {
  return (
    <div class="layout-mini" style={preset.miniStyle}>
      {preset.cells.map((cell, i) => (
        <div key={i} class="layout-mini-cell" style={cell} />
      ))}
    </div>
  );
}

export function VersionIndicator({ version }) {
  if (!version?.current) return <span class="version-indicator" title="Version unavailable">version unavailable</span>;
  if (version.update_available) {
    return <a class="version-indicator update" href="https://github.com/ealeixandre/moa/releases/latest" target="_blank" rel="noreferrer" title={`Update available: ${version.latest}`}>
      {version.current} ↑ {version.latest}
    </a>;
  }
  return <span class="version-indicator" title="Moa version">{version.current}</span>;
}

export function LayoutBar({ state, onOpenPalette, onOpenPairing, version }) {
	const attentionItems = state.attentionItems || [];
	const openAttentionSession = (sessionId) => {
		if (sessionId) assignToTile(state.focusedTile, sessionId);
	};

  return (
    <div class="layout-bar">
      <button
        class="palette-trigger"
        onClick={onOpenPalette}
        title={`Sessions (${formatShortcut('K', { mod: true })})`}
      >
        <Search />
        <span>Sessions</span>
        <kbd class="shortcut-hint">{formatShortcut('K', { mod: true })}</kbd>
      </button>
      <button class="layout-btn" onClick={onOpenPairing} title="Pair Pulse">
        <QrCode />
      </button>
      <div class="layout-bar-divider" />
      {PRESETS.map((p) => (
        <button
          key={p.id}
          class="layout-btn"
          onClick={() => applyPreset(p.id)}
          title={p.label}
        >
          <LayoutPreview preset={p} />
        </button>
      ))}
      <div class="layout-bar-divider" />
      <button class="layout-btn add-pane-btn" onClick={() => addPane('horizontal')} title="Add column">
        <PanelRight />
      </button>
      <button class="layout-btn add-pane-btn" onClick={() => addPane('vertical')} title="Add row">
        <PanelBottom />
      </button>
      <div class="layout-bar-spacer" />
      {attentionItems.length > 0 && (
        <div class="layout-attention" aria-label="Needs attention">
          <Bell />
          <span>Needs attention</span>
          {attentionItems.map(item => (
            <button key={item.id} title={item.spoken} onClick={() => openAttentionSession(item.session_id)}>
              {item.alias || item.spoken}
            </button>
          ))}
        </div>
      )}
      <VersionIndicator version={version} />
      <NotificationSettings state={state} />
    </div>
  );
}
