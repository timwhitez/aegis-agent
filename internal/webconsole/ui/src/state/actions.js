// Shell / data actions. Components call these; only state/ modules mutate the store.
import * as api from '../lib/api.js';
import { t, toggleLocale as toggleLocaleCore } from '../lib/i18n.js';
import { sessionDetailDisplayStatus, sessionSummaryDisplayStatus } from '../lib/status.js';
import { firstPromptFromMessages, mergeMessageWindows, rootSessions } from '../lib/session-list.js';
import { isLocalActivitySettled } from '../lib/send-recovery.js';
import { relayHints } from './relay.js';
import { emptyComposerSlice, emptySessionSlice, getState, update } from './store.js';
import { navigate, sessionPath } from './router.js';
import { pushToast, toastError } from './toasts.js';
import { saveUIPrefs } from './prefs.js';
import { composerDrafts } from './composer-drafts.js';

export { navigate, sessionPath };

const notifiedMissing = new Set();
let sessionRequestSeq = 0;
let appliedSessionSeq = 0;
let overviewRequestSeq = 0;
let lastMalformedToastAt = 0;
let lastOverviewReconcile = '';
const pendingRefresh = { session: null, overview: null };

function errorInfo(error) {
  return { message: error?.message || t('common.error.title'), detail: error?.detail || '', action: error?.action || '', status: error?.status || 0 };
}

function persistPrefs() {
  const { ui } = getState();
  saveUIPrefs({ sidebarCollapsed: ui.sidebarCollapsed, inspectorOpen: ui.inspectorOpen, inspectorTab: ui.inspectorTab });
}

// ---- navigation ------------------------------------------------------------------

/** New session: reset the composer, go to /, close the drawer and focus the composer. */
export function newSession() {
  // Keep the open session's unsent draft; the new-session draft starts empty with a new identity.
  composerDrafts.resetNewSession();
  update('composer', (composer) => ({ ...emptyComposerSlice(), failedDrafts: composer.failedDrafts, focusToken: composer.focusToken + 1 }));
  navigate('/');
  update('ui', { sidebarOpen: false, paletteOpen: false });
}

export function openSession(id) {
  if (!id) return;
  navigate(sessionPath(id));
  update('ui', { sidebarOpen: false });
}

/** Reacts to route changes: opens/clears the session slice. Called by main.js. */
export function syncSessionForRoute(route) {
  composerDrafts.syncRoute(route);
  const { session } = getState();
  if (route.name === 'session') {
    if (session.id === route.params.id) return;
    update('session', () => ({ ...emptySessionSlice(route.params.id), loading: true }));
    refreshSession();
    return;
  }
  if (session.id) update('session', () => emptySessionSlice());
}

// ---- data loading ----------------------------------------------------------------

/** Missing session: toast once per id, stop polling it and return to /. */
export function handleMissingSession(id) {
  if (!notifiedMissing.has(id)) {
    notifiedMissing.add(id);
    pushToast({ tone: 'warning', message: t('shell.session.missing', { id }) });
  }
  update('session', { missing: true, loading: false, refreshing: false });
  if (getState().route.name === 'session' && getState().route.params.id === id) {
    navigate('/', { replace: true });
  }
}

/** GET /api/sessions/{id}?limit=40 and merge the message tail into the window. */
export async function refreshSession({ silent = false } = {}) {
  const { session } = getState();
  const id = session.id;
  if (!id || session.missing) return null;
  const seq = ++sessionRequestSeq;
  update('session', { refreshing: true });
  try {
    const detail = await api.getSession(id, { limit: 40 });
    const current = getState().session;
    if (current.id !== id || seq < appliedSessionSeq) return null;
    appliedSessionSeq = seq;
    const messages = mergeMessageWindows(current.messages, detail?.messages || []);
    const hasMoreEarlier = current.messages.length === 0 && !current.earlierLoaded
      ? Boolean(detail?.has_more_messages)
      : current.hasMoreEarlier;
    const displayStatus = sessionDetailDisplayStatus(detail);
    const generating = displayStatus === 'running';
    // Local send feedback ends once the run settled after it (or it aged out).
    const activity = current.activity && isLocalActivitySettled(current.activity, { detail, generating }) ? null : current.activity;
    update('session', {
      detail,
      messages,
      hasMoreEarlier,
      loading: false,
      refreshing: false,
      error: null,
      generating,
      activity,
      lastRefreshedAt: Date.now(),
    });
    learnSessionTitle(id, messages, hasMoreEarlier);
    reconcileOverviewStatus(id, displayStatus);
    return detail;
  } catch (error) {
    if (getState().session.id !== id) return null;
    if (error?.status === 404) {
      handleMissingSession(id);
      return null;
    }
    update('session', { loading: false, refreshing: false, error: errorInfo(error) });
    if (!silent) toastError(error, 'shell.session.loadFailed');
    return null;
  }
}

