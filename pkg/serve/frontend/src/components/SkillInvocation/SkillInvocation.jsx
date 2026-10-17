import { useState } from "preact/hooks";
import { Rewind as RewindIcon } from "lucide-preact";
import { ActivityLedger } from "../ActivityLedger/ActivityLedger.jsx";
import { RewindConfirm } from "../UserWaypoint/UserWaypoint.jsx";
import "./SkillInvocation.css";

// SkillInvocation — a skill the user invoked with /<name>: one compact ledger
// row that opens onto the SKILL.md, standing alone as the start of a turn. It
// is what the user did, so it keeps the rewind every message of theirs has:
// back to just before it, with the command they typed returned to the composer.
export function SkillInvocation({ rows, command, onRewind, rewindDisabled, onOpenTimeline }) {
  const [confirm, setConfirm] = useState(false);
  return (
    <>
      <div class="zl-skill">
        <ActivityLedger rows={rows} className="zl-skill-ledger" />
        {onRewind && (
          <button
            type="button"
            class="zl-skill-rewind"
            disabled={rewindDisabled}
            onClick={() => setConfirm(true)}
            aria-label="Rewind the conversation to this skill"
            title="Rewind here"
          >
            <RewindIcon size={12} aria-hidden="true" />
          </button>
        )}
      </div>
      {onRewind && (
        <RewindConfirm
          open={confirm}
          preview={command}
          edit
          midTurn={false}
          onConfirm={() => {
            setConfirm(false);
            onRewind();
          }}
          onOpenTimeline={onOpenTimeline}
          onClose={() => setConfirm(false)}
        />
      )}
    </>
  );
}
