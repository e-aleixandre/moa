import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { SessionRow } from "../SessionRow/SessionRow.jsx";
import { Segmented } from "../Segmented/Segmented.jsx";
import { Field } from "../../primitives/Field/Field.jsx";
import { Button } from "../../primitives/Button/Button.jsx";
import { deriveModelSpecs } from "../../data/selectors.js";
import { defaultModelSpec } from "../CommandPalette/command-palette-model.js";
import { modelCodename, shortPath } from "../../data/util/format.js";
import { api } from "../../data/api.js";
import { bookTree, childrenSummary, groupChildren, ownerRowState, waitingChildren } from "../../data/owners-model.js";
// The chassis of the dossier: the owner's panel IS the session dossier's
// drawer with different contents, so it takes that sheet rather than a copy.
import "../SessionPanel/SessionPanel.css";
import "./Owners.css";

/* Owners — the project owner as a surface: the sidebar's third list, the New
   owner page, the owner's dossier (Overview / Book) and the chip a child
   wears. Markup and CSS are the catalogue's (catalog/owners-lab.jsx and the
   `ow-*` block it drew), MOVED here rather than imitated: the class names
   travelled with the rules, so these pieces ARE the accepted design instead of
   a translation of it. The catalogue imports them now, which is what makes one
   definition rather than two (tmp/redesign/fidelity/METODO.md).

   The data is the app's: owners from GET /api/owners (data/owners.js), their
   children selected out of the roster the store already holds, and the book
   over HTTP. Nothing here fetches a list on its own.

   They speak the vocabulary that already exists rather than inventing one:
   a child is the product's own SessionRow, a group heading is the inbox's
   `Group` (uppercase label, yellow count pill only when it is something that
   stopped), and the dots are the session dots. An owner surface that invented
   its own row would make the same list mean two things.

   WHAT THE OWNER'S DECISION CHANGED: the list is no longer a surface with a
   head and a way back. Owners is a MODE of the sidebar, a peer of Recent and
   By project, so the column's head and foot belong to the sidebar in every
   mode and this file contributes only the BODY of the list. The one page that
   still carries a head is New owner, because that one IS pushed on top of the
   list and has to say how to leave — the same shape New session has. */

