// SunriseIcon — the Daybreak mark: a sun rising over the horizon. It says the
// session asks for Daybreak on a model that carries it, nothing more.
export function SunriseIcon({ class: cls = "", title = "Daybreak" }) {
  return (
    <svg class={`db-sunrise${cls ? ` ${cls}` : ""}`} viewBox="0 0 16 16" role="img" aria-label={title}>
      <title>{title}</title>
      <path d="M4.5 11.5a3.5 3.5 0 0 1 7 0" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
      <path d="M1.5 11.5h13M8 2.5v2M3.4 5.4l1.3 1.3M12.6 5.4l-1.3 1.3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
    </svg>
  );
}
