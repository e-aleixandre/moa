import { Bell, BellOff, Search, PanelRight, PanelBottom } from 'lucide-preact';
import { applyPreset, addPane, toggleSound } from '../state.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { PRESETS } from '../layoutPresets.js';

function LayoutPreview({ preset }) {
  return (
    <div class="layout-mini" style={preset.miniStyle}>
      {preset.cells.map((cell, i) => (
        <div key={i} class="layout-mini-cell" style={cell} />
      ))}
    </div>
  );
}

export function LayoutBar({ state, onOpenPalette }) {
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
      <button
        class={`sound-toggle ${state.soundEnabled ? 'on' : ''}`}
        onClick={toggleSound}
        title={state.soundEnabled ? 'Sound on' : 'Sound off'}
      >
        {state.soundEnabled ? <Bell /> : <BellOff />}
      </button>
    </div>
  );
}
