import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import * as M from '../../internal/webconsole/ui/src/lib/session-model.js';

const ids = (list) => list.map((message) => message.id);
const lines = (...parts) => parts.join('\n');

// ---------------------------------------------------------------------------
// Message windows (ported from webconsole_utils_test.mjs)
// ---------------------------------------------------------------------------

test('mergeMessageWindows preserves older loaded messages when server tail overlaps', () => {
  const merged = M.mergeMessageWindows(['m1', 'm2', 'm3', 'm4'].map((id) => ({ id })), ['m3', 'm4', 'm5'].map((id) => ({ id })));
  assert.deepEqual(ids(merged.messages), ['m1', 'm2', 'm3', 'm4', 'm5']);
  assert.equal(merged.hasGap, false);
  assert.equal(merged.gapAnchorId, '');
});

test('mergeMessageWindows exposes a paging gap when server tail no longer overlaps loaded history', () => {
  const merged = M.mergeMessageWindows(['m1', 'm2', 'm3'].map((id) => ({ id })), ['m8', 'm9'].map((id) => ({ id })));
  assert.deepEqual(ids(merged.messages), ['m1', 'm2', 'm3', 'm8', 'm9']);
  assert.equal(merged.hasGap, true);
  assert.equal(merged.gapAnchorId, 'm8');
});

test('mergeMessagesBeforeAnchor fills middle pages before the server tail anchor', () => {
  const merged = M.mergeMessagesBeforeAnchor(['m1', 'm2', 'm8', 'm9'].map((id) => ({ id })), ['m2', 'm3', 'm4', 'm5'].map((id) => ({ id })), 'm8');
  assert.deepEqual(ids(merged), ['m1', 'm2', 'm3', 'm4', 'm5', 'm8', 'm9']);
});

test('earlier page preserves a polling-created gap while it was in flight', () => {
  const request = M.earlierMessagesRequest({ hasMore: true, oldestId: 'm41', loadedAll: false, gapAnchorId: '' });
  assert.deepEqual(request, { beforeId: 'm41', fillingGap: false });
  const detail = { metadata: { id: 's' }, timeline: [], messages: ['m41', 'm42', 'm50', 'm51'].map((id) => ({ id, role: 'assistant', text: id })) };
  const paging = { hasMore: true, oldestId: 'm41', loadedAll: false, gapAnchorId: 'm50' };
  const result = M.applyEarlierMessagesPage(detail, paging, request, { messages: [{ id: 'm39', role: 'assistant', text: 'older 39' }, { id: 'm40', role: 'assistant', text: 'older 40' }], has_more: false });
  assert.deepEqual(ids(result.detail.messages), ['m39', 'm40', 'm41', 'm42', 'm50', 'm51']);
  assert.equal(result.paging.hasMore, true);
  assert.equal(result.paging.loadedAll, false);
  assert.equal(result.paging.gapAnchorId, 'm50');
  assert.equal(result.paging.oldestId, 'm39');
  assert.deepEqual(ids(detail.messages), ['m41', 'm42', 'm50', 'm51'], 'inputs are not mutated');
});

test('filling a gap keeps the gap anchor until the server reports no more', () => {
  const detail = { metadata: { id: 's' }, messages: ['m1', 'm8', 'm9'].map((id) => ({ id })) };
  const paging = { hasMore: true, oldestId: 'm1', loadedAll: false, gapAnchorId: 'm8' };
  const request = M.earlierMessagesRequest(paging);
  assert.deepEqual(request, { beforeId: 'm8', fillingGap: true });
  const partial = M.applyEarlierMessagesPage(detail, paging, request, { messages: [{ id: 'm6' }, { id: 'm7' }], has_more: true });
  assert.deepEqual(ids(partial.detail.messages), ['m1', 'm6', 'm7', 'm8', 'm9']);
  assert.equal(partial.paging.gapAnchorId, 'm6');
  const done = M.applyEarlierMessagesPage(partial.detail, partial.paging, M.earlierMessagesRequest(partial.paging), { messages: [{ id: 'm2' }], has_more: false });
  assert.equal(done.paging.gapAnchorId, '');
  assert.equal(done.paging.loadedAll, true);
  assert.equal(M.earlierMessagesRequest(done.paging), null);
  const empty = M.applyEarlierMessagesPage(detail, paging, request, { messages: [] });
  assert.deepEqual(empty.paging, { ...paging, hasMore: false, loadedAll: true, gapAnchorId: '' });
});

test('polling refresh keeps earlier-loaded messages and dedupes the timeline by message id', () => {
  const previous = {
    metadata: { id: 's1' },
    messages: [{ id: 'm1', role: 'user', text: 'old prompt', created_at: '2026-01-01T00:00:01Z' }, { id: 'm2', role: 'assistant', text: 'old answer', created_at: '2026-01-01T00:00:02Z' }],
    timeline: []
  };
  const next = {
    metadata: { id: 's1' },
    has_more_messages: true,
    messages: [{ id: 'm2', role: 'assistant', text: 'old answer', created_at: '2026-01-01T00:00:02Z' }, { id: 'm3', role: 'assistant', text: 'new', created_at: '2026-01-01T00:00:03Z' }],
    timeline: [
      { kind: 'message', message_id: 'm3', time: '2026-01-01T00:00:03Z', role: 'assistant', text: 'new' },
      { kind: 'event', event_id: 'e1', event_type: 'provider.call', time: '2026-01-01T00:00:02.5Z' }
    ]
  };
  const first = M.applySessionDetailRefresh(previous, next, { hasMore: true, oldestId: 'm1', loadedAll: true, gapAnchorId: '' });
  assert.deepEqual(ids(first.detail.messages), ['m1', 'm2', 'm3']);
  assert.equal(first.paging.oldestId, 'm1');
  assert.equal(first.paging.hasMore, false, 'loadedAll suppresses has_more_messages');
  const messageEntries = first.detail.timeline.filter((item) => item.kind === 'message').map((item) => item.message_id);
  assert.deepEqual(messageEntries, ['m3', 'm2', 'm1']);
  assert.equal(first.detail.timeline.length, 4);

  const other = M.applySessionDetailRefresh(previous, { metadata: { id: 's2' }, messages: [{ id: 'x1' }] }, first.paging);
  assert.deepEqual(ids(other.detail.messages), ['x1'], 'another session never inherits loaded messages');
  assert.equal(other.paging.loadedAll, false);
});

test('mergeLoadedMessagesIntoDetail reports gaps and keeps an existing anchor while it is loaded', () => {
  const gap = M.mergeLoadedMessagesIntoDetail([{ id: 'a' }, { id: 'b' }], { messages: [{ id: 'x' }] }, M.createMessagePaging());
  assert.equal(gap.paging.gapAnchorId, 'x');
  assert.deepEqual(ids(gap.detail.messages), ['a', 'b', 'x']);
  const keep = M.mergeLoadedMessagesIntoDetail(gap.detail.messages, { messages: [{ id: 'x' }, { id: 'y' }] }, gap.paging);
  assert.equal(keep.paging.gapAnchorId, 'x');
  const cleared = M.mergeLoadedMessagesIntoDetail([{ id: 'a' }], { messages: [{ id: 'a' }, { id: 'b' }] }, { gapAnchorId: 'zz' });
  assert.equal(cleared.paging.gapAnchorId, '');
});

// ---------------------------------------------------------------------------
// Display stream, harness reminders, finish, thinking
// ---------------------------------------------------------------------------

test('tool results merge into the matching assistant call by tool_call_id or provider_call_id', () => {
  const display = M.buildDisplayMessageStream([
    { id: 'a1', role: 'assistant', tool_calls: [{ id: 'c1', name: 'shell' }, { id: 'c2', provider_call_id: 'p2', name: 'read_file' }], created_at: '2026-01-01T00:00:00Z' },
    { id: 't1', role: 'tool', tool_results: [{ tool_call_id: 'c1', name: 'shell' }, { tool_call_id: 'p2', name: 'read_file' }, { tool_call_id: 'zz', name: 'grep' }], created_at: '2026-01-01T00:00:05Z' }
  ]);
  assert.equal(display.length, 2);
  assert.deepEqual(display[0].tool_results.map((item) => item.tool_call_id), ['c1', 'p2']);
  assert.deepEqual(display[0]._merged_tool_result_message_ids, ['t1']);
  assert.equal(display[0]._tool_result_times.c1, '2026-01-01T00:00:05Z');
  assert.deepEqual(display[1].tool_results.map((item) => item.tool_call_id), ['zz']);
});

