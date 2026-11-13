// Session message stream view-model (src/features/session/stream/model.js).
import test from 'node:test';
import assert from 'node:assert/strict';
import '../../internal/webconsole/ui/src/i18n/index.js';
import { setLocale } from '../../internal/webconsole/ui/src/lib/i18n.js';
import { buildConversation } from '../../internal/webconsole/ui/src/lib/session-model.js';
import * as V from '../../internal/webconsole/ui/src/features/session/stream/model.js';

const at = (seconds) => new Date(Date.UTC(2026, 10, 13, 8, 0, seconds)).toISOString();
const call = (id, name, args) => ({ id, name, arguments: JSON.stringify(args) });
const result = (id, name, output, extra = {}) => ({ tool_call_id: id, name, display_output: output, ...extra });

function withLocale(locale, fn) {
  setLocale(locale, { persist: false });
  try {
    return fn();
  } finally {
    setLocale('zh-CN', { persist: false });
  }
}

const mainMessages = [
  { id: 'u1', role: 'user', text: 'E2E_UI_MAIN raw user text', created_at: at(0) },
  { id: 'a1', role: 'assistant', text: '', tool_calls: [call('c1', 'todo_write', { todos: [{ content: 'x' }] })], created_at: at(1) },
  { id: 't1', role: 'tool', tool_results: [result('c1', 'todo_write', '[{"content":"x"}]')], created_at: at(2) },
  { id: 'a2', role: 'assistant', text: '', tool_calls: [call('c2', 'shell', { command: "printf 'x' > e2e-created.txt" })], created_at: at(3) },
  { id: 't2', role: 'tool', tool_results: [result('c2', 'shell', 'ok')], created_at: at(15) },
  { id: 'a3', role: 'assistant', text: '', tool_calls: [call('c3', 'finish', { message: 'E2E main completed' })], created_at: at(16) },
  { id: 't3', role: 'tool', tool_results: [result('c3', 'finish', 'E2E main completed', { final: true })], created_at: at(16) },
];

test('tool kinds map to icons', () => {
  assert.equal(V.toolKind('shell'), 'shell');
  assert.equal(V.toolKind('read_file'), 'read');
  assert.equal(V.toolKind('edit_file'), 'write');
  assert.equal(V.toolKind('grep'), 'search');
  assert.equal(V.toolKind('agent_spawn'), 'agent');
  assert.equal(V.toolKind('record_goal_progress'), 'goal');
  assert.equal(V.toolKind('todo_write'), 'todo');
  assert.equal(V.toolKind('submit_plan'), 'plan');
  assert.equal(V.toolKind('mystery'), 'other');
  assert.equal(V.toolIcon('shell'), 'terminal');
  assert.equal(V.toolIcon('mystery'), 'wrench');
});

test('formatElapsed is localized and compact', () => {
  assert.equal(V.formatElapsed(400), '不到 1 秒');
  assert.equal(V.formatElapsed(12_400), '12 秒');
  assert.equal(V.formatElapsed(125_000), '2 分 5 秒');
  assert.equal(V.formatElapsed(3_720_000), '1 小时 2 分');
  assert.equal(V.formatElapsed(null), '');
  withLocale('en', () => {
    assert.equal(V.formatElapsed(12_400), '12s');
    assert.equal(V.formatElapsed(125_000), '2m 5s');
  });
});

test('slotText resolves nested descriptor params and keeps raw text out of t()', () => {
  withLocale('en', () => {
    const slot = { key: 'goal.tool.update', params: { status: 'complete', statusLabel: { key: 'goalStatus.complete', params: { fallback: 'Complete' } } } };
    assert.equal(V.slotText(slot), 'Mark goal as Complete');
    assert.equal(V.slotText({ raw: 'durable text' }), '');
    assert.equal(V.slotText({ key: 'tool.search.pattern', raw: 'needle' }), 'Search');
  });
});

