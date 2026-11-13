// Shared harness for ui_session_actions_*_test.mjs: an isolated store, a
// recording mock of lib/api.js and createSessionActions() wired to them.
import { createStore } from '../../internal/webconsole/ui/src/lib/store.js';
import { APIError } from '../../internal/webconsole/ui/src/lib/api.js';
import { setLocale } from '../../internal/webconsole/ui/src/lib/i18n.js';
import '../../internal/webconsole/ui/src/i18n/index.js';
import { emptySessionSlice, initialState } from '../../internal/webconsole/ui/src/state/store.js';
import { createComposerDrafts } from '../../internal/webconsole/ui/src/state/composer-drafts.js';
import { createSessionActions } from '../../internal/webconsole/ui/src/state/session-actions.js';

export { APIError };

export function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    values,
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
  };
}

export function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

export function apiError(message, { status = 400, code = '', detail = '', action = '', payload = null } = {}) {
  return new APIError(message, { status, code, detail, action, payload });
}

const DEFAULT_REPLIES = {
  startSession: () => ({ status: 'accepted', session_id: 'sess_new' }),
  continueSession: (id) => ({ status: 'accepted', session_id: id }),
  steerSession: () => ({ status: 'queued' }),
  revisePlanMode: () => ({ status: 'accepted' }),
  stopSession: () => ({ status: 'stopping' }),
  interruptSession: () => ({ status: 'interrupting' }),
  approvePlanMode: () => ({}),
  approveMissionPlan: () => ({}),
  getApprovalReceipt: () => { throw apiError('not found', { status: 404 }); },
  cancelPlanMode: () => ({ status: 'cancelled' }),
  answerPlanModeInput: () => ({ status: 'accepted' }),
  createGoal: () => ({}),
  updateGoal: () => ({}),
  pauseGoal: () => ({}),
  resumeGoal: () => ({}),
  completeGoal: () => ({}),
  deleteGoal: () => ({}),
};

/** createHarness({ api: { name: fn }, confirm, storage, locale }) */
export function createHarness({ api = {}, confirm = async () => true, storage = memoryStorage(), locale = 'en' } = {}) {
  setLocale(locale, { persist: false });
  const store = createStore(initialState());
  let randomSeed = 0;
  const drafts = createComposerDrafts({ getState: store.getState, update: store.update, random: () => ((randomSeed += 0.137) % 1) });
  const calls = [];
  const toasts = [];
  const dialogs = [];
  const navigations = [];
  const refreshes = [];
  const replies = { ...DEFAULT_REPLIES, ...api };
  const mockApi = Object.fromEntries(Object.entries(replies).map(([name, reply]) => [name, async (...args) => {
    calls.push({ name, args });
    return reply(...args);
  }]));
  const navigate = (path) => {
    navigations.push(path);
    const match = /^\/s\/([^/]+)$/.exec(path);
    const route = match ? { name: 'session', path, params: { id: decodeURIComponent(match[1]) } } : path === '/' ? { name: 'new', path: '/', params: {} } : { name: path.slice(1), path, params: {} };
    store.update('route', () => route);
    drafts.syncRoute(route);
    const id = route.name === 'session' ? route.params.id : '';
    if (store.getState().session.id !== id) store.update('session', () => emptySessionSlice(id));
  };
  let requestSeq = 0;
  let sendSeq = 0;
  const actions = createSessionActions({
    api: mockApi,
    getState: store.getState,
    update: store.update,
    navigate,
    refreshSession: async () => { refreshes.push(store.getState().session.id); return null; },
    refreshOverview: async () => null,
    toggleInspector: (open) => store.update('ui', { inspectorOpen: Boolean(open) }),
    pushToast: (toast) => { toasts.push(toast); return toasts.length; },
    toastError: (error, fallbackKey) => { toasts.push({ tone: 'danger', message: error?.message || fallbackKey, detail: error?.detail || '', action: error?.action || '', fallbackKey }); return toasts.length; },
    confirmDialog: async (spec) => { dialogs.push(spec); return confirm(spec); },
    drafts,
    storage,
    requestID: () => 'req_' + (++requestSeq),
    createSendId: () => 'send_' + (++sendSeq),
  });
  actions.attach(store);

  /** Opens /s/<id> with a loaded detail (state, plan_mode, goal …). */
  function openSession(id, detail = {}, { generating = false, messages = [] } = {}) {
    navigate('/s/' + encodeURIComponent(id));
    store.update('session', (session) => ({ ...session, detail: { messages, ...detail }, messages, generating, loading: false }));
  }

  function setComposer(values) {
    store.update('composer', values);
  }

  return { store, drafts, actions, calls, toasts, dialogs, navigations, refreshes, navigate, openSession, setComposer, storage, state: () => store.getState() };
}