/**
 * The sidebar shows the open session with its detail-derived status; when the
 * polled overview summary disagrees, refresh the overview once per
 * (session, status) so the other rows and pages catch up too.
 */
function reconcileOverviewStatus(id, displayStatus) {
  const summary = getState().overview.sessions.find((item) => item.id === id);
  if (!summary || sessionSummaryDisplayStatus(summary) === displayStatus) return;
  const key = id + '\n' + displayStatus;
  if (lastOverviewReconcile === key) return;
  lastOverviewReconcile = key;
  queueRefresh('overview', 300);
}

function learnSessionTitle(id, messages, hasMoreEarlier) {
  const prompt = firstPromptFromMessages(messages, hasMoreEarlier);
  if (!prompt || getState().overview.titles[id] === prompt) return;
  update('overview', (overview) => ({ ...overview, titles: { ...overview.titles, [id]: prompt } }));
}

/** GET messages?before_id=… and prepend (deduped by id). */
export async function loadEarlierMessages() {
  const { session } = getState();
  const beforeId = session.messages[0]?.id;
  if (!session.id || session.loadingEarlier || !session.hasMoreEarlier || !beforeId) return;
  const id = session.id;
  update('session', { loadingEarlier: true });
  try {
    const page = await api.getSessionMessages(id, { beforeId, limit: 40 });
    if (getState().session.id !== id) return;
    update('session', (current) => {
      const messages = mergeMessageWindows(page?.messages || [], current.messages);
      return { ...current, messages, loadingEarlier: false, earlierLoaded: true, hasMoreEarlier: Boolean(page?.has_more) };
    });
    const current = getState().session;
    learnSessionTitle(id, current.messages, current.hasMoreEarlier);
  } catch (error) {
    if (getState().session.id !== id) return;
    update('session', { loadingEarlier: false });
    toastError(error, 'shell.session.earlierFailed');
  }
}

export async function refreshOverview({ silent = true } = {}) {
  const seq = ++overviewRequestSeq;
  update('overview', { loading: true });
  try {
    const data = await api.getOverview();
    if (seq !== overviewRequestSeq) return null;
    update('overview', { data, sessions: rootSessions(data?.recent_sessions), loading: false, error: null, loadedAt: Date.now() });
    return data;
  } catch (error) {
    if (seq !== overviewRequestSeq) return null;
    update('overview', { loading: false, error: errorInfo(error) });
    if (!silent) toastError(error, 'shell.sessions.loadFailed');
    return null;
  }
}

export async function refreshMeta() {
  update('meta', { loading: true });
  try {
    const data = await api.getMeta();
    update('meta', { data, loading: false, error: null });
    return data;
  } catch (error) {
    update('meta', { loading: false, error: errorInfo(error) });
    return null;
  }
}

/** Debounced refresh used by relay hints. */
export function queueRefresh(channel, delay) {
  if (pendingRefresh[channel]) clearTimeout(pendingRefresh[channel]);
  pendingRefresh[channel] = setTimeout(() => {
    pendingRefresh[channel] = null;
    if (channel === 'session') refreshSession({ silent: true });
    else refreshOverview({ silent: true });
  }, delay);
}

// ---- relay / connection ------------------------------------------------------------

export function setRelayStatus(status, attempts = 0) {
  const previous = getState().connection.relay;
  update('connection', { relay: status, attempts });
  if (status === 'open' && previous !== 'open') {
    queueRefresh('overview', 120);
    if (getState().session.id) queueRefresh('session', 120);
  }
}

