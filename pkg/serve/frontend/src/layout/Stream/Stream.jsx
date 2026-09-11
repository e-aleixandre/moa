import "./Stream.css";
import { ConversationStream } from "./ConversationStream.jsx";

export function Stream(props) {
  return <ConversationStream {...props} />;
}

// Transcript — the catalogue's scrollable conversation column, MOVED
// (catalog/zones-lab.jsx `Transcript`, zones-lab.css `.zl-transcript`).
// Production Stream wraps this shell with stick-to-bottom, hydration and
// the "new messages" button. The catalogue imports this component and
// feeds it fixtures, which is the one criterion that separates a move
// from another imitation.
export function Transcript({ dense, className = "", children, scrollRef, ...rest }) {
  return (
    <div class={`zl-transcript${dense ? " is-dense" : ""}${className ? ` ${className}` : ""}`} ref={scrollRef} {...rest}>
      {children}
    </div>
  );
}
