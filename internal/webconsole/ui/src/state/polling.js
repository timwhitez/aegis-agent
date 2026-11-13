// Polling scheduler (spec 17 §9): active session detail every 1.5s and the
// sidebar overview every 2s, paused while the page is hidden, and only while
// a run may be producing changes or the live relay is disconnected.
import { sessionHasCurrentProcessHandle, sessionDetailHasActiveDescendants, sessionSummaryDisplayStatus, isActiveRuntimeStatus } from '../lib/status.js';

export const POLL_SESSION_MS = 1500;
export const POLL_OVERVIEW_MS = 2000;

function detailIsLive(detail) {
  return sessionHasCurrentProcessHandle(detail) || sessionDetailHasActiveDescendants(detail);
}

export function shouldPollSession(state) {
  const { session, connection } = state;
  if (!connection.pageVisible || !session.id || session.missing) return false;
  if (connection.relay !== 'open') return true;
  if (session.generating) return true;
  return !session.detail || detailIsLive(session.detail);
}

export function overviewHasLiveSessions(overview) {
  const sessions = Array.isArray(overview?.recent_sessions) ? overview.recent_sessions : [];
  return sessions.some((item) => isActiveRuntimeStatus(sessionSummaryDisplayStatus(item)));
}

export function shouldPollOverview(state) {
  const { session, connection, overview } = state;
  if (!connection.pageVisible) return false;
  if (!overview.data) return true;
  if (connection.relay !== 'open') return true;
  if (session.generating) return true;
  if (session.detail && detailIsLive(session.detail)) return true;
  return overviewHasLiveSessions(overview.data);
}

/**
 * createPoller({ getState, subscribe, refreshSession, refreshOverview, timers })
 * -> { sync, stop }. Each channel runs one request at a time and reschedules
 * after it settles, so slow responses never overlap.
 */
export function createPoller({ getState, subscribe, refreshSession, refreshOverview, timers = globalThis }) {
  const channels = {
    session: { should: shouldPollSession, interval: POLL_SESSION_MS, run: refreshSession, timer: null, inFlight: false },
    overview: { should: shouldPollOverview, interval: POLL_OVERVIEW_MS, run: refreshOverview, timer: null, inFlight: false },
  };
  let stopped = false;

  function schedule(channel) {
    channel.timer = timers.setTimeout(async () => {
      channel.timer = null;
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
    }, channel.interval);
  }

  function sync() {
    if (stopped) return;
    const state = getState();
    for (const channel of Object.values(channels)) {
      const should = channel.should(state);
      if (should && !channel.timer && !channel.inFlight) schedule(channel);
      if (!should && channel.timer) {
        timers.clearTimeout(channel.timer);
        channel.timer = null;
      }
    }
  }

  const unsubscribe = subscribe ? subscribe(() => sync()) : () => {};
  sync();

  return {
    sync,
    stop() {
      stopped = true;
      unsubscribe();
      for (const channel of Object.values(channels)) {
        if (channel.timer) timers.clearTimeout(channel.timer);
        channel.timer = null;
      }
    },
  };
}
