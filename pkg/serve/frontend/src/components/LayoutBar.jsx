import { Square, Columns2, Grid2x2, LayoutGrid, Grid3x3, Bell, BellOff } from 'lucide-preact';
import { setLayout, toggleSound } from '../state.js';
import { formatShortcut } from '../hooks/useHotkeys.js';

const LAYOUTS = [
  { n: 1, icon: Square, label: '1 tile' },
  { n: 2, icon: Columns2, label: '2 tiles' },
  { n: 4, icon: Grid2x2, label: '4 tiles' },
  { n: 6, icon: LayoutGrid, label: '6 tiles' },
  { n: 8, icon: Grid3x3, label: '8 tiles' },
];

export function LayoutBar({ state }) {
  return (
    <div class="layout-bar">
      {LAYOUTS.map(({ n, icon: Icon, label }) => (
        <button
          key={n}
          class={`layout-btn ${state.layout === n ? 'active' : ''}`}
          onClick={() => setLayout(n)}
          title={`${label} (${formatShortcut(String(n), { ctrl: true })})`}
        >
          <Icon />
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
