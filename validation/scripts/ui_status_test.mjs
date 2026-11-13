import test from 'node:test';
import assert from 'node:assert/strict';
import {
  handleStartedNoLaterThanState, isAttentionStatus, planModeActionDisplayStatus, sessionDetailDisplayStatus, sessionDetailHasActiveDescendants,
  sessionHandleIsSettling, sessionHasCurrentProcessHandle, sessionHasDirectControl, sessionSummaryDisplayStatus, statusLabelKey, statusTone,
} from '../../internal/webconsole/ui/src/lib/status.js';

test('plan-mode actions outrank the runtime status', () => {
  assert.equal(planModeActionDisplayStatus('awaiting_approval'), 'awaiting_plan_approval');
  assert.equal(planModeActionDisplayStatus('AWAITING_USER_INPUT'), 'awaiting_plan_input');
  assert.equal(planModeActionDisplayStatus('executing'), '');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'running' }, plan_mode: { status: 'awaiting_approval' }, active_handle: true }), 'awaiting_plan_approval');
  assert.equal(sessionSummaryDisplayStatus({ status: 'paused', plan_mode_status: 'awaiting_user_input' }), 'awaiting_plan_input');
  assert.equal(sessionSummaryDisplayStatus({ status: 'failed' }), 'failed');
  assert.equal(sessionSummaryDisplayStatus({}), 'unknown');
});

test('a live current-process handle means running unless it is only settling', () => {
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'paused' }, active_handle: true }), 'running');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'awaiting_input' }, active_handle_owner: { owned_by_current_process: true } }), 'running');
  // The server prunes settled handles of completed sessions, so a handle on a
  // completed root is its accepted follow-up: no "completed" flash while it starts.
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'completed', updated_at: '2026-11-13T12:00:00Z' }, active_handle: true, active_handle_owner: { state: 'current_process', owned_by_current_process: true, started_at: '2026-11-13T12:00:01Z' } }), 'running');
  // Settling: the run wrote its final state after the handle started; the release is pending.
  const settling = { state: { status: 'awaiting_input', updated_at: '2026-11-13T12:00:05.5Z' }, active_handle: true, active_handle_owner: { state: 'current_process', owned_by_current_process: true, started_at: '2026-11-13T12:00:01Z' } };
  assert.equal(sessionHandleIsSettling(settling), true);
  assert.equal(sessionDetailDisplayStatus(settling), 'awaiting_input');
  assert.equal(sessionHandleIsSettling({ ...settling, state: { ...settling.state, status: 'running' } }), false, 'a running state is never settling');
  assert.equal(sessionDetailDisplayStatus({ state: { status: 'failed' } }), 'failed');
  assert.equal(sessionDetailDisplayStatus(null), 'unknown');
  assert.equal(sessionHasCurrentProcessHandle({ active_handle: false, active_handle_owner: { owned_by_current_process: false } }), false);
  assert.equal(sessionHasDirectControl({ active_handle: true, active_handle_owner: { owned_by_current_process: false } }), false);
  assert.equal(sessionHasDirectControl({ active_handle: true, active_handle_owner: { owned_by_current_process: true } }), true);
});

test('handleStartedNoLaterThanState mirrors the server comparison with nanosecond precision', () => {
  assert.equal(handleStartedNoLaterThanState('2026-11-13T12:00:00.123456789Z', '2026-11-13T12:00:00.1234568Z'), true);
  assert.equal(handleStartedNoLaterThanState('2026-11-13T12:00:00.1234568Z', '2026-11-13T12:00:00.123456789Z'), false, 'same millisecond, later nanoseconds');
  assert.equal(handleStartedNoLaterThanState('2026-11-13T12:00:00Z', '2026-11-13T12:00:00Z'), true, 'equal times');
  assert.equal(handleStartedNoLaterThanState('2026-11-13T20:00:00+08:00', '2026-11-13T12:00:00.5Z'), true, 'offsets');
  assert.equal(handleStartedNoLaterThanState('', '2026-11-13T12:00:00Z'), false, 'missing start time');
  assert.equal(handleStartedNoLaterThanState('2026-11-13T12:00:00Z', 'not-a-time'), false, 'unparseable state time');
});

test('active descendants are queued/pending/running children or jobs', () => {
  assert.equal(sessionDetailHasActiveDescendants({ children: { sessions: [{ status: 'completed' }], jobs: [] } }), false);
  assert.equal(sessionDetailHasActiveDescendants({ children: { sessions: [{ status: 'running' }] } }), true);
  assert.equal(sessionDetailHasActiveDescendants({ children: { jobs: [{ status: 'done', session_status: 'queued' }] } }), true);
  assert.equal(sessionDetailHasActiveDescendants({}), false);
});

test('tone mapping follows the design contract', () => {
  assert.equal(statusTone('running'), 'info');
  for (const status of ['awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input', 'paused']) assert.equal(statusTone(status), 'warning', status);
  assert.equal(statusTone('completed'), 'success');
  assert.equal(statusTone('failed'), 'danger');
  for (const status of ['cancelled', 'queued', 'unknown', '', undefined, 'weird']) assert.equal(statusTone(status), 'neutral', String(status));
});

test('attention group and label keys', () => {
  for (const status of ['running', 'awaiting_input', 'awaiting_plan_approval', 'awaiting_plan_input']) assert.ok(isAttentionStatus(status), status);
  for (const status of ['paused', 'completed', 'failed', 'queued']) assert.ok(!isAttentionStatus(status), status);
  assert.equal(statusLabelKey('Running'), 'common.status.running');
  assert.equal(statusLabelKey('something_new'), '');
});
