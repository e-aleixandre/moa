export function groupByProvider(models) {
  const groups = [];
  const seen = new Map();
  for (const model of models || []) {
    const provider = model.provider || "";
    if (!seen.has(provider)) {
      const group = { provider, items: [] };
      seen.set(provider, group);
      groups.push(group);
    }
    seen.get(provider).items.push(model);
  }
  return groups;
}

export function specMatches(model, query) {
  return (
    (model.codename || "").toLowerCase().includes(query) ||
    (model.name || "").toLowerCase().includes(query) ||
    (model.alias || "").toLowerCase().includes(query) ||
    (model.provider || "").toLowerCase().includes(query)
  );
}

export function pinnedModelSpecs(models, pinnedIDs) {
  const byID = new Map((models || []).map((model) => [model.catalogId, model]));
  return (pinnedIDs || []).map((id) => byID.get(id)).filter(Boolean);
}

export function visiblePinnedSpecs(models, expanded, threshold = 4) {
  return expanded ? models : models.slice(0, threshold);
}
