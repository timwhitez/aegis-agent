import test, { beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { createStore, shallowEqual } from '../../internal/webconsole/ui/src/lib/store.js';
import { filterSessions, firstPromptFromMessages, groupSessions, mergeMessageWindows, rootSessions, summaryTitle } from '../../internal/webconsole/ui/src/lib/session-list.js';
import { parseRoute } from '../../internal/webconsole/ui/src/state/router.js';
import { createPoller, shouldPollOverview, shouldPollSession, POLL_OVERVIEW_MS, POLL_SESSION_MS } from '../../internal/webconsole/ui/src/state/polling.js';
import { backoffDelay, createRelay, parseRelayFrame, relayHints } from '../../internal/webconsole/ui/src/state/relay.js';
import { emptySessionSlice, getState, initialState, setState } from '../../internal/webconsole/ui/src/state/store.js';
import * as actions from '../../internal/webconsole/ui/src/state/actions.js';
import { confirmDialog, promptDialog, resolveDialog } from '../../internal/webconsole/ui/src/state/dialogs.js';
import '../../internal/webconsole/ui/src/i18n/index.js';

test('createStore merges partial state, updates slices and notifies subscribers', () => {
  const store = createStore({ a: { x: 1, y: 2 }, b: 1 });
  const seen = [];
  const off = store.subscribe((state, prev) => seen.push([prev.b, state.b]));
  store.setState({ b: 2 });
  store.update('a', { y: 3 });
  assert.deepEqual(store.getState(), { a: { x: 1, y: 3 }, b: 2 });
  const before = store.getState();
  store.setState((state) => state);
  assert.equal(store.getState(), before, 'returning the same state is a no-op');
  store.update('a', (slice) => slice);
  assert.equal(seen.length, 2);
  off();
  store.setState({ b: 3 });
  assert.equal(seen.length, 2);
});

test('select only fires when the selected value changes under equality', () => {
  const store = createStore({ list: [1], n: 0 });
  const changes = [];
  store.select((state) => ({ first: state.list[0] }), (next, prev) => changes.push([prev.first, next.first]), shallowEqual);
  store.setState({ n: 1 });
  store.setState({ list: [1, 2] });
  store.setState({ list: [5] });
  assert.deepEqual(changes, [[1, 5]]);
  assert.ok(shallowEqual({ a: 1 }, { a: 1 }));
  assert.ok(!shallowEqual({ a: 1 }, { a: 1, b: 2 }));
  assert.ok(shallowEqual([1, 2], [1, 2]));
  assert.ok(!shallowEqual([1], { 0: 1 }));
});

test('router recognises the spec routes and rejects unknown ones', () => {
  assert.deepEqual(parseRoute('/'), { name: 'new', path: '/', params: {} });
  assert.deepEqual(parseRoute('/s/abc%2F1'), { name: 'session', path: '/s/abc%2F1', params: { id: 'abc/1' } });
  assert.equal(parseRoute('/s/abc/').name, 'session');
  for (const page of ['sessions', 'skills', 'workspace', 'settings']) assert.equal(parseRoute(`/${page}`).name, page);
  for (const path of ['/legacy/', '/s/', '/unknown', '/sessions/x', '/s/%E0%A4%A']) assert.equal(parseRoute(path), null, path);
});

test('session list: roots only, titles, search and day groups with attention pinned', () => {
  const now = new Date(2026, 10, 13, 15, 0, 0).getTime();
  const at = (days, hours = 0) => new Date(now - days * 86400000 - hours * 3600000).toISOString();
  const items = [
    { id: 'root_a', status: 'completed', updated_at: at(0, 1), goal_objective: 'Ship the console', workdir: '/w/a' },
    { id: 'child', status: 'running', parent_session_id: 'root_a', root_session_id: 'root_a', updated_at: at(0) },
    { id: 'root_b', status: 'running', updated_at: at(3) },
    { id: 'root_c', status: 'paused', plan_mode_status: 'awaiting_approval', updated_at: at(20) },
    { id: 'root_d', status: 'failed', updated_at: at(1, 1), agent_name: 'reviewer' },
    { id: 'root_e', status: 'completed', updated_at: at(4) },
    { id: 'root_f', status: 'completed', updated_at: at(30) },
  ];
  const roots = rootSessions(items);
  assert.deepEqual(roots.map((item) => item.id), ['root_a', 'root_b', 'root_c', 'root_d', 'root_e', 'root_f']);
  assert.deepEqual(groupSessions(roots, now).map((group) => [group.key, group.items.map((item) => item.id)]), [
    ['attention', ['root_b', 'root_c']],
    ['today', ['root_a']],
    ['yesterday', ['root_d']],
    ['week', ['root_e']],
    ['older', ['root_f']],
  ]);
  assert.equal(summaryTitle(roots[0]), 'Ship the console');
  assert.equal(summaryTitle(roots[3]), 'reviewer');
  assert.equal(summaryTitle(roots[4]), '');
  assert.equal(summaryTitle(roots[4], { root_e: 'Fix the tests\nplease' }), 'Fix the tests');
  assert.deepEqual(filterSessions(roots, 'SHIP').map((item) => item.id), ['root_a']);
  assert.deepEqual(filterSessions(roots, '/w/a').map((item) => item.id), ['root_a']);
  assert.deepEqual(filterSessions(roots, 'root_f').map((item) => item.id), ['root_f']);
  assert.equal(filterSessions(roots, '').length, roots.length);
});

test('message windows merge by id without dropping earlier pages', () => {
  const m = (id, text = id) => ({ id, role: 'user', text });
  const tail = [m('3'), m('4')];
  const polled = mergeMessageWindows(tail, [m('4', 'four v2'), m('5')]);
  assert.deepEqual(polled.map((x) => [x.id, x.text]), [['3', '3'], ['4', 'four v2'], ['5', '5']]);
  const withEarlier = mergeMessageWindows([m('1'), m('2'), m('3')], polled);
  assert.deepEqual(withEarlier.map((x) => x.id), ['1', '2', '3', '4', '5']);
  const afterPoll = mergeMessageWindows(withEarlier, [m('5'), m('6')]);
  assert.deepEqual(afterPoll.map((x) => x.id), ['1', '2', '3', '4', '5', '6']);
  assert.equal(firstPromptFromMessages([{ role: 'assistant', text: 'hi' }, m('1', 'first prompt\nmore')], false), 'first prompt');
  assert.equal(firstPromptFromMessages([m('1', 'later steer')], true), '');
});

function stateWith(patch = {}) {
  const base = initialState();
  return {
    ...base,
    connection: { ...base.connection, relay: 'open', pageVisible: true, ...(patch.connection || {}) },
    session: { ...base.session, ...(patch.session || {}) },
    overview: { ...base.overview, ...(patch.overview || {}) },
  };
}

test('polling runs only while needed and never while hidden', () => {
  const idleDetail = { state: { status: 'completed' }, active_handle: false, children: { sessions: [], jobs: [] } };
  const quietOverview = { recent_sessions: [{ id: 'a', status: 'completed' }] };
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: idleDetail } })), false);
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: null } })), true, 'until first detail');
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: idleDetail, generating: true } })), true);
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: { ...idleDetail, active_handle: true } } })), true);
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: { ...idleDetail, children: { sessions: [{ status: 'running' }] } } } })), true);
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: idleDetail }, connection: { relay: 'closed' } })), true, 'relay down');
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', detail: null }, connection: { pageVisible: false } })), false, 'hidden');
  assert.equal(shouldPollSession(stateWith({ session: { id: 's', missing: true } })), false, 'missing');
  assert.equal(shouldPollSession(stateWith({ session: { id: '' } })), false);

  assert.equal(shouldPollOverview(stateWith({ overview: { data: null } })), true);
  assert.equal(shouldPollOverview(stateWith({ overview: { data: quietOverview } })), false);
  assert.equal(shouldPollOverview(stateWith({ overview: { data: { recent_sessions: [{ id: 'a', status: 'running' }] } } })), true, 'live sidebar status');
  assert.equal(shouldPollOverview(stateWith({ overview: { data: quietOverview }, connection: { relay: 'closed' } })), true);
  assert.equal(shouldPollOverview(stateWith({ overview: { data: null }, connection: { pageVisible: false } })), false);
  assert.equal(POLL_SESSION_MS, 1500);
  assert.equal(POLL_OVERVIEW_MS, 2000);
});

