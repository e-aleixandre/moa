import { useEffect, useRef, useState } from 'preact/hooks';
import { Check, FileCode2, FileImage, FileText, File as FileIcon, Info, Share2 } from 'lucide-preact';
import { HtmlResourceInfo } from '../HtmlResourceInfo/HtmlResourceInfo.jsx';
import { downloadFile } from '../../data/util/file-download.js';
import { iconKindFor, isHTMLPreviewable } from '../../data/util/file-card.js';
import './Artifacts.css';

const ICONS = { image: FileImage, text: FileText, file: FileIcon, archive: FileIcon };

// KindIcon — the type is the icon and nothing else: no badge, no format label.
export function KindIcon({ artifact, size = 15 }) {
  if (isHTMLPreviewable(artifact?.name, artifact?.mime)) return <FileCode2 size={size} aria-hidden="true" />;
  const Icon = ICONS[iconKindFor(artifact?.mime)] || FileIcon;
  return <Icon size={size} aria-hidden="true" />;
}

// ShareButton — reuses the existing download helper (native share sheet on
// mobile, blob URL on desktop), so an artifact shares exactly like a file card.
export function ShareButton({ artifact, labelled = false }) {
  const [status, setStatus] = useState('idle');
  const timer = useRef(null);
  useEffect(() => () => clearTimeout(timer.current), []);

  const share = async (event) => {
    event.stopPropagation();
    if (status === 'busy') return;
    setStatus('busy');
    try {
      await downloadFile({ name: artifact.name, mime: artifact.mime, url: artifact.url });
      setStatus('done');
    } catch (error) {
      setStatus(error?.name === 'AbortError' ? 'idle' : 'error');
    }
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setStatus('idle'), 2200);
  };

  const label = status === 'error' ? 'Retry' : status === 'busy' ? 'Preparing…' : status === 'done' ? 'Done' : 'Share';
  return (
    <button
      type="button"
      class={`af-icon-button af-share${labelled ? ' is-labelled' : ''}`}
      onClick={share}
      disabled={status === 'busy'}
      title="Download or share"
      aria-label={`Download or share ${artifact.name}`}
    >
      {status === 'done' ? <Check size={16} /> : <Share2 size={16} />}
      {labelled && <span>{label}</span>}
    </button>
  );
}

// ResourceInfoButton — keeps the existing HTML domain inspection reachable from
// an artifact. Informational only; it adds no consent step.
function ResourceInfoButton({ artifact }) {
  const [open, setOpen] = useState(false);
  if (!isHTMLPreviewable(artifact.name, artifact.mime)) return null;
  return (
    <>
      <button
        type="button"
        class="af-icon-button"
        onClick={(event) => { event.stopPropagation(); setOpen(true); }}
        title="Inspect external resources"
        aria-label={`Inspect external resources in ${artifact.name}`}
      >
        <Info size={16} />
      </button>
      {open && <HtmlResourceInfo name={artifact.name} url={artifact.url} onClose={() => setOpen(false)} />}
    </>
  );
}

// ArtifactRow — ONE row shape shared by the conversation card and the list:
// the file line in mono (the ledger's voice), the title, and the optional
// description. The whole row opens the artifact; sharing is a separate target.
export function ArtifactRow({ artifact, onOpen, trailing }) {
  return (
    <div class="af-row">
      <button
        type="button"
        class="af-row-open"
        data-artifacts-trigger="true"
        onClick={() => onOpen(artifact)}
        aria-label={`Open ${artifact.title}`}
      >
        <span class="af-row-file">
          <KindIcon artifact={artifact} />
          <span>{artifact.name}</span>
          {trailing && <span class="af-row-trailing">{trailing}</span>}
        </span>
        <span class="af-row-title">{artifact.title}</span>
        {artifact.description && <span class="af-row-sub">{artifact.description}</span>}
        {!artifact.available && <span class="af-row-flag">Source unavailable</span>}
      </button>
      <ResourceInfoButton artifact={artifact} />
      <ShareButton artifact={artifact} />
    </div>
  );
}
