// tasks-view.js — the Tasks view is a VIEW like the grid (?view=tasks): the
// desktop's middle zone, a full-screen push on the phone. Leaving it goes
// back to whichever view it was opened from.

import { navigate } from './router.js';
import { store } from './store.js';

let from = null;

export function openTasksView() {
  const view = store.get().view;
  if (view === 'tasks') return;
  from = view;
  navigate('tasks');
}

export function closeTasksView() {
  if (store.get().view !== 'tasks') return;
  const back = from === 'tasks' ? null : from;
  from = null;
  navigate(back);
}

export function toggleTasksView() {
  if (store.get().view === 'tasks') closeTasksView();
  else openTasksView();
}