test('createPoller schedules one request per channel and stops when no longer needed', async () => {
  const timers = [];
  const fake = {
    setTimeout(fn, ms) { const timer = { fn, ms, cleared: false }; timers.push(timer); return timer; },
    clearTimeout(timer) { if (timer) timer.cleared = true; },
  };
  let state = stateWith({ session: { id: 's', detail: null }, overview: { data: null } });
  const listeners = new Set();
  const calls = [];
  const poller = createPoller({
    getState: () => state,
    subscribe: (fn) => { listeners.add(fn); return () => listeners.delete(fn); },
    refreshSession: async () => { calls.push('session'); },
    refreshOverview: async () => { calls.push('overview'); },
    timers: fake,
  });
  const active = () => timers.filter((timer) => !timer.cleared && !timer.fired);
  assert.deepEqual(active().map((timer) => timer.ms).sort(), [1500, 2000]);
  poller.sync();
  assert.equal(active().length, 2, 'sync does not double-schedule');
  const sessionTimer = active().find((timer) => timer.ms === 1500);
  sessionTimer.fired = true;
  await sessionTimer.fn();
  assert.deepEqual(calls, ['session']);
  assert.equal(active().filter((timer) => timer.ms === 1500).length, 1, 'rescheduled after the request settles');
  state = stateWith({ session: { id: 's', detail: { state: { status: 'completed' } } }, overview: { data: { recent_sessions: [] } } });
  listeners.forEach((fn) => fn());
  assert.equal(active().length, 0, 'idle state stops both channels');
  poller.stop();
});

