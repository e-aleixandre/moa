import { Bell, BellOff } from 'lucide-preact';
import { applyPreset, toggleSound } from '../state.js';
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

export function LayoutBar({ state }) {
  return (
    <div class="layout-bar">
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
