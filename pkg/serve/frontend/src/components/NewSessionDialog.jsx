import { useState } from 'preact/hooks';
import { store, createSession, toggleDialog } from '../state.js';

export function NewSessionDialog({ open }) {
  const [title, setTitle] = useState('');
  const [model, setModel] = useState('sonnet');
  const [cwd, setCwd] = useState('');
  const [loading, setLoading] = useState(false);

  if (!open) return null;

  const handleCreate = async () => {
    setLoading(true);
    try {
      const opts = { model: model || 'sonnet' };
      if (title.trim()) opts.title = title.trim();
      if (cwd.trim()) opts.cwd = cwd.trim();
      await createSession(opts);
      setTitle('');
      setCwd('');
      toggleDialog();
    } catch (e) {
      console.error('Create session failed:', e);
    } finally {
      setLoading(false);
    }
  };

  const handleKeyDown = (e) => {
    if (e.key === 'Escape') toggleDialog();
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleCreate();
    }
  };

  return (
    <div class="dialog-overlay" onClick={(e) => { if (e.target === e.currentTarget) toggleDialog(); }}>
      <div class="dialog" onKeyDown={handleKeyDown}>
        <h2>New Session</h2>
        <label>Title (optional)</label>
        <input
          value={title}
          onInput={e => setTitle(e.target.value)}
          placeholder="e.g. refactor auth"
          autoFocus
        />
        <label>Model</label>
        <input
          value={model}
          onInput={e => setModel(e.target.value)}
          placeholder="sonnet"
        />
        <label>Working Directory (optional)</label>
        <input
          value={cwd}
          onInput={e => setCwd(e.target.value)}
          placeholder="/path/to/project"
        />
        <div class="dialog-actions">
          <button class="btn-cancel" onClick={toggleDialog}>Cancel</button>
          <button class="btn-create" onClick={handleCreate} disabled={loading}>
            {loading ? 'Creating…' : 'Create'}
          </button>
        </div>
      </div>
    </div>
  );
}