test('assistant tool-only messages do not produce blank text items', () => {
  const items = M.buildConversation([{
    id: 'msg_tool_only_blank_text', role: 'assistant', text: '\n  \t', thinking: '  ', created_at: '2026-06-26T01:02:03Z',
    tool_calls: [{ id: 'call_edit', name: 'edit_file', arguments: { path: 'rancher/reports/progress.md' } }],
    tool_results: [{ tool_call_id: 'call_edit', name: 'edit_file', display_output: 'edited rancher/reports/progress.md', is_error: false }]
  }]);
  assert.equal(items.length, 1);
  assert.equal(items[0].kind, 'steps');
  assert.equal(items[0].steps.length, 1);
  assert.equal(items[0].steps[0].name, 'edit_file');
  assert.deepEqual(items[0].steps[0].summary, { raw: 'rancher/reports/progress.md' });
  assert.equal(items[0].steps[0].status, 'ok');
  assert.equal(items.some((item) => item.kind === 'message'), false);
});

test('harness reminders keep raw text but use a distinct non-user identity', () => {
  const rawText = 'Harness resume note: <checkpoint> **keep literal**';
  const reminder = { id: 'h1', role: 'user', text: rawText, meta: { source: 'harness_reminder' } };
  const ordinary = { id: 'u1', role: 'user', text: 'ordinary operator prompt', meta: { source: 'user' } };
  const steer = { id: 'u2', role: 'user', text: 'steer prompt', meta: { source: 'steer' } };
  assert.equal(M.isHarnessReminder(reminder), true);
  assert.equal(M.messageVariant(reminder), 'harness_reminder');
  assert.deepEqual(M.messageActor(reminder), { key: 'actor.harness' });
  assert.equal(M.messageIcon(reminder), 'bell-ring');
  assert.deepEqual(M.messageActor(ordinary), { key: 'actor.you' });
  assert.equal(M.messageVariant(ordinary), 'user');
  assert.deepEqual(M.messageActor(steer), { key: 'actor.youSteer' });
  const items = M.buildConversation([reminder, ordinary]);
  assert.equal(items[0].variant, 'harness_reminder');
  assert.equal(items[0].text, rawText, 'reminder text is returned verbatim for a raw node');
  assert.equal(items[1].variant, 'user');
  assert.equal(M.deriveSessionTitle({ id: 'session_1' }, { messages: [reminder, ordinary] }).raw, 'ordinary operator prompt');
});

test('assistant actor uses the raw agent identity or a fallback key', () => {
  assert.deepEqual(M.messageActor({ role: 'assistant' }, { agent_name: 'Ready', agent_role: 'evaluator' }), { raw: 'Ready \u00b7 evaluator' });
  assert.deepEqual(M.messageActor({ role: 'assistant' }, {}), { key: 'actor.agent' });
  assert.deepEqual(M.messageActor({ role: 'system' }), { key: 'actor.system' });
});

test('finish final message is displayed exactly once', () => {
  const items = M.buildConversation([
    { id: 'u', role: 'user', text: 'do it', created_at: '2026-01-01T00:00:00Z' },
    { id: 'a', role: 'assistant', text: '', created_at: '2026-01-01T00:00:01Z', tool_calls: [{ id: 'f1', name: 'finish', arguments: { message: 'All done' } }] },
    { id: 't', role: 'tool', created_at: '2026-01-01T00:00:02Z', tool_results: [{ tool_call_id: 'f1', name: 'finish', final: true, display_output: '**All done**' }] }
  ]);
  assert.deepEqual(items.map((item) => item.kind), ['message', 'steps', 'message']);
  const finals = items.filter((item) => item.final);
  assert.equal(finals.length, 1);
  assert.equal(finals[0].text, '**All done**');
  assert.equal(finals[0].markdownKey, 'final:f1');
  assert.deepEqual(items[1].steps[0].summary, { key: 'tool.finish.captured' });
  assert.deepEqual(items[1].steps[0].resultSummary, { key: 'tool.finish.captured' });

  const withText = M.buildConversation([
    { id: 'a', role: 'assistant', text: 'Here is the answer', tool_calls: [{ id: 'f2', name: 'finish' }],
      tool_results: [{ tool_call_id: 'f2', name: 'finish', final: true, display_output: 'Here is the answer' }] }
  ]);
  assert.equal(withText.filter((item) => item.kind === 'message').length, 1, 'assistant text is the single visible reply');
  assert.equal(withText.some((item) => item.final), false);
});

test('thinking stays on text messages and becomes a folded step for tool-only turns', () => {
  const items = M.buildConversation([
    { id: 'a1', role: 'assistant', text: 'Plan', thinking: 'reasoning text', created_at: '2026-01-01T00:00:00Z' },
    { id: 'a2', role: 'assistant', thinking: 'more thinking', created_at: '2026-01-01T00:00:01Z', tool_calls: [{ id: 'c', name: 'grep', arguments: { pattern: 'TODO', path: 'src' } }],
      tool_results: [{ tool_call_id: 'c', name: 'grep', display_output: 'src/a.go:1: TODO' }] }
  ]);
  assert.equal(items[0].thinking, 'reasoning text');
  assert.equal(items[1].kind, 'steps');
  assert.equal(items[1].steps[0].kind, 'thinking');
  assert.deepEqual(items[1].steps[0].summary, { key: 'step.thinking', params: { chars: 13 } });
  assert.deepEqual(items[1].steps[1].summary, { key: 'tool.search.pattern', raw: 'TODO', secondaryRaw: 'src' });
  assert.equal(items[1].toolCount, 1);
});

// ---------------------------------------------------------------------------
// Step grouping (new)
// ---------------------------------------------------------------------------

function toolTurn(id, call, result, at, resultAt) {
  return [
    { id: 'a_' + id, role: 'assistant', created_at: at, tool_calls: [call] },
    ...(result ? [{ id: 't_' + id, role: 'tool', created_at: resultAt, tool_results: [result] }] : [])
  ];
}

test('consecutive tool steps between assistant texts fold into one group with duration and status', () => {
  const messages = [
    { id: 'u1', role: 'user', text: 'fix the build', created_at: '2026-01-01T00:00:00Z' },
    { id: 'a0', role: 'assistant', text: 'Looking around.', created_at: '2026-01-01T00:00:01Z' },
    ...toolTurn('1', { id: 'c1', name: 'shell', arguments: JSON.stringify({ command: 'go build ./...\necho done' }) },
      { tool_call_id: 'c1', name: 'shell', display_output: 'ok' }, '2026-01-01T00:00:02Z', '2026-01-01T00:00:04Z'),
    ...toolTurn('2', { id: 'c2', name: 'read_file', arguments: { path: 'internal/a.go' } },
      { tool_call_id: 'c2', name: 'read_file', display_output: 'package a', is_error: true }, '2026-01-01T00:00:05Z', '2026-01-01T00:00:06Z'),
    ...toolTurn('3', { id: 'c3', name: 'write_file', arguments: { path: 'internal/a.go', content: 'x' } },
      { tool_call_id: 'c3', name: 'write_file', display_output: 'wrote' }, '2026-01-01T00:00:07Z', '2026-01-01T00:00:12Z'),
    { id: 'a9', role: 'assistant', text: 'Fixed.', created_at: '2026-01-01T00:00:13Z' }
  ];
  const items = M.buildConversation(messages);
  assert.deepEqual(items.map((item) => item.kind + ':' + (item.variant || '')), ['message:user', 'message:assistant', 'steps:', 'message:assistant']);
  const group = items[2];
  assert.equal(group.id, 'steps:c1');
  assert.deepEqual(group.steps.map((step) => step.id), ['c1', 'c2', 'c3']);
  assert.deepEqual(group.steps.map((step) => step.status), ['ok', 'error', 'ok']);
  assert.equal(group.status, 'error');
  assert.deepEqual(group.counts, { total: 3, ok: 2, error: 1, running: 0, pending: 0 });
  assert.equal(group.startedAt, '2026-01-01T00:00:02.000Z');
  assert.equal(group.endedAt, '2026-01-01T00:00:12.000Z');
  assert.equal(group.durationMs, 10000);
  assert.deepEqual(group.steps[0].summary, { raw: 'go build ./...' });
  assert.deepEqual(group.steps[0].args, { command: 'go build ./...\necho done' });
  assert.equal(group.steps[0].startedAt, '2026-01-01T00:00:02Z');
  assert.equal(group.steps[0].endedAt, '2026-01-01T00:00:04Z');
  assert.deepEqual(group.steps[1].resultSummary, { raw: 'package a' });
});

