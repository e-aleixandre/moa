import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { ArrowLeft, Folder } from "lucide-preact";
import { projectLabel, shortPath, tildify, expandHome, basename } from "../../../data/util/format.js";

// NewSessionView — the drawer's second screen: choose where the session runs,
// then create it. It replaces the drawer's list in place rather than opening
// the command palette, which on a phone is a whole other chassis carrying its
// own session list — two lists for one job, and a back button that landed in
// the wrong one.
//
// It offers both ways in, same as the palette's create step: the projects you
// already work in (tap one, done) and a typed path with folder completion off
// the server (/api/fs/complete), so a folder you have never opened is still
// reachable from the phone.
//
// The model is NOT chosen here. A new session starts on the default and the
// status line changes it in one tap — putting eleven chips under this list only
// buried the thing the screen is actually for.
export function NewSessionView({ projects = [], onBack, onCreate }) {
  // The typed path. Empty (or non-path text) means "filtering the projects
  // above"; anything starting with / or ~ switches this into the explorer.
  const [text, setText] = useState("");
  const [caps, setCaps] = useState({});
  const [exploreDir, setExploreDir] = useState("");
  const [dirFilter, setDirFilter] = useState("");
  const [entries, setEntries] = useState([]);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [creating, setCreating] = useState(false);
  const inputRef = useRef(null);

  const homeDir = caps.homeDir || "";
  const serverCwd = caps.workspaceRoot || "";
  const isPath = text.startsWith("/") || text.startsWith("~");

  useEffect(() => {
    let cancelled = false;
    fetch("/api/capabilities", { headers: { "X-Moa-Request": "1" } })
      .then((r) => r.json())
      .then((c) => {
        if (cancelled) return;
        setCaps(c || {});
        setExploreDir(c.workspaceRoot || c.homeDir || "/");
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, []);

  // Folder completion for the explorer, debounced and cancellable so a stale
  // response can't clobber a newer directory (same contract as the palette's).
  useEffect(() => {
    if (!exploreDir) return undefined;
    let cancelled = false;
    setLoading(true);
    setFailed(false);
    const timer = setTimeout(() => {
      fetch("/api/fs/complete?path=" + encodeURIComponent(exploreDir + "/"), {
        headers: { "X-Moa-Request": "1" },
      })
        .then((r) => r.json())
        .then((data) => {
          if (cancelled) return;
          setEntries(Array.isArray(data.entries) ? data.entries : []);
          setLoading(false);
        })
        .catch(() => {
          if (cancelled) return;
          setEntries([]);
          setLoading(false);
          setFailed(true);
        });
    }, 130);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [exploreDir]);

  // Typing splits into "the folder we are listing" and "the prefix we filter
  // its children by", so /a/b/pro lists /a/b filtered to names starting "pro".
  const onInput = (v) => {
    setText(v);
    if (!(v.startsWith("/") || v.startsWith("~"))) { setDirFilter(""); return; }
    const expanded = expandHome(v.replace(/\/+$/, ""), homeDir) || "/";
    if (v.endsWith("/")) {
      setExploreDir(expanded);
      setDirFilter("");
    } else {
      const cut = expanded.lastIndexOf("/");
      setExploreDir(cut <= 0 ? "/" : expanded.slice(0, cut));
      setDirFilter(expanded.slice(cut + 1));
    }
  };

  const enterDir = (path) => {
    setExploreDir(path);
    setDirFilter("");
    setText(tildify(path, homeDir));
    inputRef.current?.focus();
  };

  const shownProjects = useMemo(() => {
    if (isPath) return [];
    const f = text.trim().toLowerCase();
    return projects.filter(
      (p) => !f || projectLabel(p.cwd).toLowerCase().includes(f) || p.cwd.toLowerCase().includes(f),
    );
  }, [projects, text, isPath]);

  const shownDirs = useMemo(() => {
    const f = dirFilter.toLowerCase();
    return entries.filter((n) => !f || n.toLowerCase().startsWith(f));
  }, [entries, dirFilter]);

  // Where "Create here" lands: the folder the explorer is showing, falling back
  // to the server's own workspace before anything has been typed.
  const target = exploreDir || serverCwd;

  const create = (cwd) => {
    if (creating || !cwd) return;
    setCreating(true);
    onCreate?.(cwd);
  };

  return (
    <div class="sdrawer-new-view">
      <div class="sdrawer-head">
        <button type="button" class="sdrawer-back" aria-label="Back to sessions" onClick={onBack}>
          <ArrowLeft size={15} aria-hidden="true" />
        </button>
        <h2>New session</h2>
      </div>

      <div class="sdrawer-search">
        <input
          ref={inputRef}
          type="text"
          aria-label="Project or path"
          placeholder="Filter, or type a path…"
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          spellcheck={false}
          value={text}
          onInput={(e) => onInput(e.target.value)}
        />
      </div>

      <div class="sdrawer-list">
        {!isPath && (
          <>
            <span class="sdrawer-group">Project</span>
            {shownProjects.length === 0 && (
              <span class="sdrawer-note">
                {projects.length === 0 ? "No projects yet" : "No project matches"}
              </span>
            )}
            {shownProjects.map((p) => (
              <button
                key={p.cwd}
                type="button"
                class="sdnew-project"
                disabled={creating}
                onClick={() => create(p.cwd)}
              >
                <span class="sdnew-name">{projectLabel(p.cwd)}</span>
                <span class="sdnew-path">{shortPath(p.cwd)}</span>
              </button>
            ))}
          </>
        )}

        <span class="sdrawer-group">Browse · {tildify(exploreDir, homeDir) || "…"}</span>
        {failed && <span class="sdrawer-note">Could not read this folder</span>}
        {!failed && loading && entries.length === 0 && <span class="sdrawer-note">Loading…</span>}
        {!failed && !loading && shownDirs.length === 0 && (
          <span class="sdrawer-note">
            {dirFilter ? `No folder starts with “${dirFilter}”` : "No subfolders"}
          </span>
        )}
        {shownDirs.map((name) => {
          const full = exploreDir === "/" ? "/" + name : exploreDir + "/" + name;
          return (
            <button key={full} type="button" class="sdnew-dir" onClick={() => enterDir(full)}>
              <Folder size={14} aria-hidden="true" />
              <span>{name}</span>
            </button>
          );
        })}
      </div>

      <div class="sdrawer-foot">
        <button
          type="button"
          class="sdnew-create"
          disabled={creating || !target}
          onClick={() => create(target)}
        >
          {creating ? "Creating…" : `Create in ${basename(target)}`}
        </button>
      </div>
    </div>
  );
}
