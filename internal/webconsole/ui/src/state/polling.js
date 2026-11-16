// Polling scheduler. The /ws relay only reports connection health: the server
// pushes no session events, and other tabs, the CLI or other processes write
// the same session files. So while the page is visible both channels always
// poll: fast while a run may be producing changes, before the first load, or
// while the relay is down, and at an idle cadence otherwise. Paused while the
// page is hidden; main.js refreshes both channels when it becomes visible.
import { sessionHasCurrentProcessHandle, sessionDetailHasActiveDescendants, sessionSummaryDisplayStatus, isActiveRuntimeStatus } from '../lib/status.js';

export const POLL_SESSION_MS = 1500;
export const POLL_OVERVIEW_MS = 2000;
export const POLL_IDLE_MS = 5000;

function detailIsLive(detail) {
  return sessionHasCurrentProcessHandle(detail) || sessionDetailHasActiveDescendants(detail);
}

export function shouldPollSession(state) {
  const { session, connection } = state;
  return Boolean(connection.pageVisible && session.id && !session.missing);
}

/** Session detail cadence: POLL_SESSION_MS while it may change quickly, else POLL_IDLE_MS. */
export function sessionPollInterval(state) {
  const { session, connection } = state;
  if (connection.relay !== 'open' || session.generating || !session.detail || detailIsLive(session.detail)) return POLL_SESSION_MS;
  return POLL_IDLE_MS;
}

export function overviewHasLiveSessions(overview) {
  const sessions = Array.isArray(overview?.recent_sessions) ? overview.recent_sessions : [];
  return sessions.some((item) => isActiveRuntimeStatus(sessionSummaryDisplayStatus(item)));
}

export function shouldPollOverview(state) {
  return Boolean(state.connection.pageVisible);
}

/** Sidebar overview cadence: POLL_OVERVIEW_MS while anything is live, else POLL_IDLE_MS. */
export function overviewPollInterval(state) {
  const { session, connection, overview } = state;
  if (!overview.data || connection.relay !== 'open' || session.generating) return POLL_OVERVIEW_MS;
  if ((session.detail && detailIsLive(session.detail)) || overviewHasLiveSessions(overview.data)) return POLL_OVERVIEW_MS;
  return POLL_IDLE_MS;
}

/**
 * createPoller({ getState, subscribe, refreshSession, refreshOverview, timers })
 * -> { sync, stop }. Each channel runs one request at a time and reschedules
 * after it settles, so slow responses never overlap. A pending idle timer is
 * pulled in as soon as the state asks for the faster cadence (a run starts).
 */
export function createPoller({ getState, subscribe, refreshSession, refreshOverview, timers = globalThis }) {
  const channels = {
    session: { should: shouldPollSession, interval: sessionPollInterval, run: refreshSession, timer: null, timerMs: 0, inFlight: false },
    overview: { should: shouldPollOverview, interval: overviewPollInterval, run: refreshOverview, timer: null, timerMs: 0, inFlight: false },
  };
  let stopped = false;

  function clear(channel) {
    if (channel.timer) timers.clearTimeout(channel.timer);
    channel.timer = null;
    channel.timerMs = 0;
  }

  function schedule(channel, ms) {
    channel.timerMs = ms;
    channel.timer = timers.setTimeout(async () => {
      channel.timer = null;
      channel.timerMs = 0;
      if (stopped || !channel.should(getState())) return;
      channel.inFlight = true;
      try {
        await channel.run({ silent: true });
      } catch {
        // Refresh actions surface their own errors; keep polling.
      } finally {
        channel.inFlight = false;
        sync();
      }
    }, ms);
  }

  function sync() {
    if (stopped) return;
    const state = getState();
    for (const channel of Object.values(channels)) {
      if (!channel.should(state)) {
        clear(channel);
        continue;
      }
      if (channel.inFlight) continue;
      const ms = channel.interval(state);
      if (channel.timer && channel.timerMs > ms) clear(channel);
      if (!channel.timer) schedule(channel, ms);
    }
  }

  const unsubscribe = subscribe ? subscribe(() => sync()) : () => {};
  sync();

  return {
    sync,
    stop() {
      stopped = true;
      unsubscribe();
      for (const channel of Object.values(channels)) clear(channel);
    },
  };
}