export function applyRelayFrame(frame) {
  const hints = relayHints(frame, getState().session.id);
  if (hints.liveEvent) {
    // receivedAt orders time-less relay events against local send feedback (session.activity.at).
    const liveEvent = { ...hints.liveEvent, receivedAt: Date.now() };
    update('session', (session) => ({ ...session, liveEvents: [...session.liveEvents.slice(-199), liveEvent] }));
  }
  if (hints.generating !== undefined) update('session', { generating: hints.generating });
  if (hints.error) pushToast({ tone: 'danger', message: t('shell.relay.sessionError', { message: hints.error }) });
  if (hints.refreshSession !== null) queueRefresh('session', hints.refreshSession);
  if (hints.refreshOverview !== null) queueRefresh('overview', hints.refreshOverview);
}

export function reportMalformedRelayFrame() {
  const now = Date.now();
  if (now - lastMalformedToastAt < 10000) return;
  lastMalformedToastAt = now;
  pushToast({ tone: 'warning', message: t('shell.relay.malformed') });
}

export function setPageVisible(visible) {
  update('connection', { pageVisible: Boolean(visible) });
}

export function setOnline(online) {
  update('connection', { online: Boolean(online) });
}

// ---- layout / UI preferences -------------------------------------------------------

export function setViewport(viewport) {
  const current = getState().ui.viewport;
  if (current.narrow === viewport.narrow && current.wide === viewport.wide && current.mobile === viewport.mobile) return;
  update('ui', (ui) => ({ ...ui, viewport, sidebarOpen: viewport.narrow ? ui.sidebarOpen : false }));
}

/** ⌘B: drawer on narrow viewports, collapse preference on desktop. */
export function toggleSidebar() {
  const { ui } = getState();
  if (ui.viewport.narrow) {
    update('ui', { sidebarOpen: !ui.sidebarOpen });
    return;
  }
  update('ui', { sidebarCollapsed: !ui.sidebarCollapsed });
  persistPrefs();
}

export function setSidebarOpen(open) {
  update('ui', { sidebarOpen: Boolean(open) });
}

export function toggleInspector(force) {
  const open = typeof force === 'boolean' ? force : !getState().ui.inspectorOpen;
  update('ui', { inspectorOpen: open });
  persistPrefs();
}

export function setInspectorTab(tab) {
  update('ui', { inspectorTab: tab, inspectorOpen: true });
  persistPrefs();
}

export function setSidebarQuery(query) {
  update('ui', { sidebarQuery: String(query || '') });
}

export function openPalette() {
  update('ui', { paletteOpen: true, shortcutsOpen: false });
}

export function closePalette() {
  update('ui', { paletteOpen: false });
}

export function openShortcuts() {
  update('ui', { shortcutsOpen: true, paletteOpen: false });
}

export function closeShortcuts() {
  update('ui', { shortcutsOpen: false });
}

export function toggleLocale() {
  toggleLocaleCore();
}

/** Shows a session in the Sessions page (the page reads ui.historyFocus). */
export function viewSessionInHistory(id) {
  update('ui', { historyFocus: id || '' });
  navigate('/sessions');
}

// ---- composer UI state (sending lives in session-actions.js) -------------------------

/** Requests composer focus (Composer focuses its textarea when focusToken changes). */
export function focusComposer() {
  update('composer', (composer) => ({ ...composer, focusToken: composer.focusToken + 1 }));
}

export function setComposerText(text, { focus = false } = {}) {
  update('composer', (composer) => ({ ...composer, text: String(text ?? ''), focusToken: focus ? composer.focusToken + 1 : composer.focusToken }));
}

/** Goal and Plan are mutually exclusive start modes: enabling one turns the other off. */
export function toggleGoalMode(force) {
  update('composer', (composer) => {
    const goalEnabled = typeof force === 'boolean' ? force : !composer.goalEnabled;
    return { ...composer, goalEnabled, planEnabled: goalEnabled ? false : composer.planEnabled };
  });
}

export function togglePlanMode(force) {
  update('composer', (composer) => {
    const planEnabled = typeof force === 'boolean' ? force : !composer.planEnabled;
    return { ...composer, planEnabled, goalEnabled: planEnabled ? false : composer.goalEnabled };
  });
}

/** "打断当前步骤": the next steer interrupts the current step. */
export function toggleInterruptMode(force) {
  update('composer', (composer) => ({ ...composer, interrupt: typeof force === 'boolean' ? force : !composer.interrupt }));
}

/** Provider/model override for start/continue ('' = inherit the default). */
export function setComposerModel({ provider = '', model = '' } = {}) {
  update('composer', { provider: String(provider || ''), model: String(model || '') });
}
