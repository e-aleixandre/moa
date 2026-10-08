export function providerSourceLabel(source) {
  if (!source?.kind) return "";
  const known = source.usage_complete === true && Number.isFinite(source.estimated_cost) && source.estimated_cost >= 0;
  if (source.kind === "api_backup") {
    const cost = known ? `estimated API cost $${source.estimated_cost.toFixed(4)}` : "API cost unknown";
    const drops = (source.input_transformations || []).some((v) => v.startsWith("thinking_dropped:"));
    return `API backup · ${cost}${drops ? " · upstream dropped earlier thinking" : ""}`;
  }
  if (source.kind === "oauth") return `OAuth · plan / upstream usage credits${known ? ` · equivalent $${source.estimated_cost.toFixed(4)}` : ""}`;
  if (source.kind === "api_key") return known ? `API · estimated cost $${source.estimated_cost.toFixed(4)}` : "API · cost unknown";
  return "";
}
