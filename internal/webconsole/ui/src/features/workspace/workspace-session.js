// Tracks the session the operator last worked in, so the Workspace page can
// bucket its browsed path by (workspace root, session) and start at the
// session workdir (spec/17-web-console.md §7 workspace rules). The session
// slice is cleared on page routes, so the last session-route context is kept
// here. Read-only: subscribes to the store, never writes it.
import { store } from '../../state/store.js';
import { sessionWorkdirFromDetail } from '../../lib/workspace-model.js';

let context = { id: '', workdir: '', backed: false };

function derive(state, previous) {
  const route = state?.route || {};
  if (route.name === 'session') {
    const id = String(route.params?.id || '');
    const workdir = sessionWorkdirFromDetail(id, state.session?.detail) || (previous.id === id ? previous.workdir : '');
    return { id, workdir, backed: Boolean(id) && !state.session?.missing };
  }
  if (route.name === 'new') return { id: '', workdir: '', backed: false };
  return previous;
}

context = derive(store.getState(), context);
store.subscribe((state) => {
  const next = derive(state, context);
  if (next.id !== context.id || next.workdir !== context.workdir || next.backed !== context.backed) context = next;
});

/** { id, workdir, backed } of the last session route ('' id = new-session composer). */
export function lastSessionContext() {
  return { ...context };
}
