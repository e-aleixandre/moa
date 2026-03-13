import { useState, useEffect, useRef } from 'preact/hooks';
import { Settings, Brain, ChevronDown, Check } from 'lucide-preact';
import { configureSession } from '../state.js';
import { api } from '../api.js';

const THINKING_LEVELS = [
  { value: 'off', label: 'Off' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
];

export function SettingsDropdown({ sessionId, session }) {
  const [open, setOpen] = useState(false);
  const [models, setModels] = useState(null);
  const ref = useRef(null);

  const busy = session?.state === 'running' || session?.state === 'permission';

  // Fetch models on first open
  useEffect(() => {
    if (open && !models) {
      api('GET', '/api/models').then(setModels).catch(() => setModels([]));
    }
  }, [open]);

  // Close on outside click
  useEffect(() => {
    if (!open) return;
    const handle = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener('mousedown', handle);
    return () => document.removeEventListener('mousedown', handle);
  }, [open]);

  const currentThinking = (session?.thinking === 'none' ? 'off' : session?.thinking) || 'medium';

  const handleModel = async (spec) => {
    try {
      await configureSession(sessionId, { model: spec, thinking: '' });
    } catch (e) {
      console.error('Model change failed:', e);
    }
  };

  const handleThinking = async (level) => {
    try {
      await configureSession(sessionId, { model: '', thinking: level });
    } catch (e) {
      console.error('Thinking change failed:', e);
    }
  };

  return (
    <div class="settings-dropdown-wrap" ref={ref}>
      <button
        class="settings-trigger"
        onClick={(e) => { e.stopPropagation(); setOpen(!open); }}
        title="Session settings"
      >
        <Settings />
      </button>

      {open && (
        <div class="settings-dropdown" onClick={(e) => e.stopPropagation()}>
          {busy && (
            <div class="settings-busy-note">
              Settings locked while agent is running
            </div>
          )}

          <div class="settings-section">
            <div class="settings-section-label"><Brain /> Thinking</div>
            <div class="settings-thinking-row">
              {THINKING_LEVELS.map(({ value, label }) => (
                <button
                  key={value}
                  class={`settings-thinking-btn ${currentThinking === value ? 'active' : ''}`}
                  onClick={() => !busy && handleThinking(value)}
                  disabled={busy}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>

          <div class="settings-section">
            <div class="settings-section-label"><ChevronDown /> Model</div>
            <div class="settings-model-list">
              {!models && <div class="settings-loading">Loading…</div>}
              {models && models.map((m) => {
                const spec = m.provider + '/' + m.id;
                const isActive = session?.model?.includes(m.name) || session?.model === m.name;
                return (
                  <button
                    key={m.id}
                    class={`settings-model-item ${isActive ? 'active' : ''}`}
                    onClick={() => !busy && handleModel(spec)}
                    disabled={busy}
                  >
                    <span class="settings-model-provider">{m.provider}</span>
                    <span class="settings-model-name">{m.name}</span>
                    {m.alias && <span class="settings-model-alias">{m.alias}</span>}
                    {isActive && <Check />}
                  </button>
                );
              })}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
