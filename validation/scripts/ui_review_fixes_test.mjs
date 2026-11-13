import test from 'node:test';
import assert from 'node:assert/strict';
import { bridgeMessageGap, mergeMessageWindows, windowsDisjoint } from '../../internal/webconsole/ui/src/lib/session-list.js';
import { createComposerDrafts } from '../../internal/webconsole/ui/src/state/composer-drafts.js';

const msgs = (from, to) => Array.from({ length: to - from + 1 }, (_, index) => ({ id: 'm' + (from + index), role: 'assistant', text: String(from + index) }));
const ids = (list) => list.map((message) => message.id);

// Simulated server history m0..m199 with before_id paging.
function pager(total = 200) {
  const history = msgs(0, total - 1);
  const calls = [];
  return {
    calls,
    fetchPage: async (beforeId, limit = 40) => {
      calls.push(beforeId);
      const end = history.findIndex((message) => message.id === beforeId);
      const start = Math.max(0, end - limit);
      return { messages: history.slice(start, end), has_more: start > 0 };
    },
  };
}

test('windowsDisjoint detects tails that share no id with the loaded window', () => {
  assert.equal(windowsDisjoint(msgs(20, 99), msgs(160, 199)), true);
  assert.equal(windowsDisjoint(msgs(20, 99), msgs(90, 129)), false);
  assert.equal(windowsDisjoint([], msgs(0, 39)), false);
  assert.equal(windowsDisjoint(msgs(0, 9), []), false);
});

test('a non-overlapping tail is bridged by paging back to the loaded window', async () => {
  const { fetchPage, calls } = pager();
  const result = await bridgeMessageGap({ known: msgs(20, 99), tail: msgs(160, 199), fetchPage: (id) => fetchPage(id, 40), maxPages: 10 });
  assert.equal(result.bridged, true);
  assert.equal(result.reachedStart, false);
  assert.deepEqual(ids(result.messages), ids(msgs(20, 199)), 'no message between m99 and m160 is missing');
  assert.deepEqual(calls, ['m160', 'm120']);
});

test('reaching the history start keeps the full fetched history', async () => {
  const { fetchPage } = pager(60);
  const result = await bridgeMessageGap({ known: [{ id: 'gone', role: 'user', text: 'x' }], tail: msgs(40, 59), fetchPage: (id) => fetchPage(id, 40) });
  assert.equal(result.bridged, true);
  assert.equal(result.reachedStart, true);
  assert.deepEqual(ids(result.messages), ids(msgs(0, 59)));
});

test('an unbridgeable gap reports not bridged and keeps only fetched messages', async () => {
  const { fetchPage } = pager();
  const result = await bridgeMessageGap({ known: msgs(0, 9), tail: msgs(180, 199), fetchPage: (id) => fetchPage(id, 10), maxPages: 2 });
  assert.equal(result.bridged, false);
  assert.deepEqual(ids(result.messages), ids(msgs(160, 199)));
});

test('mergeMessageWindows still dedupes overlapping windows', () => {
  assert.deepEqual(ids(mergeMessageWindows(msgs(0, 5), msgs(3, 8))), ids(msgs(0, 8)));
});

test('newSession keeps the open session draft and resets only the new-session draft', () => {
  let state = { composer: { text: '', goalEnabled: false, planEnabled: false, interrupt: false, provider: '', model: '' } };
  const getState = () => state;
  const update = (slice, value) => {
    const next = typeof value === 'function' ? value(state[slice]) : { ...state[slice], ...value };
    state = { ...state, [slice]: next };
  };
  let n = 0;
  const drafts = createComposerDrafts({ getState, update, random: () => ((n += 1) % 10) / 10 });
  drafts.syncRoute({ name: 'session', params: { id: 'sess_a' } });
  update('composer', { text: 'unsent for A\n  keep spaces', planEnabled: true });
  drafts.resetNewSession();
  update('composer', { text: '', planEnabled: false });
  drafts.syncRoute({ name: 'new', params: {} });
  update('composer', { text: 'new session draft' });
  drafts.syncRoute({ name: 'session', params: { id: 'sess_a' } });
  assert.equal(state.composer.text, 'unsent for A\n  keep spaces');
  assert.equal(state.composer.planEnabled, true);
  // A second newSession while on the new-session composer drops that draft.
  drafts.syncRoute({ name: 'new', params: {} });
  drafts.resetNewSession();
  assert.equal(drafts.draftFor(drafts.currentKey()).text, 'new session draft', 'live composer is cleared by actions.newSession, not by the stash');
  update('composer', { text: '' });
  drafts.syncRoute({ name: 'session', params: { id: 'sess_a' } });
  assert.equal(state.composer.text, 'unsent for A\n  keep spaces', 'session A draft survives repeated new sessions');
});
