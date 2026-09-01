// WebSocket history normalization and transcript projections.


// --- Message normalization ---

export function normalizeHistory(raw, liveSubagents = []) {
  const result = [];
  const resultMap = {};
  const legacySubagentJobIds = legacySubagentJobIdsOf(raw, liveSubagents);
  for (const msg of raw) {
    if (msg.role === 'tool_result') {
      resultMap[msg.tool_call_id] = msg;
    }
  }
  for (let index = 0; index < raw.length; index++) {
    const msg = raw[index];
    if (msg.role === 'assistant') {
      const textParts = [];
      for (const c of (msg.content || [])) {
        if (c.type === 'text' && c.text) {
          textParts.push(c.text);
        } else if (c.type === 'tool_call') {
          if (textParts.length > 0) {
            result.push({ role: 'assistant', _msg_id: msg.msg_id, timestamp: msg.timestamp, requested_model: msg.requested_model, model: msg.model, content: [{ type: 'text', text: textParts.join('') }] });
            textParts.length = 0;
          }
          const tr = resultMap[c.tool_call_id];
          let resultText = null;
          let status = 'running';
          if (tr) {
            resultText = (tr.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
            if (tr.custom?.rejected === true) {
              status = 'rejected';
            } else if (tr.is_error) {
              status = 'error';
            } else {
              status = 'done';
            }
          }
          result.push({
            _type: 'tool_start',
            _msg_id: msg.msg_id,
            tool_call_id: c.tool_call_id,
            tool_name: c.tool_name,
            args: c.arguments || {},
            status,
            result: resultText,
            note: extractToolNote(resultText, status === 'rejected'),
            // The subagent tool records the job it spawned on its result: the
            // tool call ID is the provider's, so this is the only link from a
            // restored card to a subagent transcript on disk.
            subagentJobId: tr?.custom?.subagent_job_id || undefined,
            timestamp: tr?.timestamp || msg.timestamp,
          });
        }
      }
      if (textParts.length > 0) {
        result.push({ role: 'assistant', _msg_id: msg.msg_id, timestamp: msg.timestamp, requested_model: msg.requested_model, model: msg.model, content: [{ type: 'text', text: textParts.join('') }] });
      }
    } else if (msg.role === 'shell' || (msg.role === 'user' && msg.custom?.shell)) {
      const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
      const { command, output } = parseShellBody(text);
      result.push({
        _type: 'tool_start',
        _msg_id: msg.msg_id,
        tool_call_id: 'shell_' + (msg.msg_id || index),
        tool_name: 'bash',
        args: { command },
        status: 'done',
        result: output,
      });
    } else if (msg.role === 'goal') {
      // Persistent goal-lifecycle marker (start / iteration verdict / end).
      // Rendered as a system line, matching the live goal event styling.
      const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
      result.push({ _type: 'system', _msg_id: msg.msg_id, text });
    } else if (msg.role === 'session_event' && msg.custom?.type === 'compaction_marker') {
      // Compaction entries are durable tree events, rather than conversational
      // messages. Preserve their complete payload as a first-class normalized
      // row so the stream projection can render the same card after init and
      // when a command event includes the freshly persisted entry.
      result.push({
        _type: 'compaction_marker',
        _msg_id: msg.msg_id,
        timestamp: msg.timestamp,
        summary: typeof msg.custom.summary === 'string' ? msg.custom.summary : '',
        tokensBefore: Number.isFinite(msg.custom.tokens_before) ? msg.custom.tokens_before : 0,
        readFiles: Array.isArray(msg.custom.read_files) ? msg.custom.read_files.filter(f => typeof f === 'string') : [],
        modifiedFiles: Array.isArray(msg.custom.modified_files) ? msg.custom.modified_files.filter(f => typeof f === 'string') : [],
      });
    } else if (msg.role === 'user') {
      if (msg.custom?.source === 'compaction_notice') {
        // moa talking to itself. It rides as a user message because providers
        // accept no other role mid-conversation, but rendering it as one puts
        // a <system-reminder> block in the transcript under the user's name.
        result.push({
          _type: 'system',
          _msg_id: msg.msg_id,
          timestamp: msg.timestamp,
          text: '⚠ Context filling up — asked the agent to save unsaved work',
        });
      } else if (msg.custom?.source === 'secret_batch') {
        result.push({
          _type: 'secret_batch',
          _msg_id: msg.msg_id,
          timestamp: msg.timestamp,
          aliases: Array.isArray(msg.custom.secret_aliases) ? msg.custom.secret_aliases : [],
        });
      } else if (msg.custom?.source === 'subagent') {
        // When a real job ID is available, key the restored card
        // `subagent-<jobId>` so projectStream folds it into the turn's
        // delegation block by that ID. Unmatched legacy cards retain a
        // synthetic key. accentIndex, if saved, keeps the row's color stable
        // across reloads; the projection falls back to a jobId hash otherwise.
        const jobId = msg.custom.subagent_job_id ||
          legacySubagentJobIds.get(subagentTaskIdentity(msg.custom.subagent_task));
        result.push({
          _type: 'tool_start',
            _msg_id: msg.msg_id,
          tool_call_id: jobId ? 'subagent-' + jobId : 'subagent_' + (msg.msg_id || index),
          tool_name: 'subagent',
          args: { task: msg.custom.subagent_task || '' },
          status: subagentRestoreStatus(msg.custom.subagent_status),
          accentIndex: Number.isInteger(msg.custom.subagent_accent_index)
            ? msg.custom.subagent_accent_index
            : undefined,
          result: msg.custom.subagent_status === 'completed' ? (msg.custom.subagent_result || '') : '',
          error: msg.custom.subagent_status === 'failed' ? (msg.custom.subagent_result || '') : '',
          timestamp: msg.timestamp,
        });
      } else if (msg.custom?.source === 'bash_job') {
        const bashText = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
        result.push({
          _type: 'tool_start',
            _msg_id: msg.msg_id,
          tool_call_id: 'bash_complete_' + (msg.msg_id || index),
          tool_name: 'bash',
          args: { command: msg.custom.bash_command || '' },
          status: (msg.custom.bash_status || '') === 'failed' ? 'error' : 'done',
          result: bashText,
        });
      } else {
        // Backwards compatibility: detect prefix-based notifications
        // from sessions saved before custom metadata was introduced.
        const userText = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
        const subagent = parseSubagentNotification(userText);
        if (subagent) {
          const jobId = subagent.jobId || legacySubagentJobIds.get(subagentTaskIdentity(subagent.task));
          result.push({
            _type: 'tool_start',
            _msg_id: msg.msg_id,
            tool_call_id: jobId ? 'subagent-' + jobId : 'subagent_' + (msg.msg_id || index),
            subagentJobId: jobId || undefined,
            tool_name: 'subagent',
            args: { task: subagent.task },
            status: subagentRestoreStatus(subagent.status),
            result: subagent.result,
          });
        } else {
          const bash = parseBashNotification(userText);
          if (bash) {
            result.push({
              _type: 'tool_start',
            _msg_id: msg.msg_id,
              tool_call_id: 'bash_complete_' + (msg.msg_id || index),
              tool_name: 'bash',
              args: { command: bash.command },
              status: bash.status === 'failed' ? 'error' : 'done',
              result: userText,
            });
          } else {
            // Preserve the server's msg_id as _msg_id so a later Steered event
            // (seq > snapshot cut) can dedup this same user message by identity
            // instead of appending it a second time.
            result.push(msg.msg_id ? { ...msg, _msg_id: msg.msg_id } : msg);
          }
        }
      }
    }
  }
  return result;
}

// A delta init is a suffix of the durable tree, not a replay stream. Append it
// in place so the cached prefix keeps both its array and row identities; this
// lets stream projection memoization retain the expensive already-rendered
// history. Tool results are special: their matching assistant call may be in
// that prefix, while normalizeHistory normally sees both at once.
export function appendNormalizedHistoryDelta(prefix, raw, liveSubagents = []) {
  const toolResults = new Map();
  for (const msg of raw) {
    if (msg?.role !== 'tool_result' || !msg.tool_call_id) continue;
    const result = (msg.content || []).filter(c => c.type === 'text').map(c => c.text).join('');
    const update = {
      result,
      status: msg.custom?.rejected === true ? 'rejected' : msg.is_error ? 'error' : 'done',
      timestamp: msg.timestamp,
    };
    // Carry the spawned job the same way normalizeHistory does when it sees the
    // call and its result together. A delta splits them: the launch row is
    // already in the cached prefix, so dropping this link leaves that row
    // unmatchable and the turn draws a second, unopenable card for one child.
    const subagentJobId = msg.custom?.subagent_job_id;
    if (subagentJobId) update.subagentJobId = subagentJobId;
    toolResults.set(msg.tool_call_id, update);
  }
  for (let i = 0; i < prefix.length; i++) {
    const row = prefix[i];
    const update = row?._type === 'tool_start' && toolResults.get(row.tool_call_id);
    if (!update) continue;
    prefix[i] = {
      ...row, ...update,
      note: extractToolNote(update.result, update.status === 'rejected'),
      timestamp: update.timestamp || row.timestamp,
    };
  }

  for (const row of normalizeHistory(raw, liveSubagents)) {
    const duplicate = row._type === 'tool_start'
      ? prefix.some(existing => existing?._type === 'tool_start' && existing.tool_call_id === row.tool_call_id)
      : row._msg_id && prefix.some(existing => existing?._type !== 'tool_start' && existing._msg_id === row._msg_id);
    if (!duplicate) prefix.push(row);
  }
  return prefix;
}

// Match an old terminal card to a snapshot job only when the task identifies
// exactly one card and one live job. This lets a legacy card use the same
// canonical key as a current card without suppressing distinct live jobs.
function legacySubagentJobIdsOf(raw, liveSubagents) {
  const historyTaskCounts = new Map();
  for (const msg of (raw || [])) {
    const task = legacySubagentTaskOf(msg);
    if (task) historyTaskCounts.set(task, (historyTaskCounts.get(task) || 0) + 1);
  }

  const liveJobsByTask = new Map();
  for (const subagent of (liveSubagents || [])) {
    if (!subagent || !subagent.job_id ||
        (subagent.status && subagent.status !== 'running' && subagent.status !== 'cancelling')) continue;
    const task = subagentTaskIdentity(subagent.task);
    if (!task) continue;
    const jobs = liveJobsByTask.get(task) || [];
    jobs.push(subagent.job_id);
    liveJobsByTask.set(task, jobs);
  }

  const matched = new Map();
  for (const [task, count] of historyTaskCounts) {
    const jobs = liveJobsByTask.get(task);
    if (count === 1 && jobs?.length === 1) matched.set(task, jobs[0]);
  }
  return matched;
}

function legacySubagentTaskOf(msg) {
  if (!msg || msg.role !== 'user') return '';
  if (msg.custom?.source === 'subagent') {
    return msg.custom.subagent_job_id ? '' : subagentTaskIdentity(msg.custom.subagent_task);
  }
  const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
  return subagentTaskIdentity(parseSubagentNotification(text)?.task);
}

function subagentTaskIdentity(task) {
  return String(task || '').trim();
}

// normalizeConversationProjection adapts the REST transcript DTO used by
// persisted subagents to MessageList's established render model. Tool result
// output is outside the default transcript budget, but action and target are
// retained so persisted activity is as informative as live activity.
export function normalizeConversationProjection(raw, toolDetailBase = '') {
  return (raw || []).map(item => {
    if (item.role === 'tool') {
      const status = item.status === 'ok' ? 'done'
        : item.status === 'pending' ? 'running'
          : item.status || 'running';
      return {
        _type: 'tool_start',
        tool_call_id: item.id,
        tool_name: item.tool || 'tool',
        args: projectionToolArgs(item),
        activity: { action: item.action || '', target: item.target || '' },
        status,
        result: null,
        ...(toolDetailBase && item.id
          ? { detailUrl: `${toolDetailBase}?detail=full&item_id=${encodeURIComponent(item.id)}` }
          : {}),
      };
    }
    if (item.role === 'compaction_summary') {
      // The child's compaction reaches the stream as the same marker the parent
      // emits, so both render one card instead of leaving an unexplained gap.
      // Children persist only the summary text — no token or file counts — and
      // the card already omits what is absent.
      return {
        _type: 'compaction_marker',
        _msg_id: item.id,
        summary: item.text || '',
      };
    }
    return {
      role: item.role,
      _msg_id: item.id,
      content: item.text ? [{ type: 'text', text: item.text }] : [],
      ...(item.source ? { custom: { source: item.source } } : {}),
    };
  });
}

function projectionToolArgs(item) {
  const target = item.target || '';
  if (target.startsWith('{')) {
    try {
      const args = JSON.parse(target);
      if (args && typeof args === 'object' && !Array.isArray(args)) return args;
    } catch { /* truncated JSON remains useful as a display target below */ }
  }
  if (!target) return {};
  switch (item.tool) {
    case 'bash': return { command: target };
    case 'edit':
    case 'write': return { path: target };
    case 'fetch_content': return { url: target };
    case 'subagent': return { task: target };
    case 'web_search': return { query: target };
    default: return { target };
  }
}

function parseShellBody(body) {
  if (!body.startsWith('$ ')) return { command: '', output: body };
  const rest = body.slice(2);
  const nl = rest.indexOf('\n');
  if (nl < 0) return { command: rest, output: '' };
  const command = rest.slice(0, nl);
  let output = rest.slice(nl + 1);
  if (output === '(no output)') output = '';
  return { command, output };
}

export function extractToolNote(result, rejected) {
  const text = (result || '').trim();
  if (!text) return null;

  if (rejected) {
    let reason = text;
    if (reason.startsWith('Error: ')) reason = reason.slice('Error: '.length);
    if (reason.startsWith('Permission denied: ')) reason = reason.slice('Permission denied: '.length);
    reason = reason.trim();
    if (!reason || reason === 'denied by user') return 'Rejected';
    return `Rejected reason: ${reason}`;
  }

  const marker = 'Permission feedback:';
  const idx = text.lastIndexOf(marker);
  if (idx < 0) return null;
  const fb = text.slice(idx + marker.length).trim();
  if (!fb) return null;
  return `Feedback: ${fb}`;
}


/** Parse a subagent notification from a user message text (mirrors TUI's parseSubagentNotification). */
function subagentRestoreStatus(raw) {
  // Backend persists completed | failed | cancelled. Map to the projection's
  // tool_start status vocabulary, keeping `cancelled` distinct from `error`
  // so DelegationBlock can render ⊘ instead of ✗.
  const s = String(raw || '');
  if (s === 'completed') return 'done';
  if (s === 'cancelled') return 'cancelled';
  return 'error';
}

export function parseSubagentNotification(text) {
  const prefixes = {
    '[subagent completed] ': 'completed',
    '[subagent failed] ': 'failed',
    '[subagent cancelled] ': 'cancelled',
  };
  for (const [prefix, status] of Object.entries(prefixes)) {
    if (text.startsWith(prefix)) {
      const rest = text.slice(prefix.length);
      const firstNewline = rest.indexOf('\n');
      const jobLine = firstNewline >= 0 ? rest.slice(0, firstNewline) : rest;
      const jobMatch = /^Job (\S+) (?:finished|failed|was cancelled)\.$/.exec(jobLine);
      let task = '';
      let result = '';
      const payload = firstNewline >= 0 ? rest.slice(firstNewline + 1) : '';
      if (payload.startsWith('Task: ')) {
        const taskAndResult = payload.slice('Task: '.length);
        const markers = [
          '\n\nResult (last 50 lines):\n',
          '\n\nResult (truncated — use subagent_status for full output):\n',
          '\n\nResult:\n',
          '\nError: ',
        ];
        let markerAt = -1;
        let marker = '';
        for (const candidate of markers) {
          const at = taskAndResult.indexOf(candidate);
          if (at >= 0 && (markerAt < 0 || at < markerAt)) {
            markerAt = at;
            marker = candidate;
          }
        }
        if (markerAt >= 0) {
          task = taskAndResult.slice(0, markerAt).trim();
          result = taskAndResult.slice(markerAt + marker.length).trim();
        } else {
          task = taskAndResult.trim();
        }
      }
      return { jobId: jobMatch ? jobMatch[1] : '', task, status, result };
    }
  }
  return null;
}

/** Parse an async bash completion notification from a user message text (mirrors TUI's parseBashNotification). */
function parseBashNotification(text) {
  const prefixes = {
    '[bash job completed] ': 'completed',
    '[bash job failed] ': 'failed',
    '[bash job cancelled] ': 'cancelled',
  };
  for (const [prefix, status] of Object.entries(prefixes)) {
    if (text.startsWith(prefix)) {
      const rest = text.slice(prefix.length);
      const lines = rest.split('\n');
      let command = '';
      if (lines.length >= 2 && lines[1].startsWith('Command: ')) {
        command = lines[1].slice('Command: '.length);
      }
      return { command, status };
    }
  }
  return null;
}
