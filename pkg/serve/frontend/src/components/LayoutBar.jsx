import { setLayout, toggleSound } from '../state.js';

const LAYOUTS = [1, 2, 4, 6];

export function LayoutBar({ state }) {
  return (
    <div class="layout-bar">
      {LAYOUTS.map(n => (
        <button
          key={n}
          class={`layout-btn ${state.layout === n ? 'active' : ''}`}
          onClick={() => setLayout(n)}
          title={`${n} tile${n > 1 ? 's' : ''}`}
        >
          {n === 1 ? '□' : n === 2 ? '⬜⬜' : n === 4 ? '⊞' : '⊞⊞'}
        </button>
      ))}
      <div class="layout-bar-spacer" />
      <button
        class={`sound-toggle ${state.soundEnabled ? 'on' : ''}`}
        onClick={toggleSound}
        title={state.soundEnabled ? 'Sound on' : 'Sound off'}
      >
        {state.soundEnabled ? '🔔' : '🔕'}
      </button>
    </div>
  );
}
