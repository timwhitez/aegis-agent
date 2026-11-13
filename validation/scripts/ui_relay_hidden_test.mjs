// Relay hints never trigger REST refreshes while the page is hidden
// (spec/17-web-console-frontend.md §5 "页面隐藏时暂停"); visibilitychange
// refreshes on resume. Drives the real state/actions.js with a stubbed fetch.
import test from 'node:test';
import assert from 'node:assert/strict';
import { getState, update } from '../../internal/webconsole/ui/src/state/store.js';
import { applyRelayFrame, queueRefresh, setPageVisible, setRelayStatus } from '../../internal/webconsole/ui/src/state/actions.js';

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const calls = [];
globalThis.fetch = async (url) => {
  calls.push(String(url));
  return { ok: true, status: 200, statusText: 'OK', json: async () => ({ state: { status: 'running' }, messages: [], recent_sessions: [] }) };
};

function frames() {
  applyRelayFrame({ type: 'engine_event', payload: { session_id: 'S', type: 'tool.completed' } });
  applyRelayFrame({ type: 'status', payload: { session_id: 'S', status: 'running' } });
  applyRelayFrame({ type: 'session', payload: { session_id: 'S' } });
  applyRelayFrame({ type: 'message', payload: { session_id: 'S' } });
}

test('relay frames and relay reconnects do not refresh while hidden; they do again once visible', async () => {
  update('route', () => ({ name: 'session', path: '/s/S', params: { id: 'S' } }));
  update('session', (session) => ({ ...session, id: 'S', detail: { state: { status: 'running' } } }));
  update('overview', (overview) => ({ ...overview, data: { recent_sessions: [] } }));
  update('connection', { relay: 'open' });

  setPageVisible(false);
  for (let i = 0; i < 3; i += 1) {
    frames();
    await wait(100);
  }
  setRelayStatus('closed');
  setRelayStatus('open');
  queueRefresh('overview', 10);
  await wait(400);
  assert.deepEqual(calls, [], 'no REST call from relay hints while hidden');
  assert.ok(getState().session.liveEvents.length >= 3, 'frames still update in-memory live events');

  // A refresh queued while visible is dropped when the page hides before it fires.
  setPageVisible(true);
  frames();
  setPageVisible(false);
  await wait(400);
  assert.deepEqual(calls, [], 'pending relay refreshes are cancelled when the page hides');

  setPageVisible(true);
  frames();
  await wait(400);
  assert.ok(calls.includes('/api/sessions/S?limit=40'), 'session refresh resumes when visible: ' + calls.join(', '));
  assert.ok(calls.includes('/api/overview'), 'overview refresh resumes when visible');
});
