import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { ArrowLeft, Loader2, Trash2, X } from 'lucide-preact';
import { useVoiceGesture } from '../../hooks/useVoiceGesture.js';
import { useCanTranscribe } from '../../hooks/useCanTranscribe.js';
import { MicButton } from '../MicButton/MicButton.jsx';
import { sendMessage } from '../../data/session-actions.js';
import { closeArtifacts } from '../../data/artifacts.js';
import { addToast } from '../../data/notifications.js';
import { copyToClipboard } from '../../data/util/format.js';
import { requestConversationPin } from '../../data/stream-scroll.js';
import { clip, docNotesMessage, loadNotes, saveNotes } from '../../data/doc-notes.js';
import './DocNotes.css';

let seq = 0;
const newId = () => `n${Date.now().toString(36)}${(seq++).toString(36)}`;

// useDocNotes — owns the notes of the open artifact. The reader feeds it picks
// (a tapped block, a selection, a quick reply); the dock turns a pick into a
// note. `sessionId` is the conversation that delivered the document: that is
// where its notes go (the artifact URL's own session, so a subagent's file
// published in its parent is answered in the parent).
export function useDocNotes(sessionId, artifact) {
  const fileId = artifact?.id;
  const [notes, setNotes] = useState(() => (fileId ? loadNotes(sessionId, fileId) : []));
  const [pick, setPick] = useState(null);
  const [editingId, setEditingId] = useState(null);
  const [reviewing, setReviewing] = useState(false);
  const [sending, setSending] = useState(false);
  const [flash, setFlash] = useState('');
  // The notes that failed to go, with the document they belong to, or null.
  const [failed, setFailed] = useState(null);
  const sendError = failed && failed.sessionId === sessionId && failed.fileId === fileId ? failed : null;
  // Where each text note sits in the document as it is NOW (see resolveAnchors),
  // set by the reader. Null until the reader has resolved; then a note the
  // document no longer holds has no place, and so never answers a tap.
  const resolvedRef = useRef(null);
  const anchorOf = (n) => {
    const resolved = resolvedRef.current;
    if (!resolved) return n.anchor;
    return resolved.has(n.id) ? resolved.get(n.id) : null;
  };
  const sendingRef = useRef(false);
  // What the note field holds right now, so switching to another block never
  // throws away what was typed. The field owns it; the hook only reads it.
  const typed = useRef('');
  const voiceRef = useRef(null);
  const latest = useRef({});
  latest.current = { notes, pick, editingId, sessionId, fileId, sendError, anchorOf };

  const persist = useCallback((next) => {
    setNotes(next);
    const { sessionId: s, fileId: f } = latest.current;
    if (f) saveNotes(s, f, next);
  }, []);

  // A typed-but-unsaved note becomes a draft instead of vanishing when the
  // pick moves elsewhere or the reader closes.
  // `at` names the document the note belongs to when the reader is already
  // moving to another one.
  const flush = useCallback((at) => {
    const { notes: current, pick: p, editingId: id } = latest.current;
    const comment = typed.current.trim();
    typed.current = '';
    if (!p || !comment) return current;
    const next = id
      ? current.map((n) => (n.id === id ? { ...n, comment } : n))
      : [...current, { ...p, id: newId(), comment, created: Date.now() }];
    if (at) saveNotes(at.sessionId, at.fileId, next);
    else persist(next);
    return next;
  }, [persist]);

  useEffect(() => {
    setNotes(fileId ? loadNotes(sessionId, fileId) : []);
    setPick(null); setEditingId(null); setReviewing(false); setFailed(null);
    resolvedRef.current = null;
    typed.current = '';
    return () => { if (fileId) flush({ sessionId, fileId }); };
  }, [sessionId, fileId]);

  const drafts = notes.filter((n) => !n.sentAt);

  const choose = useCallback((next) => {
    const { pick: p, editingId: id } = latest.current;
    // Moving the handles of a selection inside the same block refines the pick
    // being written; it is not a new note.
    if (p && !id && next.anchor != null && next.anchor === p.anchor) {
      setPick({ ...p, quote: next.quote, precise: next.precise });
      return;
    }
    const after = flush();
    // Tapping a block that already has a draft opens that draft.
    const existing = !next.precise && next.anchor != null
      && after.find((n) => !n.sentAt && latest.current.anchorOf(n) === next.anchor && n.kind !== 'area');
    if (existing) {
      typed.current = existing.comment || '';
      setPick({ ...existing });
      setEditingId(existing.id);
      return;
    }
    typed.current = '';
    setEditingId(null);
    setPick(next);
  }, [flush]);

  const cancel = useCallback(() => { typed.current = ''; setPick(null); setEditingId(null); }, []);

  const add = useCallback((comment) => {
    const { notes: current, pick: p, editingId: id } = latest.current;
    if (!p) return;
    typed.current = '';
    persist(id
      ? current.map((n) => (n.id === id ? { ...n, comment } : n))
      : [...current, { ...p, id: newId(), comment, created: Date.now() }]);
    setPick(null); setEditingId(null);
  }, [persist]);

  // The send is slow and the reader is not frozen while it runs, so what it
  // settles is identified up front: the document it started on and the notes it
  // carried. Nothing it does afterwards reads "the current" reader.
  const deliver = useCallback(async (batch, target) => {
    const { text, attachments } = docNotesMessage({ name: artifact.name, url: artifact.url }, batch);
    sendingRef.current = true;
    setSending(true);
    setFailed(null);
    try {
      // sendMessage returns nothing when the conversation is not loaded here:
      // that is a failure the user must hear about, not a send.
      const action = await sendMessage(target.sessionId, text, attachments);
      if (!action) throw new Error('not sent');
      return true;
    } catch (_) {
      // What failed to go is what "Copy notes" must hand back, under the name
      // of the document it was written on.
      setFailed({ ...target, batch, name: artifact.name, url: artifact.url });
      return false;
    } finally {
      sendingRef.current = false;
      setSending(false);
    }
  }, [artifact]);

  // stamp sets (or, with no time, takes back) the sent mark on `batch` under the
  // key it belongs to, whichever document the reader is on by now.
  const stamp = useCallback((target, batch, at) => {
    const here = latest.current.sessionId === target.sessionId && latest.current.fileId === target.fileId;
    const base = here ? latest.current.notes : loadNotes(target.sessionId, target.fileId);
    const byId = new Map(batch.map((n) => [n.id, { ...n, sentAt: at }]));
    const next = base.map((n) => byId.get(n.id) || n);
    for (const [id, n] of byId) if (!base.some((b) => b.id === id)) next.push(n);
    saveNotes(target.sessionId, target.fileId, next);
    if (here) setNotes(next);
    return here;
  }, []);

  // Send now closes the note at once: it is stamped sent before the request
  // goes and handed back as a draft if the request fails, so nothing typed
  // meanwhile can collide with it.
  const sendNow = useCallback(async (comment) => {
    const { notes: current, pick: p, editingId: id, sessionId: s, fileId: f } = latest.current;
    if (!p) return;
    const note = { ...(id ? current.find((n) => n.id === id) : null), ...p, id: id || newId(), comment, created: Date.now() };
    const target = { sessionId: s, fileId: f };
    stamp(target, [note], Date.now());
    typed.current = '';
    setPick(null); setEditingId(null);
    if (!(await deliver([note], target))) { stamp(target, [note], undefined); return; }
    setFlash('Sent');
    setTimeout(() => setFlash(''), 1800);
  }, [deliver, stamp]);

  const sendAll = useCallback(async () => {
    if (sendingRef.current) return;
    const after = flush();
    const batch = after.filter((n) => !n.sentAt);
    if (!batch.length) return;
    const target = { sessionId: latest.current.sessionId, fileId: latest.current.fileId };
    requestConversationPin(target.sessionId);
    if (!(await deliver(batch, target))) return;
    const here = stamp(target, batch, Date.now());
    if (!here) return;
    setPick(null); setEditingId(null); setReviewing(false);
    closeArtifacts();
  }, [deliver, flush, stamp]);

  // A quick reply is a whole note on its own; the same reply again takes it
  // back, another one replaces it.
  const quick = useCallback((target, reply) => {
    const current = flush();
    const existing = current.find((n) => !n.sentAt && latest.current.anchorOf(n) === target.anchor && n.kind !== 'area');
    if (existing && existing.comment === reply) persist(current.filter((n) => n.id !== existing.id));
    else if (existing) persist(current.map((n) => (n.id === existing.id ? { ...n, comment: reply } : n)));
    else persist([...current, { ...target, id: newId(), comment: reply, created: Date.now() }]);
    cancel();
  }, [flush, persist, cancel]);

  const remove = useCallback((id) => {
    const next = latest.current.notes.filter((n) => n.id !== id);
    persist(next);
    if (!next.some((n) => !n.sentAt)) setReviewing(false);
  }, [persist]);

  const edit = useCallback((id) => {
    const note = latest.current.notes.find((n) => n.id === id);
    if (!note) return;
    typed.current = note.comment || '';
    setReviewing(false);
    setEditingId(id);
    setPick({ ...note });
  }, []);

  const copyNotes = useCallback(async () => {
    const failedSend = latest.current.sendError;
    const file = failedSend ? { name: failedSend.name, url: failedSend.url } : { name: artifact.name, url: artifact.url };
    const batch = failedSend ? failedSend.batch : latest.current.notes.filter((n) => !n.sentAt);
    const ok = await copyToClipboard(docNotesMessage(file, batch).text);
    addToast({ title: ok ? 'Notes copied' : 'Could not copy', type: ok ? 'info' : 'error' });
  }, [artifact]);

  // Escape peels one layer: a live recording, then the review page, then the
  // open note. False means there was nothing of ours to close.
  const escape = useCallback(() => {
    const { pick: p } = latest.current;
    if (voiceRef.current?.recording) { voiceRef.current.cancel(); return true; }
    if (reviewing) { setReviewing(false); return true; }
    if (p) { cancel(); return true; }
    return false;
  }, [reviewing, cancel]);

  return {
    notes, drafts, pick, editingId, reviewing, sending, flash, sendError, typed, voiceRef,
    choose, cancel, add, sendNow, sendAll, remove, edit, quick, copyNotes, resolvedRef, escape, setReviewing,
  };
}