test('unresolved trailing calls are running only while the session is live', () => {
  const messages = [
    { id: 'u1', role: 'user', text: 'go' },
    ...toolTurn('1', { id: 'c1', name: 'shell', arguments: { command: 'sleep 1' } }, null, '2026-01-01T00:00:00Z')
  ];
  const live = M.buildConversation(messages, { live: true, now: Date.parse('2026-01-01T00:00:30Z') });
  assert.equal(live[1].status, 'running');
  assert.equal(live[1].steps[0].status, 'running');
  assert.equal(live[1].durationMs, 30000);
  const settled = M.buildConversation(messages, { live: false });
  assert.equal(settled[1].status, 'pending');
  assert.equal(settled[1].steps[0].status, 'pending');
});

test('only the last group can be running', () => {
  const messages = [
    ...toolTurn('1', { id: 'c1', name: 'request_user_input', arguments: { questions: [{ id: 'q' }, { id: 'r' }] } }, null, '2026-01-01T00:00:00Z'),
    { id: 'a', role: 'assistant', text: 'between' },
    ...toolTurn('2', { id: 'c2', name: 'shell', arguments: { command: 'ls' } }, null, '2026-01-01T00:00:01Z')
  ];
  const items = M.buildConversation(messages, { live: true });
  assert.equal(items[0].status, 'pending');
  assert.deepEqual(items[0].steps[0].summary, { key: 'tool.requestUserInput', params: { count: 2 } });
  assert.equal(items[2].status, 'running');
});

test('results without a loaded call become result-only steps', () => {
  const items = M.buildConversation([{ id: 't0', role: 'tool', created_at: '2026-01-01T00:00:00Z', tool_results: [{ tool_call_id: 'lost', name: 'shell', display_output: 'tail output', is_error: false }] }]);
  assert.equal(items.length, 1);
  assert.equal(items[0].steps[0].kind, 'result');
  assert.equal(items[0].steps[0].id, 'result:lost');
  assert.deepEqual(items[0].steps[0].summary, { raw: 'tail output' });
});

test('optimistic messages are appended after durable history', () => {
  const items = M.buildConversation([{ id: 'u1', role: 'user', text: 'first' }], { optimistic: [{ id: 'send_1', optimisticKey: 'send_1', role: 'user', text: 'pending launch prompt', pending: true }] });
  assert.equal(items.length, 2);
  assert.equal(items[1].pending, true);
  assert.equal(items[1].text, 'pending launch prompt');
});

test('stepGroupStatus ranks running over error over pending over ok', () => {
  assert.equal(M.stepGroupStatus([{ status: 'ok' }, { status: 'pending' }]), 'pending');
  assert.equal(M.stepGroupStatus([{ status: 'error' }, { status: 'pending' }]), 'error');
  assert.equal(M.stepGroupStatus([{ status: 'error' }, { status: 'running' }]), 'running');
  assert.equal(M.stepGroupStatus([]), 'ok');
});

test('one-line summaries for common tools', () => {
  const s = (name, args) => M.summarizeToolCall({ name, arguments: args });
  assert.deepEqual(s('shell', { command: '\n  npm test -- --watch=false\nmore' }), { raw: 'npm test -- --watch=false' });
  assert.deepEqual(s('read_file', { path: 'a/b.txt' }), { raw: 'a/b.txt' });
  assert.deepEqual(s('edit_file', { path: 'x'.repeat(60) }), { raw: '\u2026' + 'x'.repeat(41) });
  assert.deepEqual(s('glob', { pattern: '**/*.go' }), { key: 'tool.search.pattern', raw: '**/*.go', secondaryRaw: '' });
  assert.deepEqual(s('agent_spawn', { agent_name: 'reviewer', agent_role: 'evaluator', prompt: 'review' }), { raw: 'reviewer \u00b7 evaluator', secondaryRaw: 'review' });
  assert.deepEqual(s('agent_status', { session_id: 'child_session_123456' }), { raw: 'child_\u20263456', secondaryRaw: '' });
  assert.deepEqual(s('todo_write', { todos: [{}, {}, {}] }), { key: 'tool.todoItems', params: { count: 3 } });
  assert.deepEqual(s('submit_plan', { title: 'Ship v3', summary: 'x' }), { raw: 'Ship v3' });
  assert.deepEqual(s('request_user_input', { questions: [{}] }), { key: 'tool.requestUserInput', params: { count: 1 } });
  assert.deepEqual(s('finish', { message: 'Done.' }), { raw: 'Done.' });
  assert.deepEqual(s('finish', {}), { key: 'tool.finish.call' });
  assert.deepEqual(s('update_goal', { status: 'complete' }).key, 'goal.tool.update');
  assert.deepEqual(s('custom_tool', { message: 'hello' }), { raw: 'hello' });
  assert.deepEqual(s('custom_tool', {}), { key: 'tool.noArguments' });
  assert.deepEqual(s('custom_tool', 'not json'), { raw: 'not json' });
});

test('tool result summaries keep raw output separate from fixed keys', () => {
  assert.deepEqual(M.summarizeToolResult({ name: 'shell', display_output: '' }), { key: 'tool.noOutput' });
  assert.deepEqual(M.summarizeToolResult({ name: 'shell', display_output: 'a\n  b', is_error: true }), { raw: 'a\n  b' });
  assert.deepEqual(M.summarizeToolResult({ name: 'todo_write', display_output: '[{},{}]' }), { key: 'tool.todoSaved', params: { count: 2 } });
  assert.deepEqual(M.summarizeToolResult({ name: 'todo_write', display_output: '[{}]', metadata: { noop: true } }), { key: 'tool.todoUnchanged', params: { count: 1 } });
  assert.deepEqual(M.summarizeToolResult({ name: 'agent_status', display_output: JSON.stringify({ status: 'awaiting_input' }) }), { key: 'status.awaiting_input', params: { fallback: 'Awaiting input' } });
  assert.deepEqual(M.summarizeToolResult({ name: 'agent_wait', display_output: JSON.stringify({ final_text: 'child said hi' }) }), { raw: 'child said hi' });
});

test('agent tool results expose child rows', () => {
  const spawn = M.agentToolResultView({ name: 'agent_spawn', display_output: JSON.stringify({ session_id: 'child_1', status: 'running', agent_name: 'Settings' }) });
  assert.deepEqual(spawn.label, { raw: 'Settings' });
  assert.equal(spawn.tone, 'live');
  const list = M.agentToolResultView({ name: 'agent_list', display_output: JSON.stringify({ sessions: [{ id: 's1', status: 'completed', created_at: '1' }], jobs: [{ id: 'j9', status: 'queued', created_at: '0' }] }) });
  assert.deepEqual(list.rows.map((row) => row.id), ['j9', 's1']);
});

// ---------------------------------------------------------------------------
// Goal tools: raw facts separate from fallback keys (ported goal locale cases)
// ---------------------------------------------------------------------------

const summary = 'The approved local validation command completed with the expected output.';
const goalFixture = { goal_id: 'goal_progress', mode: 'mission', status: 'active', tokens_used: 0, progress: [{ id: 'progress_raw', kind: 'progress', summary }] };

test('progress call summary returns kind and summary as raw facts', () => {
  const result = M.goalToolCallSummary({ name: 'record_goal_progress', arguments: { kind: 'progress', summary } });
  assert.equal(result.key, 'goal.tool.recordProgress');
  assert.deepEqual(result.rawFacts, { kind: 'progress', summary: M.truncateText(summary, 110) });
  assert.deepEqual(result.fallbackKeys, { kind: '', summary: '' });
});

