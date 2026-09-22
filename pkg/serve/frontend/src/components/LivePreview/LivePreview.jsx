import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { ArrowLeft, MousePointerClick, X, RotateCw, Smartphone, Tablet, Monitor, Scan, PencilLine, MoreHorizontal, Link } from "lucide-preact";
import { Sheet } from "../Sheet/Sheet.jsx";
import { ActionMenu } from "../ActionMenu/ActionMenu.jsx";
import { AssistantDocument } from "../AssistantDocument/AssistantDocument.jsx";
import { Button } from "../../primitives/index.js";
import { renderMarkdown } from "../../data/util/markdown.js";
import { feedbackMessage, previewReferenceContext } from "../../data/util/preview-reference.js";
import { Composer } from "../../layout/Composer/Composer.jsx";
import { useStore } from "../../hooks/useStore.js";
import { PreviewStream } from "./PreviewStream.jsx";
import { streamEvents, stageState } from "./stream.js";
import { PreviewAddressSetup, PreviewErrorBanner, PreviewLoading, PreviewRecoveryNotice, PreviewURLSetup, displayURL } from "./PreviewSetup.jsx";
import { activatePreview, deactivatePreview, fetchPreviewStatus, portOf, suggestPublicURL, validPublicURL } from "./preview-proxy.js";
import { applyGesture, appToStage, chainPan, panBy, pinchState, stageGesture, wheelFactor, zoomAt, IDENTITY } from "./zoom.js";
import { createScrollChain, setViewIfChanged } from "./scroll-chain.js";
import { applyReport, backTitle, canGoBack, newEpoch, press, resetBack, shouldAutoReturn, INITIAL_BACK } from "./preview-back.js";
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
// dissolves (PreviewStream), a running or waiting run marks the stage's top edge
// in the ledger's state colours, and what changed is shown INSIDE the app by the
// inspector. Idle, the only thing over the app is the "Write to Moa" pill.
//
// The controls are the conversation header's own buttons (ChatHead's borderless
// `zl-desk-act`). On desktop they are that header's row; on the phone the app
// takes the whole screen and they gather in one dock where the thumb is. The
// address is pressed to change it, the way an address bar works.

// Each width is an icon first (the device it stands for); only the active one
// spells its number. The number always lives in the accessible name, so "768"
// is still what a screen reader or a tooltip says. Glyph sizes follow the
// device: phone < tablet < desktop, so the three read apart at a glance.
const WIDTHS = [
  { value: "390", label: "390", icon: Smartphone, size: 14, ariaLabel: "Phone · 390px" },
  { value: "768", label: "768", icon: Tablet, size: 16, ariaLabel: "Tablet · 768px" },
  { value: "1280", label: "1280", icon: Monitor, size: 17, ariaLabel: "Desktop · 1280px" },
  { value: "fit", label: "Fit", icon: Scan, size: 15, ariaLabel: "Fit to pane" },
];

const INSPECTOR_READY_GRACE_MS = 10_000;

const URL_PREFIX = "moa-preview-url:";
const urlKey = (sessionId) => `${URL_PREFIX}${sessionId}`;

