import test from 'node:test';
import assert from 'node:assert/strict';
import {
  activeHandleView, badgeTone, budgetInspector, checkpointView, contextLoadPlan, contextReportView, goalActions, goalCoverage, goalHistory,
  goalItemTitle, goalLinkedFacts, hasAgents, hasPlanMode, hasTasks, parentCoordinationView, pendingSteers, planHistory,
  providerAttemptStats, providerFailureClass, providerOptionRows, recentProviderAttempts, relatedSessions, requiredArtifactRows,
  resolveInspectorTab, visibleInspectorTabs, withMessages,
} from '../../internal/webconsole/ui/src/features/inspector/inspector-model.js';
import { INSPECTOR_TABS } from '../../internal/webconsole/ui/src/state/prefs.js';

const base = { metadata: { id: 'session_a' }, state: { status: 'completed' }, task_board: { todo: [], tasks: [], counters: {} }, children: { sessions: [], jobs: [] }, background_notifications: [] };

test('only relevant inspector tabs are visible, in INSPECTOR_TABS order', () => {
  assert.deepEqual(visibleInspectorTabs(null), ['overview']);
  assert.deepEqual(visibleInspectorTabs(base), ['overview', 'context', 'timeline']);
  const full = {
    ...base,
    plan_mode: { plan_mode_id: 'plan_1', status: 'awaiting_approval' },
    goal: { goal_id: 'goal_1', status: 'active' },
    task_board: { todo: [{ content: 'x', status: 'pending' }], tasks: [], counters: {} },
    children: { sessions: [{ id: 'child_1' }], jobs: [] },
    file_changes: [{ path: 'a.txt', writes: 1 }],
  };
  assert.deepEqual(visibleInspectorTabs(full), INSPECTOR_TABS.slice());
  assert.equal(hasPlanMode({ plan_mode: { status: 'off' } }), false);
  assert.equal(hasTasks({ task_board: { groups: { ready: [{ id: 't' }] } } }), true);
  assert.equal(hasAgents({ background_notifications: [{ id: 'n' }] }), true, 'notifications alone show Agents');
  assert.deepEqual(visibleInspectorTabs(base, { fileCount: 2 }), ['overview', 'context', 'timeline', 'files']);
});

test('files tab follows inferred changes from the merged message window', () => {
  const messages = [
    { id: 'm1', role: 'assistant', tool_calls: [{ id: 'c1', name: 'write_file', arguments: JSON.stringify({ path: 'out.txt', content: 'a\nb' }) }] },
    { id: 'm2', role: 'tool', tool_results: [{ tool_call_id: 'c1', name: 'write_file', display_output: 'ok' }] },
  ];
  assert.equal(visibleInspectorTabs(withMessages(base, messages)).includes('files'), true);
  assert.equal(withMessages(base, []), base);
});

test('Context loads once per session visit; Refresh and a session switch load again (spec 17 §7.4.1)', () => {
  const report = { schema_version: 1, root_session_id: 'session_a' };
  assert.deepEqual(contextLoadPlan(undefined, { visit: 3 }), { report: null, load: true }, 'first open loads');
  assert.deepEqual(contextLoadPlan({ report, visit: 3 }, { visit: 3 }), { report, load: false }, 'Tasks → Context reuses the loaded report');
  assert.deepEqual(contextLoadPlan({ report, visit: 3 }, { visit: 3, refresh: true }), { report, load: true }, 'Refresh always requests');
  assert.deepEqual(contextLoadPlan({ report, visit: 3 }, { visit: 4 }), { report, load: true }, 'a new visit shows the old report while loading');
  assert.deepEqual(contextLoadPlan({ report: null, visit: 3 }, { visit: 3 }), { report: null, load: true }, 'no report yet: load');
});

test('a hidden persisted tab falls back to overview', () => {
  assert.equal(resolveInspectorTab('plan', ['overview', 'timeline']), 'overview');
  assert.equal(resolveInspectorTab('timeline', ['overview', 'timeline']), 'timeline');
  assert.equal(resolveInspectorTab('bogus', []), 'overview');
});