test('missing progress facts fall back to keys instead of raw model text', () => {
  const result = M.goalToolCallSummary({ name: 'record_goal_progress', arguments: {} });
  assert.deepEqual(result.rawFacts, { kind: '', summary: '' });
  assert.deepEqual(result.fallbackKeys, { kind: 'goal.progress.kindFallback', summary: 'goal.progress.summaryFallback' });
});

test('get_goal is a fixed operator label without raw facts', () => {
  assert.deepEqual(M.goalToolCallSummary({ name: 'get_goal', arguments: {} }), { key: 'goal.tool.read', params: {}, rawFacts: {} });
  const body = M.goalToolCallBody({ name: 'get_goal', arguments: {} });
  assert.deepEqual(body.action.key, 'goal.tool.read');
  assert.deepEqual(body.name, { raw: 'get_goal' });
});

test('operator-looking and HTML-like progress facts stay raw values', () => {
  for (const value of ['<custom kind>', 'Latest progress', 'Settings', 'Progress', '原始进度']) {
    const body = M.goalToolCallBody({ name: 'record_goal_progress', arguments: { kind: value, summary: value } });
    assert.deepEqual(body.summary, { raw: value });
    assert.deepEqual(body.chips.find((chip) => chip.key === 'goal.chip.kind'), { key: 'goal.chip.kind', raw: value });
    assert.equal(body.action.rawFacts.kind, value);
  }
});

test('progress result summary separates raw mode/kind/summary from status and metrics', () => {
  const output = { ...goalFixture, tokens_used: 7, token_budget: 100, provider_time_used_seconds: 3, provider_time_budget_seconds: 10 };
  const result = M.goalToolResultSummary({ name: 'record_goal_progress', display_output: JSON.stringify(output) });
  assert.equal(result.key, 'goal.tool.state');
  assert.deepEqual(result.rawFacts, { mode: 'mission', goalId: 'goal_progress' });
  assert.deepEqual(result.statusLabel, { key: 'goalStatus.active', params: { fallback: 'Active' } });
  assert.equal(result.tokensUsed, 7);
  assert.equal(result.providerTimeUsedSeconds, 3);
  assert.deepEqual(result.latestProgress, { id: 'progress_raw', kind: 'progress', summary });
  const other = M.goalToolResultSummary({ name: 'get_goal', display_output: JSON.stringify(goalFixture) });
  assert.equal(other.latestProgress, null);
  assert.deepEqual(M.goalToolResultSummary({ name: 'get_goal', display_output: 'null' }), { key: 'goal.tool.noGoal', goalOff: true });
});

test('goal tool card shows compact status and never leaks the objective text', () => {
  const objective = 'Sensitive objective should stay in the Goal panel, not the tool lane body.';
  const card = M.goalToolCard({ name: 'create_goal', display_output: JSON.stringify({ goal_id: 'goal_tool_lane', mode: 'goal', status: 'active', objective, tokens_used: 3, time_used_seconds: 1, updated_at: '2026-06-12T06:16:00Z', progress: goalFixture.progress }) });
  assert.equal(card.status, 'active');
  assert.equal(card.tone, 'live');
  assert.deepEqual(card.objective, { key: 'goal.objectiveStored', params: { chars: objective.length } });
  assert.equal(JSON.stringify(card).includes(objective), false);
  assert.deepEqual(card.metrics, [{ key: 'goal.metric.tokens', params: { value: '3' } }, { key: 'goal.metric.providerTime', params: { value: '1s' } }]);
  assert.deepEqual(card.counts, [{ key: 'goal.count.progress', params: { count: 1 } }]);
  assert.deepEqual(card.latestProgressTitle, { key: 'goal.latestProgress' });
  assert.equal(card.latestProgress.summary, summary);
  const callSummary = M.goalToolCallSummary({ name: 'create_goal', arguments: { mode: 'goal', objective, token_budget: 100 } });
  assert.deepEqual(callSummary, { key: 'goal.tool.create', params: { chars: objective.length }, rawFacts: { mode: 'goal' } });
});

test('session goal line exposes accounting values and latest facts as params', () => {
  const detail = {
    state: { status: 'running', phase: 'tool_execute' },
    goal: { goal_id: 'goal_runtime_status', mode: 'goal', status: 'active', objective: 'Keep the Goal visible.', tokens_used: 42, time_used_seconds: 9, updated_at: '2026-06-12T06:14:43Z' },
    goal_facts: { latest_history: { type: 'goal.accounting.updated', created_at: '2026-06-12T06:14:43Z' }, progress: [] }
  };
  const line = M.sessionGoalLine(detail);
  assert.equal(line.off, false);
  assert.deepEqual(line.badge, { key: 'goalStatus.active', params: { fallback: 'Active' } });
  assert.equal(line.tone, 'live');
  assert.deepEqual(line.parts.map((part) => part.key), ['goal.line.session', 'phase.tool_execute', 'goal.line.tokens', 'goal.line.providerTime', 'goal.line.latest', 'goal.line.at']);
  assert.equal(line.parts[2].params.value, '42');
  assert.equal(line.parts[3].params.value, '9s');
  assert.equal(line.parts[4].params.type, 'goal.accounting.updated');
  assert.deepEqual(line.objective, { raw: 'Keep the Goal visible.' });

  const budgeted = M.sessionGoalLine({ state: { status: 'completed', phase: 'turn_decide' }, goal: { status: 'complete', tokens_used: 12, provider_time_used_seconds: 6, token_budget: 100, provider_time_budget_seconds: 20 } });
  assert.equal(budgeted.parts[1].key, 'phase.turn_decide');
  assert.equal(budgeted.parts[2].params.value, '12 / 100');
  assert.equal(budgeted.parts[3].params.value, '6s / 20s');
  assert.deepEqual(M.sessionGoalLine({ state: {} }), { off: true, badge: { key: 'goal.off' }, copy: { key: 'goal.noneAttached' }, parts: [] });
});

test('completed goal during session follow-up is historical', () => {
  const detail = { state: { status: 'running' }, goal: { status: 'complete', completion_audit: { summary: 'done' } } };
  assert.equal(M.isHistoricalCompletedGoal(detail), true);
  assert.deepEqual(M.sessionGoalLine(detail).historicalBadge, { key: 'goal.historical' });
  assert.equal(M.isHistoricalCompletedGoal({ state: { status: 'completed' }, goal: { status: 'complete' } }), false);
  assert.equal(M.goalRuntimeFacts({ goal: { status: 'budget_limited' }, goal_facts: { progress: [{ kind: 'budget_wrapup', created_at: 't' }] } }).latestType, 'budget_wrapup');
});

test('phase labels are dynamic keys with a humanized fallback', () => {
  assert.deepEqual(M.phaseLabel('turn_decide'), { key: 'phase.turn_decide', params: { fallback: 'Turn decide' } });
  assert.deepEqual(M.phaseLabel(''), { key: 'phase.prepare', params: { fallback: 'Prepare' } });
  assert.deepEqual(M.statusLabel('awaiting_input'), { key: 'status.awaiting_input', params: { fallback: 'Awaiting input' } });
});

// ---------------------------------------------------------------------------
// Activity, events and timeline
// ---------------------------------------------------------------------------

test('session activity distinguishes cancelled from failed and paused', () => {
  const cancelled = M.sessionActivityForState({ status: 'cancelled' });
  assert.deepEqual(cancelled, { title: { key: 'activity.cancelled.title' }, copy: { key: 'activity.cancelled.copy' }, tone: 'danger' });
  assert.deepEqual(M.sessionActivityForState({ status: 'failed', last_error: 'boom' }).copy, { raw: 'boom' });
  assert.equal(M.sessionActivityForState({ status: 'failed' }).copy.key, 'activity.failed.copy');
  assert.equal(M.sessionActivityForState({ status: 'paused' }).title.key, 'activity.paused.title');
  assert.equal(M.sessionActivityForState({ status: 'paused', pause_reason: 'manual_stop' }).title.key, 'activity.stopped.title');
  assert.equal(M.sessionActivityForState({ status: 'paused', pause_reason: 'manual_interrupt' }).title.key, 'activity.interrupted.title');
  assert.equal(M.sessionActivityForDetail({ state: { status: 'awaiting_input' }, plan_mode: { status: 'awaiting_approval' } }).title.key, 'activity.awaitingPlanApproval.title');
  assert.equal(M.sessionActivityForDetail({ plan_mode: { status: 'awaiting_user_input' } }).title.key, 'activity.awaitingPlanInput.title');
});

