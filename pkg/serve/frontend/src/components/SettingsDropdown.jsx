import { useState, useEffect, useRef } from 'preact/hooks';
import { Settings, Brain, ChevronDown, Check, Shield } from 'lucide-preact';
import { configureSession } from '../session-actions.js';
import { api } from '../api.js';

const THINKING_LEVELS = [
  { value: 'off', label: 'Off' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'Extra' },
];

const PERMISSION_MODES = [
  { value: 'yolo', label: 'YOLO', desc: 'Auto-approve all' },
  { value: 'ask', label: 'ASK', desc: 'Confirm writes' },
  { value: 'auto', label: 'AUTO', desc: 'AI decides' },
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
      await configureSession(sessionId, { thinking: level });
    } catch (e) {
      console.error('Thinking change failed:', e);
    }
  };

  const handlePermission = async (mode) => {
    try {
      await configureSession(sessionId, { permissionMode: mode });
    } catch (e) {
      console.error('Permission mode change failed:', e);
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
            <div class="settings-section-label"><Shield /> Permissions</div>
            <div class="settings-thinking-row">
              {PERMISSION_MODES.map(({ value, label, desc }) => (
                <button
                  key={value}
                  class={`settings-thinking-btn ${(session?.permissionMode || 'yolo') === value ? 'active' : ''}`}
                  onClick={() => !busy && handlePermission(value)}
                  disabled={busy}
                  title={desc}
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