const HUES = [210, 265, 170, 320, 40, 190];
function hueOf(name) {
  const s = String(name || "");
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}
function FolderIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M1.9 5.1V3.9a.9.9 0 0 1 .9-.9h2.6l1.2 1.4h6.5a.9.9 0 0 1 .9.9v.3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
      <rect x="1.9" y="5.6" width="12.2" height="7.4" rx="1" fill="none" stroke="currentColor" stroke-width="1.4" />
    </svg>
  );
}
function FileIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M3.6 2.5h5l3 3v8h-8z M8.6 2.5v3h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
    </svg>
  );
}
function GoIcon() {
  return (
    <svg class="ow-go" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

// Head — the inbox's head, one level of hierarchy and a back that says where
// it goes. Same component shape so the two doors of the sidebar (Inbox,
// Owners) cannot drift apart.
function Head({ title, onBack, backLabel, right }) {
  return (
    <div class="ow-head">
      {onBack && (
        <button type="button" class="ow-back" onClick={onBack} aria-label={backLabel}>
          <BackIcon />
        </button>
      )}
      <span class="ow-title">{title}</span>
      {right}
    </div>
  );
}

function Group({ label, n, attn }) {
  return (
    <div class={`ow-group${attn ? " is-attn" : ""}`}>
      <span>{label}</span>
      {attn ? <span class="ow-pill ow-data">{n > 9 ? "9+" : n}</span> : <span class="ow-group-n ow-data">{n}</span>}
    </div>
  );
}

// Monogram — an owner is a named thing, and the list is scanned by NAME (one
// owner per project, so the project never disambiguates it). Two letters of
// the name on a hued tile, the same mark the inbox gives its sources.
function OwnerMark({ name }) {
  return (
    <span class="ow-mono" style={`--h:${hueOf(name)}`} aria-hidden="true">{String(name).slice(0, 2)}</span>
  );
}

/* ── The list ─────────────────────────────────────────────────────────── */

// The owner row. `active` is the same treatment a current session row gets
// (SessionRow.css `.zl-row.is-current`): a raised plane and a heavier title,
// never a left bar — that gesture means "you said this" (CRITERIO §1).
function OwnerListRow({ owner, onOpen, active = false, triage = false, onOpenChild }) {
  const summary = childrenSummary(owner.children || []);
  const state = ownerRowState(owner);
  const path = shortPath(owner.root, 40);
  const label = `${owner.name}, in ${path}. ${summary.text}.`;
  const waiting = triage ? waitingChildren(owner) : [];
  return (
    <div class={`ow-row-slot${active ? " is-current" : ""}`}>
      <button
        type="button"
        class={`ow-row${active ? " is-current" : ""}`}
        onClick={() => onOpen?.(owner)}
        aria-current={active ? "true" : undefined}
        aria-label={label}
      >
        <OwnerMark name={owner.name} />
        <span class="ow-row-main">
          <span class="ow-row-l1">
            <span class="ow-row-name">{owner.name}</span>
            <span class="ow-row-meta">
              <span class={`ow-dot is-${state}`} aria-hidden="true" />
            </span>
          </span>
          <span class="ow-row-path ow-data">{path}</span>
          <span class={`ow-row-brief tone-${summary.tone}`}>{summary.text}</span>
        </span>
      </button>
      {/* VARIANT (triage): the children that have STOPPED, at most three,
          indented under their owner. Only what is waiting — a running child
          here would be the session list printed twice. */}
      {waiting.length > 0 && (
        <div class="ow-row-kids">
          {waiting.map((child) => (
            <ChildRow child={child} onOpen={onOpenChild} key={child.id} />
          ))}
        </div>
      )}
    </div>
  );
}

function OwnersLoading() {
  return (
    <div class="ow-list" aria-busy="true">
      <span class="ow-sr-only">Loading owners</span>
      {[0, 1].map((i) => (
        <div class="ow-ghost" aria-hidden="true" key={i}>
          <span class="ow-ghost-mono" />
          <span class="ow-ghost-main">
            <span class="ow-ghost-bar" style="width:42%" />
            <span class="ow-ghost-bar is-t" style="width:64%" />
            <span class="ow-ghost-bar" style="width:34%" />
          </span>
        </div>
      ))}
    </div>
  );
}

function OwnersError({ detail, retrying, onRetry }) {
  return (
    <div class="ow-state is-error" role="alert">
      <span class="ow-state-t"><span class="ow-dot is-error" aria-hidden="true" />Can't read the owners</span>
      {detail && <span class="ow-state-d ow-data">{detail}</span>}
      <span class="ow-state-p">The owners and their books are on disk; nothing was lost. Try again, or check that moa is up.</span>
      {onRetry && (
        <button type="button" class="ow-btn" onClick={onRetry} disabled={retrying}>
          {retrying ? "Retrying…" : "Retry"}
        </button>
      )}
    </div>
  );
}

// The empty state says what an owner IS in one sentence and offers the one
// action. It is the only screen where the accent is spent on "New owner":
// with no list to look at, creating one IS the dominant action (CRITERIO §2).
function OwnersEmpty({ onNew }) {
  return (
    <div class="ow-state is-empty">
      <span class="ow-state-t">No project has an owner yet.</span>
      <span class="ow-state-p">
        An owner is one standing agent per project: it keeps the project's book,
        starts the sessions that work on it and reads what they report back.
      </span>
      <Button variant="accent" size="lg" className="ow-cta" onClick={onNew}>New owner</Button>
    </div>
  );
}

/* ── New owner ────────────────────────────────────────────────────────── */

// The folder explorer is the one the palette's create step already uses:
// /api/fs/complete with a trailing slash means "list this directory",
// debounced, and a stale response cannot clobber a newer directory
// (CommandPalette.jsx:340-358). It is duplicated here rather than shared
// because the palette's copy is welded into its own step machine; the note in
// CRITERIO-VISUAL §4 about NewSessionView and the palette already names this
// as a debt, and a third implementation would be a third place to fix.
function useDirEntries(dir) {
  const [entries, setEntries] = useState([]);
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    if (!dir) return undefined;
    let cancelled = false;
    setLoading(true);
    const timer = setTimeout(() => {
      api("GET", `/api/fs/complete?path=${encodeURIComponent(dir + "/")}`)
        .then((data) => {
          if (cancelled) return;
          setEntries(Array.isArray(data?.entries) ? data.entries : []);
          setLoading(false);
        })
        .catch(() => { if (!cancelled) { setEntries([]); setLoading(false); } });
    }, 130);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [dir]);
  return { entries, loading };
}

function useOwnerModels() {
  const [models, setModels] = useState([]);
  const [fallback, setFallback] = useState("");
  useEffect(() => {
    let live = true;
    Promise.all([
      api("GET", "/api/capabilities").catch(() => ({})),
      api("GET", "/api/models").catch(() => []),
    ]).then(([caps, list]) => {
      if (!live) return;
      const specs = deriveModelSpecs(list);
      setModels(specs);
      setFallback(defaultModelSpec(caps, specs));
    });
    return () => { live = false; };
  }, []);
  return { models, fallback };
}

const THINKING = [
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
];

// createFailure turns the server's refusal into the sentence that says WHAT TO
// DO. The two it answers are the two the API actually returns (409 for an
// existing owner or open sessions, 400 for a bad folder or model); anything
// else keeps the server's own words rather than inventing a diagnosis.
export function createFailure(error) {
  const status = error?.status;
  const text = String(error?.message || error || "");
  if (status === 409 && /open sessions/i.test(text)) {
    return {
      title: "This project still has sessions open.",
      detail: "Close them or let them finish, then create the owner: a session resolves its owner when it is built.",
    };
  }
  if (status === 409) {
    return {
      title: "This project already has an owner.",
      detail: "One owner per codebase, and every worktree of a repository shares it. Open it from the list instead.",
    };
  }
  if (status === 400) {
    return { title: "moa could not use that.", detail: text.replace(/^400:\s*/, "").trim() || "Check the folder exists and the model is one of the ones listed." };
  }
  return { title: "The owner was not created.", detail: text.replace(/^\d{3}:\s*/, "").trim() || "Try again, or check that moa is up." };
}

const basename = (p) => String(p || "").replace(/\/+$/, "").split("/").filter(Boolean).pop() || "";

export function NewOwner({ defaultDir = "", onCreate, phone = false }) {
  const [dir, setDir] = useState(defaultDir);
  // What the server said, in the form, beside the button that caused it. A
  // create fails for two reasons the user can act on — the codebase already
  // has an owner, or its sessions are still open — and both are sentences
  // about THIS form, not global news for a toast.
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState(null);
  const [filter, setFilter] = useState("");
  const [name, setName] = useState("");
  const [touchedName, setTouchedName] = useState(false);
  const { entries, loading } = useDirEntries(dir);
  const { models, fallback } = useOwnerModels();
  // The backend's own defaults for an owner, and the reason is worth showing:
  // it judges rather than codes, so it runs the strongest model at the
  // cheapest thinking (pkg/serve/owners.go:15-21).
  const [model, setModel] = useState("");
  const [thinking, setThinking] = useState("low");
  const chosenModel = model || models.find((m) => m.alias === "opus")?.id || fallback;

  // The name follows the folder until you type one. A project's owner is
  // almost always called after the project, and making that the default is
  // what turns four fields into one decision.
  const suggested = basename(dir);
  const effectiveName = touchedName ? name : suggested;

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const list = q ? entries.filter((e) => e.toLowerCase().startsWith(q)) : entries;
    return list.slice(0, 40);
  }, [entries, filter]);

  const enter = (entry) => {
    setDir(`${dir.replace(/\/+$/, "")}/${entry}`);
    setFilter("");
  };
  const up = () => {
    const parent = dir.replace(/\/+$/, "").split("/").slice(0, -1).join("/");
    setDir(parent || "/");
    setFilter("");
  };

  return (
    <div class="ow-form">
      <label class="ow-field">
        <span class="ow-label">Project folder</span>
        <Field
          variant="box"
          size="lg"
          mono
          leading={<FolderIcon />}
          value={dir}
          onInput={(e) => { setDir(e.currentTarget.value); setFilter(""); }}
          aria-label="Project folder"
          spellcheck={false}
        />
      </label>

      <div class="ow-browse">
        <div class="ow-browse-head">
          <button type="button" class="ow-up" onClick={up} aria-label="Up one folder">..</button>
          <Field
            variant="inset"
            size="md"
            className="ow-browse-filter"
            placeholder="Filter subfolders"
            value={filter}
            onInput={(e) => setFilter(e.currentTarget.value)}
            aria-label="Filter subfolders"
          />
        </div>
        <div class="ow-browse-list" role="listbox" aria-label="Subfolders">
          {loading && <p class="ow-quiet">Reading…</p>}
          {!loading && shown.length === 0 && <p class="ow-quiet">No subfolders — the owner is created here.</p>}
          {!loading && shown.map((entry) => (
            <button type="button" class="ow-entry" key={entry} onClick={() => enter(entry)}>
              <FolderIcon />
              <span class="ow-entry-t ow-data">{entry}</span>
            </button>
          ))}
        </div>
      </div>

      <label class="ow-field">
        <span class="ow-label">Name</span>
        <Field
          variant="box"
          size="lg"
          value={effectiveName}
          placeholder={suggested || "Owner name"}
          onInput={(e) => { setTouchedName(true); setName(e.currentTarget.value); }}
          aria-label="Owner name"
        />
        <span class="ow-hint">What you will call it in the list. Its conversation keeps this name.</span>
      </label>

      <div class="ow-field">
        <span class="ow-label">Model</span>
        <div class="ow-picks" role="radiogroup" aria-label="Model">
          {models.map((m) => {
            const on = m.id === chosenModel;
            return (
              <button
                type="button"
                role="radio"
                aria-checked={on}
                class={`ow-pick${on ? " is-on" : ""}`}
                key={m.id}
                onClick={() => setModel(m.id)}
              >
                <span class="ow-mono is-sm" style={`--h:${hueOf(m.codename)}`} aria-hidden="true">{String(m.codename).slice(0, 2)}</span>
                <span class="ow-pick-t">{m.codename || modelCodename(m.id)}</span>
                {m.sub && <span class="ow-pick-s ow-data">{m.sub}</span>}
              </button>
            );
          })}
        </div>
      </div>

      <div class="ow-field">
        <span class="ow-label">Thinking</span>
        <Segmented options={THINKING} value={thinking} onChange={setThinking} className="ow-seg" />
        <span class="ow-hint">
          An owner reads reports and keeps a book rather than writing code, so it
          defaults to the strongest model at the cheapest thinking.
        </span>
      </div>

      <div class="ow-form-foot">
        {failure && (
          <p class="ow-fail" role="alert">
            <span class="ow-fail-t">{failure.title}</span>
            <span class="ow-fail-d">{failure.detail}</span>
          </p>
        )}
        <Button
          variant="accent"
          size="lg"
          className="ow-cta"
          disabled={busy || !dir || !effectiveName}
          onClick={async () => {
            setBusy(true);
            setFailure(null);
            try {
              await onCreate?.({ root: dir, name: effectiveName, model: chosenModel, thinking });
            } catch (error) {
              setFailure(createFailure(error));
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? "Creating…" : "Create owner"}
        </Button>
        <p class="ow-hint">
          Creates the book and its conversation. The project's open sessions must
          be closed first — whether a session has an owner is resolved when it is
          built.
        </p>
      </div>
      {phone && <div class="ow-form-pad" aria-hidden="true" />}
    </div>
  );
}

/* ── OwnersView ───────────────────────────────────────────────────────── */

// The BODY of the sidebar's Owners mode. It is not a surface any more: the
// column's head (wordmark, search, +, the three-way mode control) and foot
// (inbox, version, settings) belong to the Sidebar and are identical in all
// three modes, so nothing here draws a head, a title or a way back. You leave
// Owners the way you entered it — by choosing another mode.
//
// New owner is the exception: a page pushed INSIDE the column, exactly as New
// session is, so it keeps a head with a back that returns to the list.
export function OwnersView({
  owners = [],
  health,
  onRetry,
  onOpen,
  onOpenChild,
  onCreate,
  activeId = null,
  triage = false,
  variant = "column",
  defaultPage = "list",
  defaultDir = "",
}) {
  const [page, setPage] = useState(defaultPage);
  const status = health?.status || "ready";
  const phone = variant === "sheet";
  const creating = page === "new";

  if (creating) {
    return (
      <div class={`ow-owners${phone ? " is-phone" : ""}`}>
        <Head title="New owner" onBack={() => setPage("list")} backLabel="Back to owners" />
        <div class="ow-body is-sub">
          <NewOwner
            defaultDir={defaultDir}
            phone={phone}
            /* The page leaves only once the owner exists: a form that closes
               on a failed request loses both the failure and everything that
               was typed. */
            onCreate={async (spec) => { await onCreate?.(spec); setPage("list"); }}
          />
        </div>
      </div>
    );
  }

  let body;
  if (status === "loading") {
    body = <OwnersLoading />;
  } else if (status === "error") {
    body = <OwnersError detail={health?.error} retrying={health?.retrying} onRetry={onRetry} />;
  } else if (owners.length === 0) {
    body = <OwnersEmpty onNew={() => setPage("new")} />;
  } else {
    body = (
      <div class="ow-list">
        {owners.map((owner) => (
          <OwnerListRow
            owner={owner}
            onOpen={onOpen}
            onOpenChild={onOpenChild}
            active={owner.id === activeId}
            triage={triage}
            key={owner.id}
          />
        ))}
        {/* A quiet row at the end of the list, not an accent bar across the
            foot: with owners on screen the LIST is what the mode is for, and
            one owner per project means this is pressed once a project
            (CRITERIO §2). The foot below belongs to the app, not to this. */}
        <button type="button" class="ow-new" onClick={() => setPage("new")}>
          <PlusIcon />
          New owner
        </button>
      </div>
    );
  }

  return (
    <div class={`ow-owners is-mode${phone ? " is-phone" : ""}`}>
      <div class="ow-body">{body}</div>
    </div>
  );
}

/* ── The owner's dossier: Overview and Book ───────────────────────────── */

function ChildRow({ child, onOpen }) {
  return (
    <SessionRow
      title={child.title}
      state={child.state}
      unseen={child.unseen}
      when={child.when}
      brief={child.brief || undefined}
      briefTone={child.state === "permission" ? "yellow" : child.state === "error" ? "red" : child.unseen ? "mauve" : "neutral"}
      onClick={() => onOpen?.(child)}
    />
  );
}

export function OwnerOverview({ owner, onOpenChild }) {
  const groups = groupChildren(owner.children || []);
  const summary = childrenSummary(owner.children || []);
  return (
    <div class="ow-page">
      <div class="ow-sum">
        <span class={`ow-sum-t tone-${summary.tone}`}>{summary.text}</span>
        <span class="ow-sum-d">Every session whose folder resolves to this project is one of these — nothing is linked by hand.</span>
      </div>
      {groups.length === 0 && <p class="ow-quiet">No session has run in this project yet.</p>}
      {groups.map((group) => (
        <div class="ow-cgroup" key={group.key}>
          <Group label={group.label} n={group.children.length} attn={group.attn} />
          {group.children.map((child) => <ChildRow child={child} onOpen={onOpenChild} key={child.id} />)}
        </div>
      ))}
    </div>
  );
}

function fmtBytes(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  return `${(v / 1024).toFixed(1)} kB`;
}

export function OwnerBook({ book = [], openPath = null, onOpenFile, onSave, status = "ready", error = null, fileStatus = "ready" }) {
  const tree = bookTree(book);
  const file = openPath ? book.find((f) => f.path === openPath) : null;
  if (file) {
    if (fileStatus === "loading") return <p class="ow-quiet">Reading {file.label}…</p>;
    return <BookFile file={file} onSave={onSave} />;
  }
  if (status === "loading") return <p class="ow-quiet">Reading the book…</p>;
  if (status === "error") {
    return (
      <div class="ow-state is-error" role="alert">
        <span class="ow-state-t"><span class="ow-dot is-error" aria-hidden="true" />Can't read the book</span>
        {error && <span class="ow-state-d ow-data">{error}</span>}
        <span class="ow-state-p">The book is on disk under the project's config directory; nothing was lost.</span>
      </div>
    );
  }
  if (book.length === 0) {
    return (
      <div class="ow-page">
        <p class="ow-page-intro">
          This book is empty. The owner writes it as it learns the project —
          what was decided and why, who asks for what, the detail behind each
          area.
        </p>
      </div>
    );
  }
  return (
    <div class="ow-page">
      <p class="ow-page-intro">
        The owner's memory, on disk under the project's config directory. Its
        children never read it — they are given the index, which is why keeping
        that one file honest is the owner's job.
      </p>
      {tree.index.map((f) => (
        <button type="button" class="ow-file is-index" key={f.path} onClick={() => onOpenFile?.(f.path)}>
          <FileIcon />
          <span class="ow-file-main">
            <span class="ow-file-t">{f.label}</span>
            <span class="ow-file-d">The index every session of this project is given</span>
          </span>
          <GoIcon />
        </button>
      ))}
      {tree.sections.map((section) => (
        <div class="ow-fgroup" key={section.dir || "root"}>
          {section.label && <Group label={section.label} n={section.files.length} />}
          {section.files.map((f) => (
            <button type="button" class="ow-file" key={f.path} onClick={() => onOpenFile?.(f.path)}>
              <FileIcon />
              <span class="ow-file-main">
                <span class="ow-file-t">{f.label}</span>
                <span class="ow-file-d ow-data">{fmtBytes(f.bytes)}</span>
              </span>
              <GoIcon />
            </button>
          ))}
        </div>
      ))}
    </div>
  );
}

// PROJECT.md is editable and nothing else is, in v1. It is the only file that
// reaches a child's prompt (pkg/owner/owner.go:44-50), so it is the one whose
// wording you may need to fix without asking the owner to do it; the rest is
// the owner's own record, and a half-edited decision file is worse than none.
function BookFile({ file, onSave }) {
  const editable = file.path === "PROJECT.md";
  const [draft, setDraft] = useState(file.body);
  const dirty = editable && draft !== file.body;
  const ref = useRef(null);
  useEffect(() => { setDraft(file.body); }, [file.path, file.body]);
  if (!editable) {
    return (
      <div class="ow-page is-file">
        <div class="ow-file-head">
          <span class="ow-file-path ow-data">{file.path}</span>
          <span class="ow-file-ro">Read-only</span>
        </div>
        <pre class="ow-doc">{file.body}</pre>
      </div>
    );
  }
  return (
    <div class="ow-page is-file">
      <div class="ow-file-head">
        <span class="ow-file-path ow-data">{file.path}</span>
        <span class="ow-file-ro is-edit">Editable</span>
      </div>
      <textarea
        class="ow-edit"
        ref={ref}
        value={draft}
        spellcheck={false}
        aria-label="PROJECT.md"
        onInput={(e) => setDraft(e.currentTarget.value)}
      />
      <div class="ow-edit-foot">
        <button type="button" class="ow-btn is-quiet" disabled={!dirty} onClick={() => setDraft(file.body)}>Revert</button>
        <Button variant="solid" size="lg" disabled={!dirty} onClick={() => onSave?.(file.path, draft)}>Save</Button>
      </div>
    </div>
  );
}

const PANEL_TABS = [
  { id: "overview", label: "Overview" },
  { id: "book", label: "Book" },
];

// OwnerPanel — RECOMMENDED placement: the owner's dossier is the same third
// zone the session dossier already occupies (DesktopDossier on the desktop, a
// MobileSheet on the phone), with two tabs instead of the session's facts.
//
// Why tabs and not two pages of the session dossier: "what this thing is and
// what it has done" for an owner IS its children and its book — there is no
// third thing to list, so a root page of two rows that each push would be a
// menu with two items. Tabs keep both one tap away while the transcript is
// what you are actually reading.
export function OwnerPanel({
  owner,
  book = [],
  bookStatus = "ready",
  bookError = null,
  fileStatus = "ready",
  tab = "overview",
  onTab,
  openPath = null,
  onOpenFile,
  onSave,
  onOpenChild,
  onClose,
  open = true,
  variant = "",
}) {
  const sheet = variant === "sheet";
  const file = openPath ? book.find((f) => f.path === openPath) : null;
  return (
    <aside
      /* The chassis is the session dossier's own (`zl-side-right`): same
         width, same slide, same host geometry in DesktopShell's third zone.
         An owner's dossier IS that zone with different contents, so it must
         not be a second drawer with its own arithmetic. */
      class={`zl-side-right ow-panel${sheet ? " is-sheet" : ""}${open ? " is-open" : ""}`}
      aria-label={`${owner.name}, the owner of this project`}
      aria-hidden={!open}
      /* Closed it is slid off-screen, not gone — the same rule the session
         dossier keeps: inert removes it from the tab order without touching
         what it paints. */
      inert={!open}
    >
      <div class="ow-panel-head">
        {file ? (
          <>
            <button type="button" class="ow-back" onClick={() => onOpenFile?.(null)} aria-label="Back to the book">
              <BackIcon />
            </button>
            <span class="ow-panel-title">{file.label}</span>
          </>
        ) : (
          <span class="ow-panel-eyebrow">This owner</span>
        )}
        {onClose && (
          <button type="button" class="ow-x" onClick={onClose} aria-label="Close">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          </button>
        )}
      </div>
      {!file && (
        <div class="ow-tabs" role="tablist" aria-label="Owner">
          {PANEL_TABS.map((t) => (
            <button
              type="button"
              role="tab"
              aria-selected={t.id === tab}
              class={`ow-tab${t.id === tab ? " is-on" : ""}`}
              key={t.id}
              onClick={() => onTab?.(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>
      )}
      <div class="ow-panel-body" key={`${tab}-${openPath || ""}`}>
        {tab === "overview"
          ? <OwnerOverview owner={owner} onOpenChild={onOpenChild} />
          : (
            <OwnerBook
              book={book}
              openPath={openPath}
              onOpenFile={onOpenFile}
              onSave={onSave}
              status={bookStatus}
              error={bookError}
              fileStatus={fileStatus}
            />
          )}
      </div>
    </aside>
  );
}

/* ── The chip in a child ─────────────────────────────────────────────── */

// OwnerChip — in a child session, the one thing that says this conversation
// belongs to a project that has an owner, and the door to it. Deliberately
// small and neutral: it is provenance, not a state, so it takes no identity
// colour and no dot (CRITERIO §1). On the desktop it needs no change to
// production — ChatHead already renders `headExtra` beside its actions.
export function OwnerChip({ name, onClick, compact = false }) {
  return (
    <button
      type="button"
      class={`ow-chip${compact ? " is-compact" : ""}`}
      onClick={onClick}
      aria-label={`Owner: ${name}. Open its conversation`}
      title={`Owner: ${name}`}
    >
      <span class="ow-chip-k">Owner</span>
      <span class="ow-chip-sep" aria-hidden="true">·</span>
      <span class="ow-chip-v">{name}</span>
    </button>
  );
}