test('refresh activity rules follow running state and the current-process handle', () => {
  const running = M.liveActivityForRefresh({ state: { status: 'running', phase: 'provider_call' } });
  assert.equal(running.generating, true);
  assert.deepEqual(running.activity.title, { key: 'phase.provider_call', params: { fallback: 'Provider call' } });
  assert.equal(M.liveActivityForRefresh({ state: { status: 'running' } }, { hasLiveEvents: true }).activity, null);
  const handle = M.liveActivityForRefresh({ state: { status: 'awaiting_input' }, active_handle_owner: { owned_by_current_process: true } });
  assert.equal(handle.generating, true);
  assert.deepEqual(handle.activity.title, { key: 'activity.startingTurn' });
  const settled = M.liveActivityForRefresh({ state: { status: 'paused' } }, { hasLiveEvents: true });
  assert.equal(settled.generating, false);
  assert.equal(settled.clearInterrupt, true);
  assert.equal(settled.activity.title.key, 'activity.paused.title');
});

test('engine events map to activity keys with raw tool subjects', () => {
  const activity = M.liveActivityFromEvent({ type: 'tool.before', data: { tool_name: 'shell', arguments: JSON.stringify({ command: 'ls -la' }) } });
  assert.deepEqual(activity.title, { key: 'event.toolStarted' });
  assert.deepEqual(activity.subject, { raw: 'shell' });
  assert.deepEqual(activity.copy, { raw: 'ls -la' });
  assert.equal(activity.tone, 'live');
  assert.equal(M.liveActivityFromEvent({ type: 'assistant.message' }), null);
  assert.equal(M.shouldPromoteLiveActivity('session.failed'), true);
  assert.equal(M.shouldRefreshAfterEvent('session.steer.accepted'), true);
  assert.equal(M.shouldRefreshAfterEvent('provider.call'), false);
  assert.equal(M.needsOverviewRefresh('queue.job.claimed'), true);
  assert.equal(M.needsOverviewRefresh('planmode.created'), true);
  assert.equal(M.needsOverviewRefresh('tool.after'), false);
  assert.equal(M.needsOverviewRefresh(undefined), false);
});

test('event descriptors return keys and keep durable values raw', () => {
  const steer = M.describeEvent('session.steer.queued', { id: 'steer_1' });
  assert.deepEqual(steer.title, { key: 'event.steerQueued' });
  assert.equal(steer.tone, 'live');
  assert.equal(M.describeEvent('session.steer.deferred').tone, 'queued');
  const failed = M.describeEvent('session.failed', { error: 'provider 500' });
  assert.deepEqual(failed.copy, { raw: 'provider 500' });
  assert.equal(failed.tone, 'danger');
  const paused = M.describeEvent('session.paused', { reason: 'manual_stop' });
  assert.deepEqual(paused.title, { key: 'event.runStopped' });
  const queue = M.describeEvent('queue.job.failed', { job_id: 'job_abcdefghijklmnop', agent_role: 'evaluator' });
  assert.deepEqual(queue.copy, { key: 'event.queueRole', raw: 'evaluator' });
  assert.deepEqual(queue.meta, { raw: 'job_ab\u2026mnop' });
  const plan = M.describeEvent('planmode.input_requested', { questions: 2, plan_version: 3 });
  assert.deepEqual(plan.copy, { key: 'event.planmode.questions', params: { count: 2 } });
  assert.deepEqual(plan.meta, { key: 'event.planVersion', params: { version: 3 } });
  const unknown = M.describeEvent('custom.thing', { a: 1 }, 'prepare', 'evt_1');
  assert.deepEqual(unknown.title, { key: 'eventType.custom.thing', params: { fallback: 'Custom thing' } });
  assert.deepEqual(unknown.copy, { key: 'event.recorded' });
});

test('mission timeline keeps goal identity raw without skipping operator labels', () => {
  for (const eventType of ['mission.plan.updated', 'mission.plan.approved', 'mission.validation.updated']) {
    const item = { kind: 'event', event_type: eventType, data: { goal_id: 'goal_reviewed_id', plan_status: 'needs_approval' } };
    const descriptor = M.describeTimelineItem(item);
    assert.deepEqual(descriptor.meta, { raw: 'goal_r\u2026d_id' });
    assert.equal(descriptor.title.key.startsWith('event.mission.'), true);
    assert.equal(descriptor.title.raw, undefined);
    assert.deepEqual(descriptor.copy, { key: 'event.mission.planStatus', params: { status: 'needs_approval', statusLabel: { key: 'planStatus.needs_approval', params: { fallback: 'Needs approval' } } } });
    assert.equal(M.describeTimelineItem({ ...item, data: {} }).meta.raw, undefined);
  }
});

test('timeline message items describe roles, tool results and background results', () => {
  const user = M.describeTimelineItem({ kind: 'message', role: 'user', text: 'hello', message_id: 'm_1' });
  assert.deepEqual(user.title, { key: 'timeline.message.user', params: { fallback: 'User' } });
  assert.deepEqual(user.copy, { raw: 'hello' });
  assert.deepEqual(user.meta, { raw: 'm_1' });
  const reminder = M.describeTimelineItem({ kind: 'message', role: 'user', text: 'note', data: { source: 'harness_reminder' } });
  assert.equal(reminder.title.key, 'timeline.message.harness_reminder');
  const tool = M.describeTimelineItem({ kind: 'message', role: 'tool', text: '' });
  assert.deepEqual(tool.copy, { key: 'timeline.toolResults.copy' });
  const payload = '<background-agent-results>' + JSON.stringify({ background_results: [{ status: 'completed', final_text: 'child done' }, { status: 'failed', last_error: 'boom' }] }) + '</background-agent-results>';
  const background = M.describeTimelineItem({ kind: 'message', role: 'user', text: payload });
  assert.equal(background.title.key, 'timeline.backgroundResults');
  assert.equal(background.tone, 'danger');
  assert.deepEqual(background.copy, { key: 'background.accepted', params: { count: 2, completed: 1, failed: 1 }, raw: 'child done' });
});

test('timeline items are chronological with search and kind filter', () => {
  const detail = { timeline: [
    { kind: 'event', event_type: 'tool.after', event_id: 'evt_newest', time: '2026-05-29T00:00:03Z', data: { tool_name: 'grep' } },
    { kind: 'message', message_id: 'm1', role: 'user', text: 'Find the bug', time: '2026-05-29T00:00:02Z' },
    { kind: 'event', event_type: 'session.started', event_id: 'evt_oldest', time: '2026-05-29T00:00:01Z' }
  ] };
  assert.deepEqual(M.buildTimelineItems(detail).map((item) => item.id), ['event:evt_oldest', 'message:m1', 'event:evt_newest']);
  assert.deepEqual(M.buildTimelineItems(detail, { filter: 'message' }).map((item) => item.id), ['message:m1']);
  assert.deepEqual(M.buildTimelineItems(detail, { filter: 'event' }).map((item) => item.id), ['event:evt_oldest', 'event:evt_newest']);
  assert.deepEqual(M.buildTimelineItems(detail, { search: 'BUG' }).map((item) => item.id), ['message:m1']);
  assert.deepEqual(M.buildTimelineItems(detail, { search: 'grep' }).map((item) => item.id), ['event:evt_newest']);
  const translated = M.buildTimelineItems(detail, { search: 'tool finished', t: (key) => (key === 'event.toolFinished' ? 'Tool finished' : key) });
  assert.deepEqual(translated.map((item) => item.id), ['event:evt_newest']);
});

