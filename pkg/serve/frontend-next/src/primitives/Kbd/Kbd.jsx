import "./Kbd.css";

// Kbd — tecla de atajo de teclado.
export function Kbd({ children, ...rest }) {
  return (
    <kbd class="kbd" {...rest}>
      {children}
    </kbd>
  );
}
