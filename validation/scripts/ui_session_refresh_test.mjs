// Drives the real state/actions.js refreshSession against a stubbed global
// fetch: the detail is applied on every refresh, overlapping refreshes share
// one message-gap bridge per session visit, and the bridged window stays
// contiguous (spec/17-web-console-frontend.md §5.1). Also covers the
// session.generating fact across an accepted local launch (§7.2).
import test from 'node:test';
import assert from 'node:assert/strict';
import { getState, update } from '../../internal/webconsole/ui/src/state/store.js';
import { refreshSession } from '../../internal/webconsole/ui/src/state/actions.js';
import { composerMode } from '../../internal/webconsole/ui/src/features/session/controls/composer-model.js';

const msgs = (from, to) => Array.from({ length: to - from + 1 }, (_, index) => ({ id: 'm' + (from + index), role: 'assistant', text: String(from + index) }));
const ids = (list) => list.map((message) => message.id);
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function until(predicate, label, timeoutMs = 3000) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    if (predicate()) return;
    await wait(5);
  }
  assert.fail('timed out waiting for ' + label);
}
const reply = (body) => ({ ok: true, status: 200, statusText: 'OK', json: async () => body });

// Fake server for one session: GET detail returns the newest 40 messages and
// GET messages pages with before_id. Detail and page responses can be gated.
function fakeServer(id, { total, detailFor = () => ({}) } = {}) {
  const server = { history: msgs(0, total - 1), detailCalls: 0, pageCalls: 0, pagesInFlight: 0, maxPagesInFlight: 0, pageGate: null, detailGate: null };
  globalThis.fetch = async (url) => {
    const parsed = new URL(String(url), 'http://console.test');
    if (parsed.pathname === '/api/sessions/' + id) {
      server.detailCalls += 1;
      const n = server.detailCalls;
      if (server.detailGate) await server.detailGate;
      return reply({ state: { status: 'running', phase: 'phase_' + n }, active_handle: true, messages: server.history.slice(-40), has_more_messages: server.history.length > 40, ...detailFor(n) });
    }
    if (parsed.pathname === '/api/sessions/' + id + '/messages') {
      server.pageCalls += 1;
      server.pagesInFlight += 1;
      server.maxPagesInFlight = Math.max(server.maxPagesInFlight, server.pagesInFlight);
      try {
        if (server.pageGate) await server.pageGate.promise;
        const end = server.history.findIndex((message) => message.id === parsed.searchParams.get('before_id'));
        const start = Math.max(0, end - Number(parsed.searchParams.get('limit')));
        return reply({ messages: server.history.slice(start, end), has_more: start > 0 });
      } finally {
        server.pagesInFlight -= 1;
      }
    }
    if (parsed.pathname === '/api/overview') return reply({ recent_sessions: [] });
    return { ok: false, status: 404, statusText: 'Not Found', json: async () => ({}) };
  };
  return server;
}

function gate() {
  let open;
  const promise = new Promise((resolve) => { open = resolve; });
  return { promise, open };
}

function openSession(id, patch = {}) {
  update('route', () => ({ name: 'session', path: '/s/' + id, params: { id } }));
  update('session', (session) => ({ ...session, id, missing: false, detail: { state: { status: 'running' } }, messages: [], hasMoreEarlier: false, earlierLoaded: false, generating: false, activity: null, ...patch }));
}

test('overlapping refreshes apply each detail at once and share one gap bridge; the final window is contiguous', async () => {
  const server = fakeServer('S1', { total: 400 });
  server.pageGate = gate();
  // Window loaded before the tab was hidden: m0..m39; the server tail is now m360..m399.
  openSession('S1', { messages: msgs(0, 39) });

  const first = await refreshSession({ silent: true });
  assert.equal(first.state.phase, 'phase_1');
  let session = getState().session;
  assert.equal(session.detail.state.phase, 'phase_1', 'the detail is applied while the gap bridge runs');
  assert.deepEqual(ids(session.messages), ids(msgs(0, 39)), 'no message is dropped while bridging');
  assert.equal(session.refreshing, false);
  await until(() => server.pageCalls === 1, 'the bridge requests its first page');

  // More messages arrive; relay hints, polling and visibility refreshes overlap the bridge.
  server.history = msgs(0, 409);
  const overlapping = await Promise.all([refreshSession({ silent: true }), refreshSession({ silent: true }), refreshSession({ silent: true })]);
  assert.deepEqual(overlapping.map((detail) => detail?.state?.phase), ['phase_2', 'phase_3', 'phase_4']);
  session = getState().session;
  assert.equal(session.detail.state.phase, 'phase_4', 'every successful refresh applies its detail immediately');
  assert.equal(server.pageCalls, 1, 'overlapping refreshes join the in-flight bridge instead of starting their own');

  server.pageGate.open();
  await until(() => getState().session.messages.some((message) => message.id === 'm409'), 'the bridged window plus the newest tail');
  session = getState().session;
  assert.deepEqual(ids(session.messages), ids(msgs(0, 409)), 'loaded window + gap + bridged tail + newer tail, in order, without gaps');
  assert.equal(session.hasMoreEarlier, false);
  assert.equal(server.pageCalls, 2, 'one bridge: m160..m359 then m0..m159 (meets the loaded window)');
  assert.equal(server.maxPagesInFlight, 1, 'exactly one bridge was ever in flight');
  assert.equal(session.detail.state.phase, 'phase_4');

  // The bridge is done: the next refresh merges its overlapping tail directly.
  server.history = msgs(0, 414);
  await refreshSession({ silent: true });
  assert.deepEqual(ids(getState().session.messages), ids(msgs(0, 414)));
  assert.equal(server.pageCalls, 2);
});

