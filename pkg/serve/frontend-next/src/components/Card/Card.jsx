import "./Card.css";

// Card — contenedor genérico reutilizable (fondo, borde, radius). No impone
// layout interno; el consumidor decide con className/estilos propios.
export function Card({ children, className = "", ...rest }) {
  return (
    <div class={`card ${className}`.trim()} {...rest}>
      {children}
    </div>
  );
}
