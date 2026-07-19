import { Rewind, Settings, History, Trash2, Copy, X } from "lucide-preact";
import {
  StateDot,
  Chip,
  Button,
  IconButton,
  Kbd,
  ThinkingMeter,
} from "../primitives/index.js";
import "./primitives-gallery.css";

const STATES = ["idle", "running", "permission", "error", "saved"];
const TONES = ["neutral", "idle", "running", "permission", "error"];
const METER_LEVELS = ["off", "low", "medium", "high", "xhigh"];
const METER_VARIANTS = ["bars", "dial", "glyph"];

function StateDotRow() {
  return (
    <div class="gallery-row">
      {STATES.map((state) => (
        <div class="gallery-item" key={state}>
          <StateDot state={state} />
          <span class="gallery-item-label">{state}</span>
        </div>
      ))}
    </div>
  );
}

function ChipRow() {
  return (
    <div class="gallery-row tight">
      {TONES.map((tone) => (
        <Chip key={tone} tone={tone === "neutral" ? undefined : tone}>
          <StateDot state={tone === "neutral" ? "saved" : tone} size={6} />
          {tone}
        </Chip>
      ))}
      <Chip mono>v0.10.2</Chip>
      <Chip onClick={() => {}}>clicable</Chip>
    </div>
  );
}

function ButtonRow() {
  return (
    <>
      <div class="gallery-row tight">
        <Button variant="solid" size="md">Solid md</Button>
        <Button variant="ghost" size="md">Ghost md</Button>
        <Button variant="danger" size="md">Danger md</Button>
      </div>
      <div class="gallery-row tight">
        <Button variant="solid" size="sm">Solid sm</Button>
        <Button variant="ghost" size="sm">Ghost sm</Button>
        <Button variant="danger" size="sm">Danger sm</Button>
      </div>
      <div class="gallery-row tight">
        <Button variant="solid" disabled>Solid disabled</Button>
        <Button variant="ghost" disabled>Ghost disabled</Button>
        <Button variant="danger" disabled>Danger disabled</Button>
      </div>
    </>
  );
}

function IconButtonRow() {
  return (
    <div class="gallery-row tight">
      <IconButton label="Rewind"><Rewind size={15} /></IconButton>
      <IconButton label="Settings"><Settings size={15} /></IconButton>
      <IconButton label="History"><History size={15} /></IconButton>
      <IconButton label="Copy"><Copy size={15} /></IconButton>
      <IconButton label="Delete"><Trash2 size={15} /></IconButton>
      <IconButton label="Close" disabled><X size={15} /></IconButton>
    </div>
  );
}

function KbdRow() {
  return (
    <div class="gallery-row tight">
      <Kbd>⌘K</Kbd>
      <Kbd>Esc</Kbd>
      <Kbd>Enter</Kbd>
      <Kbd>Shift</Kbd>
      <Kbd>⌥</Kbd>
    </div>
  );
}

function ThinkingMeterTable() {
  return (
    <div class="meter-table">
      {METER_VARIANTS.map((variant) => (
        <div class="meter-table-row" key={variant}>
          <span class="meter-table-label">{variant}</span>
          <div class="meter-table-cells">
            {METER_LEVELS.map((level) => (
              <div class="meter-table-cell" key={level}>
                <ThinkingMeter variant={variant} level={level} />
                <span class="meter-table-cell-label">{level}</span>
              </div>
            ))}
          </div>
        </div>
      ))}
      <div class="meter-table-row">
        <span class="meter-table-label">bars (hot)</span>
        <div class="meter-table-cells">
          {METER_LEVELS.map((level) => (
            <div class="meter-table-cell" key={level}>
              <ThinkingMeter variant="bars" level={level} hot />
              <span class="meter-table-cell-label">{level}</span>
            </div>
          ))}
        </div>
      </div>
      <div class="meter-table-row">
        <span class="meter-table-label">dial (hot)</span>
        <div class="meter-table-cells">
          {METER_LEVELS.map((level) => (
            <div class="meter-table-cell" key={level}>
              <ThinkingMeter variant="dial" level={level} hot />
              <span class="meter-table-cell-label">{level}</span>
            </div>
          ))}
        </div>
      </div>
      <div class="meter-table-row">
        <span class="meter-table-label">glyph (hot)</span>
        <div class="meter-table-cells">
          {METER_LEVELS.map((level) => (
            <div class="meter-table-cell" key={level}>
              <ThinkingMeter variant="glyph" level={level} hot />
              <span class="meter-table-cell-label">{level}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// PrimitivesGallery — shows the component system's atoms
// (Phase 1, block 1) in all their states, for visual review on /next.
export function PrimitivesGallery() {
  return (
    <section>
      <h2>Átomos</h2>

      <h3>StateDot</h3>
      <StateDotRow />

      <h3>Chip</h3>
      <ChipRow />

      <h3>Button</h3>
      <ButtonRow />

      <h3>IconButton</h3>
      <IconButtonRow />

      <h3>Kbd</h3>
      <KbdRow />

      <h3>ThinkingMeter</h3>
      <ThinkingMeterTable />
    </section>
  );
}
