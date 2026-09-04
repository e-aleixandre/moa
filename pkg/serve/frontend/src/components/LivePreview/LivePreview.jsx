import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { MousePointerClick, MessageSquare, X, Minus, Plus, MoreVertical, Mic, Loader2, Square, ChevronUp, ArrowLeft, ArrowRight, ArrowUp, ArrowDown } from "lucide-preact";
import { Sheet } from "../Sheet/Sheet.jsx";
import { Segmented } from "../Segmented/Segmented.jsx";
import { AssistantDocument } from "../AssistantDocument/AssistantDocument.jsx";
import { Button } from "../../primitives/index.js";
import { renderMarkdown } from "../../data/util/markdown.js";
import { feedbackMessage, previewReferenceContext } from "../../data/util/preview-reference.js";
import { sendMessage } from "../../data/session-actions.js";
import { useStore } from "../../hooks/useStore.js";
import { useMenuKeyboard } from "../../hooks/useMenuKeyboard.js";
import { useCanTranscribe } from "../../hooks/useCanTranscribe.js";
import { useVoiceGesture } from "../../hooks/useVoiceGesture.js";
import { addToast } from "../../data/notifications.js";
import { PreviewStream } from "./PreviewStream.jsx";
import { streamEvents, stageState } from "./stream.js";
import { applyGesture, clampPan, pinchState, stageGesture, zoomAt, IDENTITY } from "./zoom.js";
import "./LivePreview.css";

// LivePreview — PROTOTYPE. A dev server rendered inside moa, next to the
// conversation, so the user can look at their app at a chosen viewport width
// and hand the agent a pointer to a concrete element ("this button", not "the
// button on the pricing page").
//
// The iframe loads a cross-origin URL the user types, so it is NOT sandboxed
// the way srcdoc previews are (see data/util/html-preview.js): Vite's HMR needs
// websockets and same-origin access inside the frame. The bridge with the
// previewed app is postMessage only (inspector.js, copied into that app).
//
// DESIGN RULE, everywhere below: the app owns every pixel, permanently. The
// preview covers the transcript, so the run still has to be visible — but not as
// a place. It is a STREAM: each thing the agent does floats up over the app and
// dissolves (PreviewStream), the tool in flight tints the stage's own frame, and
// what changed is shown INSIDE the app by the inspector. Idle, there is nothing
// over the app at all.
//
// The URL follows the same rule: it is typed once, on a first-run screen, and
// then its row is gone for good — changing it is a rare action, so it lives in
// the overflow menu with the reload.

const WIDTHS = [
  { value: "390", label: "390" },
  { value: "768", label: "768" },
  { value: "1280", label: "1280" },
  { value: "fit", label: "Fit" },
];

const urlKey = (sessionId) => `moa-preview-url:${sessionId}`;

export function loadPreviewURL(sessionId) {
  try {
    return localStorage.getItem(urlKey(sessionId)) || "";
  } catch {
    return "";
  }
}

function savePreviewURL(sessionId, url) {
  try {
    localStorage.setItem(urlKey(sessionId), url);
  } catch {
    /* private mode / quota — the URL just won't survive a reload */
  }
}