// recentPreviewURLs — the addresses this browser already previewed in other
// sessions, read from the per-session keys that already exist. No new storage.
export function recentPreviewURLs(exclude = "", limit = 3) {
  const seen = new Set();
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key || !key.startsWith(URL_PREFIX)) continue;
      const url = localStorage.getItem(key);
      if (url && url !== exclude) seen.add(url);
    }
  } catch {
    return [];
  }
  return [...seen].sort().slice(0, limit);
}

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
  const [reloadNonce, setReloadNonce] = useState(0);
  const [previewPublicURL, setPreviewPublicURL] = useState("");
  const [previewError, setPreviewError] = useState("");
  // setupMode is which question the panel is asking: the app URL, the address
  // the browser reaches the proxy through, or nothing (it is showing the app).
  const [setupMode, setSetupMode] = useState(null);
  const [addressDraft, setAddressDraft] = useState("");
  const [addressError, setAddressError] = useState("");
  const [proxySupported, setProxySupported] = useState(true);
  const [inspectorReady, setInspectorReady] = useState(true);
  const [bridgeLost, setBridgeLost] = useState(false);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const boxRef = useRef(box);
  const [view, setViewState] = useState(IDENTITY);
  const [notes, setNotes] = useState([]);
  const [reading, setReading] = useState(null);
  // Keeps the message being read while the sheet plays its exit.
  const lastReading = useRef(null);
  if (reading) lastReading.current = reading;
  const iframeRef = useRef(null);
  const inspectorReadyRef = useRef(true);
  const bridgeFallbackRef = useRef(null);
  const stageRef = useRef(null);
  const geometry = useRef({ base: 1, w: 0, h: 0, stage: { w: 0, h: 0 } });
  const noteSeq = useRef(0);
  const inspectButtonRef = useRef(null);
  // The view is read by handlers that fire between renders (a scroll answer
  // arriving while the finger is still moving), so it is kept in a ref that is
  // written BEFORE the state: two answers in the same frame must not both clamp
  // from the same stale pan and lose the second one.
  const viewRef = useRef(view);
  const setView = (next) => {
    viewRef.current = next;
    setViewState(next);
  };
  const chain = useRef(null);
  if (!chain.current) chain.current = createScrollChain();
  // Only an inspector that says so answers scroll packets. An older copy is
  // still a working preview: it keeps the fire-and-forget relay and the shell
  // never waits for anything, so nothing can pile up or stall.
  const chainable = useRef(false);
  // The desktop bridge's frame of reference. A mouse or trackpad over the app is
  // inside the iframe's document, so the inspector relays it; the epoch is what
  // tells an answer minted for one document, viewport width or stage size apart
  // from an answer minted for the next. It deliberately does NOT change with the
  // zoom: a trackpad pinch is a burst of wheel events already in flight with the
  // epoch the app last heard, and bumping per step would throw the pinch away.
  const viewEpoch = useRef(0);
  const [spacePan, setSpacePan] = useState(false);
  // Back is the app's own history, not moa's: the shell never traverses
  // anything itself, it only mirrors what the current document says about its
  // own Navigation API and asks that document to go back. See preview-back.js.
  const [back, setBack] = useState(INITIAL_BACK);
  const backRef = useRef(back);
  const setBackState = (next) => {
    backRef.current = next;
    setBack(next);
  };
  const [touchInput, disableTouchInput] = useTouchPreviewInput();
  const isMobile = useStore((s) => s.isMobile);
  // Between "Open" and the frame's load event: what the stage shows instead of
  // a blank rectangle.
  const [frameLoaded, setFrameLoaded] = useState(false);
  const [widthMenuOpen, setWidthMenuOpen] = useState(false);

  const clearBridgeFallback = () => {
    if (bridgeFallbackRef.current) clearTimeout(bridgeFallbackRef.current);
    bridgeFallbackRef.current = null;
  };

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
  //
  // Opening the panel is what turns the proxy on: there is no flag and no
  // restart. What Moa cannot know by itself is the address the browser reaches
  // that listener through, so the first time it asks, proposing the host the
  // user is already on. Afterwards the address is remembered server-side.
  useEffect(() => {
    if (!open) return undefined;
    let cancelled = false;
    const restore = async () => {
      const saved = loadPreviewURL(sessionId);
      setDraftURL(saved);
      setEditingURL(false);
      try {
        const status = await fetchPreviewStatus();
        if (cancelled) return;
        const supported = status.supported !== false;
        setProxySupported(supported);
        setPreviewPublicURL(supported ? status.public_url || "" : "");
        setPreviewError(status.error || "");
        if (!saved) {
          setSetupMode("url");
          return;
        }
        setTargetURL(saved);
        if (!supported) {
          setFrameURL(saved);
          setSetupMode(null);
          return;
        }
        if (!status.public_url) {
          setAddressDraft(suggestPublicURL(window.location, status.suggested_port));
          setSetupMode("address");
          return;
        }
        await startPreview(saved, status.public_url, () => cancelled);
      } catch {
        if (!cancelled) {
          setFrameURL("");
          setPreviewError("Moa could not reach its own preview settings. Reload the page and try again.");
        }
      }
    };
    restore();
    return () => { cancelled = true; };
  }, [open, sessionId]);

  // Closing the panel takes the listener down. Without this the port would stay
  // open (and the app stay reachable through it) for as long as moa serve runs,
  // which is exactly what hot activation exists to avoid.
  useEffect(() => {
    if (!open) return undefined;
    return () => { deactivatePreview().catch(() => {}); };
  }, [open]);

  // Every activation carries a token. A PUT that comes back after the user has
  // changed target, corrected the address or closed the panel is dropped
  // instead of silently mounting a preview nobody asked for any more — the
  // failure mode of a slow response and a second tab.
  const activation = useRef(0);

  const startPreview = async (target, publicURL, cancelled = () => false) => {
    const token = ++activation.current;
    const stale = () => cancelled() || activation.current !== token;
    clearBridgeFallback();
    inspectorReadyRef.current = false;
    setInspectorReady(false);
    setBridgeLost(false);
    setBackState(resetBack(backRef.current));
    setFrameURL("");
    try {
      const result = await activatePreview(fetch, {
        url: target,
        publicURL,
        port: publicURL ? portOf(publicURL) : 0,
        parentOrigin: location.origin,
      });
      if (stale()) return false;
      setPreviewPublicURL(result.public_url || publicURL || "");
      setFrameURL(result.preview_url || result.public_url || "");
      setPreviewError("");
      setSetupMode(null);
      setSelected(null);
      setView(IDENTITY);
      setReloadNonce((n) => n + 1);
      return true;
    } catch (error) {
      if (stale()) return false;
      setFrameURL("");
      setPreviewError(String(error?.message || error) || "The preview proxy could not be started.");
      return false;
    }
  };

  // Measure the stage so the scaled iframe can be laid out in real pixels.
  useLayoutEffect(() => {
    if (!open) return undefined;
    const stageEl = stageRef.current;
    if (!stageEl) return undefined;
    const measure = () => {
      const next = { w: stageEl.clientWidth, h: stageEl.clientHeight };
      const previous = boxRef.current;
      if (previous.w !== next.w || previous.h !== next.h) chain.current.invalidate();
      boxRef.current = next;
      setBox(next);
    };
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
        inspectorReadyRef.current = true;
        setInspectorReady(true);
        setBridgeLost(false);
        clearBridgeFallback();
        chainable.current = data.chain === true;
        // A ready message is emitted once per inspector document. Treat it as
        // a document boundary even when the iframe URL did not change (an
        // in-frame navigation): old packets and a held Space belong to it.
        viewEpoch.current += 1;
        chain.current.invalidate();
        setSpacePan(false);
        post({ type: "moa-view", epoch: viewEpoch.current, zoom: viewRef.current.zoom, zoomed: viewRef.current.zoom !== 1 });
        return;
      }
      if (data.type === "moa-preview-navigation") {
        const current = backRef.current;
        const next = applyReport(current, data);
        setBackState(next);
        if (shouldAutoReturn(current, data)) returnToApp();
        return;
      }
      if (data.type === "moa-scrolled") {
        const request = chain.current.resolve(data.id, data);
        if (!request) return;
        const g = geometry.current;
        const current = viewRef.current;
        setViewIfChanged(setView, current, chainPan(current, request, data, g, g.stage));
        return;
      }
      // ── Desktop, relayed from inside the frame ──────────────────────────
      if (data.epoch !== viewEpoch.current) return;
      if (data.type === "moa-wheel-zoom") {
        if (!Number.isFinite(data.x) || !Number.isFinite(data.y)) return;
        const g = geometry.current;
        const current = viewRef.current;
        const anchor = appToStage(current, g, data.x, data.y);
        const factor = Number.isFinite(data.factor) && data.factor > 0 ? data.factor : wheelFactor(data.deltaY);
        setViewIfChanged(setView, current, zoomAt(current, factor, anchor, g, g.stage));
        return;
      }
      if (data.type === "moa-wheel-pan") {
        if (!Number.isFinite(data.rdx) || !Number.isFinite(data.rdy)) return;
        if (!Number.isFinite(data.dx) || !Number.isFinite(data.dy)) return;
        const g = geometry.current;
        const current = viewRef.current;
        // The same residual math the touch chain uses, at the scale the app
        // measured the wheel at — not the scale the view may have become.
        const request = { dx: data.rdx, dy: data.rdy, scale: (g.base || 1) * (data.zoom || current.zoom || 1) };
        setViewIfChanged(setView, current, chainPan(current, request, data, g, g.stage));
        return;
      }
      if (data.type === "moa-space") {
        setSpacePan(data.down === true && viewRef.current.zoom !== 1);
        return;
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [open, onClose, frameURL]);

  // A new view, a new document or a closed panel: whatever the app was still
  // going to answer about belongs to a gesture that no longer exists.
  useEffect(() => {
    chain.current.invalidate();
    return () => chain.current.invalidate();
  }, [frameURL, reloadNonce, width, open]);

  // The desktop epoch: bumped when the coordinates the app reports stop meaning
  // what they meant — a new document, another viewport width, a resized stage.
  // Not when the zoom changes; that is what keeps a pinch burst continuous.
  useEffect(() => {
    viewEpoch.current += 1;
    setSpacePan(false);
  }, [frameURL, reloadNonce, width, box.w, box.h, open]);

  // What the app has to know to relay anything at all: the epoch to stamp and
  // whether there is a zoom to scroll against. Sent on every zoom change, which
  // is a message per pinch step and nothing else.
  useEffect(() => {
    if (!open || !frameURL) return;
    post({ type: "moa-view", epoch: viewEpoch.current, zoom: view.zoom, zoomed: view.zoom !== 1 });
  }, [open, frameURL, reloadNonce, width, box.w, box.h, view.zoom]);

  // Space is only relayed by the app while the pointer is inside it. With the
  // focus on moa's own chrome the key never reaches the frame, so the shell
  // watches for it too — under exactly the same rule: never where text is typed.
  useEffect(() => {
    if (!open || !frameURL) return undefined;
    const editing = () => {
      const el = document.activeElement;
      if (!el || el.nodeType !== 1) return false;
      const tag = (el.tagName || "").toLowerCase();
      return tag === "input" || tag === "textarea" || tag === "select" || el.isContentEditable === true;
    };
    const onKeyDown = (e) => {
      if (e.key !== " " || e.repeat || viewRef.current.zoom === 1 || editing()) return;
      e.preventDefault();
      setSpacePan(true);
    };
    const onKeyUp = (e) => {
      if (e.key === " ") setSpacePan(false);
    };
    // A Space held while the window loses focus never gets its keyup.
    const release = () => setSpacePan(false);
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", release);
    document.addEventListener("visibilitychange", release);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("keyup", onKeyUp);
      window.removeEventListener("blur", release);
      document.removeEventListener("visibilitychange", release);
    };
  }, [open, frameURL]);

  // Reset before iframe load can finish its handshake; a passive effect could
  // otherwise invalidate the freshly acknowledged epoch without another hello.
  useLayoutEffect(() => {
    setBackState(resetBack(backRef.current));
  }, [frameURL, reloadNonce, open]);

  useEffect(() => {
    setFrameLoaded(false);
    if (!frameURL) return undefined;
    inspectorReadyRef.current = false;
    setInspectorReady(false);
    // A new document brings its own copy of the inspector, which may be older
    // than this one: it has to say again that it answers scroll packets.
    chainable.current = false;
    return () => clearBridgeFallback();
  }, [frameURL, reloadNonce]);

  // Closing the panel drops the selection but keeps the URL (persisted).
  useEffect(() => {
    if (open) return;
    clearBridgeFallback();
    inspectorReadyRef.current = false;
    setBridgeLost(false);
    setSelected(null);
    setInspect(false);
    setView(IDENTITY);
    setNotes([]);
    setReading(null);
  }, [open]);

  // The question is the field. It can mount after the sheet has already put
  // focus on the first control it found (close), so focus follows the field's
  // own mount, one frame later — after the sheet's effect, not before it.
  const setupField = useRef(null);
  const setupFieldRef = useRef((el) => {
    const mounted = el && el !== setupField.current;
    setupField.current = el;
    if (mounted) requestAnimationFrame(() => { if (el.isConnected) el.focus({ preventScroll: true }); });
  });

  if (!open) return null;

  const commitURL = async (raw = draftURL) => {
    const next = normalizeURL(raw);
    setDraftURL(next);
    setEditingURL(false);
    if (!next) return;
    setTargetURL(next);
    savePreviewURL(sessionId, next);
    if (!proxySupported) {
      clearBridgeFallback();
      inspectorReadyRef.current = false;
      setInspectorReady(false);
      setBridgeLost(false);
      setBackState(resetBack(backRef.current));
      setFrameURL(next);
      setSetupMode(null);
      setSelected(null);
      setView(IDENTITY);
      setReloadNonce((n) => n + 1);
      return;
    }
    if (!previewPublicURL) {
      // First run: the app URL is known, the address of the proxy is not. Ask
      // for it now, with a proposal, rather than opening a listener the browser
      // may have no way to reach.
      try {
        const status = await fetchPreviewStatus();
        setAddressDraft(addressDraft || suggestPublicURL(window.location, status.suggested_port));
      } catch {
        setAddressDraft(addressDraft || suggestPublicURL(window.location, 0));
      }
      setSetupMode("address");
      return;
    }
    // A target switch gets a new capability and a new iframe document. Never
    // leave the previous target running while the proxy is being repointed.
    await startPreview(next, previewPublicURL);
  };

  // commitAddress is the one-time confirmation of the address the browser uses
  // to reach the proxy. Moa binds the port it names, so a busy port or an
  // unusable address comes back here as an error the user can correct.
  const commitAddress = async () => {
    const address = addressDraft.trim().replace(/\/+$/, "");
    if (!validPublicURL(address)) {
      setAddressError("Enter a full address, including http:// or https:// and the port.");
      return;
    }
    setAddressError("");
    const started = await startPreview(targetURL, address);
    if (!started) setSetupMode("address");
  };

  // Moving the view by any other means ends the chain: a scroll answer minted
  // for the old pan must not be applied on top of the new one.
  const resetChainAnd = (apply) => (next) => {
    chain.current.invalidate();
    apply(next);
  };

  const reload = () => {
    clearBridgeFallback();
    inspectorReadyRef.current = false;
    setInspectorReady(false);
    setBridgeLost(false);
    setPreviewError("");
    setBackState(resetBack(backRef.current));
    setSelected(null);
    setReloadNonce((n) => n + 1);
  };

  // The iframe's src is the proxy URL configured by startPreview, not whatever
  // external page the frame last reached. Re-keying it is therefore a parent-
  // owned return to the configured app, never a history simulation.
  const returnToApp = () => reload();

  const toggleInspect = () => {
    const next = !inspect;
    setInspect(next);
    postInspect(next);
    if (!next) setSelected(null);
  };

  const onFrameLoad = () => {
    setFrameLoaded(true);
    // A navigation inside the app (or a reload) drops the inspector state:
    // re-arm it so the toggle keeps meaning what it says.
    chain.current.invalidate();
    chainable.current = false;
    inspectorReadyRef.current = false;
    setInspectorReady(false);
    setBridgeLost(false);
    clearBridgeFallback();
    // A normal injected document answers moa-ready immediately after this
    // load. Cold-start compilation happens before iframe load, so this is a
    // post-load grace period rather than an app-start timeout. Ten seconds
    // tolerates a busy local runtime without leaving a genuinely uninspectable
    // page unexplained for too long. The iframe load event exposes no response
    // headers; probing the proxy with fetch would duplicate a potentially
    // stateful navigation, so absence of the bridge still has to be inferred.
    bridgeFallbackRef.current = setTimeout(() => {
      if (!inspectorReadyRef.current) setBridgeLost(true);
    }, INSPECTOR_READY_GRACE_MS);
    if (inspect) postInspect(true);
    // A new document: Back goes back to disabled AND the epoch moves, so a
    // report still in flight from the document being replaced cannot re-enable
    // it. Only this document's answer to this hello can.
    const next = newEpoch(backRef.current);
    setBackState(next);
    post({ type: "moa-hello", navigationEpoch: next.epoch });
  };

  const goBack = () => {
    const { state, command } = press(backRef.current);
    if (!command) return;
    setBackState(state);
    if (command.type === "moa-preview-return") {
      returnToApp();
      return;
    }
    post(command);
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

  const showSetup = setupMode === "address" ? "address" : (!targetURL || editingURL || setupMode === "url") ? "url" : null;
  // The stage has an app in it (or one on its way). Until then the only
  // controls that exist are the ones that can do something: close.
  const showError = !!previewError && !showSetup;
  const hasApp = !!frameURL && !showSetup && !showError;
  const loading = !showSetup && !showError && !!targetURL && (!frameURL || !frameLoaded);
  const showRecovery = bridgeLost && hasApp;
  const recent = showSetup === "url" ? recentPreviewURLs(targetURL) : [];
  const changeURL = () => {
    setDraftURL(targetURL);
    setEditingURL(true);
  };
  const pickWidth = (next) => {
    chain.current.invalidate();
    setWidth(next);
    setView(IDENTITY);
  };

  const preview = (
    <>
      {/* One task, two densities. Desktop: the conversation header's own row,
          always there. Phone: a head only until the app loads; then the app
          takes the whole screen and every control lives in the dock, where
          the thumb is. */}
      {isMobile ? (
        !hasApp && (
          <PreviewHead
            address={targetURL && !showSetup ? displayURL(targetURL) : ""}
            onClose={onClose}
          />
        )
      ) : (
        <PreviewRail
          address={targetURL && !showSetup ? displayURL(targetURL) : ""}
          hasApp={hasApp}
          back={back}
          onBack={goBack}
          onReload={reload}
          onChangeURL={changeURL}
          width={width}
          onWidth={pickWidth}
          inspect={inspect}
          inspectButtonRef={inspectButtonRef}
          onInspect={toggleInspect}
          onClose={onClose}
        />
      )}

      {/* The live edge: the ledger's running mark (a steady blue baseline and a
          sweep over it) along the stage's top edge while the agent works, a
          still amber line while the run waits on the user, nothing when idle.
          State colours only: which tool is running is what the cards say. */}
      <div
        class={`live-preview-stage is-${stage.mode}${composerOpen ? " has-composer" : " has-composer-handle"}${hasApp && frameLoaded ? " is-loaded" : ""}`}
        onPointerDownCapture={(event) => {
          if (selected && !event.target.closest(".live-preview-composer")) setSelected(null);
        }}
      >
        <span class="live-preview-edge" aria-hidden="true" />
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

        {loading && <PreviewLoading url={targetURL} />}

        {showSetup === "url" && (
          <PreviewURLSetup
            value={draftURL}
            onInput={setDraftURL}
            onCommit={commitURL}
            onCancel={() => setEditingURL(false)}
            canCancel={!!targetURL}
            recent={recent}
            inputRef={setupFieldRef.current}
            onPick={(url) => {
              setDraftURL(url);
              commitURL(url);
            }}
          />
        )}

        {showSetup === "address" && (
          <PreviewAddressSetup
            value={addressDraft}
            onInput={setAddressDraft}
            onCommit={commitAddress}
            inputRef={setupFieldRef.current}
            onBack={() => { setSetupMode("url"); setDraftURL(targetURL); }}
            error={addressError || previewError}
          />
        )}

        {showError && (
          <PreviewErrorBanner
            message={previewError}
            onChangeAddress={() => {
              setAddressDraft(previewPublicURL || addressDraft);
              setSetupMode("address");
            }}
          />
        )}

        {showRecovery && (
          <PreviewRecoveryNotice
            message="Inspect isn’t available on this page."
            onReturn={returnToApp}
          />
        )}

        {/* Siblings of the scroller, not children: what floats over the app must
            not slide away when the app under it scrolls. */}
        {hasApp && (
          <>
            {touchInput && (
              <TouchZoomOverlay
                geometry={geometry}
                viewRef={viewRef}
                onView={setView}
                inspect={inspect}
                post={post}
                onMouse={disableTouchInput}
                chain={chain.current}
                chainable={chainable}
              />
            )}
            {/* Space is held: for as long as that lasts, and no longer, the
                drag belongs to the frame. Released, there is nothing over the
                app again and clicking and selecting text are its own. */}
            {!touchInput && spacePan && zoomed && (
              <DesktopPanLayer geometry={geometry} viewRef={viewRef} onView={resetChainAnd(setView)} />
            )}
          </>
        )}
        {composerOpen ? (
          <PreviewComposer
            sessionId={sessionId}
            session={session}
            selected={selected}
            onClose={() => {
              setSelected(null);
              setComposerOpen(false);
            }}
            onSent={() => {
              setSelected(null);
              setComposerOpen(false);
              note("sent", "Sent");
            }}
          />
        ) : hasApp && !loading && inspect ? (
          // Inspect is a mode over the app, so the mode says what it wants and
          // how to leave it, where the thumb already is.
          <div class="live-preview-composer-handle is-hint" role="status">
            <span class="live-preview-composer-handle-pill">
              <MousePointerClick size={14} aria-hidden="true" />
              {touchInput ? "Tap an element" : "Click an element"}
              <button type="button" class="live-preview-hint-done" onClick={toggleInspect}>
                Done
              </button>
            </span>
          </div>
        ) : hasApp && isMobile ? (
          <PreviewDock
            address={displayURL(targetURL)}
            loading={loading}
            back={back}
            onBack={goBack}
            onReload={reload}
            onChangeURL={changeURL}
            width={width}
            onWidth={pickWidth}
            menuOpen={widthMenuOpen}
            onMenu={setWidthMenuOpen}
            inspectButtonRef={inspectButtonRef}
            onInspect={toggleInspect}
            onWrite={() => setComposerOpen(true)}
            onClose={onClose}
          />
        ) : hasApp && !loading ? (
          <button
            type="button"
            class="live-preview-composer-handle"
            onClick={() => setComposerOpen(true)}
            title="Open the message composer"
          >
            <span class="live-preview-composer-handle-pill">
              <PencilLine size={14} aria-hidden="true" />
              Write to Moa
            </span>
          </button>
        ) : null}
        {!showSetup && (
          <PreviewStream
            events={events}
            notes={notes}
            onOpenText={setReading}
            onGoToChat={onClose}
          />
        )}
      </div>

      {/* Rendered unconditionally so the sheet can animate out; the text it is
          showing is held for the length of the exit, or it leaves blank. */}
      <Sheet open={!!reading} onClose={() => setReading(null)} title="Message" class="lp-msg-sheet">
          <AssistantDocument html={renderMarkdown(reading || lastReading.current || "")} />
          <div class="lp-msg-actions">
            <Button variant="solid" size="sm" onClick={onClose}>
              Go to chat
            </Button>
          </div>
        </Sheet>
    </>
  );
  return inline
    ? <section class="live-preview-inline" aria-label="Live preview">{preview}</section>
    : <Sheet open={open} onClose={onClose} ariaLabel="Live preview" class="live-preview-sheet" page>{preview}</Sheet>;
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

// PreviewRail — the desktop row, in the conversation header's grammar. Three
// zones that mean three things: the address and what acts on it (back, reload,
// change), how it is shown (widths, inspect), and the panel itself (close),
// set apart by hairlines so the control that ends the preview never reads as
// one that changes it. Before there is an app only the title and close exist.
function PreviewRail({
  address, hasApp, back, onBack, onReload, onChangeURL,
  width, onWidth, inspect, inspectButtonRef, onInspect, onClose,
}) {
  return (
    <div class="live-preview-bar">
      {hasApp && (
        <button
          type="button"
          class="zl-desk-act live-preview-act"
          onClick={onBack}
          disabled={!canGoBack(back)}
          aria-label="Back in preview"
          title={backTitle(back)}
        >
          <ArrowLeft size={16} aria-hidden="true" />
        </button>
      )}
      {address && hasApp ? (
        <button
          type="button"
          class="live-preview-address"
          onClick={onChangeURL}
          aria-label={`Change URL, now ${address}`}
          title="Change URL"
        >
          {address}
        </button>
      ) : address ? (
        <span class="live-preview-title is-address">{address}</span>
      ) : (
        <span class="live-preview-title">Live preview</span>
      )}
      {hasApp && (
        <button type="button" class="zl-desk-act live-preview-act" onClick={onReload} aria-label="Reload" title="Reload">
          <RotateCw size={15} aria-hidden="true" />
        </button>
      )}
      <span class="live-preview-bar-spring" />
      {hasApp && (
        <div class="live-preview-widths" role="radiogroup" aria-label="Viewport width">
          {WIDTHS.map((w) => {
            const Icon = w.icon;
            const on = w.value === width;
            return (
              <button
                key={w.value}
                type="button"
                role="radio"
                aria-checked={on}
                class={`zl-desk-act live-preview-act live-preview-width${on ? " is-on" : ""}`}
                onClick={() => onWidth(w.value)}
                aria-label={w.ariaLabel}
                title={w.ariaLabel}
              >
                <Icon size={w.size} aria-hidden="true" />
                {on && <span class="live-preview-width-label" aria-hidden="true">{w.label}</span>}
              </button>
            );
          })}
        </div>
      )}
      {hasApp && <span class="live-preview-bar-sep" aria-hidden="true" />}
      {hasApp && (
        <button
          type="button"
          class={`zl-desk-act live-preview-act live-preview-inspect${inspect ? " is-on" : ""}`}
          ref={inspectButtonRef}
          onClick={onInspect}
          aria-pressed={inspect}
          aria-label="Inspect"
          title="Inspect — point at an element in the app"
        >
          <MousePointerClick size={16} aria-hidden="true" />
          <span class="live-preview-action-label">Inspect</span>
        </button>
      )}
      {hasApp && <span class="live-preview-bar-sep" aria-hidden="true" />}
      <button type="button" class="zl-desk-act live-preview-act" onClick={onClose} aria-label="Close preview" title="Close preview">
        <X size={16} aria-hidden="true" />
      </button>
    </div>
  );
}

// PreviewHead — the phone's row before there is an app: its name (or the
// address that failed) and close. Nothing that could not act is drawn.
function PreviewHead({ address, onClose }) {
  return (
    <div class="live-preview-bar is-compact">
      {address
        ? <span class="live-preview-title is-address">{address}</span>
        : <span class="live-preview-title">Live preview</span>}
      <span class="live-preview-bar-spring" />
      <button type="button" class="zl-desk-act live-preview-act" onClick={onClose} aria-label="Close preview" title="Close preview">
        <X size={16} aria-hidden="true" />
      </button>
    </div>
  );
}

// PreviewDock — the phone's controls once an app is loaded, in one floating
// piece where the thumb is: close, back, a menu for the rare actions (the
// address, reload, the four widths), Inspect and "Write to Moa". Every target
// is 44px.
function PreviewDock({
  address, loading, back, onBack, onReload, onChangeURL,
  width, onWidth, menuOpen, onMenu, inspectButtonRef, onInspect, onWrite, onClose,
}) {
  return (
    <div class="live-preview-dock" role="toolbar" aria-label="Preview controls">
      <button type="button" class="zl-desk-act live-preview-act" onClick={onClose} aria-label="Close preview" title="Close preview">
        <X size={16} aria-hidden="true" />
      </button>
      <button
        type="button"
        class="zl-desk-act live-preview-act"
        onClick={onBack}
        disabled={!canGoBack(back)}
        aria-label="Back in preview"
        title={backTitle(back)}
      >
        <ArrowLeft size={16} aria-hidden="true" />
      </button>
      <ActionMenu
        open={menuOpen}
        onOpenChange={onMenu}
        icon={MoreHorizontal}
        label="Preview options"
        triggerClass="zl-desk-act live-preview-act"
        triggerSize={18}
        placement="up"
        actions={[
          { id: "url", icon: Link, label: address, onClick: onChangeURL },
          { id: "reload", icon: RotateCw, label: "Reload", onClick: onReload },
          ...WIDTHS.map((w) => ({
            id: w.value,
            icon: w.icon,
            label: w.ariaLabel,
            active: w.value === width,
            onClick: () => onWidth(w.value),
          })),
        ]}
      />
      <span class="live-preview-bar-sep" aria-hidden="true" />
      <button
        type="button"
        class="zl-desk-act live-preview-act"
        ref={inspectButtonRef}
        onClick={onInspect}
        disabled={loading}
        aria-pressed={false}
        aria-label="Inspect"
        title="Inspect — point at an element in the app"
      >
        <MousePointerClick size={16} aria-hidden="true" />
      </button>
      <button type="button" class="live-preview-dock-write" onClick={onWrite} disabled={loading}>
        <PencilLine size={14} aria-hidden="true" />
        Write to Moa
      </button>
    </div>
  );
}

// PreviewComposer — the conversation's own Composer, over the app. Its head
// says what the message will point at (or that it points at nothing) and holds
// the way out, inside the panel rather than floating above it.
function PreviewComposer({ sessionId, session, selected, onClose, onSent }) {
  const context = selected && previewReferenceContext(selected.ancestors);
  const label = selected?.text || selected?.tag || "Element";

  return (
    <section class="live-preview-composer" role="dialog" aria-label="Message the agent">
      <div class="live-preview-composer-head">
        {selected ? (
          <span class="live-preview-reference" title={`${label}${context ? ` · ${context}` : ""}`}>
            <MousePointerClick size={13} aria-hidden="true" />
            <span class="live-preview-reference-label">{label}</span>
            {context && <span class="live-preview-reference-context">{context}</span>}
          </span>
        ) : (
          <span class="live-preview-composer-title" />
        )}
        <button type="button" class="zl-desk-act live-preview-act live-preview-composer-close" onClick={onClose} aria-label="Close message composer">
          <X size={16} aria-hidden="true" />
        </button>
      </div>
      <Composer
        sessionId={sessionId}
        session={session}
        compact
        shortPlaceholder
        transformMessage={(text) => feedbackMessage(text, selected)}
        onSent={onSent}
      />
    </section>
  );
}

// On touch-only devices this layer owns the first contact before WebKit chooses
// a touch-active document. Fine pointers never get this layer: their iframe is
// a normal browser surface for click, hover, wheel and keyboard input.
function TouchZoomOverlay({ geometry, viewRef, onView, inspect, post, onMouse, chain, chainable }) {
  const layerRef = useRef(null);
  const inspectRef = useRef(inspect);
  const postRef = useRef(post);
  const onViewRef = useRef(onView);
  const onMouseRef = useRef(onMouse);
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
    // Whether the app has already been told which scroller this gesture owns.
    // Chained packets pick a target once and keep it, root included, so content
    // moving under the finger cannot hand the rest of the drag to something else.
    let relayTargeted = false;
    let lastTap = null;
    const point = (touch) => {
      const g = geometry.current;
      const rect = el.getBoundingClientRect();
      const scale = g.base * viewRef.current.zoom;
      return { x: (touch.clientX - rect.left - viewRef.current.x) / scale, y: (touch.clientY - rect.top - viewRef.current.y) / scale };
    };
    const pinchPoints = (a, b) => {
      const rect = el.getBoundingClientRect();
      return pinchState(a, b, rect);
    };
    const flushRelay = () => {
      relayRAF = 0;
      if (!relayMove) return;
      // Zoomed, the app gets first refusal: the packet carries an identity and
      // what the app could not scroll comes back and moves the pan instead.
      // At zoom 1 there is no pan to give it to, so the relay stays as it was.
      const packet = { type: "moa-scroll", ...relayMove };
      if (relayMove.scale !== undefined && chainable.current) {
        packet.id = chain.request({ dx: relayMove.dx, dy: relayMove.dy, scale: relayMove.scale });
        packet.reset = !relayTargeted;
        relayTargeted = true;
      }
      delete packet.scale;
      postRef.current(packet);
      relayMove = null;
    };
    // A second finger ends the one-finger chain outright: the packet still
    // queued for this frame belongs to a drag the pinch has just replaced, and
    // its answer must not arrive on top of the pinch's own math.
    const dropRelay = () => {
      relay = null;
      relayMove = null;
      if (relayRAF) cancelAnimationFrame(relayRAF);
      relayRAF = 0;
      chain.invalidate();
    };
    const onTouchStart = (e) => {
      e.preventDefault();
      if (e.touches.length >= 2) {
        dropRelay();
        start = viewRef.current;
        pinch = pinchPoints(e.touches[0], e.touches[1]);
      } else if (e.touches.length === 1) {
        chain.invalidate();
        const t = e.touches[0];
        relayTargeted = false;
        relay = { x: t.clientX, y: t.clientY, startX: t.clientX, startY: t.clientY, started: performance.now(), moved: false, point: point(t) };
      }
    };
    const onTouchMove = (e) => {
      e.preventDefault();
      const g = geometry.current;
      if (e.touches.length >= 2) {
        dropRelay();
        if (!pinch) { start = viewRef.current; pinch = pinchPoints(e.touches[0], e.touches[1]); }
        onViewRef.current(applyGesture(start, stageGesture(start, g, pinch, pinchPoints(e.touches[0], e.touches[1])), g, g.stage));
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
          // The scale a packet is measured at is the scale its residual has to
          // be read back at, whatever the view has become by then.
          relayMove.scale = viewRef.current.zoom === 1 ? undefined : scale;
          if (!relayRAF) relayRAF = requestAnimationFrame(flushRelay);
        }
      }
    };
    const onTouchEnd = (e) => {
      e.preventDefault();
      if (e.type === "touchcancel") {
        dropRelay();
        pinch = null;
        return;
      }
      if (e.touches.length < 2) pinch = null;
      if (e.touches.length === 0 && relay) {
        const duration = performance.now() - relay.started;
        if (e.type === "touchend" && !relay.moved && duration < 300 && e.changedTouches.length) {
          const p = point(e.changedTouches[0]);
          if (lastTap && performance.now() - lastTap.at < 300 && Math.hypot(p.x - lastTap.x, p.y - lastTap.y) < 10) {
            chain.invalidate();
            onViewRef.current(IDENTITY);
            lastTap = null;
          } else {
            postRef.current({ type: inspectRef.current ? "moa-inspect-tap" : "moa-tap", x: p.x, y: p.y });
            lastTap = { ...p, at: performance.now() };
          }
        }
        relay = null;
      }
      // A packet the finger produced on its way up is still that movement: it
      // is flushed, and its answer is allowed to finish the gesture it belongs
      // to. Only a pinch, a reset or a new frame invalidates it.
      if (e.touches.length === 0 && relayRAF) {
        cancelAnimationFrame(relayRAF);
        flushRelay();
      }
    };
    const onWheel = (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      chain.invalidate();
      const g = geometry.current;
      const rect = el.getBoundingClientRect();
      onViewRef.current(zoomAt(viewRef.current, wheelFactor(e.deltaY), { x: e.clientX - rect.left, y: e.clientY - rect.top }, g, g.stage));
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
      chain.invalidate();
    };
  }, [geometry, chain, chainable, viewRef]);

  return <div class="live-preview-zoomlayer" ref={layerRef} role="presentation" />;
}

