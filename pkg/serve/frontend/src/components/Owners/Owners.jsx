import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { SessionRow } from "../SessionRow/SessionRow.jsx";
import { Field } from "../../primitives/Field/Field.jsx";
import { Button } from "../../primitives/Button/Button.jsx";
import { defaultModelSpec } from "../CommandPalette/command-palette-model.js";
import {
  ModelSelector, PickerPopover, thinkingButtonsFor,
} from "../ModelSelector/ModelSelector.jsx";
import { thinkingPositionFor } from "../../data/selectors.js";
import { ensureModelCatalog, modelCatalog } from "../../data/model-catalog.js";
import { useStore } from "../../hooks/useStore.js";
import { modelCodename } from "../../data/util/format.js";
import { api } from "../../data/api.js";
import { bookTree, groupChildren, ownerLine, ownerRows, ownerState } from "../../data/owners-model.js";
import { AVATAR_COLORS, AVATAR_SHAPES, OwnerAvatar, OwnerAvatarFor, defaultAvatar } from "./OwnerAvatar.jsx";
import { loadOwnerBook, openBookFile, ownersSlice, saveBookFile, updateOwner } from "../../data/owners.js";
import { openSession } from "../../data/tile-actions.js";
import { setSessionPanelPage } from "../../data/session-panel.js";
import { addToast } from "../../data/notifications.js";
import { EditOwner } from "./EditOwner.jsx";
import { OwnerIdentityPicker } from "./OwnerIdentityPicker.jsx";

import "./OwnerAvatar.css";
// The identity picker's sheet lives beside the owner row it is choosing a face
// for, so the swatch grid and the row cannot drift apart.
import "./OwnerRow.css";
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

/* ── The list ─────────────────────────────────────────────────────────── */

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

// useOwnerModels answers two things the model row needs: the catalogue of
// models, and WHICH ONE a new session would use. The catalogue comes from the
// app's shared slice (data/model-catalog.js) rather than a fetch of its own —
// the same entries the composer's selector is drawing, so the two surfaces
// cannot disagree about what exists. `defaultModel` is the SERVER's default
// (`/api/capabilities`), resolved through the palette's own `defaultModelSpec`,
// which is what makes "the default" here mean the same thing as "the default"
// in New session rather than a second opinion about it.
function useOwnerModels() {
  const catalog = useStore(modelCatalog);
  const [defaultModel, setDefaultModel] = useState("");
  useEffect(() => { ensureModelCatalog(); }, []);
  const models = catalog.entries || [];
  useEffect(() => {
    if (!models.length) return undefined;
    let live = true;
    api("GET", "/api/capabilities")
      .catch(() => ({}))
      .then((caps) => { if (live) setDefaultModel(defaultModelSpec(caps, models)); });
    return () => { live = false; };
  }, [models.length]);
  return { models, defaultModel };
}

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


/* ── The model step on a phone ──────────────────────────────────────────
   On a phone the model is chosen on a PAGE of the New owner sheet, the way
   Edit owner is a page of the dossier: a second sheet over the form meant two
   grabbers, and one Escape closing both. The page is the ModelSelector in its
   hosted mode, so its own levels (All models → a provider) are this sheet's
   pages too. `null` is the form itself. */
export function modelPageParent(view) {
  if (view == null || view === "root") return null;
  return view === "providers" ? "root" : "providers";
}

export function modelPageTitle(view) {
  if (view == null) return "New owner";
  if (view === "root") return "Model";
  if (view === "providers") return "All models";
  return view;
}

export function modelPageBackLabel(view) {
  return `Back to ${modelPageTitle(modelPageParent(view))}`;
}

const basename = (p) => String(p || "").replace(/\/+$/, "").split("/").filter(Boolean).pop() || "";

