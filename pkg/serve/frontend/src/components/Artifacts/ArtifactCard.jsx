import { FileCard } from '../FileCard/FileCard.jsx';
import { openArtifactFromCard } from '../../data/artifacts.js';
import { artifactFileId, seedFromFile } from '../../data/artifacts-model.js';
import { humanSize } from '../../data/util/file-card.js';
import { Artifact, artifactKind } from './Artifact.jsx';

// ArtifactCard — what a successful send_file renders as in the conversation.
// Markup is the catalogue's Artifact (a raised file card). A descriptor that
// is not an artifact URL (an older transcript, another tool's file) keeps
// the plain download card.
export function ArtifactCard({ file, sessionId }) {
  const artifact = sessionId && artifactFileId(file?.url) ? seedFromFile(file) : null;
  if (!artifact) return <FileCard file={file} />;
  return (
    <Artifact
      name={artifact.name}
      kind={artifactKind(file)}
      size={humanSize(artifact.size)}
      onOpen={() => openArtifactFromCard(sessionId, file)}
    />
  );
}
