// The application store instance. See spec/17-web-console-frontend.md §5
// "模块契约" for the slice contract; only state/ modules mutate it.
import { createStore } from '../lib/store.js';
import { getLocale } from '../lib/i18n.js';
import { loadUIPrefs } from './prefs.js';

export function emptySessionSlice(id = '') {
  return {
    id, // durable session id of the open session ('' on / and pages)
    detail: null, // last GET /api/sessions/{id}?limit=40 payload
    messages: [], // merged message window (older pages + polled tail), deduped by id
    hasMoreEarlier: false, // more messages exist before messages[0]
    earlierLoaded: false, // an earlier page was merged (hasMoreEarlier then comes from it)
    loading: false, // first load in flight
    loadingEarlier: false,
    refreshing: false, // background refresh in flight
    error: null, // { message, detail, action, status } of the last failed refresh
    missing: false, // server answered 404; polling stops
    generating: false, // a run is known to be active (set by session-actions / relay)
    liveEvents: [], // recent relay engine_event payloads for this session (memory only)
    lastRefreshedAt: 0,
  };
}

export function emptyComposerSlice() {
  return {
    text: '',
    goalEnabled: false,
    planEnabled: false,
    interrupt: false, // steer with interrupt
    provider: '', // '' = inherit default
    model: '',
    revising: false, // "要求修改" plan revision mode
    submitting: false,
    focusToken: 0, // bump to request focus (Composer focuses on change)
    failedDrafts: [], // memory-only failed sends, see send-recovery.js
  };
}

export function initialState() {
  const prefs = loadUIPrefs();
  return {
    route: { name: 'new', path: '/', params: {} },
    meta: { data: null, loading: false, error: null },
    connection: { relay: 'connecting', attempts: 0, pageVisible: true, online: true },
    overview: { data: null, sessions: [], loading: false, error: null, loadedAt: 0, titles: {} },
    session: emptySessionSlice(),
    composer: emptyComposerSlice(),
    ui: {
      locale: getLocale(),
      sidebarCollapsed: prefs.sidebarCollapsed, // desktop preference
      sidebarOpen: false, // drawer open (narrow viewports)
      inspectorOpen: prefs.inspectorOpen,
      inspectorTab: prefs.inspectorTab,
      paletteOpen: false,
      shortcutsOpen: false,
      sidebarQuery: '',
      historyFocus: '', // session id the Sessions page should reveal ("在会话管理中查看")
      viewport: { narrow: false, wide: true, mobile: false }, // narrow ≤1024, wide ≥1280, mobile ≤768
    },
    toasts: [], // [{ id, tone, message, detail, action, timeout }]
    dialogs: [], // [{ id, kind: 'confirm'|'prompt', title, body, ..., resolve }]
  };
}

export const store = createStore(initialState());
export const getState = store.getState;
export const setState = store.setState;
export const update = store.update;