// The dock under the reader: a hint, the tray, or the note being written.
export function DocNotesDock({ dn }) {
  if (dn.reviewing) return null;
  if (dn.pick) return <NoteComposer dn={dn} key={dn.editingId || dn.pick.anchor} />;
  const count = dn.drafts.length;
  return (
    <div class={`dn-dock${count ? ' has-notes' : ' dn-dock-hint'}`}>
      {dn.sendError && (
        <span class="dn-error" role="alert">
          Not sent
          <button type="button" class="dn-link" onClick={dn.copyNotes}>Copy notes</button>
        </span>
      )}
      {!count && !dn.flash && !dn.sendError && <span class="dn-hint-t">Tap a paragraph to comment</span>}
      {dn.flash && !count && <span class="dn-hint-t dn-flash">{dn.flash}</span>}
      {count > 0 && (
        <>
          <button type="button" class="dn-tray" onClick={() => dn.setReviewing(true)} aria-label={`Review ${count} ${count === 1 ? 'note' : 'notes'}`}>
            <span class="dn-tray-n">{count}</span>
            <span>{count === 1 ? 'note' : 'notes'}</span>
            {dn.flash && <span class="dn-flash">· {dn.flash}</span>}
          </button>
          <button type="button" class="dn-btn dn-btn-primary" onClick={dn.sendAll} disabled={dn.sending}>
            {dn.sending ? <Loader2 size={15} class="spin" /> : null}
            Send {count}
          </button>
        </>
      )}
    </div>
  );
}

