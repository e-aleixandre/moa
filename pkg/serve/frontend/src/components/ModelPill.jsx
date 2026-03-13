import { shortModel } from '../util/format.js';

const LEVELS = ['low', 'medium', 'high'];

export function ModelPill({ model, thinking }) {
  const raw = (thinking || 'off').toLowerCase();
  const level = raw === 'none' ? 'off' : raw;
  const filled = level === 'high' ? 3 : level === 'medium' ? 2 : level === 'low' ? 1 : 0;

  return (
    <span class="model-pill">
      <span class="model-pill-name">{shortModel(model)}</span>
      <span class={`thinking-meter level-${filled}`} title={`Thinking: ${level}`}>
        {[0, 1, 2].map(i => (
          <span key={i} class={`thinking-block ${i < filled ? 'on' : ''}`} />
        ))}
      </span>
    </span>
  );
}