// DesktopPanLayer — the ONLY thing the shell ever puts over the app on a fine
// pointer, and only while Space is held over a zoomed preview. It is the one
// gesture that cannot be relayed: a drag has to be tracked across the whole
// stage, and half of it happens outside the frame. Mouse and trackpad reach it
// the same way, through pointer events. Unmounted the instant Space comes up,
// so a click, a hover or a text selection never meets it.
function DesktopPanLayer({ geometry, viewRef, onView }) {
  const layerRef = useRef(null);
  const onViewRef = useRef(onView);
  onViewRef.current = onView;

  useEffect(() => {
    const el = layerRef.current;
    if (!el) return undefined;
    let dragging = null;
    const onDown = (e) => {
      dragging = { x: e.clientX, y: e.clientY, id: e.pointerId };
      el.setPointerCapture?.(e.pointerId);
    };
    const onMove = (e) => {
      if (!dragging || e.pointerId !== dragging.id) return;
      const dx = e.clientX - dragging.x;
      const dy = e.clientY - dragging.y;
      dragging.x = e.clientX;
      dragging.y = e.clientY;
      if (!dx && !dy) return;
      const g = geometry.current;
      onViewRef.current(panBy(viewRef.current, dx, dy, g, g.stage));
    };
    // Capture lost, button released, gesture cancelled: all the same end.
    const onUp = (e) => {
      if (dragging && e.pointerId === dragging.id) el.releasePointerCapture?.(e.pointerId);
      dragging = null;
    };
    el.addEventListener("pointerdown", onDown);
    el.addEventListener("pointermove", onMove);
    el.addEventListener("pointerup", onUp);
    el.addEventListener("pointercancel", onUp);
    el.addEventListener("lostpointercapture", onUp);
    return () => {
      el.removeEventListener("pointerdown", onDown);
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onUp);
      el.removeEventListener("lostpointercapture", onUp);
    };
  }, [geometry, viewRef]);

  return <div class="live-preview-panlayer" ref={layerRef} role="presentation" />;
}
