import { useEffect, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { DiffBlock } from "../DiffBlock/DiffBlock.jsx";
import { patchRowBudget } from "./rows.js";
import "./PatchFiles.css";

function Chevron({ open }) {
  return (
    <svg class={`zl-lg-chev${open ? " is-open" : ""}`} viewBox="0 0 12 12" aria-hidden="true">
      <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

// PatchFiles — the per-file list of an apply_patch call, from the diffs the tool
// recorded for what it wrote. Always visible (the closed card already says what
// changed and by how much); a file's diff opens with its own line, or all at
// once with the row that owns the list. Diffs are parsed and mounted only for
// open files, and only up to one cap for the whole card.
//
// A persisted child's transcript lists the files without their diffs (has_diff);
// `detailUrl` is where they are fetched, once, the first time anything opens.
export function PatchFiles({ files: listed, rowOpen, detailUrl, more }) {
  const [mine, setMine] = useState({});
  const [all, setAll] = useState(false);
  const [fetched, setFetched] = useState(null);
  const [failed, setFailed] = useState(false);
  const files = fetched ? listed.map((f, i) => ({ ...f, diff: fetched[i]?.diff || f.diff, truncated: fetched[i]?.truncated || f.truncated })) : listed;
  const isOpen = (i) => (mine[i] ?? rowOpen) === true;
  const wantsDiffs = Boolean(detailUrl) && !fetched && !failed && listed.some((f, i) => f.has_diff && !f.diff && isOpen(i));
  useEffect(() => {
    if (!wantsDiffs) return undefined;
    let active = true;
    api("GET", detailUrl).then((detail) => {
      if (active) setFetched(Array.isArray(detail?.patch_files) ? detail.patch_files : []);
    }).catch(() => { if (active) setFailed(true); });
    return () => { active = false; };
  }, [wantsDiffs, detailUrl]);
  const { shown, hidden } = patchRowBudget(files, isOpen, all ? Infinity : undefined);
  return (
    <div class="zl-pf">
      {files.map((file, i) => {
        const hasBody = Boolean(file.diff || file.has_diff);
        const open = hasBody && isOpen(i);
        const name = file.move_path ? `${file.path} → ${file.move_path}` : file.path;
        const Tag = hasBody ? "button" : "div";
        return (
          <div class="zl-pf-item" key={`${i}:${file.path}`}>
            <Tag
              type={hasBody ? "button" : undefined}
              class={`zl-pf-file${open ? " is-open" : ""}`}
              onClick={hasBody ? () => setMine({ ...mine, [i]: !open }) : undefined}
              aria-expanded={hasBody ? open : undefined}
              title={name}
            >
              <span class={`zl-pf-letter is-${file.op}`}>{file.op}</span>
              <span class="zl-pf-path zl-data">{name}</span>
              {file.op === "D" && <span class="zl-pf-note zl-data">deleted</span>}
              {file.truncated && !file.diff && <span class="zl-pf-note zl-data">too large to show</span>}
              {file.add > 0 && <span class="zl-pf-add zl-data">+{file.add}</span>}
              {file.del > 0 && <span class="zl-pf-del zl-data">−{file.del}</span>}
              {hasBody && <Chevron open={open} />}
            </Tag>
            {open && (
              <div class="zl-pf-body">
                {shown[i]
                  ? <DiffBlock className="flush" lines={shown[i]} filename={name} />
                  : <div class="zl-pf-note zl-pf-cut">{failed ? "Could not load the diff" : "Loading…"}</div>}
                {file.truncated && <div class="zl-pf-note zl-pf-cut">Diff cut short</div>}
              </div>
            )}
          </div>
        );
      })}
      {more?.files > 0 && <div class="zl-pf-note zl-pf-cut">+{more.files} more files</div>}
      {hidden > 0 && (
        <button type="button" class="zl-pf-more" onClick={() => setAll(true)}>
          Show all {hidden} more rows
        </button>
      )}
    </div>
  );
}
