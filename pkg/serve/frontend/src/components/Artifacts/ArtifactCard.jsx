import { FileCard } from '../FileCard/FileCard.jsx';
import { openArtifactFromCard } from '../../data/artifacts.js';
import { artifactFileId, seedFromFile } from '../../data/artifacts-model.js';
import { ArtifactRow } from './ArtifactRow.jsx';
import './Artifacts.css';

// ArtifactCard — what a successful send_file renders as in the conversation.
// The whole card opens the CURRENT artifact in the shared drawer, scoped to the
// conversation that delivered it. A descriptor that is not an artifact URL
// (an older transcript, another tool's file) keeps the plain download card.
export function ArtifactCard({ file, sessionId }) {
  const artifact = sessionId && artifactFileId(file?.url) ? seedFromFile(file) : null;
  if (!artifact) return <FileCard file={file} />;
  return (
    <article class="af-card" aria-label={`Artifact: ${artifact.title}`}>
      <ArtifactRow artifact={artifact} onOpen={() => openArtifactFromCard(sessionId, file)} />
    </article>
  );
}
