// WebSocket relay on /ws: connection status and refresh hints only (no control
// actions). Exponential backoff with jitter; paused while the page is hidden.

export const RELAY_BACKOFF_BASE_MS = 1000;
export const RELAY_BACKOFF_MAX_MS = 15000;
export const LIVE_EVENT_LIMIT = 200;

const SESSION_REFRESH_EVENTS = /^(turn\.|tool\.|message|session\.|goal\.|plan|planmode\.|steer\.|child\.|task|todo|approval|run\.)/;

export function backoffDelay(attempt, random = Math.random) {
  const base = Math.min(RELAY_BACKOFF_BASE_MS * 2 ** Math.max(0, attempt), RELAY_BACKOFF_MAX_MS);
  const jitter = base * 0.2 * (random() * 2 - 1);
  return Math.max(RELAY_BACKOFF_BASE_MS, Math.round(base + jitter));
}

/**
 * Pure translation of one relay message into store hints:
 * { refreshSession: ms|null, refreshOverview: ms|null, liveEvent, generating, error }.
 * Messages for other sessions only refresh the sidebar.
 */
export function relayHints(message, currentSessionId) {
  const hints = { refreshSession: null, refreshOverview: null, liveEvent: null, generating: undefined, error: '' };
  if (!message || typeof message !== 'object') return hints;
  const payload = message.payload && typeof message.payload === 'object' ? message.payload : {};
  const sessionId = payload.sessionId || payload.session_id || '';
  const isCurrent = Boolean(currentSessionId) && sessionId === currentSessionId;
  switch (message.type) {
    case 'session':
      hints.refreshOverview = 240;
      if (isCurrent) hints.refreshSession = 120;
      break;
    case 'message':
      if (isCurrent) hints.refreshSession = 120;
      break;
    case 'status':
      hints.refreshOverview = 260;
      if (isCurrent) {
        hints.refreshSession = 120;
        hints.generating = payload.status === 'running';
      }
      break;
    case 'engine_event':
      if (isCurrent) {
        hints.liveEvent = payload;
        if (SESSION_REFRESH_EVENTS.test(String(payload.type || ''))) hints.refreshSession = 220;
      }
      if (/^(session\.|run\.|goal\.|planmode\.)/.test(String(payload.type || ''))) hints.refreshOverview = 260;
      break;
    case 'error':
      if (!sessionId || isCurrent) {
        hints.error = String(payload.content || payload.message || payload.error || '');
        if (isCurrent) {
          hints.generating = false;
          hints.refreshSession = 120;
        }
        hints.refreshOverview = 240;
      }
      break;
    default:
      break;
  }
  return hints;
}

/** Parses a relay frame; throws on malformed JSON or a missing type. */
export function parseRelayFrame(data) {
  const parsed = JSON.parse(String(data));
  if (!parsed || typeof parsed !== 'object' || typeof parsed.type !== 'string') {
    throw new Error('relay frame without type');
  }
  return parsed;
}

/**
 * createRelay({ url, onStatus(status, attempts), onMessage(frame), onMalformed(error), isHidden, WebSocketImpl, timers })
 * -> { connect, close, resume }. status: 'connecting' | 'open' | 'closed'.
 */
export function createRelay({ url, onStatus, onMessage, onMalformed, isHidden = () => false, WebSocketImpl = globalThis.WebSocket, timers = globalThis }) {
  let socket = null;
  let attempts = 0;
  let reconnectTimer = null;
  let closed = false;

  function clearReconnect() {
    if (reconnectTimer) timers.clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  function scheduleReconnect() {
    if (closed || reconnectTimer || isHidden()) return;
    const delay = backoffDelay(attempts);
    attempts += 1;
    reconnectTimer = timers.setTimeout(() => {
      reconnectTimer = null;
      if (!isHidden()) connect();
    }, delay);
  }

  function connect() {
    if (closed || !WebSocketImpl) return;
    if (socket && (socket.readyState === 0 || socket.readyState === 1)) return;
    clearReconnect();
    onStatus?.('connecting', attempts);
    let ws;
    try {
      ws = new WebSocketImpl(url);
    } catch {
      onStatus?.('closed', attempts);
      scheduleReconnect();
      return;
    }
    socket = ws;
    ws.onopen = () => {
      if (socket !== ws) return;
      attempts = 0;
      onStatus?.('open', attempts);
    };
    ws.onmessage = (event) => {
      if (socket !== ws) return;
      let frame;
      try {
        frame = parseRelayFrame(event.data);
      } catch (error) {
        onMalformed?.(error);
        return;
      }
      onMessage?.(frame);
    };
    ws.onerror = () => {};
    ws.onclose = () => {
      if (socket !== ws) return;
      socket = null;
      onStatus?.('closed', attempts);
      scheduleReconnect();
    };
  }

  return {
    connect,
    /** Called when the page becomes visible again: reconnect immediately. */
    resume() {
      if (closed) return;
      if (!socket) {
        clearReconnect();
        connect();
      }
    },
    /** Called when the page is hidden: stop reconnect attempts (socket stays). */
    pause() {
      clearReconnect();
    },
    close() {
      closed = true;
      clearReconnect();
      if (socket) {
        const ws = socket;
        socket = null;
        ws.close();
      }
    },
  };
}
