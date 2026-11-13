import test from 'node:test';
import assert from 'node:assert/strict';
import {
  HISTORY_PAGE_SIZE,
  STATUS_FILTERS,
  agentLabel,
  ancestorIds,
  buildSessionTree,
  clampPage,
  filterHistory,
  isActiveSummary,
  isChildSummary,
  isFiltering,
  matchesQuery,
  matchesStatus,
  mutationError,
  pageForIndex,
  pageRange,
  paginate,
  providerModelLabel,
  statusFilter,
} from '../../internal/webconsole/ui/src/features/history/history-model.js';
import { filterSessions, summaryTitle } from '../../internal/webconsole/ui/src/lib/session-list.js';
import { dictionaries } from '../../internal/webconsole/ui/src/i18n/index.js';

const s = (id, extra = {}) => ({ id, status: 'completed', updated_at: '2026-11-13T10:00:00Z', ...extra });

test('summary title prefers the server title and search matches it', () => {
  const item = s('root_a', { title: 'Fix the login bug', goal_objective: 'Goal text', agent_name: 'main' });
  assert.equal(summaryTitle(item, { root_a: 'Learned prompt' }), 'Fix the login bug');
  assert.equal(summaryTitle(s('root_b', { goal_objective: 'Goal text' }), { root_b: 'Learned prompt' }), 'Learned prompt');
  assert.equal(summaryTitle(s('root_c', { goal_objective: 'Goal text' })), 'Goal text');
  assert.deepEqual(filterSessions([item, s('root_z')], 'LOGIN').map((entry) => entry.id), ['root_a']);
});

test('status filters map display statuses and every label key exists', () => {
  assert.deepEqual(STATUS_FILTERS.map((filter) => filter.id), ['all', 'active', 'completed', 'failed', 'stopped']);
  for (const filter of STATUS_FILTERS) {
    for (const locale of ['zh-CN', 'en']) assert.ok(dictionaries[locale][filter.labelKey], `${locale} ${filter.labelKey}`);
  }
  assert.equal(statusFilter('nope').id, 'all');
  assert.ok(matchesStatus(s('a', { status: 'running' }), 'active'));
  assert.ok(matchesStatus(s('a', { status: 'completed', plan_mode_status: 'awaiting_approval' }), 'active'), 'plan-mode actions count as active');
  assert.ok(matchesStatus(s('a', { status: 'error' }), 'failed'));
  assert.ok(matchesStatus(s('a', { status: 'cancelled' }), 'stopped'));
  assert.ok(!matchesStatus(s('a', { status: 'completed' }), 'failed'));
  assert.ok(matchesStatus(s('a', { status: 'whatever' }), 'all'));
  assert.ok(isActiveSummary(s('a', { status: 'queued' })));
  assert.ok(!isActiveSummary(s('a')));
});

test('query matching covers title, id, workdir, agent and provider/model', () => {
  const item = s('sess_123', { title: 'Résumé parser', workdir: '/w/proj', agent_name: 'reviewer', agent_role: 'evaluator', provider: 'openai', model: 'gpt-5.4' });
  for (const query of ['resume', 'SESS_1', '/w/proj', 'reviewer', 'evaluator', 'openai', 'gpt-5', '']) {
    assert.ok(matchesQuery(item, query), query);
  }
  assert.ok(!matchesQuery(item, 'missing'));
  assert.ok(matchesQuery(s('x'), 'learned', { x: 'Learned prompt' }));
  assert.equal(isFiltering({ query: '  ' }), false);
  assert.equal(isFiltering({ query: 'a' }), true);
  assert.equal(isFiltering({ status: 'failed' }), true);
  const list = [s('a', { title: 'alpha', status: 'failed' }), s('b', { title: 'beta' }), null, { title: 'no id' }];
  assert.deepEqual(filterHistory(list, { query: 'a', status: 'failed' }).map((item) => item.id), ['a']);
  assert.deepEqual(filterHistory(list).map((item) => item.id), ['a', 'b']);
});

test('tree nests children under parents on the same page and tolerates cycles', () => {
  const items = [
    s('root'),
    s('child_1', { parent_session_id: 'root', root_session_id: 'root', depth: 1 }),
    s('grand', { parent_session_id: 'child_1', root_session_id: 'root', depth: 2 }),
    s('orphan', { parent_session_id: 'elsewhere', root_session_id: 'elsewhere', depth: 1 }),
    s('loop_a', { parent_session_id: 'loop_b' }),
    s('loop_b', { parent_session_id: 'loop_a' }),
  ];
  const tree = buildSessionTree(items);
  // Parent cycles (malformed data) never nest: both members stay top-level.
  assert.deepEqual(tree.map((node) => node.item.id), ['root', 'orphan', 'loop_a', 'loop_b']);
  assert.deepEqual(tree[0].children.map((node) => node.item.id), ['child_1']);
  assert.deepEqual(tree[0].children[0].children.map((node) => node.item.id), ['grand']);
  assert.deepEqual(tree[2].children, []);
  assert.deepEqual(ancestorIds(items, 'grand'), ['child_1', 'root']);
  assert.deepEqual(ancestorIds(items, 'orphan'), []);
  assert.ok(isChildSummary(items[3]));
  assert.ok(!isChildSummary(items[0]));
});

test('paging helpers', () => {
  const items = Array.from({ length: 45 }, (_, index) => s(`s${index}`));
  const page3 = paginate(items, 3, 20);
  assert.equal(page3.items.length, 5);
  assert.deepEqual([page3.page, page3.totalPages, page3.total, page3.start, page3.end], [3, 3, 45, 41, 45]);
  assert.equal(paginate(items, 99, 20).page, 3, 'clamped to the last page');
  assert.deepEqual(paginate([], 2).items, []);
  assert.equal(paginate([], 2).page, 1);
  assert.deepEqual(pageRange({ page: 2, pageSize: 20, total: 45, count: 20 }), { start: 21, end: 40 });
  assert.deepEqual(pageRange({ total: 0, count: 0 }), { start: 0, end: 0 });
  assert.equal(clampPage('x', 4), 1);
  assert.equal(clampPage(7, 4), 4);
  assert.equal(pageForIndex(0), 1);
  assert.equal(pageForIndex(HISTORY_PAGE_SIZE), 2);
  assert.equal(pageForIndex(-1), 1);
});

test('row labels and mutation error classification', () => {
  assert.equal(providerModelLabel(s('a', { provider: 'openai', model: 'gpt-5.4' })), 'openai · gpt-5.4');
  assert.equal(providerModelLabel(s('a', { model: 'gpt-5.4' })), 'gpt-5.4');
  assert.equal(agentLabel(s('a', { agent_name: 'e2e-child', agent_role: 'evaluator' })), 'e2e-child · evaluator');
  assert.equal(agentLabel(s('a', { agent_role: 'evaluator' })), 'evaluator');
  assert.equal(agentLabel(s('a')), '');
  assert.deepEqual(mutationError({ status: 409, message: 'cannot delete an active session tree' }), {
    kind: 'conflict', message: 'cannot delete an active session tree', detail: '', action: '', status: 409,
  });
  assert.equal(mutationError({ status: 404 }).kind, 'missing');
  assert.equal(mutationError(new Error('boom')).kind, 'error');
});