test('badge tones cover runtime, task, goal and attempt tokens', () => {
  assert.equal(badgeTone('running'), 'info');
  assert.equal(badgeTone('in_progress'), 'info');
  assert.equal(badgeTone('complete'), 'success');
  assert.equal(badgeTone('blocked'), 'warning');
  assert.equal(badgeTone('failure'), 'danger');
  assert.equal(badgeTone('cancelled'), 'neutral');
  assert.equal(badgeTone(''), 'neutral');
});

test('active handle owner and related session links', () => {
  assert.equal(activeHandleView({}), null);
  const owned = activeHandleView({ active_handle: true, active_handle_owner: { state: 'current_process', owned_by_current_process: true, pid: 42, started_at: '2026-01-01T00:00:00Z' } });
  assert.equal(owned.state, 'current_process');
  assert.equal(owned.key, 'inspector.handle.currentProcess');
  assert.equal(owned.pid, 42);
  assert.equal(activeHandleView({ active_handle_owner: { state: 'settled' } }).tone, 'neutral');
  assert.deepEqual(relatedSessions({ metadata: { id: 'c', parent_session_id: 'p', root_session_id: 'p', queue_job_id: 'job_1' } }), { parent: 'p', root: '', queueJob: 'job_1' });
  assert.deepEqual(relatedSessions({ metadata: { id: 'c', parent_session_id: 'p', root_session_id: 'r' } }).root, 'r');
});

test('provider failure classes follow the v1 heuristics', () => {
  assert.equal(providerFailureClass(''), '');
  assert.equal(providerFailureClass('upstream auth_unavailable'), 'authRouting');
  assert.equal(providerFailureClass('401 Unauthorized: invalid api key'), 'credential');
  assert.equal(providerFailureClass('HTTP 429 rate_limit'), 'rateLimit');
  assert.equal(providerFailureClass('invalid_request: unsupported parameter'), 'requestContract');
  assert.equal(providerFailureClass('stream disconnected before completion'), 'transport');
  assert.equal(providerFailureClass('server_error from upstream'), 'serverError');
  assert.equal(providerFailureClass('tool failed'), '');
});

test('provider attempts summarize retry evidence newest first', () => {
  const attempts = [
    { turn: 1, attempt: 1, outcome: 'retry', error: 'timeout', cache_read_input_tokens: 10, created_at: '2026-01-01T00:00:01Z' },
    { turn: 1, attempt: 2, outcome: 'success', cache_read_input_tokens: 5, cache_creation_input_tokens: 3, created_at: '2026-01-01T00:00:02Z' },
    { turn: 2, attempt: 1, outcome: 'auto_resume', created_at: '2026-01-01T00:00:03Z' },
  ];
  assert.deepEqual(providerAttemptStats(attempts), { total: 3, success: 1, failure: 0, retry: 1, autoResume: 1, cacheRead: 15, cacheCreation: 3 });
  const recent = recentProviderAttempts({ provider_attempts: attempts }, 2);
  assert.deepEqual(recent.map((item) => item.outcome), ['auto_resume', 'success']);
});

test('provider option rows skip empty values and keep booleans', () => {
  const rows = providerOptionRows({ reasoning_effort: 'high', max_output_tokens: 2048, store: false, temperature: 0, retry_policy: { max_attempts: 3 }, timeout_policy: { request_timeout_sec: 30 } });
  assert.deepEqual(rows.map((row) => row.key), ['inspector.option.reasoningEffort', 'inspector.option.maxOutputTokens', 'inspector.option.temperature', 'inspector.option.store', 'inspector.option.retryAttempts', 'inspector.option.requestTimeout']);
  assert.equal(rows.find((row) => row.key === 'inspector.option.temperature').value, '0', 'temperature 0 is a real setting');
  assert.equal(rows.find((row) => row.key === 'inspector.option.store').value, false);
  assert.deepEqual(providerOptionRows(null), []);
});

