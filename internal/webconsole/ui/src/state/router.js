// History API router. Routes (spec §4): / · /s/{id} · /sessions · /skills ·
// /workspace · /settings. Unknown paths are replaced with /.
import { getState, update } from './store.js';

export const PAGE_ROUTES = Object.freeze(['sessions', 'skills', 'workspace', 'settings']);

/** parseRoute('/s/abc') -> { name: 'session', path, params: { id: 'abc' } } | null */
export function parseRoute(pathname) {
  const path = String(pathname || '/').replace(/\/{2,}/g, '/');
  if (path === '/' || path === '') return { name: 'new', path: '/', params: {} };
  const session = /^\/s\/([^/]+)\/?$/.exec(path);
  if (session) {
    let id = session[1];
    try {
      id = decodeURIComponent(id);
    } catch {
      return null;
    }
    return id ? { name: 'session', path: `/s/${encodeURIComponent(id)}`, params: { id } } : null;
  }
  const page = /^\/([a-z]+)\/?$/.exec(path);
  if (page && PAGE_ROUTES.includes(page[1])) return { name: page[1], path: `/${page[1]}`, params: {} };
  return null;
}

export const sessionPath = (id) => `/s/${encodeURIComponent(id)}`;

function applyRoute(route) {
  const current = getState().route;
  if (current.path === route.path && current.name === route.name) return;
  update('route', () => route);
}

/** navigate(path, { replace }) pushes (or replaces) history and updates state.route. */
export function navigate(path, { replace = false } = {}) {
  const route = parseRoute(path) || parseRoute('/');
  const history = globalThis.history;
  if (history && globalThis.location?.pathname !== route.path) {
    if (replace) history.replaceState(null, '', route.path);
    else history.pushState(null, '', route.path);
  }
  applyRoute(route);
  return route;
}

/** Reads location, normalises unknown routes to / and listens to popstate. */
export function startRouter() {
  const sync = () => {
    const route = parseRoute(globalThis.location?.pathname || '/');
    if (!route) {
      navigate('/', { replace: true });
      return;
    }
    applyRoute(route);
  };
  sync();
  globalThis.addEventListener?.('popstate', sync);
  return () => globalThis.removeEventListener?.('popstate', sync);
}

/** onClick handler for in-app <a href> links: plain left clicks use navigate(). */
export function onLinkClick(event) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const anchor = event.currentTarget;
  const href = anchor?.getAttribute?.('href');
  if (!href || !href.startsWith('/') || anchor.target) return;
  event.preventDefault();
  navigate(href);
}
