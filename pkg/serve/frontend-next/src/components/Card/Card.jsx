import "./Card.css";

// Card — generic reusable container (background, border, radius). Doesn't impose
// internal layout; the consumer decides with its own className/styles.
export function Card({ children, className = "", ...rest }) {
  return (
    <div class={`card ${className}`.trim()} {...rest}>
      {children}
    </div>
  );
}