// normalizeURL — a bare "host:5173" typed on a phone keyboard should load.
export function normalizeURL(raw) {
  const text = (raw || "").trim();
  if (!text) return "";
  if (/^https?:\/\//i.test(text)) return text;
  return `http://${text}`;
}

export function LivePreview({ sessionId, open, onClose, inline = false }) {
  const session = useStore((s) => s.sessions[sessionId]);
  const [targetURL, setTargetURL] = useState("");
  const [frameURL, setFrameURL] = useState("");
  const [draftURL, setDraftURL] = useState("");
  const [editingURL, setEditingURL] = useState(false);
  const [width, setWidth] = useState("fit");
  const [inspect, setInspect] = useState(false);
  const [selected, setSelected] = useState(null);
  const [composerOpen, setComposerOpen] = useState(false);
  const [comment, setComment] = useState("");
  const [sending, setSending] = useState(false);
  const [reloadNonce, setReloadNonce] = useState(0);
  const [previewPublicURL, setPreviewPublicURL] = useState("");
  const [previewError, setPreviewError] = useState("");
  const [inspectorReady, setInspectorReady] = useState(true);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [view, setView] = useState(IDENTITY);
  const [notes, setNotes] = useState([]);
  const [reading, setReading] = useState(null);
  const iframeRef = useRef(null);
  const stageRef = useRef(null);
  const geometry = useRef({ base: 1, w: 0, h: 0, stage: { w: 0, h: 0 } });
  const noteSeq = useRef(0);
  const inspectButtonRef = useRef(null);
  const [touchInput, disableTouchInput] = useTouchPreviewInput();

  const events = streamEvents(session);
  const stage = stageState(session);

  // note — a card the SHELL raises next to the ones the run raises: the
  // acknowledgement of a feedback message.
  // Same lifetime, same lane, so they never fight the run for a corner.
  const note = (kind, text) => {
    const id = `note:${noteSeq.current++}`;
    setNotes((prev) => [...prev.slice(-2), { id, kind: "note", note: kind, text }]);
    // The card owns its own exit (reconcile); this only stops the list from
    // growing forever behind it.
    setTimeout(() => setNotes((prev) => prev.filter((n) => n.id !== id)), 8000);
  };

  // The saved value is always the upstream target; the iframe uses the proxy URL.
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    const restore = async () => {
      const saved = loadPreviewURL(sessionId);
      setDraftURL(saved);
      setEditingURL(false);
      try {
        const response = await fetch("/api/preview/target");
        if (!response.ok) throw new Error("preview configuration failed");
        const config = await response.json();
        if (cancelled) return;
        const target = saved || config.url || "";
        setPreviewPublicURL(config.enabled ? config.public_url || "" : "");
        setPreviewError("");
        if (!target) return;
        setTargetURL(target);
        if (!config.enabled || !config.public_url) {
          setFrameURL(target);
          return;
        }
        const put = await fetch("/api/preview/target", {
          method: "PUT",
          headers: { "Content-Type": "application/json", "X-Moa-Request": "1" },
          body: JSON.stringify({ url: target, parent_origin: location.origin }),
        });
        if (!put.ok) throw new Error("preview target failed");
        const result = await put.json();
        if (!cancelled) setFrameURL(result.preview_url || config.public_url);
      } catch {
        if (!cancelled) {
          setFrameURL("");
          setPreviewError("The preview proxy is unavailable. Try again.");
        }
      }
    };
    restore();
    return () => { cancelled = true; };
  }, [open, sessionId]);

  // Measure the stage so the scaled iframe can be laid out in real pixels.
  useLayoutEffect(() => {
    if (!open) return undefined;
    const stageEl = stageRef.current;
    if (!stageEl) return undefined;
    const measure = () => setBox({ w: stageEl.clientWidth, h: stageEl.clientHeight });
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(stageEl);
    window.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [open, frameURL]);

  // The inspector lives in the previewed app; the only channel is postMessage.
  const post = (msg) => {
    const origin = frameURL ? new URL(frameURL).origin : "";
    if (origin) iframeRef.current?.contentWindow?.postMessage(msg, origin);
  };
  const postInspect = (enabled) => post({ type: "moa-inspect", enabled });

  // Frame geometry: the unscaled size of the iframe and the scale the chosen
  // width is already drawn at. Everything the pinch math needs, in one place.
  const fixed = width === "fit" ? null : Number(width);
  const frameW = fixed || Math.max(box.w, 1);
  const base = fixed && box.w ? Math.min(1, box.w / fixed) : 1;
  const frameH = Math.max(box.h, 1) / base;
  geometry.current = { base, w: frameW, h: frameH, stage: { w: box.w, h: box.h } };

  useEffect(() => {
    if (!open) return undefined;
    const onMessage = (event) => {
      const data = event.data;
      if (!data || typeof data.type !== "string") return;
      const frame = iframeRef.current;
      const expectedOrigin = frameURL ? new URL(frameURL).origin : "";
      if (!expectedOrigin || event.origin !== expectedOrigin) return;
      if (!frame || event.source !== frame.contentWindow) return;

      if (data.type === "moa-element") {
        setSelected(data);
        setComposerOpen(true);
        // A mouse click inside a cross-origin frame gives that frame focus.
        // Return it to moa's chrome once the selection is delivered.
        requestAnimationFrame(() => inspectButtonRef.current?.focus());
        return;
      }
      if (data.type === "moa-escape") {
        onClose();
        requestAnimationFrame(() => document.querySelector("[data-preview-trigger='true']")?.focus());
      }
      if (data.type === "moa-ready") {
        setInspectorReady(true);
        return;
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [open, onClose, frameURL]);

  useEffect(() => {
    if (!frameURL) return undefined;
    setInspectorReady(false);
    const timer = setTimeout(() => setInspectorReady((ready) => ready), 3000);
    return () => clearTimeout(timer);
  }, [frameURL, reloadNonce]);

  // iOS Safari pinch guard. A pinch that has to
  // cross the iframe boundary is split between two touch-active documents and
  // never becomes one two-finger sequence — measured on a real iPhone, both the
  // in-frame bridge and same-origin listeners flicker. Zoom mode puts a layer of
  // THIS document over the whole stage before the first contact, so there is
  // only one document involved; these guards are the last mile, stopping
  // WebKit's legacy gesture* events from page-zooming moa's shell. Outside Zoom
  // mode nothing global is installed: the rest of moa must keep its own gestures.
  useEffect(() => {
    if (!open) return undefined;
    const stop = (e) => e.preventDefault();
    const opts = { passive: false };
    document.addEventListener("gesturestart", stop, opts);
    document.addEventListener("gesturechange", stop, opts);
    document.addEventListener("gestureend", stop, opts);
    return () => {
      document.removeEventListener("gesturestart", stop, opts);
      document.removeEventListener("gesturechange", stop, opts);
      document.removeEventListener("gestureend", stop, opts);
    };
  }, [open]);

  // Closing the panel drops the selection but keeps the URL (persisted).
  useEffect(() => {
    if (open) return;
    setSelected(null);
    setComment("");
    setInspect(false);
    setView(IDENTITY);
    setNotes([]);
    setReading(null);
  }, [open]);

  if (!open) return null;

  const commitURL = async () => {
    const next = normalizeURL(draftURL);
    setDraftURL(next);
    setEditingURL(false);
    if (!next) return;
    if (next === targetURL && frameURL) return;
    if (previewPublicURL) {
      // A target switch gets a new capability and a new iframe document. Never
      // leave the previous target running while the proxy is being configured.
      setFrameURL("");
      setPreviewError("");
      try {
        const response = await fetch("/api/preview/target", { method: "PUT", headers: { "Content-Type": "application/json", "X-Moa-Request": "1" }, body: JSON.stringify({ url: next, parent_origin: location.origin }) });
        if (!response.ok) throw new Error("preview target failed");
        const result = await response.json();
        setTargetURL(next);
        setFrameURL(result.preview_url || previewPublicURL);
        setSelected(null);
        setView(IDENTITY);
        savePreviewURL(sessionId, next);
        setReloadNonce((n) => n + 1);
      } catch {
        setTargetURL(next);
        setPreviewError("The preview proxy is unavailable. Try again.");
        savePreviewURL(sessionId, next);
      }
      return;
    }
    setTargetURL(next);
    setFrameURL(next);
    setSelected(null);
    setView(IDENTITY);
    savePreviewURL(sessionId, next);
    setReloadNonce((n) => n + 1);
  };

  const reload = () => {
    setSelected(null);
    setReloadNonce((n) => n + 1);
  };

  const toggleInspect = () => {
    const next = !inspect;
    setInspect(next);
    postInspect(next);
    if (!next) setSelected(null);
  };

  const onFrameLoad = () => {
    // A navigation inside the app (or a reload) drops the inspector state:
    // re-arm it so the toggle keeps meaning what it says.
    if (inspect) postInspect(true);
    post({ type: "moa-hello" });
  };

  const send = () => {
    const message = feedbackMessage(comment, selected);
    if (sending || !message) return;
    setSending(true);
    Promise.resolve(sendMessage(sessionId, message))
      .catch(() => {})
      .then(() => {
        setSending(false);
        setSelected(null);
        setComposerOpen(false);
        setComment("");
        note("sent", "Sent");
      });
  };

  const zoomed = view.zoom !== 1;
  const scale = base * view.zoom;
  const frameStyle = {
    width: `${frameW}px`,
    height: `${frameH}px`,
    transform: `translate(${view.x}px, ${view.y}px) scale(${scale})`,
    transformOrigin: "0 0",
  };
  // The holder carries the SCALED size: a transform does not change layout, so
  // without it the stage would either not scroll at all or scroll over the
  // unscaled height. Zoomed, the pan is the position and the holder just fills.
  const holderStyle = zoomed ? { width: "100%", height: "100%" } : { width: `${frameW * scale}px`, height: `${frameH * scale}px` };

  const showSetup = !targetURL || editingURL;

  const preview = (
    <>
      <div class="live-preview-bar">
        <PreviewMenu
          onChangeURL={() => {
            setDraftURL(targetURL);
            setEditingURL(true);
          }}
          onReload={reload}
        />
        <Segmented
          className="live-preview-widths"
          options={WIDTHS}
          value={width}
          onChange={(next) => {
            setWidth(next);
            setView(IDENTITY);
          }}
          aria-label="Viewport width"
        />
        <button
          type="button"
          class={`live-preview-action${inspect ? " is-on" : ""}`}
          ref={inspectButtonRef}
          onClick={toggleInspect}
          aria-pressed={inspect}
          title="Inspect — tap an element in the app"
        >
          <MousePointerClick size={15} />
          <span class="live-preview-action-label">Inspect</span>
        </button>
        <button
          type="button"
          class={`live-preview-action${composerOpen && !selected ? " is-on" : ""}`}
          onClick={() => setComposerOpen((openComposer) => !openComposer)}
          aria-pressed={composerOpen && !selected}
          title="Message the agent"
        >
          <MessageSquare size={15} />
          <span class="live-preview-action-label">Message</span>
        </button>
        <button type="button" class="live-preview-action" onClick={onClose} title="Close preview">
          <X size={15} />
        </button>
      </div>
      {!inspectorReady && <div class="live-preview-inspector-warning">Inspector did not connect. Navigate the app directly, or add the inspector script to enable touch inspection and gestures.</div>}
      {previewError && <div class="live-preview-proxy-error" role="alert">{previewError}</div>}

      {/* The live border: the stage's own frame carries the identity color of
          the tool in flight and breathes while it runs, amber and still while
          the run is parked on the user, invisible when idle. Peripheral vision
          is the whole point — it is read without looking, and it costs the app
          nothing but the 2px the panel's edge already spent. */}
      <div
        class={
          `live-preview-stage is-${stage.mode}` +
          `${stage.mode === "working" && stage.kind ? ` k-${stage.kind}` : ""}`
        }
        onPointerDownCapture={(event) => {
          if (selected && !event.target.closest(".live-preview-inspect-popover")) setSelected(null);
        }}
      >
        <div
          class={`live-preview-scroller${zoomed ? " is-zoomed" : ""}`}
          ref={stageRef}
        >
          {frameURL && (
            <div class="live-preview-holder" style={holderStyle}>
              <iframe
                key={`${frameURL}#${reloadNonce}`}
                ref={iframeRef}
                class="live-preview-frame"
                src={frameURL}
                style={frameStyle}
                onLoad={onFrameLoad}
                sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-modals"
                title="Live preview"
              />
            </div>
          )}
        </div>

        {/* First run (and "Change URL"): the ONLY moment the URL is worth a
            screen. It takes the stage instead of a permanent row, so once the
            app is loaded those pixels go back to it for good. */}
        {showSetup && (
          <div class="live-preview-setup">
            <p class="live-preview-setup-title">Enter your app URL</p>
            <div class="live-preview-setup-row">
              <input
                class="live-preview-url"
                type="url"
                inputMode="url"
                autocapitalize="off"
                autocorrect="off"
                spellcheck={false}
                placeholder="http://localhost:5173"
                value={draftURL}
                autofocus
                onInput={(e) => setDraftURL(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") commitURL();
                  if (e.key === "Escape" && targetURL) setEditingURL(false);
                }}
                aria-label="Preview URL"
              />
              <Button variant="solid" size="sm" onClick={commitURL} disabled={!draftURL.trim()}>
                Load
              </Button>
            </div>
            <p class="live-preview-setup-hint">
              Enter your development server URL.
            </p>
          </div>
        )}

        {/* Siblings of the scroller, not children: what floats over the app must
            not slide away when the app under it scrolls. */}
        {frameURL && !showSetup && (
          <>
            {touchInput && <TouchZoomOverlay geometry={geometry} view={view} onView={setView} inspect={inspect} post={post} onMouse={disableTouchInput} />}
            <ZoomControls geometry={geometry} view={view} onView={setView} />
          </>
        )}
        {zoomed && (
          <button
            type="button"
            class="live-preview-chip is-zoom"
            onClick={() => setView(IDENTITY)}
            title="Reset zoom"
          >
            1:1
          </button>
        )}
        {(selected || composerOpen) && (
          <InspectPopover
            selected={selected}
            comment={comment}
            onComment={setComment}
            onClose={() => {
              setSelected(null);
              setComposerOpen(false);
            }}
            onSend={send}
            sending={sending}
            stageRef={stageRef}
            iframeRef={iframeRef}
            geometry={geometry}
            view={view}
          />
        )}
        {!showSetup && (
          <PreviewStream
            events={events}
            notes={notes}
            onOpenText={setReading}
            onGoToChat={onClose}
          />
        )}
      </div>

      {reading && (
        <Sheet open onClose={() => setReading(null)} title="Message" class="lp-msg-sheet">
          <AssistantDocument html={renderMarkdown(reading)} />
          <div class="lp-msg-actions">
            <Button variant="solid" size="sm" onClick={onClose}>
              Go to chat
            </Button>
          </div>
        </Sheet>
      )}
    </>
  );
  return inline
    ? <section class="live-preview-inline" aria-label="Live preview">{preview}</section>
    : <Sheet open onClose={onClose} ariaLabel="Live preview" class="live-preview-sheet">{preview}</Sheet>;
}

function useTouchPreviewInput() {
  const get = () => typeof window !== "undefined"
    && window.matchMedia("(pointer: coarse)").matches
    && !window.matchMedia("(any-pointer: fine)").matches;
  const [touch, setTouch] = useState(get);
  useEffect(() => {
    if (typeof window === "undefined") return undefined;
    const queries = [window.matchMedia("(pointer: coarse)"), window.matchMedia("(any-pointer: fine)")];
    const update = () => setTouch(get());
    queries.forEach((query) => query.addEventListener?.("change", update));
    return () => queries.forEach((query) => query.removeEventListener?.("change", update));
  }, []);
  return [touch, () => setTouch(false)];
}

// PreviewMenu — the two rare actions on the URL, out of the way. Changing the
// dev server address happens once a session at most, so it does not get to keep
// a row of the app forever; reload joins it because it is one tap deeper and
// still instant.
function PreviewMenu({ onChangeURL, onReload }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const triggerRef = useRef(null);
  const actionsRef = useRef(null);
  const { onMenuKeyDown, closeMenu } = useMenuKeyboard(open, setOpen, triggerRef, actionsRef);

  useEffect(() => {
    if (!open) return undefined;
    const onDocDown = (event) => {
      if (ref.current && !ref.current.contains(event.target)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocDown);
    return () => document.removeEventListener("mousedown", onDocDown);
  }, [open]);

  return (
    <div class="live-preview-menu" ref={ref}>
      <button
        type="button"
        class="live-preview-action"
        ref={triggerRef}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Preview options"
        onClick={() => setOpen((v) => !v)}
      >
        <MoreVertical size={15} />
      </button>
      {open && (
        <div
          class="live-preview-menu-items"
          role="menu"
          aria-label="Preview options"
          ref={actionsRef}
          onKeyDown={onMenuKeyDown}
        >
          <button
            type="button"
            role="menuitem"
            class="live-preview-menu-item"
            onClick={() => {
              closeMenu();
              onReload();
            }}
          >
            Reload
          </button>
          <button
            type="button"
            role="menuitem"
            class="live-preview-menu-item"
            onClick={() => {
              closeMenu();
              onChangeURL();
            }}
          >
            Change URL
          </button>
        </div>
      )}
    </div>
  );
}

// The iframe is transformed by base × zoom plus the current pan. Reading its
// rendered origin keeps the centred unzoomed viewport and the zoomed viewport
// on the same coordinate system; rect pixels then use the exact scale from
// zoom.js. The stream is a reserved bottom lane, so the feedback never covers
// a live card.
function InspectPopover({ selected, comment, onComment, onClose, onSend, sending, stageRef, iframeRef, geometry, view }) {
  const popoverRef = useRef(null);
  const textareaRef = useRef(null);
  const [position, setPosition] = useState({ left: 0, top: 0, ready: false });
  const canTranscribe = useCanTranscribe();

  const appendTranscript = (text) => {
    onComment((current) => `${current}${current && !/\s$/.test(current) ? " " : ""}${text}`);
    textareaRef.current?.focus();
  };
  const onVoiceError = (detail) => addToast({ title: "Voice input", detail, type: "error" });
  const {
    handlers: voiceHandlers, recording, transcribing, locked, showSlideHint, supported,
  } = useVoiceGesture({
    onTranscript: appendTranscript,
    onError: onVoiceError,
    onSend,
    sendOnPointerCancel: !!comment.trim(),
  });
  const canVoice = canTranscribe && supported;

  useEffect(() => {
    const onKeyDown = (event) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  useLayoutEffect(() => {
    const place = () => {
      const stage = stageRef.current;
      const frame = iframeRef.current;
      const popover = popoverRef.current;
      if (!stage || !popover) return;
      if (!selected) {
        const stream = stage.querySelector(".lp-stream");
        const streamHeight = stream?.getBoundingClientRect().height || 0;
        setPosition({ left: Math.max(8, (stage.clientWidth - popover.offsetWidth) / 2), top: Math.max(8, stage.clientHeight - popover.offsetHeight - streamHeight - 16), ready: true });
        return;
      }
      if (!frame || !selected.rect) return;
      const stageRect = stage.getBoundingClientRect();
      const frameRect = frame.getBoundingClientRect();
      const scale = (geometry.current.base || 1) * (view.zoom || 1);
      const target = {
        left: frameRect.left - stageRect.left + selected.rect.x * scale,
        top: frameRect.top - stageRect.top + selected.rect.y * scale,
        width: selected.rect.width * scale,
        height: selected.rect.height * scale,
      };
      const margin = 8;
      const stream = stage.parentElement?.querySelector(".lp-stream");
      const streamTop = stream ? stream.getBoundingClientRect().top - stageRect.top - margin : stage.clientHeight;
      const bottom = Math.max(margin, Math.min(stage.clientHeight - margin, streamTop));
      const width = popover.offsetWidth;
      const height = popover.offsetHeight;
      const left = Math.min(Math.max(margin, target.left + target.width / 2 - width / 2), Math.max(margin, stage.clientWidth - width - margin));
      const below = target.top + target.height + margin;
      const above = target.top - margin - height;
      let top;
      if (below + height <= bottom) top = below;
      else if (above >= margin) top = above;
      else top = Math.max(margin, Math.min((bottom - height) / 2, bottom - height));
      setPosition({ left, top, ready: true });
    };
    place();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(place);
    if (popoverRef.current) observer?.observe(popoverRef.current);
    if (stageRef.current) observer?.observe(stageRef.current);
    window.visualViewport?.addEventListener("resize", place);
    return () => {
      observer?.disconnect();
      window.visualViewport?.removeEventListener("resize", place);
    };
  }, [selected, geometry, view]);

  const selector = selected && `${selected.tag}${selected.id ? `#${selected.id}` : ""}${(selected.classes || []).slice(0, 2).map((name) => `.${name}`).join("")}`;
  const context = selected && previewReferenceContext(selected.ancestors);
  const voiceTitle = transcribing ? "Transcribing…"
    : recording ? (locked ? "Tap to stop & transcribe" : "Release to transcribe · slide up to lock")
    : canVoice ? "Hold to talk" : "Voice input unavailable";
  const VoiceIcon = transcribing ? Loader2 : recording && locked ? Square : Mic;

  return (
    <div
      ref={popoverRef}
      class={`live-preview-inspect-popover${selected ? "" : " is-message"}`}
      style={{ left: `${position.left}px`, top: `${position.top}px`, visibility: position.ready ? "visible" : "hidden" }}
      role="dialog"
      aria-label={selected ? "Feedback for selected element" : "Message the agent"}
    >
      <div class="live-preview-inspect-head">
        {selected ? <><span class="live-preview-element-tag">{selected.tag}</span><code class="live-preview-selector">{selector}</code></> : <span class="live-preview-message-title">Message the agent</span>}
        <button type="button" class="live-preview-inspect-close" onClick={onClose} aria-label="Close feedback"><X size={15} /></button>
      </div>
      {selected?.text && <p class="live-preview-inspect-text">“{selected.text}”{context && <small>in {context}</small>}</p>}
      <div class="live-preview-inspect-compose">
        <textarea
          ref={textareaRef}
          class="live-preview-comment"
          rows={2}
          placeholder="What should change here?"
          value={comment}
          onInput={(event) => onComment(event.currentTarget.value)}
          aria-label="Comment for the agent"
        />
        <div class="live-preview-inspect-actions">
          {showSlideHint && <span class="live-preview-voice-hint"><ChevronUp size={13} />Slide up to lock</span>}
          <button
            type="button"
            class={`live-preview-voice${recording ? " is-recording" : ""}${transcribing ? " is-transcribing" : ""}`}
            aria-label={recording ? "Stop recording" : "Record feedback"}
            title={voiceTitle}
            disabled={!canVoice || transcribing}
            {...(canVoice || recording || transcribing ? voiceHandlers : {})}
          >
            <VoiceIcon size={16} class={transcribing ? "spin" : ""} />
          </button>
          <Button variant="solid" size="sm" onClick={onSend} disabled={sending || transcribing}>
            {sending ? "Sending…" : "Send"}
          </Button>
        </div>
        <div class={`live-preview-attachment${selected ? "" : " is-empty"}`}>
          {selected ? "Element attached" : "No element attached"}
        </div>
      </div>
    </div>
  );
}

// On touch-only devices this layer owns the first contact before WebKit chooses
// a touch-active document. Fine pointers never get this layer: their iframe is
// a normal browser surface for click, hover, wheel and keyboard input.
function TouchZoomOverlay({ geometry, view, onView, inspect, post, onMouse }) {
  const layerRef = useRef(null);
  const viewRef = useRef(view);
  const inspectRef = useRef(inspect);
  const postRef = useRef(post);
  const onViewRef = useRef(onView);
  const onMouseRef = useRef(onMouse);
  viewRef.current = view;
  inspectRef.current = inspect;
  postRef.current = post;
  onViewRef.current = onView;
  onMouseRef.current = onMouse;

  useEffect(() => {
    const el = layerRef.current;
    if (!el) return undefined;
    let start = IDENTITY;
    let pinch = null;
    let relay = null;
    let relayMove = null;
    let relayRAF = 0;
    let lastTap = null;
    const point = (touch) => {
      const g = geometry.current;
      const rect = el.getBoundingClientRect();
      const scale = g.base * viewRef.current.zoom;
      return { x: (touch.clientX - rect.left - viewRef.current.x) / scale, y: (touch.clientY - rect.top - viewRef.current.y) / scale };
    };
    const flushRelay = () => {
      relayRAF = 0;
      if (!relayMove) return;
      postRef.current({ type: "moa-scroll", ...relayMove });
      relayMove = null;
    };
    const onTouchStart = (e) => {
      e.preventDefault();
      if (e.touches.length >= 2) {
        relay = null;
        start = viewRef.current;
        pinch = pinchState(e.touches[0], e.touches[1]);
      } else if (e.touches.length === 1) {
        const t = e.touches[0];
        relay = { x: t.clientX, y: t.clientY, startX: t.clientX, startY: t.clientY, started: performance.now(), moved: false, point: point(t) };
      }
    };
    const onTouchMove = (e) => {
      e.preventDefault();
      const g = geometry.current;
      if (e.touches.length >= 2) {
        relay = null;
        if (!pinch) { start = viewRef.current; pinch = pinchState(e.touches[0], e.touches[1]); }
        onViewRef.current(applyGesture(start, stageGesture(start, g, pinch, pinchState(e.touches[0], e.touches[1])), g, g.stage));
        return;
      }
      if (e.touches.length === 1 && relay) {
        const t = e.touches[0];
        const dx = t.clientX - relay.x;
        const dy = t.clientY - relay.y;
        relay.moved ||= Math.hypot(t.clientX - relay.startX, t.clientY - relay.startY) >= 10;
        relay.x = t.clientX;
        relay.y = t.clientY;
        if (relay.moved) {
          const scale = g.base * viewRef.current.zoom;
          if (!relayMove) relayMove = { x: relay.point.x, y: relay.point.y, dx: 0, dy: 0, reset: true };
          relayMove.dx -= dx / scale;
          relayMove.dy -= dy / scale;
          if (!relayRAF) relayRAF = requestAnimationFrame(flushRelay);
        }
      }
    };
    const onTouchEnd = (e) => {
      e.preventDefault();
      if (e.touches.length < 2) pinch = null;
      if (e.touches.length === 0 && relay) {
        const duration = performance.now() - relay.started;
        if (e.type === "touchend" && !relay.moved && duration < 300 && e.changedTouches.length) {
          const p = point(e.changedTouches[0]);
          if (lastTap && performance.now() - lastTap.at < 300 && Math.hypot(p.x - lastTap.x, p.y - lastTap.y) < 10) {
            onViewRef.current(IDENTITY);
            lastTap = null;
          } else {
            postRef.current({ type: inspectRef.current ? "moa-inspect-tap" : "moa-tap", x: p.x, y: p.y });
            lastTap = { ...p, at: performance.now() };
          }
        }
        relay = null;
      }
    };
    const onWheel = (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      const g = geometry.current;
      const rect = el.getBoundingClientRect();
      onViewRef.current(zoomAt(viewRef.current, Math.exp(-e.deltaY / 300), { x: e.clientX - rect.left, y: e.clientY - rect.top }, g, g.stage));
    };
    const onPointerDown = (e) => {
      if (e.pointerType !== "mouse") return;
      e.preventDefault();
      const p = point(e);
      postRef.current({ type: inspectRef.current ? "moa-inspect-tap" : "moa-tap", x: p.x, y: p.y });
      onMouseRef.current();
    };
    const opts = { passive: false };
    el.addEventListener("touchstart", onTouchStart, opts);
    el.addEventListener("touchmove", onTouchMove, opts);
    el.addEventListener("touchend", onTouchEnd, opts);
    el.addEventListener("touchcancel", onTouchEnd, opts);
    el.addEventListener("wheel", onWheel, opts);
    el.addEventListener("pointerdown", onPointerDown, opts);
    return () => {
      el.removeEventListener("touchstart", onTouchStart, opts);
      el.removeEventListener("touchmove", onTouchMove, opts);
      el.removeEventListener("touchend", onTouchEnd, opts);
      el.removeEventListener("touchcancel", onTouchEnd, opts);
      el.removeEventListener("wheel", onWheel, opts);
      el.removeEventListener("pointerdown", onPointerDown, opts);
      if (relayRAF) cancelAnimationFrame(relayRAF);
    };
  }, [geometry]);

  return (
    <>
      <div class="live-preview-zoomlayer" ref={layerRef} role="presentation" />
      <span class="live-preview-zoomhint">Pinch · drag · double-tap = 1:1</span>
    </>
  );
}

function ZoomControls({ geometry, view, onView }) {
  const step = (factor) => {
    const g = geometry.current;
    onView(zoomAt(view, factor, null, g, g.stage));
  };
  const pan = (x, y) => {
    const g = geometry.current;
    const scale = g.base * view.zoom;
    onView({ zoom: view.zoom, ...clampPan(view.x + x, view.y + y, g.w * scale, g.h * scale, g.stage.w, g.stage.h) });
  };
  return (
    <div class="live-preview-zoomctl">
      <button type="button" onClick={() => step(1 / 1.4)} aria-label="Zoom out" title="Zoom out"><Minus size={15} /></button>
      <button type="button" onClick={() => step(1.4)} aria-label="Zoom in" title="Zoom in"><Plus size={15} /></button>
      <button type="button" onClick={() => onView(IDENTITY)} aria-label="Reset zoom" title="Reset zoom">1:1</button>
      <span class="live-preview-pan-controls" aria-label="Pan preview">
        <button type="button" onClick={() => pan(48, 0)} aria-label="Pan left" title="Pan left"><ArrowLeft size={13} /></button>
        <button type="button" onClick={() => pan(-48, 0)} aria-label="Pan right" title="Pan right"><ArrowRight size={13} /></button>
        <button type="button" onClick={() => pan(0, 48)} aria-label="Pan up" title="Pan up"><ArrowUp size={13} /></button>
        <button type="button" onClick={() => pan(0, -48)} aria-label="Pan down" title="Pan down"><ArrowDown size={13} /></button>
      </span>
    </div>
  );
}
