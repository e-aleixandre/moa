import { FileText } from 'lucide-preact';
import { openArtifactFromCard } from '../../data/artifacts.js';
import '../DocNotes/DocNotes.css';

// PROTOTYPE (lab): a batch of notes, painted the way a Live Preview reference
// is — each quote hangs off the message's peach spine, the reply under it. The
// head reopens the document.
export function DocNotesMessage({ parsed, sessionId }) {
  const { name, url, notes } = parsed;
  return (
    <div class="dn-msg">
      <button type="button" class="dn-msg-head" onClick={() => openArtifactFromCard(sessionId, { name, url })}>
        <FileText size={13} aria-hidden="true" />
        {notes.length} {notes.length === 1 ? 'note' : 'notes'} on {name}
      </button>
      {notes.map((note) => (
        <div class="dn-msg-note" key={note.n}>
          <div class="ref-spine" role="note">
            {note.quote
              ? <span class="ref-spine-q dn-msg-q">«{note.quote}»</span>
              : <span class="ref-tag">area</span>}
            {note.where && <span class="ref-spine-page">{note.where}</span>}
          </div>
          <div class="dn-msg-c">{note.comment}</div>
        </div>
      ))}
    </div>
  );
}