test('conversation memo reuses items for equivalent polled windows', () => {
  let builds = 0;
  const compute = V.createConversationMemo((messages, options) => { builds += 1; return buildConversation(messages, options); });
  const optimistic = [];
  const first = compute({ messages: mainMessages, optimistic, live: false });
  const polled = compute({ messages: mainMessages.map((message) => ({ ...message })), optimistic, live: false });
  assert.equal(polled, first, 'same ids => same items');
  assert.equal(builds, 1);
  compute({ messages: mainMessages, optimistic, live: true });
  assert.equal(builds, 2, 'live flag change rebuilds');
  compute({ messages: mainMessages.slice(0, 3), optimistic, live: true });
  assert.equal(builds, 3, 'window change rebuilds');
});

test('final answer renders once; steps fold into one group with a stable signature', () => {
  const items = buildConversation(mainMessages);
  assert.deepEqual(items.map((item) => item.kind + ':' + (item.variant || item.status)), ['message:user', 'steps:ok', 'message:assistant']);
  const final = items[2];
  assert.equal(final.final, true);
  assert.equal(final.text, 'E2E main completed');
  const group = items[1];
  const summary = V.stepGroupSummary(group);
  assert.equal(summary.count, 3);
  assert.deepEqual(summary.names, ['todo_write', 'shell', 'finish']);
  assert.equal(V.stepGroupAutoOpen(summary), false);
  assert.equal(V.stepGroupLabel(summary), '已执行 3 个步骤');
  assert.equal(V.itemSignature(group), V.itemSignature(buildConversation(mainMessages.map((m) => ({ ...m })))[1]));
  assert.notEqual(V.itemSignature(group), V.itemSignature(buildConversation(mainMessages.slice(0, 4), { live: true })[1]));
  const shell = group.steps[1];
  assert.equal(V.stepDurationMs(shell), 12_000);
  assert.equal(V.stepArgumentsText(shell), "printf 'x' > e2e-created.txt");
  assert.match(V.stepArgumentsText(group.steps[0]), /"todos"/);
});

test('finish-only groups fold into the final answer; mixed groups stay', () => {
  const finishOnly = [
    { id: 'u2', role: 'user', text: 'follow-up', created_at: at(20) },
    { id: 'a9', role: 'assistant', text: '', tool_calls: [call('c9', 'finish', { message: 'done again' })], created_at: at(21) },
    { id: 't9', role: 'tool', tool_results: [result('c9', 'finish', 'done again', { final: true })], created_at: at(21) },
  ];
  const raw = buildConversation(finishOnly);
  assert.deepEqual(raw.map((item) => item.kind), ['message', 'steps', 'message']);
  const folded = V.foldFinishOnlyGroups(raw);
  assert.deepEqual(folded.map((item) => item.kind), ['message', 'message']);
  assert.equal(folded[1].final, true);
  assert.equal(folded[1].finishSteps.length, 1);
  assert.equal(folded[1].finishSteps[0].name, 'finish');
  assert.notEqual(V.itemSignature(folded[1]), V.itemSignature(raw[2]));
  const mixed = V.foldFinishOnlyGroups(buildConversation(mainMessages));
  assert.deepEqual(mixed.map((item) => item.kind), ['message', 'steps', 'message'], 'groups with other tools are kept');
  const compute = V.createConversationMemo();
  assert.deepEqual(compute({ messages: finishOnly }).map((item) => item.kind), ['message', 'message'], 'memo applies the fold');
});

test('running and failing groups auto-open', () => {
  const running = buildConversation(mainMessages.slice(0, 4), { live: true });
  const group = running[1];
  assert.equal(group.status, 'running');
  const summary = V.stepGroupSummary(group);
  assert.equal(summary.running, true);
  assert.equal(summary.current.name, 'shell');
  assert.equal(V.stepGroupAutoOpen(summary), true);
  assert.equal(V.stepGroupLabel(summary), '正在执行第 2 步');
  const now = Date.parse(at(10));
  assert.equal(V.stepGroupDurationMs(group, now), 9_000);
  const failing = buildConversation([
    mainMessages[0],
    { id: 'a1', role: 'assistant', tool_calls: [call('c1', 'shell', { command: 'false' })], created_at: at(1) },
    { id: 't1', role: 'tool', tool_results: [result('c1', 'shell', 'exit 1', { is_error: true })], created_at: at(2) },
  ]);
  const failed = V.stepGroupSummary(failing[1]);
  assert.equal(failed.errors, 1);
  assert.equal(V.stepGroupAutoOpen(failed), true);
});