test('flow lane shows the latest compact events chronologically', () => {
  const detail = { timeline: [
    { kind: 'event', event_type: 'tool.after', event_id: 'evt_5_newest', time: '5' },
    { kind: 'event', event_type: 'tool.before', event_id: 'evt_4', time: '4' },
    { kind: 'event', event_type: 'provider.call', event_id: 'evt_3', time: '3' },
    { kind: 'event', event_type: 'session.paused', event_id: 'evt_2', time: '2' },
    { kind: 'event', event_type: 'session.created', event_id: 'evt_1_oldest', time: '1' }
  ] };
  assert.deepEqual(M.compactFlowItems(detail).map((item) => item.item.event_id), ['evt_3', 'evt_4', 'evt_5_newest']);
  assert.equal(M.isCompactFlowEvent('session.created'), false);
});

test('pending stage reflects running vs settling and the interrupt arm', () => {
  const stage = M.pendingStage({ generating: true, liveActivity: { title: { key: 'x' }, copy: { raw: 'y' }, tone: 'live' }, interruptArmed: true });
  assert.equal(stage.visible, true);
  assert.deepEqual(stage.stage, { key: 'stage.running' });
  assert.deepEqual(stage.interruptLabel, { key: 'stage.interruptArmed' });
  const idle = M.pendingStage({});
  assert.equal(idle.visible, false);
  assert.deepEqual(idle.title, M.DEFAULT_LIVE_ACTIVITY.title);
});

// ---------------------------------------------------------------------------
// Background results
// ---------------------------------------------------------------------------

test('background_results payloads parse and summarize without rewriting facts', () => {
  const text = '<background-agent-results>\n' + JSON.stringify({ background_results: [
    { status: 'completed', final_text: 'done', agent_name: 'reviewer', agent_role: 'evaluator', queue_job_id: 'job_1', session_id: 'child_1', effective_workdir: '/w/a' },
    { status: 'failed', session_status: 'completed', last_error: 'corrupt handoff' }
  ] }) + '\n</background-agent-results>';
  const message = { id: 'm', role: 'user', text };
  assert.equal(M.isBackgroundResultsMessage(message), true);
  assert.equal(M.isBackgroundResultsMessage({ role: 'user', text: 'plain', meta: { source: 'background_results' } }), true);
  assert.equal(M.isBackgroundResultsMessage({ role: 'user', text: '<background-agent-results>{}</background-agent-results>' }), false);
  const items = M.buildConversation([message]);
  assert.equal(items[0].variant, 'background_results');
  assert.deepEqual(items[0].actor, { key: 'actor.backgroundAgents' });
  const summaryView = items[0].background;
  assert.equal(summaryView.count, 2);
  assert.equal(summaryView.completed, 1);
  assert.equal(summaryView.failed, 1);
  assert.deepEqual(summaryView.items[0].label, { raw: 'reviewer \u00b7 evaluator' });
  assert.deepEqual(summaryView.items[1].body, { raw: 'corrupt handoff' });
  assert.equal(summaryView.items[1].tone, 'danger');
  assert.deepEqual(M.backgroundResultView({}).body, { key: 'background.noFinalText' });
});

// ---------------------------------------------------------------------------
// Todo / tasks (ported)
// ---------------------------------------------------------------------------

test('task summary keeps cancelled separate from completed', () => {
  const summaryView = M.taskBoardSummary({
    todo: [],
    tasks: [{ id: 'task_0001', subject: 'Done', status: 'completed' }, { id: 'task_0002', subject: 'Stopped', status: 'cancelled' }],
    counters: { completed: 1, cancelled: 1, done: 2, in_progress: 0 },
    groups: { ready: [], blocked: [] }
  });
  assert.equal(summaryView.completedCount, 1);
  assert.equal(summaryView.cancelledCount, 1);
  assert.equal(summaryView.taskCount, 0);
});

test('todo progress counts the todo snapshot and task groups follow runtime order', () => {
  const board = {
    todo: [{ content: 'One', status: 'in_progress' }, { content: 'Two', status: 'in_progress' }, { content: 'Three', status: 'pending' }],
    counters: { in_progress: 1, completed: 1, cancelled: 0 },
    groups: {
      cancelled: [],
      completed: [{ id: 'task_0004', subject: 'Done', status: 'completed' }],
      blocked: [{ id: 'task_0003', subject: 'Blocked task', status: 'pending', blocked_by: ['task_0001'] }],
      ready: [{ id: 'task_0002', subject: 'Ready task', status: 'pending' }],
      in_progress: [{ id: 'task_0001', subject: 'Working', status: 'in_progress' }]
    }
  };
  const summaryView = M.taskBoardSummary(board);
  assert.equal(summaryView.todoInProgress, 2);
  assert.equal(summaryView.readyCount, 1);
  assert.equal(summaryView.blockedCount, 1);
  assert.deepEqual(summaryView.groups.map((group) => group.key), ['in_progress', 'ready', 'blocked', 'completed']);
  const ready = summaryView.groups[1];
  assert.deepEqual(ready.label, { key: 'tasks.group.ready', params: { fallback: 'Ready' } });
  assert.equal(ready.items[0].status, 'ready');
  assert.deepEqual(ready.items[0].chips[0], { raw: 'task_0002' });
  assert.deepEqual(summaryView.groups[2].items[0].chips.at(-1), { key: 'tasks.blockedBy', params: { count: 1 } });
  assert.deepEqual(M.todoStats(board), { total: 3, inProgress: 2, pending: 1, completed: 0, cancelled: 0 });
});

test('task and todo views isolate durable facts from localizable fallbacks', () => {
  const task = M.taskItemView({ id: 'task_collision', subject: 'Settings', status: 'ready', priority: 'Settings', owner: 'Settings' }, 'ready');
  assert.deepEqual(task.title, { raw: 'Settings' });
  assert.deepEqual(task.description, { key: 'tasks.noDescription' });
  assert.equal(task.chips.filter((chip) => chip.raw === 'Settings').length, 2);
  assert.deepEqual(M.todoItemView({ status: 'pending' }).title, { key: 'tasks.untitledTodo' });
  assert.deepEqual(M.taskItemView({ status: 'pending' }).title, { key: 'tasks.untitledTask' });
});

test('composer todo progress merges todos and tasks active-first', () => {
  const progress = M.todoProgress({ todo: [{ content: 'a', status: 'completed' }, { content: 'b', status: 'in_progress' }], tasks: [{ id: 't', status: 'cancelled', owner: 'me' }, { subject: 'x', status: 'pending' }] });
  assert.equal(progress.total, 4);
  assert.equal(progress.done, 2);
  assert.equal(progress.active, 1);
  assert.equal(progress.percent, 50);
  assert.deepEqual(progress.items.map((item) => item.status), ['in_progress', 'pending', 'completed', 'cancelled']);
});

// ---------------------------------------------------------------------------
// File changes and shell redirects
// ---------------------------------------------------------------------------

test('shell redirect targets ignore here-doc bodies (Go filechanges parity)', () => {
  const command = lines(
    '{',
    "  cat > reports/glata-staging/evidence/static-file-sample.txt <<'MD'",
    '- GET /status -> {status}',
    '- template target should not become {r.get(status)} or \\]+ or ]+',
    'MD',
    "  python3 - <<'PY'",
    "pat=r'''(?:(?:https?:)?//[^\\s\"'<>\\\\]+|/[A-Za-z0-9_./?&=%#:@+-]{2,})'''",
    "print(f'{r.get(\"status\")} {r.get(\"file\",\"\")} -> {r.get(\"status\")}')",
    'PY',
    '} > reports/glata-staging/evidence/file-list-current.txt 2>&1',
    "printf 'saved file-list-current.txt\\n'"
  );
  const targets = M.collectShellRedirectPaths(command);
  assert.deepEqual(targets, [
    { path: 'reports/glata-staging/evidence/static-file-sample.txt', mode: 'write' },
    { path: 'reports/glata-staging/evidence/file-list-current.txt', mode: 'write' }
  ]);
});