function NoteComposer({ dn }) {
  const { pick } = dn;
  const [text, setText] = useState(dn.typed.current || pick.comment || '');
  const [voiceError, setVoiceError] = useState('');
  const field = useRef(null);
  const canTranscribe = useCanTranscribe();
  const change = useCallback((value) => { dn.typed.current = value; setText(value); }, [dn.typed]);
  // Dictation lands after what is already written, like the composer's.
  const insert = useCallback((spoken) => {
    setVoiceError('');
    setText((prev) => {
      const next = prev.trim() ? `${prev.trimEnd()} ${spoken}` : spoken;
      dn.typed.current = next;
      return next;
    });
  }, [dn.typed]);
  const voice = useVoiceGesture({ onTranscript: insert, onError: setVoiceError });
  const voiceUsable = canTranscribe && voice.supported;
  useEffect(() => {
    dn.voiceRef.current = voice;
    return () => { dn.voiceRef.current = null; };
  });

  useLayoutEffect(() => {
    const node = field.current;
    if (!node) return;
    node.style.height = 'auto';
    node.style.height = `${Math.min(node.scrollHeight, 132)}px`;
  }, [text]);

  const onKey = (event) => {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey) && text.trim()) {
      event.preventDefault();
      dn.add(text.trim());
    }
  };
  const ready = text.trim().length > 0;
  return (
    <div class="dn-dock dn-compose" onKeyDown={onKey}>
      <div class="dn-quote">
        <span class="dn-quote-q">«{clip(pick.quote)}»</span>
        {pick.where && <span class="dn-quote-where">{pick.where}</span>}
        <button type="button" class="dn-x" aria-label="Discard note" onClick={dn.cancel}><X size={16} /></button>
      </div>
      <div class="dn-field">
        <textarea
          ref={field}
          rows={1}
          value={text}
          placeholder={voiceUsable ? 'Write or tap the mic' : 'Write a note'}
          onInput={(event) => change(event.currentTarget.value)}
          aria-label="Note"
        />
        {voiceUsable && <MicButton voice={voice} />}
      </div>
      {voiceError && <div class="dn-error" role="alert">{voiceError}</div>}
      {dn.sendError && (
        <div class="dn-error" role="alert">
          Not sent
          <button type="button" class="dn-link" onClick={dn.copyNotes}>Copy notes</button>
        </div>
      )}
      <div class="dn-actions">
        <button type="button" class="dn-btn dn-btn-ghost" disabled={!ready || dn.sending} onClick={() => dn.sendNow(text.trim())}>
          {dn.sending && <Loader2 size={14} class="spin" />}Send now
        </button>
        <button type="button" class="dn-btn dn-btn-primary" disabled={!ready || dn.sending} onClick={() => dn.add(text.trim())}>
          {dn.editingId ? 'Save' : 'Add'}
        </button>
      </div>
    </div>
  );
}