test('turn helpers: latest turn messages, start time and file changes', () => {
  const messages = [
    { id: 'u0', role: 'user', text: 'old', created_at: at(0) },
    { id: 'a0', role: 'assistant', tool_calls: [call('w0', 'write_file', { path: 'old.txt', content: 'a' })], created_at: at(1) },
    { id: 't0', role: 'tool', tool_results: [result('w0', 'write_file', 'ok')], created_at: at(2) },
    { id: 'u1', role: 'user', text: 'new turn', created_at: at(10) },
    { id: 's1', role: 'user', text: 'steer', meta: { source: 'steer' }, created_at: at(11) },
    { id: 'h1', role: 'user', text: 'reminder', meta: { source: 'harness_reminder' }, created_at: at(12) },
    { id: 'a1', role: 'assistant', tool_calls: [call('e1', 'edit_file', { path: 'src/a.go', old_text: 'a', new_text: 'a\nb' }), call('w1', 'write_file', { path: 'new.txt', content: 'x\ny' })], created_at: at(13) },
    { id: 't1', role: 'tool', tool_results: [result('e1', 'edit_file', 'ok'), result('w1', 'write_file', 'ok')], created_at: at(14) },
  ];
  assert.deepEqual(V.latestTurnMessages(messages).map((m) => m.id), ['s1', 'h1', 'a1', 't1']);
  assert.equal(V.turnStartMs(messages), Date.parse(at(10)));
  assert.equal(V.turnStartMs(messages, [{ id: 'o1', role: 'user', text: 'pending', created_at: at(30), pending: true }]), Date.parse(at(30)));
  const changes = V.latestTurnFileChanges(messages);
  assert.deepEqual(changes.files.map((file) => [file.path, file.op]), [['src/a.go', 'edit'], ['new.txt', 'new']]);
  assert.equal(changes.totals.added, 3);
  assert.ok(changes.disclosure, 'partial disclosure present');
  assert.equal(V.latestTurnFileChanges(messages.slice(0, 4)).files.length, 0);
});

test('provider failures are classified like the old console', () => {
  assert.equal(V.classifyProviderFailure(''), null);
  assert.equal(V.classifyProviderFailure('provider said auth_unavailable').kind, 'authRouting');
  assert.equal(V.classifyProviderFailure('401 Unauthorized').kind, 'credentials');
  assert.equal(V.classifyProviderFailure('HTTP 429 rate_limit').kind, 'rateLimit');
  assert.equal(V.classifyProviderFailure('invalid_request: unsupported parameter').kind, 'request');
  const transport = V.classifyProviderFailure('stream disconnected', [{ type: 'provider.retry' }, { type: 'provider.retry' }]);
  assert.equal(transport.kind, 'transport');
  assert.deepEqual(transport.retries, { key: 'session.failure.transport.retries', params: { count: 2 } });
  assert.equal(V.classifyProviderFailure('server_error').kind, 'server');
  assert.equal(V.classifyProviderFailure('something else'), null);
});

test('stream end notes follow the session state', () => {
  assert.equal(V.streamEndNote(null), null);
  assert.equal(V.streamEndNote({ state: { status: 'completed' } }, { generating: true }), null);
  assert.equal(V.streamEndNote({ state: { status: 'completed', updated_at: at(5) } }).kind, 'completed');
  assert.equal(V.streamEndNote({ state: { status: 'awaiting_input' }, plan_mode: { status: 'awaiting_approval' } }), null);
  const awaiting = V.streamEndNote({ state: { status: 'awaiting_input' } }, {
    messages: [{ id: 'a', role: 'assistant', tool_calls: [call('x', 'await_input', { reason: 'Need the API key name.' })] }],
  });
  assert.equal(awaiting.kind, 'awaiting');
  assert.deepEqual(awaiting.reason, { raw: 'Need the API key name.' });
  assert.equal(V.streamEndNote({ state: { status: 'paused', pause_reason: 'manual_stop' } }).kind, 'stopped');
  assert.equal(V.streamEndNote({ state: { status: 'paused', pause_reason: 'manual_interrupt' } }).kind, 'interrupted');
  assert.deepEqual(V.streamEndNote({ state: { status: 'paused', pause_reason: 'budget_exhausted' } }).reason, { raw: 'budget_exhausted' });
  const failed = V.streamEndNote({ state: { status: 'failed', last_error: 'HTTP 429 rate_limit' }, events: [] });
  assert.equal(failed.kind, 'failed');
  assert.deepEqual(failed.error, { raw: 'HTTP 429 rate_limit' });
  assert.equal(failed.failure.kind, 'rateLimit');
  assert.equal(V.streamEndNote({ state: { status: 'running' } }), null);
});