test('shell redirect parsing skips fd, /dev/null and process targets', () => {
  assert.deepEqual(M.collectShellRedirectPaths("printf 'ok\\n' > /dev/null 2>&1"), []);
  assert.deepEqual(M.collectShellRedirectPaths('cmd 2>/dev/fd/3 >&2 > -'), []);
  assert.deepEqual(M.collectShellRedirectPaths('echo a >> "logs/out file.txt"; echo b >| c.txt; echo c 2> err.log'), [
    { path: 'logs/out file.txt', mode: 'append' },
    { path: 'c.txt', mode: 'write' },
    { path: 'err.log', mode: 'write' }
  ]);
  assert.deepEqual(M.collectShellRedirectPaths("echo '> not.txt' && echo \"a >> b\""), []);
  assert.deepEqual(M.collectShellRedirectPaths(''), []);
});

test('here-doc parsing handles <<- tab stripping, quoted delimiters and <<<', () => {
  const command = lines('cat <<-"EOF" > out.txt', '\techo > inside.txt', '\tEOF', 'cat <<< "x > y" > after.txt');
  assert.deepEqual(M.collectShellHereDocDelimiters('cat <<-"EOF" > out.txt'), [{ value: 'EOF', stripTabs: true }]);
  assert.equal(M.stripShellHereDocBodies(command).includes('inside.txt'), false);
  assert.deepEqual(M.collectShellRedirectPaths(command).map((item) => item.path), ['out.txt', 'after.txt']);
  assert.deepEqual(M.tokenizeShellCommand('a "b c" d\\ e >>f'), ['a', 'b c', 'd e', '>>', 'f']);
  assert.deepEqual(M.tokenizeShellCommand('cmd 2>&1 | tee x & y'), ['cmd', '2>', '&1', 'tee', 'x', 'y']);
  assert.equal(M.isShellOutputRedirect('2>'), true);
  assert.equal(M.isShellOutputRedirect('&>>'), true);
  assert.equal(M.cleanShellRedirectTarget('&1'), '');
});

test('inferred file changes count only successful results and mark partial disclosure', () => {
  const detail = { messages: [
    { role: 'assistant', tool_calls: [
      { id: 'c1', name: 'write_file', arguments: JSON.stringify({ path: 'reports/a.md', content: 'one\ntwo' }) },
      { id: 'c2', name: 'edit_file', arguments: JSON.stringify({ path: 'reports/b.md', old_text: 'x', new_text: 'missing' }) },
      { id: 'c3', name: 'shell', arguments: JSON.stringify({ command: 'echo hi >> reports/log.txt' }) },
      { id: 'c4', name: 'write_file', arguments: JSON.stringify({ path: 'reports/unresolved.md', content: 'x' }) },
      { id: 'c5', name: 'edit_file', arguments: JSON.stringify({ path: 'reports/a.md', old_text: 'one\ntwo\nthree', new_text: 'one' }) }
    ] },
    { role: 'tool', tool_results: [
      { tool_call_id: 'c1', name: 'write_file' },
      { tool_call_id: 'c2', name: 'edit_file', is_error: true, llm_output: 'Error: old_text not found' },
      { tool_call_id: 'c3', name: 'shell' },
      { tool_call_id: 'c5', name: 'edit_file' }
    ] }
  ] };
  const changes = M.collectFileChanges(detail);
  assert.equal(changes.source, 'inferred');
  assert.deepEqual(changes.files, [
    { path: 'reports/a.md', writes: 1, edits: 1, linesAdded: 2, linesRemoved: 2, op: 'edit' },
    { path: 'reports/log.txt', writes: 0, edits: 1, linesAdded: 0, linesRemoved: 0, op: 'edit' }
  ]);
  assert.deepEqual(changes.disclosure, { badge: { key: 'files.partial' }, note: { key: 'files.disclosure' } });
  assert.deepEqual(changes.totals, { files: 2, added: 2, removed: 2 });
});

test('durable file changes win and an empty panel has no disclosure', () => {
  const durable = M.collectFileChanges({ file_changes: [{ path: 'a.go', writes: 1, lines_added: 3 }], messages: [{ role: 'assistant', tool_calls: [{ id: 'x', name: 'write_file', arguments: { path: 'ignored' } }] }] });
  assert.equal(durable.source, 'durable');
  assert.deepEqual(durable.files, [{ path: 'a.go', writes: 1, edits: 0, linesAdded: 3, linesRemoved: 0, op: 'new' }]);
  const empty = M.collectFileChanges({ messages: [] });
  assert.deepEqual(empty.files, []);
  assert.equal(empty.disclosure, null);
  assert.equal(empty.source, 'none');
});

// ---------------------------------------------------------------------------
// Sub-agents and notifications (ported)
// ---------------------------------------------------------------------------

const failedJob = {
  id: 'job_failed_handoff', session_id: 'child_failed_handoff', parent_session_id: 'parent_failed_handoff',
  agent_name: 'reviewer', agent_role: 'evaluator', mode: 'exec', status: 'failed', session_status: 'completed',
  prompt: 'review handoff', final_text: 'child reported success before handoff failed',
  last_error: 'load child session messages.jsonl for queue job job_failed_handoff: corrupt'
};

test('queue failure outranks a completed child status', () => {
  assert.equal(M.queueJobDisplayStatus(failedJob), 'failed');
  const row = M.subAgentRowView({ session: null, job: failedJob });
  assert.equal(row.status, 'failed');
  assert.equal(row.tone, 'danger');
  assert.deepEqual(row.error, { raw: failedJob.last_error });
  assert.equal(row.finalText, null, 'errors win over final text');
  assert.equal(row.parentSessionId, 'parent_failed_handoff');
});

test('completed sub-agent rows show queue final text', () => {
  const row = M.subAgentRowView({
    session: { id: 'child_completed_with_final', status: 'completed', provider: 'openai', model: 'gpt-test', phase: 'complete', queue_job_id: 'job_completed_with_final', agent_name: 'reviewer', agent_role: 'evaluator' },
    job: { id: 'job_completed_with_final', session_id: 'child_completed_with_final', status: 'completed', session_status: 'completed', mode: 'exec', final_text: 'child completed final summary for parent handoff' }
  });
  assert.deepEqual(row.finalText, { raw: 'child completed final summary for parent handoff' });
  assert.deepEqual(row.model, { raw: 'gpt-test' });
  assert.deepEqual(row.phase, { key: 'phase.complete', params: { fallback: 'Complete' } });
});

test('blocked queue status outranks a resumable child status', () => {
  const sessionItem = { id: 'child_blocked_waiting', status: 'awaiting_input', queue_job_id: 'job_blocked_waiting', agent_name: 'reviewer', agent_role: 'evaluator' };
  const job = { id: 'job_blocked_waiting', session_id: 'child_blocked_waiting', status: 'blocked', session_status: 'awaiting_input', mode: 'exec', last_error: 'child session is resumable: awaiting_input' };
  assert.equal(M.subAgentDisplayStatus(sessionItem, job), 'blocked');
  const row = M.subAgentRowView({ session: sessionItem, job });
  assert.equal(row.status, 'blocked');
  assert.match(row.error.raw, /child session is resumable/);
  const rows = M.subAgentRows({ children: { sessions: [sessionItem], jobs: [job] } });
  assert.equal(rows.length, 1);
  assert.equal(rows[0].status, 'blocked');
});

test('sub-agent rows merge sessions and orphan jobs in created order', () => {
  const rows = M.mergeSubAgentRows(
    [{ id: 's2', created_at: '2026-01-02', queue_job_id: 'j2' }, { id: 's1', created_at: '2026-01-01' }],
    [{ id: 'j2', created_at: '2026-01-02' }, { id: 'j0', created_at: '2025-12-31' }, { id: 'j1', session_id: 's1', created_at: '2026-01-01' }]
  );
  assert.deepEqual(rows.map((row) => (row.session?.id || '-') + '/' + (row.job?.id || '-')), ['-/j0', 's1/j1', 's2/j2']);
});

