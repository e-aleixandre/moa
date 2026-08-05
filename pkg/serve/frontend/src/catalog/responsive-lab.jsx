import { DelegationBlock } from "../components/index.js";
import "./responsive-lab.css";

const WIDTHS = [
  { label: "320", width: 320, note: "narrow phone" },
  { label: "375", width: 375, note: "phone" },
  { label: "768", width: 768, note: "tablet" },
  { label: "1200", width: 1200, note: "desktop" },
];

const RESULT = "No P0/P1/P2 findings. Generation-aware acknowledgements preserve newer completions, and Copy result works in the installed PWA.";

const STATES = [
  {
    id: "running",
    label: "Running",
    settled: false,
    agent: { id: "running", name: "High-thinking review of post-v0.26.0", accent: "mauve", state: "running", action: "Reviewing session lifecycle", time: "18s" },
  },
  {
    id: "complete",
    label: "Completed · collapsed",
    settled: true,
    agent: { id: "complete", name: "High-thinking review of post-v0.26.0", accent: "mauve", state: "done", result: RESULT, time: "2m" },
  },
  {
    id: "failed",
    label: "Failed",
    settled: true,
    agent: { id: "failed", name: "Run integration tests with the long project name", accent: "peach", state: "failed", result: "go test ./... failed: example assertion did not match", time: "1m" },
  },
  {
    id: "cancelled",
    label: "Cancelled",
    settled: true,
    agent: { id: "cancelled", name: "Check current provider limits", accent: "sky", state: "cancelled", time: "7s" },
  },
];

function Specimen({ state, width }) {
  return (
    <article class="responsive-specimen" style={{ width: `${width.width}px` }}>
      <header class="responsive-specimen-head">
        <b>{width.label}px</b><span>{width.note}</span>
      </header>
      <DelegationBlock
        agents={[state.agent]}
        summary={{ total: 1, done: state.agent.state === "done" ? 1 : 0, failed: ["failed", "cancelled"].includes(state.agent.state) ? 1 : 0 }}
        settled={state.settled}
        onOpenAgent={() => {}}
      />
    </article>
  );
}

// ResponsiveLab imports the shipped component and its CSS. Only its fixture
// data and width-constrained frame are synthetic, so this is a real UI drill,
// not a hand-built visual approximation.
export function ResponsiveLab() {
  return (
    <section class="responsive-lab">
      <h2>Responsive states lab</h2>
      <p>Real components under fixed viewport widths. Expand a completed result, copy it, and compare terminal states without leaving the catalog.</p>
      {STATES.map((state) => (
        <section class="responsive-lab-state" key={state.id}>
          <h3>{state.label}</h3>
          <div class="responsive-specimens">
            {WIDTHS.map((width) => <Specimen key={width.label} state={state} width={width} />)}
          </div>
        </section>
      ))}
    </section>
  );
}