// The tray opened as a page of the reader (‹ back to the document).
export function NotesReview({ dn }) {
  const count = dn.drafts.length;
  return (
    <div class="dn-review">
      <div class="dn-review-head">
        <button type="button" class="dn-back" onClick={() => dn.setReviewing(false)}>
          <ArrowLeft size={16} aria-hidden="true" /> Document
        </button>
        <span class="dn-review-n">{count} {count === 1 ? 'note' : 'notes'}</span>
      </div>
      <ol class="dn-list">
        {dn.drafts.map((note) => (
          <li key={note.id} class="dn-item">
            <button type="button" class="dn-item-body" onClick={() => dn.edit(note.id)} aria-label="Edit note">
              {note.where && <span class="dn-quote-where">{note.where}</span>}
              <span class="dn-item-q">«{clip(note.quote)}»</span>
              <span class="dn-item-c">{note.comment}</span>
            </button>
            <button type="button" class="dn-x" aria-label="Delete note" onClick={() => dn.remove(note.id)}><Trash2 size={16} /></button>
          </li>
        ))}
      </ol>
      <div class="dn-dock has-notes">
        {dn.sendError ? (
          <span class="dn-error" role="alert">
            Not sent
            <button type="button" class="dn-link" onClick={dn.copyNotes}>Copy notes</button>
          </span>
        ) : <span class="dn-hint-t">Tap a note to edit it</span>}
        <button type="button" class="dn-btn dn-btn-primary" onClick={dn.sendAll} disabled={dn.sending || !count}>
          {dn.sending && <Loader2 size={15} class="spin" />}Send {count}
        </button>
      </div>
    </div>
  );
}