test('relay hints translate frames into refresh hints and live events', () => {
  assert.deepEqual(relayHints({ type: 'message', payload: { sessionId: 's1' } }, 's1').refreshSession, 120);
  assert.equal(relayHints({ type: 'message', payload: { sessionId: 'other' } }, 's1').refreshSession, null);
  const status = relayHints({ type: 'status', payload: { sessionId: 's1', status: 'running' } }, 's1');
  assert.equal(status.generating, true);
  assert.equal(status.refreshOverview, 260);
  const otherStatus = relayHints({ type: 'status', payload: { sessionId: 'x', status: 'running' } }, 's1');
  assert.equal(otherStatus.generating, undefined);
  assert.equal(otherStatus.refreshOverview, 260, 'sidebar still refreshes');
  const event = relayHints({ type: 'engine_event', payload: { sessionId: 's1', type: 'tool.completed' } }, 's1');
  assert.deepEqual(event.liveEvent, { sessionId: 's1', type: 'tool.completed' });
  assert.equal(event.refreshSession, 220);
  const error = relayHints({ type: 'error', payload: { sessionId: 's1', content: 'boom' } }, 's1');
  assert.equal(error.error, 'boom');
  assert.equal(error.generating, false);
  assert.equal(relayHints({ type: 'error', payload: { sessionId: 'x', content: 'other' } }, 's1').error, '');
  assert.deepEqual(relayHints(null, 's1').refreshSession, null);
  assert.throws(() => parseRelayFrame('{not json'));
  assert.throws(() => parseRelayFrame('{"payload":{}}'));
  assert.equal(parseRelayFrame('{"type":"status"}').type, 'status');
  assert.equal(backoffDelay(0, () => 0.5), 1000);
  assert.equal(backoffDelay(3, () => 0.5), 8000);
  assert.equal(backoffDelay(20, () => 0.5), 15000);
});