test('budget summary exposes attempt usage remaining deadline and reason', () => {
  const view = M.effectiveBudgetSummary({
    status: 'exhausted', attempt: 2, max_turns_per_attempt: 5, used_turns: 4, remaining_turns: 1,
    max_active_runtime_ms: 60000, used_active_runtime_ms: 45000, remaining_active_runtime_ms: 15000,
    active_runtime_checkpoint_interval_ms: 1000, active_runtime_checkpoint_at: '2026-07-15T11:59:59Z', active_runtime_lease_open: true,
    active_runtime_last_recovery_ms: 1000, active_runtime_last_recovery_at: '2026-07-15T11:00:00Z',
    absolute_deadline_at: '2026-07-15T12:00:00Z', last_reason: 'child_budget_active_runtime_exceeded', source: 'runtime.child_budget'
  });
  assert.deepEqual(view.parts, [
    { key: 'budget.attempt', params: { attempt: 2 } },
    { key: 'budget.turns', params: { used: 4, limit: 5, remaining: 1 } },
    { key: 'budget.active', params: { usedSec: 45, limitSec: 60, remainingSec: 15 } },
    { key: 'budget.checkpointLeaseOpen', params: { at: '2026-07-15T11:59:59Z' } },
    { key: 'budget.crashRecoveryAt', params: { value: '1s', at: '2026-07-15T11:00:00Z' } },
    { key: 'budget.deadline', params: { at: '2026-07-15T12:00:00Z' } },
    { raw: 'child_budget_active_runtime_exceeded' },
    { raw: 'runtime.child_budget' }
  ]);
  assert.deepEqual(M.effectiveBudgetSummary({ status: 'disabled' }).parts, [{ key: 'budget.offSnapshotted' }]);
  assert.deepEqual(M.effectiveBudgetSummary({ status: 'active' }).parts, [{ key: 'budgetStatus.active', params: { fallback: 'Active' } }]);
});

test('cancelled queue outcomes and notification parent actions', () => {
  const budget = { status: 'exhausted', attempt: 1, max_turns_per_attempt: 1, used_turns: 1, remaining_turns: 0, last_reason: 'child_budget_turns_exceeded', source: 'runtime.child_budget' };
  const row = M.subAgentRowView({ job: { id: 'job_cancelled_budget', status: 'cancelled', session_status: 'paused', stop_reason: 'agent_stop', effective_budget: budget } });
  assert.equal(row.status, 'cancelled');
  assert.ok(row.budget.parts.some((part) => part.raw === 'child_budget_turns_exceeded'));
  const notification = M.backgroundNotificationView({ id: 'n', queue_job_id: 'job_budget', status: 'blocked', session_status: 'paused', delivery_status: 'pending', available_actions: ['extend_resume', 'cancel_settle', 'inspect'], effective_budget: budget });
  assert.deepEqual(notification.actions.map((action) => action.key), ['notification.action.extend_resume', 'notification.action.cancel_settle', 'notification.action.inspect']);
  assert.deepEqual(notification.actions[0].params, { fallback: 'Extend resume' });
  assert.deepEqual(notification.pendingHint, { key: 'notification.pendingHint' });
  assert.deepEqual(notification.copy, { key: 'notification.pendingDelivery' });
});

test('notifications prefer errors and localize fallbacks without rewriting durable facts', () => {
  const failed = M.backgroundNotificationView({ queue_job_id: 'job_failed_handoff', status: 'failed', session_status: 'completed', final_text: 'child reported success', last_error: 'messages.jsonl corrupt' });
  assert.deepEqual(failed.copy, { raw: 'messages.jsonl corrupt' });
  const fallback = M.backgroundNotificationView({ id: 'notification_fallback', queue_job_id: 'job_fallback', agent_name: 'Settings', agent_role: 'evaluator', status: 'completed', session_status: 'completed', delivery_status: 'accepted' });
  assert.deepEqual(fallback.title, { raw: 'Settings \u00b7 evaluator' });
  assert.deepEqual(fallback.copy, { key: 'notification.noFinalText' });
  assert.deepEqual(fallback.meta.id, { raw: 'job_fallback' });
  assert.deepEqual(fallback.meta.deliveryStatus, { raw: 'accepted' });
  assert.deepEqual(fallback.meta.sessionStatus, { key: 'status.completed', params: { fallback: 'Completed' } });
  const raw = M.backgroundNotificationView({ id: 'notification_raw', queue_job_id: 'job_raw', status: 'completed', final_text: 'Settings' });
  assert.deepEqual(raw.copy, { raw: 'Settings' });
  assert.deepEqual(raw.title, { key: 'notification.fallbackTitle' });
});

// ---------------------------------------------------------------------------
// Titles and plan input (ported)
// ---------------------------------------------------------------------------

test('session titles use the first prompt line, then agent identity, then the short id', () => {
  assert.deepEqual(M.deriveSessionTitle({ id: 's', first_prompt: '\n  Fix the login bug\nwith details' }), { raw: 'Fix the login bug', source: 'prompt' });
  assert.deepEqual(M.deriveSessionTitle({ id: 's', goal_objective: 'Ship the console' }), { raw: 'Ship the console', source: 'objective' });
  assert.deepEqual(M.deriveSessionTitle({ id: 's', agent_name: 'Ready', agent_role: 'evaluator' }), { raw: 'Ready \u00b7 evaluator', source: 'agent' });
  assert.deepEqual(M.deriveSessionTitle({ id: 'session_0123456789abcdef' }), { raw: 'sessio\u2026cdef', source: 'id' });
  assert.deepEqual(M.deriveSessionTitle({}), { key: 'session.untitled', source: 'fallback' });
  assert.equal(M.deriveSessionTitle({ first_prompt: 'x'.repeat(200) }).raw.length, 80);
});

test('history titles localize the fallback and keep custom agent facts raw', () => {
  assert.deepEqual(M.historySessionTitle({}), { key: 'session.masterFallback' });
  assert.deepEqual(M.historySessionTitle({ agent_name: 'Ready', agent_role: 'evaluator' }), { raw: 'Ready \u00b7 evaluator' });
});

test('collectPlanInputAnswers requires explicit answers for every question', () => {
  const request = { questions: [{ id: 'scope', options: [{ label: 'Small' }] }, { id: 'validation', options: [{ label: 'Unit' }] }] };
  assert.deepEqual(M.collectPlanInputAnswers(request, { scope: { label: 'Small', value: 'Small' } }), []);
});

test('collectPlanInputAnswers preserves selected multi-question answers', () => {
  const request = { questions: [{ id: 'scope', options: [{ label: 'Small' }] }, { id: 'validation', options: [{ label: 'Unit' }] }] };
  assert.deepEqual(M.collectPlanInputAnswers(request, {
    scope: { label: 'Small', value: 'Small' },
    validation: { label: 'Other', value: 'Browser smoke', is_other: true }
  }), [
    { question_id: 'scope', label: 'Small', value: 'Small', is_other: false },
    { question_id: 'validation', label: 'Other', value: 'Browser smoke', is_other: true }
  ]);
});

test('collectPlanInputAnswers rejects non-other selections outside offered options', () => {
  const request = { questions: [{ id: 'scope', options: [{ label: 'Small' }, { label: 'Large' }] }] };
  assert.deepEqual(M.collectPlanInputAnswers(request, { scope: { label: 'Surprise', value: 'Surprise' } }), []);
});

test('collectPlanInputAnswers accepts offered option descriptions as non-other values', () => {
  const request = { questions: [{ id: 'scope', options: [{ label: 'Small', description: 'Keep it focused.' }, { label: 'Large', description: 'Include cleanup.' }] }] };
  assert.deepEqual(M.collectPlanInputAnswers(request, { scope: { label: 'Small', value: 'Keep it focused.' } }), [
    { question_id: 'scope', label: 'Small', value: 'Keep it focused.', is_other: false }
  ]);
  assert.deepEqual(M.collectPlanInputAnswers(request, { scope: { value: 'x', is_other: true } }), [{ question_id: 'scope', label: 'Other', value: 'x', is_other: true }]);
  assert.deepEqual(M.prunePlanInputSelections({ request_id: 'r2', questions: [{ id: 'scope' }] }, { r1: { scope: {} }, r2: { scope: { label: 'a' }, gone: {} } }), { r2: { scope: { label: 'a' } } });
});

test('module is pure: no DOM, storage or network globals', () => {
  const source = readFileSync(new URL('../../internal/webconsole/ui/src/lib/session-model.js', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.doesNotMatch(source, /\b(?:document|window|fetch|localStorage|sessionStorage|XMLHttpRequest)\b/);
  assert.doesNotMatch(source, /from '(?!\.)/);
});
