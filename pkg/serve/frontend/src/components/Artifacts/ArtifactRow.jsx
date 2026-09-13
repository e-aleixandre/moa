import { useEffect, useRef, useState } from 'preact/hooks';
import { Check, FileCode2, FileImage, FileText, File as FileIcon, Info, Share2 } from 'lucide-preact';
import { HtmlResourceInfo } from '../HtmlResourceInfo/HtmlResourceInfo.jsx';
import { downloadFile } from '../../data/util/file-download.js';
import { iconKindFor, isHTMLPreviewable, previewKind } from '../../data/util/file-card.js';
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

// Thumb — what the row shows of the file. A screenshot is most of what this
// list holds, and a generic glyph says nothing about which one this is: the
// image itself, at 56px, is the only mark that tells two captures apart.
// Every other kind keeps the type glyph on a tinted plate of the same size, so
// the column of marks lines up whatever the mix. The image loads lazily: 83
// rows must not fire 83 requests on open. A failed load (a 410 after the
// source moved) drops back to the glyph rather than a broken image.
function Thumb({ artifact }) {
  const [broken, setBroken] = useState(false);
  const image = artifact.available && !broken && previewKind(artifact.name, artifact.mime) === 'image';
  return (
    <span class={`af-thumb${image ? ' is-image' : ''}`} aria-hidden="true">
      {image
        ? <img src={artifact.url} alt="" loading="lazy" decoding="async" draggable={false} onError={() => setBroken(true)} />
        : <KindIcon artifact={artifact} size={18} />}
    </span>
  );
}

// ArtifactRow — ONE row shape shared by the conversation card and the list.
// The title leads: it is the name the agent gave the deliverable for you, so
// it is what you scan for. The file name is data, in the ledger's mono voice,
// one step down. The whole row opens the artifact; sharing is a separate
// target that stays out of the way until the row is pointed at.
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
        <Thumb artifact={artifact} />
        <span class="af-row-main">
          <span class="af-row-title">{artifact.title}</span>
          <span class="af-row-file">
            <span>{artifact.name}</span>
            {trailing && <span class="af-row-trailing">{trailing}</span>}
          </span>
          {artifact.description && <span class="af-row-sub" title={artifact.description}>{artifact.description}</span>}
          {!artifact.available && <span class="af-row-flag">Source unavailable</span>}
        </span>
      </button>
      <span class="af-row-acts">
        <ResourceInfoButton artifact={artifact} />
        <ShareButton artifact={artifact} />
      </span>
    </div>
  );
}