export function NewOwner({ defaultDir = "", onCreate, phone = false, onCreated, modelView = null, onModelView }) {
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
  const { models, defaultModel } = useOwnerModels();
  /* The model, and where it is chosen. This used to be a list of every model
     stacked in the form plus a Segmented for thinking — a second model picker
     with its own idea of what a model row looks like, of which one is pinned,
     of whether "low" means the same thing on every provider. It is now ONE
     ROW that states the current model and opens the product's own
     ModelSelector: the popover on the desktop, a page of this sheet on a phone, thinking
     chosen inside it exactly as it is from the composer.

     `null` means "not chosen", and what is drawn then is the same default a
     NEW SESSION would take (`/api/capabilities`), because that is the promise
     the row makes by showing a value before you touch it. Thinking starts at
     "low" on purpose: an owner judges rather than codes, which is the
     backend's own default (pkg/serve/owners.go:19-22). */
  const [model, setModel] = useState(null);
  const [thinking, setThinking] = useState("low");
  const chosenModel = model || defaultModel;
  const modelSpec = models.find((m) => m.id === chosenModel || m.name === chosenModel);

  // The name follows the folder until you type one. A project's owner is
  // almost always called after the project, and making that the default is
  // what turns four fields into one decision.
  const suggested = basename(dir);
  const effectiveName = touchedName ? name : suggested;

  /* The face. It already looks like itself before anything is pressed: the
     default is the codebase's own deterministic mark, the one the server
     would compute if the field were omitted (pkg/owner/avatar.go). It follows
     the folder until you choose a shape or a colour — browsing to another
     project and keeping the previous project's face would be a mark that says
     the wrong thing. `codebase_key` is not known in the browser, so the
     folder's basename stands in for it; the server stores what is sent, so
     what you see here is what the owner keeps. */
  const fallbackAvatar = defaultAvatar(basename(dir));
  const [chosenAvatar, setChosenAvatar] = useState(null);
  const avatar = chosenAvatar || fallbackAvatar;

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

  /* ── Hosting the ModelSelector ──────────────────────────────────────────
     On the desktop the picker mounts INSIDE the modal rather than being
     portalled to <body>: `PickerPopover` right above the row it belongs to,
     as the Composer hosts it inside a pane. On a phone it is the model page
     (`modelView`, owned by NewOwnerDialog so the sheet's head and Escape
     can walk it back). */
  const [popoverOpen, setPopoverOpen] = useState(false);
  const onModelPage = phone && modelView != null;
  const modelOpen = phone ? onModelPage : popoverOpen;
  useEffect(() => { if (modelOpen) ensureModelCatalog(); }, [modelOpen]);
  const openModel = () => (phone ? onModelView?.("root") : setPopoverOpen(!popoverOpen));
  const closeModel = () => (phone ? onModelView?.(null) : setPopoverOpen(false));
  // Back from the model page lands on the row that opened it.
  const modelRowRef = useRef(null);
  const wasOnModelPage = useRef(false);
  useEffect(() => {
    if (wasOnModelPage.current && !onModelPage) modelRowRef.current?.focus();
    wasOnModelPage.current = onModelPage;
  }, [onModelPage]);

  const thinkingOptions = thinkingButtonsFor(modelSpec, modelSpec?.provider);
  const thinkingValue = thinkingPositionFor(thinking, modelSpec, modelSpec?.provider);
  const thinkingLabel = thinkingOptions.find((t) => t.value === thinkingValue)?.label || thinkingValue;

  // The selector itself, once, for both hosts. The choice it writes is local
  // state — the owner does not exist yet — which is the only thing that
  // differs from the status line's copy. `fastSupported={false}` because
  // create has no fast flag to send: the switch says so rather than lying.
  const selector = (v) => (
    <ModelSelector
      models={models}
      selected={chosenModel}
      thinking={thinking}
      embedded
      sessionProvider={modelSpec?.provider}
      view={v.view}
      setView={v.setView}
      onSelect={(spec) => { setModel(spec); closeModel(); }}
      onThinkingChange={setThinking}
      fastSupported={false}
    />
  );
  const picker = !phone && popoverOpen && (
    <div class="ow-model-anchor">
      <PickerPopover kind="model" models={models} onClose={closeModel}>
        {selector}
      </PickerPopover>
    </div>
  );

  // The model page stands in for the form; what was typed is this
  // component's state, so it is all there on the way back.
  if (onModelPage) {
    return (
      <div class="ow-model-page">
        {selector({ view: modelView, setView: onModelView })}
      </div>
    );
  }

  return (
    <div class="ow-form">
      <OwnerIdentityPicker
        name={effectiveName}
        shape={avatar.shape}
        color={avatar.color}
        onShape={(shape) => setChosenAvatar({ ...avatar, shape })}
        onColor={(color) => setChosenAvatar({ ...avatar, color })}
      />
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
        {/* One row, and it is a DOOR. What it states is what the owner will
            run; pressing it opens the product's own ModelSelector, where
            thinking lives too. The reading is the selector's own: codename,
            then provider · context, then the thinking position — so the row
            and the surface it opens cannot describe the same choice in two
            vocabularies. */}
        <button
          type="button"
          class="ow-modelrow"
          ref={modelRowRef}
          aria-haspopup={phone ? undefined : "dialog"}
          aria-expanded={phone ? undefined : popoverOpen}
          onClick={openModel}
        >
          <span class="ow-mono is-sm" style={`--h:${hueOf(modelSpec?.codename || chosenModel)}`} aria-hidden="true">
            {String(modelSpec?.codename || modelCodename(chosenModel) || "?").slice(0, 2)}
          </span>
          <span class="ow-modelrow-txt">
            <span class="ow-modelrow-name">
              {modelSpec?.codename || modelCodename(chosenModel) || (models.length ? "Choose a model" : "Loading models…")}
            </span>
            <span class="ow-modelrow-sub ow-data">
              {modelSpec ? `${modelSpec.provider} · ${modelSpec.sub} · thinking ${thinkingLabel}` : `thinking ${thinkingLabel}`}
            </span>
          </span>
          <GoIcon />
        </button>
        <span class="ow-hint">
          An owner reads reports and keeps a book rather than writing code, so it
          starts on the default model at the cheapest thinking.
        </span>
      </div>
      {picker}

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
              await onCreate?.({ root: dir, name: effectiveName, model: chosenModel, thinking, avatar });
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
          Close the project's open sessions first: they cannot adopt an owner while running.
        </p>
      </div>
      {phone && <div class="ow-form-pad" aria-hidden="true" />}
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

export function OwnerOverview({ owner, onOpenChild, onEdit }) {
  const groups = groupChildren(owner.children || []);
  return (
    <div class="ow-page">
      <button type="button" class="ow-btn ow-edit-owner" onClick={() => onEdit?.()}>Edit owner</button>
      <OwnerStateSummary owner={owner} />
      <div class="ow-sum">
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

function OwnerStateSummary({ owner }) {
  const { lead, tail } = ownerLine(owner);
  if (!lead && !tail) return null;
  return (
    <div class="ow-sum ow-sum-state">
      {lead && <span class={`ow-sum-t tone-${lead.tone}`}>{lead.text}</span>}
      {lead && tail && <span class="ow-sum-sep" aria-hidden="true"> · </span>}
      {tail && <span class="ow-sum-t ow-sum-wait">{tail}</span>}
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

export function OwnerPanelPage({ session, page, phone = false }) {
  const slice = useStore(ownersSlice);
  const sessions = useStore((s) => s.sessions);
  const owner = ownerRows(slice.list, sessions).find((row) => row.session_id === session?.id) || null;
  const ownerId = owner?.id || null;

  useEffect(() => {
    if (page !== "book" || !ownerId) return;
    if (slice.bookOwnerId === ownerId && slice.bookStatus !== "idle") return;
    loadOwnerBook(ownerId);
  }, [page, ownerId]);

  if (!owner) return null;

  if (page === "overview") {
    return (
      <OwnerOverview
        owner={owner}
        onOpenChild={(child) => openSession(child.id)}
        onEdit={() => setSessionPanelPage("ownerEdit")}
      />
    );
  }

  // Edit owner is a STEP of this panel, so saving returns to the page it was
  // pushed from rather than dismissing a surface of its own.
  if (page === "ownerEdit") {
    return (
      <EditOwner
        owner={owner}
        phone={phone}
        onSave={(identity) => updateOwner(owner.id, identity)}
        onClose={() => setSessionPanelPage("overview")}
      />
    );
  }

  const mine = slice.bookOwnerId === ownerId;
  const files = mine ? slice.bookFiles : [];
  const openPath = mine ? slice.openPath : null;
  const book = files.map((file) => ({
    path: file.path,
    label: file.path.split("/").pop(),
    bytes: file.bytes,
    body: file.path === openPath ? slice.openBody : "",
    editable: file.path === openPath ? slice.openEditable : false,
  }));
  return (
    <OwnerBook
      book={book}
      openPath={openPath}
      onOpenFile={(path) => openBookFile(ownerId, path)}
      onSave={(path, content) => {
        saveBookFile(ownerId, path).catch((error) => {
          addToast({ title: "Could not save the index", detail: String(error.message || error), type: "error" });
        });
      }}
      status={mine ? slice.bookStatus : "idle"}
      error={mine ? slice.bookError : null}
      fileStatus={mine ? slice.openStatus : "idle"}
    />
  );
}

export function OwnerPanelAvatar({ session }) {
  const slice = useStore(ownersSlice);
  const sessions = useStore((s) => s.sessions);
  const owner = ownerRows(slice.list, sessions).find((row) => row.session_id === session?.id) || null;
  return owner ? <OwnerAvatarFor owner={owner} state={ownerState(owner)} size={24} /> : null;
}

/* ── The chip in a child ─────────────────────────────────────────────── */
