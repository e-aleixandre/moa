// call-notifications.js — retire the system notification of a call request
// that is no longer live.
//
// A call_request push is never closed by the server (a silent "close" push is
// not an option: browsers require every push to show something). Any open page
// does it instead, when the requests feed shows the request gone: answered,
// written or withdrawn. A request nobody picked up is replaced on the server
// side by «Missed call from <owner>» (same tag, kind ask), which this leaves
// alone because it only closes notifications of kind call_request.

// isCurrent is asked after the awaits: a sweep made for an older snapshot must
// not close the notification of a request that went live after it.
export async function closeEndedCallNotifications(list, registration, isCurrent = () => true) {
  if (!registration || typeof registration.getNotifications !== 'function') return;
  try {
    const live = new Set((Array.isArray(list) ? list : []).map((r) => r.id));
    const shown = await registration.getNotifications();
    if (!isCurrent()) return;
    for (const n of shown) {
      const d = n.data || {};
      if (d.kind === 'call_request' && d.request_id && !live.has(d.request_id)) n.close();
    }
  } catch (_) { /* best effort: the notification expires on its own */ }
}

let latest = 0;

export function closeEndedCallNotificationsNow(list) {
  if (typeof navigator === 'undefined' || !navigator.serviceWorker?.getRegistration) return Promise.resolve();
  const sweep = ++latest;
  return navigator.serviceWorker.getRegistration('/')
    .then((reg) => (sweep === latest ? closeEndedCallNotifications(list, reg, () => sweep === latest) : undefined))
    .catch(() => {});
}