test('pending steers keep pending/deferred requests newest first', () => {
  const steers = pendingSteers({ steer_requests: [{ id: 'a', status: 'accepted' }, { id: 'b', status: 'pending' }, { id: 'c', status: 'deferred' }] });
  assert.deepEqual(steers.map((item) => item.id), ['c', 'b']);
});

test('recovery facts: parent coordination and long-run checkpoint', () => {
  assert.equal(parentCoordinationView({}), null);
  const coord = parentCoordinationView({ parent_coordination: { parent_session_id: 's', wait_mode: 'wait-any', unresolved_child_sessions: ['c1'], failed_queue_jobs: ['j1'], updated_at: 'x' } });
  assert.equal(coord.waitState, 'waiting');
  assert.deepEqual(coord.counts.unresolved, [1, 0]);
  assert.deepEqual(coord.counts.failed, [0, 1]);
  assert.equal(parentCoordinationView({ parent_coordination: { parent_session_id: 's', parked: true } }).waitState, 'parked');
  assert.equal(parentCoordinationView({ parent_coordination: { parent_session_id: 's' } }).waitState, 'ready');
  const checkpoint = checkpointView({ longrun_checkpoint: { session_id: 's', resume_hints: ['resume child c1'], unresolved_queue_jobs: ['j2'], created_at: '2026-01-01T00:00:00Z' } });
  assert.deepEqual(checkpoint.hints, ['resume child c1']);
  assert.deepEqual(checkpoint.unresolvedJobs, ['j2']);
  assert.equal(checkpointView({ longrun_checkpoint: {} }), null);
});

test('child budget inspector derives limit / used / remaining', () => {
  assert.equal(budgetInspector(null), null);
  const view = budgetInspector({ status: 'active', attempt: 2, source: 'runtime.child_budget', max_turns_per_attempt: 5, used_turns: 2, max_active_runtime_ms: 60000, used_active_runtime_ms: 15000, active_runtime_checkpoint_at: '2026-01-01T00:00:00Z', active_runtime_lease_open: true, last_reason: 'child_budget_turns_exceeded' });
  assert.deepEqual(view.turns, { limit: 5, used: 2, remaining: 3, overrun: 0 });
  assert.equal(view.runtime.remaining, 45000);
  assert.equal(view.leaseOpen, true);
  assert.equal(view.lastReason, 'child_budget_turns_exceeded');
  assert.equal(budgetInspector({ status: 'active', max_turns_per_attempt: 4, used_turns: 1, remaining_turns: 0 }).turns.remaining, 0);
  assert.equal(budgetInspector({ status: 'disabled' }).disabled, true);
  assert.equal(budgetInspector({ status: 'active', last_reason: 'a' }, 'manual_stop').lastReason, 'manual_stop');
});

test('required artifacts map presence and validation to a state', () => {
  const rows = requiredArtifactRows({ required_artifacts: [
    { path: 'a.md', status: { present: true, touched_by_session: true } },
    { path: 'b.md', status: { present: true } },
    { path: 'c.md', status: { present: false } },
    { path: 'd.md', status: { present: true, validation_status: 'failed' } },
  ] });
  assert.deepEqual(rows.map((row) => row.state), ['completed', 'ready', 'pending', 'failed']);
});

test('plan history merges approvals and planmode events newest first', () => {
  const history = planHistory({
    plan_mode: { approvals: [{ version: 1, approved_at: '2026-01-01T00:00:05Z', source: 'web' }] },
    timeline: [
      { kind: 'event', event_id: 'e2', event_type: 'planmode.plan_submitted', time: '2026-01-01T00:00:03Z' },
      { kind: 'event', event_id: 'e1', event_type: 'tool.after', time: '2026-01-01T00:00:02Z' },
      { kind: 'message', message_id: 'm1', time: '2026-01-01T00:00:01Z' },
    ],
  });
  assert.deepEqual(history.map((entry) => entry.kind), ['approval', 'event']);
  assert.equal(history[1].item.event_type, 'planmode.plan_submitted');
});