test('live activity prefers the newest promotable event of the running turn', () => {
  const sinceMs = Date.parse(at(10));
  const detail = {
    state: { status: 'running', phase: 'tool' },
    events: [
      { id: 'e0', type: 'tool.before', time: at(0), data: { tool_name: 'old_tool' } },
      { id: 'e1', type: 'provider.call', time: at(11) },
      { id: 'e2', type: 'tool.before', time: at(12), data: { tool_name: 'shell', arguments: '{"command":"go test ./..."}' } },
      { id: 'e3', type: 'assistant.message', time: at(13) },
    ],
  };
  const activity = V.selectLiveActivity({ detail, sinceMs });
  assert.equal(activity.eventType, 'tool.before');
  assert.deepEqual(activity.subject, { raw: 'shell' });
  assert.deepEqual(activity.copy, { raw: 'go test ./...' });
  const live = V.selectLiveActivity({ detail, liveEvents: [{ type: 'provider.call' }], sinceMs });
  assert.equal(live.eventType, 'provider.call', 'relay events without time are the newest');
  const stale = V.selectLiveActivity({ detail: { state: { status: 'running', phase: 'model' }, events: [detail.events[0]] }, sinceMs });
  assert.deepEqual(stale.title, { key: 'phase.model', params: { fallback: 'Model' } }, 'falls back to the phase');
  const override = V.selectLiveActivity({ detail: { state: { status: 'queued' } }, override: { title: { key: 'activity.steerQueued.title' }, copy: { key: 'activity.steerQueued.copy' } } });
  assert.deepEqual(override.title, { key: 'activity.steerQueued.title' });
  assert.deepEqual(V.selectLiveActivity({}).title, { key: 'activity.startingTurn' });
});

test('clipText and previews', () => {
  const long = Array.from({ length: 40 }, (_, i) => 'line ' + i).join('\n');
  const clip = V.clipText(long, { maxLines: 10 });
  assert.equal(clip.truncated, true);
  assert.equal(clip.totalLines, 40);
  assert.equal(clip.text.split('\n').length, 10);
  assert.equal(V.clipText('short').truncated, false);
  assert.equal(V.clipText('x'.repeat(50), { maxChars: 10 }).text.length, 10);
  assert.equal(V.previewLine('a\n  b   c', 80), 'a b c');
  assert.equal(V.previewLine('x'.repeat(200), 10).length, 10);
});

test('display roles never mark harness reminders as the user', () => {
  assert.equal(V.displayRole('user'), 'user');
  assert.equal(V.displayRole('steer'), 'user');
  assert.equal(V.displayRole('harness_reminder'), 'harness');
  assert.equal(V.displayRole('background_results'), 'background');
  assert.equal(V.displayRole('assistant'), 'assistant');
  const items = buildConversation([{ id: 'h', role: 'user', text: 'Harness resume note: x', meta: { source: 'harness_reminder' } }]);
  assert.equal(V.displayRole(items[0].variant), 'harness');
  assert.deepEqual(items[0].actor, { key: 'actor.harness' });
});

test('metadata entries and badge tones', () => {
  assert.deepEqual(V.resultMetadataEntries({ metadata: { exit_code: 0, path: 'a.txt', nested: { x: 1 }, empty: '' } }), [['exit_code', '0'], ['path', 'a.txt']]);
  assert.deepEqual(V.resultMetadataEntries({}), []);
  assert.equal(V.badgeTone('live'), 'success');
  assert.equal(V.badgeTone('queued'), 'warning');
  assert.equal(V.badgeTone('danger'), 'danger');
  assert.equal(V.badgeTone('other'), 'neutral');
});
