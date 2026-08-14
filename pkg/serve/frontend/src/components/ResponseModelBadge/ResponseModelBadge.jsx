import "./ResponseModelBadge.css";

function canonical(model) {
  const value = String(model || "").toLowerCase().trim();
  if (value === "fable" || value.includes("claude-fable")) return "fable";
  if (value === "opus" || value.includes("claude-opus")) return "opus";
  return value.replace(/^anthropic\//, "");
}

// ResponseModelBadge is intentionally driven only by durable message
// provenance. Old messages without requested_model are simply not badged.
export function ResponseModelBadge({ message }) {
  const requested = canonical(message?.requested_model);
  const effective = canonical(message?.model);
  if (!requested || !effective || requested === effective) return null;
  const label = `${requested === "fable" ? "Fable" : message.requested_model} → ${effective === "opus" ? "Opus" : message.model} · safety fallback`;
  return <span class="response-model-badge" title={label}>{label}</span>;
}