test('goal controls follow status and mission plan rules', () => {
  assert.deepEqual(goalActions({}), { canPause: false, canResume: false, canComplete: false, canApprovePlan: false, canClear: false });
  const active = goalActions({ goal: { status: 'active', mission: { plan_status: 'needs_approval' } } });
  assert.equal(active.canPause, true);
  assert.equal(active.canResume, false);
  assert.equal(active.canComplete, true);
  assert.equal(active.canApprovePlan, true);
  const limited = goalActions({ goal: { status: 'budget_limited' } });
  assert.equal(limited.canPause && limited.canResume, true);
  const done = goalActions({ goal: { status: 'complete', mission: { plan_status: 'approved' } } });
  assert.equal(done.canComplete, false);
  assert.equal(done.canApprovePlan, false);
  const planning = goalActions({ goal: { goal_id: 'goal_1', status: 'active', mission: { plan_status: 'draft' } }, plan_mode: { status: 'planning', linked_goal_id: 'goal_1' } });
  assert.equal(planning.canApprovePlan, false, 'a linked Plan Mode is approved on the inline card once it awaits approval');
});

test('goal item titles, linked facts, coverage and history', () => {
  assert.equal(goalItemTitle({ command: 'go test ./...', kind: 'command' }, 'validation'), 'go test ./...');
  assert.equal(goalItemTitle({ name: 'Reviewer', role: 'evaluator' }, 'role'), 'Reviewer');
  assert.equal(goalItemTitle({ id: 'f1', title: 'Feature' }, 'feature'), 'Feature');
  const linked = goalLinkedFacts({ task_ids: ['t1'], child_session_ids: ['c1'], evaluator_evidence: [{ child_session_id: 'c2' }, { summary: 'ok' }] });
  assert.deepEqual(linked.children, ['c1']);
  assert.deepEqual(linked.evaluator, ['c2', 'ok']);
  assert.equal(linked.evaluatorCount, 2);
  const coverage = goalCoverage({ goal_facts: { coverage: { validation_total: 3, covered_assertions: 2, approval_blocked: true, uncovered_assertions: ['v3'] } } });
  assert.deepEqual([coverage.covered, coverage.total, coverage.blocked, coverage.uncovered], [2, 3, true, ['v3']]);
  const history = goalHistory({ goal_facts: { history: [{ type: 'goal.created', created_at: '1' }, { type: 'goal.completed', created_at: '2' }] } });
  assert.deepEqual(history.map((entry) => entry.type), ['goal.completed', 'goal.created']);
  assert.deepEqual(goalHistory({ goal_facts: { latest_history: { type: 'goal.updated' } } }).map((entry) => entry.type), ['goal.updated']);
});

test('context report view keeps aggregate metrics, rows and truncation', () => {
  assert.equal(contextReportView(null), null);
  const view = contextReportView({
    schema_version: 1,
    root_session_id: 'root_1',
    aggregate: { root_peak_estimated_input_tokens: 1200.7, total_request_count: 4, wall_time_ms: 5000, total_provider_usage: { input_tokens: 10, cache_read_input_tokens: 3 } },
    sessions: [
      { session_id: 'root_1', metrics: { peak_estimated_input_tokens: 1200, aggregate_estimated_input_tokens: 2000, request_count: 3 } },
      { session_id: 'child_1', agent_role: 'evaluator', metrics: { request_count: 1 } },
    ],
    truncation: { truncated: true, omitted_session_count: 2, omitted_request_count: 7 },
  });
  assert.equal(view.peaks.find((metric) => metric.key === 'inspector.context.rootPeak').value, 1200);
  assert.equal(view.usage.find((metric) => metric.key === 'inspector.context.cacheRead').value, 3);
  assert.equal(view.lineage.find((metric) => metric.key === 'inspector.context.requests').value, 4);
  assert.equal(view.wallTimeMs, 5000);
  assert.deepEqual(view.sessions.map((row) => [row.isRoot, row.role]), [[true, ''], [false, 'evaluator']]);
  assert.deepEqual(view.truncation, { sessions: 2, requests: 7 });
  assert.equal(contextReportView({ truncation: { truncated: false } }).truncation, null);
});
