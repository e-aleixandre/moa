import { Bell, BellOff } from 'lucide-preact';
import { setLayout, toggleSound } from '../state.js';
import { LAYOUTS, getLayout } from '../layouts.js';

function LayoutPreview({ layout }) {
  const style = {
    display: 'grid',
    gridTemplateColumns: layout.grid.columns,
    gridTemplateRows: layout.grid.rows,
    gridTemplateAreas: layout.grid.areas || undefined,
    gap: '1.5px',
    width: '22px',
    height: '15px',
  };

  return (
    <div class="layout-mini" style={style}>
      {Array.from({ length: layout.count }, (_, i) => (
        <div
          key={i}
          class="layout-mini-cell"
          style={layout.tileAreas ? { gridArea: layout.tileAreas[i] } : undefined}
        />
      ))}
    </div>
  );
}

export function LayoutBar({ state }) {
  return (
    <div class="layout-bar">
      {LAYOUTS.map((l) => (
        <button
          key={l.id}
          class={`layout-btn ${state.layout === l.id ? 'active' : ''}`}
          onClick={() => setLayout(l.id)}
          title={`${l.id} (${l.count} tile${l.count > 1 ? 's' : ''})`}
        >
          <LayoutPreview layout={l} />
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