test('a gap too large to bridge keeps the newest window and leaves older messages behind "load earlier"', async () => {
  const server = fakeServer('S2', { total: 2600 });
  openSession('S2', { messages: msgs(0, 39), earlierLoaded: true });
  await refreshSession({ silent: true });
  await until(() => server.pageCalls === 10 && !getState().session.messages.some((message) => message.id === 'm0'), 'the bridge gives up after 10 pages');
  const session = getState().session;
  assert.deepEqual(ids(session.messages), ids(msgs(560, 2599)), 'newest tail + 10 pages of 200');
  assert.equal(session.hasMoreEarlier, true);
  assert.equal(session.earlierLoaded, false);
});

test('generating is its own fact: a detail requested before an accepted local launch keeps the composer "starting"', async () => {
  const server = fakeServer('S3', { total: 4, detailFor: () => server.next });
  openSession('S3', { messages: msgs(0, 3), detail: { state: { status: 'awaiting_input', updated_at: '2026-11-13T12:00:00Z' } } });
  // A poll is in flight when the continue is accepted.
  server.next = { state: { status: 'awaiting_input', updated_at: '2026-11-13T12:00:00Z' }, active_handle: false, active_handle_owner: { state: 'settled' } };
  const detailGate = gate();
  server.detailGate = detailGate.promise;
  const stalePoll = refreshSession({ silent: true });
  await wait(5);
  update('session', (session) => ({ ...session, generating: true, activity: { kind: 'continue', at: Date.now(), title: { key: 'activity.continuing.title' } } }));
  detailGate.open();
  server.detailGate = null;
  await stalePoll;
  assert.equal(getState().session.generating, true, 'a detail requested before the accept cannot show the new run');
  assert.equal(composerMode(getState()).kind, 'starting');

  // Follow-up refresh: the handle exists but the runner has not claimed the session yet.
  server.next = { state: { status: 'awaiting_input', updated_at: '2026-11-13T12:00:00Z' }, active_handle: true, active_handle_owner: { state: 'current_process', owned_by_current_process: true, started_at: new Date().toISOString() } };
  await refreshSession({ silent: true });
  assert.equal(getState().session.generating, true);
  assert.equal(composerMode(getState()).kind, 'starting', 'launch window: no second continue');

  server.next = { state: { status: 'running', updated_at: new Date().toISOString() }, active_handle: true, active_handle_owner: { state: 'current_process', owned_by_current_process: true, started_at: new Date().toISOString() } };
  await refreshSession({ silent: true });
  assert.equal(composerMode(getState()).kind, 'steer');

  // The run settled after the accept: generating ends with the local activity.
  await wait(2);
  server.next = { state: { status: 'awaiting_input', updated_at: new Date().toISOString() }, active_handle: false, active_handle_owner: { state: 'settled' } };
  await refreshSession({ silent: true });
  assert.equal(getState().session.generating, false);
  assert.equal(getState().session.activity, null);
  assert.equal(composerMode(getState()).kind, 'continue');

  // An orphaned running state (no handle anywhere) still counts as live and steers.
  server.next = { state: { status: 'running', updated_at: new Date().toISOString() }, active_handle: false, active_handle_owner: { state: 'running_not_owned' } };
  await refreshSession({ silent: true });
  assert.equal(getState().session.generating, true);
  assert.equal(composerMode(getState()).kind, 'steer');
});