test('createRelay reports status, survives malformed frames and reconnects with backoff', () => {
  const sockets = [];
  class FakeSocket {
    constructor(url) { this.url = url; this.readyState = 0; sockets.push(this); }
    close() { this.readyState = 3; }
  }
  const timers = [];
  const statuses = [];
  const frames = [];
  const malformed = [];
  const relay = createRelay({
    url: 'ws://host/ws',
    WebSocketImpl: FakeSocket,
    onStatus: (status) => statuses.push(status),
    onMessage: (frame) => frames.push(frame),
    onMalformed: (error) => malformed.push(error),
    timers: { setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clearTimeout() {} },
  });
  relay.connect();
  sockets[0].readyState = 1;
  sockets[0].onopen();
  sockets[0].onmessage({ data: '{"type":"message","payload":{"sessionId":"s"}}' });
  assert.doesNotThrow(() => sockets[0].onmessage({ data: '<<garbage' }));
  sockets[0].onclose();
  assert.deepEqual(statuses, ['connecting', 'open', 'closed']);
  assert.equal(frames.length, 1);
  assert.equal(malformed.length, 1);
  assert.equal(timers.length, 1);
  timers[0].fn();
  assert.equal(sockets.length, 2, 'reconnected');
  relay.close();
});

test('opening a missing session toasts once, returns to / and stops polling it', async () => {
  globalThis.fetch = async () => ({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({ error: 'session not found' }) });
  setState({ ...initialState() });
  actions.syncSessionForRoute({ name: 'session', path: '/s/gone', params: { id: 'gone' } });
  setState((state) => ({ ...state, route: { name: 'session', path: '/s/gone', params: { id: 'gone' } } }));
  await actions.refreshSession();
  let state = getState();
  assert.equal(state.route.name, 'new');
  assert.equal(state.toasts.filter((toast) => toast.message.includes('gone')).length, 1);
  assert.equal(shouldPollSession({ ...state, connection: { ...state.connection, relay: 'closed' } }), false);
  actions.syncSessionForRoute({ name: 'session', path: '/s/gone', params: { id: 'gone' } });
  await actions.refreshSession();
  state = getState();
  assert.equal(state.toasts.filter((toast) => toast.message.includes('gone')).length, 1, 'toast only once');
  state.toasts.forEach((toast) => actions.navigate && toast);
});

test('refreshSession merges the polled tail with earlier pages and learns the title', async () => {
  const responses = {
    '/api/sessions/s1?limit=40': () => ({ metadata: { id: 's1' }, state: { status: 'completed' }, messages: [{ id: 'm3', role: 'assistant', text: 'c' }, { id: 'm4', role: 'user', text: 'd' }], has_more_messages: true }),
    '/api/sessions/s1/messages?before_id=m3&limit=40': () => ({ messages: [{ id: 'm1', role: 'user', text: 'Original prompt' }, { id: 'm2', role: 'assistant', text: 'b' }], has_more: false }),
  };
  globalThis.fetch = async (url) => ({ ok: true, status: 200, json: async () => responses[url]() });
  setState({ ...initialState(), session: emptySessionSlice('s1') });
  await actions.refreshSession();
  assert.deepEqual(getState().session.messages.map((m) => m.id), ['m3', 'm4']);
  assert.equal(getState().session.hasMoreEarlier, true);
  await actions.loadEarlierMessages();
  assert.deepEqual(getState().session.messages.map((m) => m.id), ['m1', 'm2', 'm3', 'm4']);
  assert.equal(getState().session.hasMoreEarlier, false);
  assert.equal(getState().overview.titles.s1, 'Original prompt');
  await actions.refreshSession();
  assert.deepEqual(getState().session.messages.map((m) => m.id), ['m1', 'm2', 'm3', 'm4'], 'polling keeps earlier pages');
  assert.equal(getState().session.hasMoreEarlier, false);
});

test('dialog service resolves confirm and prompt promises', async () => {
  setState({ ...initialState() });
  const confirmed = confirmDialog({ title: 'Delete?', danger: true });
  const [confirm] = getState().dialogs;
  assert.equal(confirm.kind, 'confirm');
  resolveDialog(confirm.id, true);
  assert.equal(await confirmed, true);
  const prompted = promptDialog({ title: 'Name', initialValue: 'a' });
  const [prompt] = getState().dialogs;
  assert.equal(prompt.initialValue, 'a');
  resolveDialog(prompt.id, null);
  assert.equal(await prompted, null);
  assert.equal(getState().dialogs.length, 0);
});

beforeEach(() => {
  delete globalThis.fetch;
});
