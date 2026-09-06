import { Layers } from 'lucide-preact';
import { IconButton } from '../../primitives/index.js';
import { useStore } from '../../hooks/useStore.js';
import { artifactsSlice, openArtifactsList } from '../../data/artifacts.js';
import './Artifacts.css';

// artifactsEntryState — what every entry shows for ITS conversation. Pure and
// exported so the ownership rule is testable without a renderer: an entry is
// "on" only while the shared drawer belongs to its own conversation, never
// because that conversation happens to be focused.
export function artifactsEntryState(state, sessionId) {
  const slice = artifactsSlice(state);
  return {
    visible: !!sessionId,
    active: !!sessionId && !!slice.view && slice.ownerSessionId === sessionId,
  };
}

function isArtifactsOwner(state, sessionId) {
  return artifactsEntryState(state, sessionId).active;
}

// ArtifactsEntry — the conversation head entry: a neighbour of the existing
// head actions, in the same 28px icon-button family.
export function ArtifactsEntry({ sessionId }) {
  const active = useStore((s) => isArtifactsOwner(s, sessionId));
  if (!sessionId) return null;
  return (
    <button
      type="button"
      class={`head-action-icon af-entry${active ? ' is-on' : ''}`}
      data-artifacts-trigger="true"
      onClick={() => openArtifactsList(sessionId)}
      aria-label="Artifacts in this conversation"
      aria-pressed={active || undefined}
      title="Artifacts"
    >
      <Layers size={14} aria-hidden="true" />
    </button>
  );
}

// ArtifactsPaneButton — the grid's discreet per-pane entry. It reuses the pane
// header's existing icon-action family so a narrow column is not saturated, and
// it is bound to that pane's conversation, not to the focused one.
export function ArtifactsPaneButton({ sessionId }) {
  const active = useStore((s) => isArtifactsOwner(s, sessionId));
  if (!sessionId) return null;
  return (
    <IconButton
      label="Artifacts"
      data-artifacts-trigger="true"
      aria-pressed={active || undefined}
      onClick={(event) => { event.stopPropagation(); openArtifactsList(sessionId); }}
    >
      <Layers size={15} aria-hidden="true" />
    </IconButton>
  );
}

// artifactsMobileAction — the mobile entry lives in the session actions menu,
// beside Live preview, rather than adding a permanent chip to the rail.
export function artifactsMobileAction(sessionId) {
  return {
    id: 'artifacts',
    icon: Layers,
    label: 'Artifacts',
    onClick: () => openArtifactsList(sessionId),
    visible: !!sessionId,
  };
}
