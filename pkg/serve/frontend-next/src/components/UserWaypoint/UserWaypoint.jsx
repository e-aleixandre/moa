import { sanitizeHtml } from "../../util/sanitize.js";
import "./UserWaypoint.css";

// UserWaypoint — el prompt del usuario como tarjeta-hito ("waypoint") dentro
// del stream: borde peach a la izquierda, cabecera "YOU" + hora, cuerpo de
// texto. `html` permite pasar cuerpo ya renderizado (p.ej. markdown); si no
// se da, `children` se usa tal cual (normalmente un <p>).
export function UserWaypoint({ time, children, html, className = "", ...rest }) {
  return (
    <div class={`waypoint ${className}`.trim()} {...rest}>
      <div class="who">
        <span class="who-label">You</span>
        {time && <time>{time}</time>}
      </div>
      {html != null ? (
        <div class="body" dangerouslySetInnerHTML={{ __html: sanitizeHtml(html) }} />
      ) : (
        <div class="body">{children}</div>
      )}
    </div>
  );
}
