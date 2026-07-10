export function applyOpsSnapshot(data, event) {
  if ((event?.type !== 'init' && event?.type !== 'snapshot') ||
      !Number.isSafeInteger(event.version) || event.version <= (data?.streamVersion || 0) ||
      !event.snapshot || !Array.isArray(event.snapshot.projects)) {
    return data;
  }
  return { ...data, streamVersion: event.version, overview: event.snapshot };
}

export function nextOpsReconnectDelay(backoff, maxBackoff = 16000) {
  return Math.min(backoff * 2, maxBackoff);
}
