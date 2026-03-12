import { useState } from 'preact/hooks';
import { resolvePermission } from '../state.js';
import { formatArgs } from '../util/format.js';

export function PermissionCard({ perm, sessionId }) {
  const [resolved, setResolved] = useState(null); // 'approved' | 'denied' | null

  const handleResolve = async (approved) => {
    setResolved(approved ? 'approved' : 'denied');
    try {
      await resolvePermission(sessionId, perm.id, approved);
    } catch (e) {
      console.error('Permission resolve failed:', e);
      setResolved(null);
    }
  };

  if (resolved) {
    return (
      <div class="permission-resolved">
        {resolved === 'approved' ? '✓ Approved' : '✗ Denied'}
      </div>
    );
  }

  return (
    <div class="permission-card">
      <div class="permission-card-title">🔒 Permission Required</div>
      <div class="permission-card-tool">
        {perm.tool_name}{'\n'}{formatArgs(perm.args)}
      </div>
      <div class="permission-card-actions">
        <button class="btn-approve" onClick={() => handleResolve(true)}>Approve</button>
        <button class="btn-deny" onClick={() => handleResolve(false)}>Deny</button>
      </div>
    </div>
  );
}
